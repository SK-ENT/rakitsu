package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/store"
	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// ============================================================
// Wire shape — tree snapshot
// ============================================================

// treeNodeSummary is the per-node shape sent in tree_snapshot — full
// metadata MINUS Entries (entry payloads can be fetched per-node via
// GET /api/chat/{id}/turn/{turn_id} when the user clicks a node). EntryCount
// gives the Viewer a quick badge without dragging the whole body across
// the wire on every mutation.
type treeNodeSummary struct {
	ID         string                 `json:"id"`
	ParentID   string                 `json:"parent_id,omitempty"`
	UserText   string                 `json:"user_text,omitempty"`
	Children   []string               `json:"children,omitempty"`
	Status     turntree.Status        `json:"status"`
	Created    time.Time              `json:"created"`
	EntryCount int                    `json:"entry_count"`
	RetryCtx   *turntree.RetryContext `json:"retry_ctx,omitempty"`
}

// treeSnapshot is the summary view of a session's branching turn history
// emitted on every tree mutation. ActivePath gives the UI a precomputed
// root→leaf id list for highlighting without having to re-walk the tree.
type treeSnapshot struct {
	Nodes      []treeNodeSummary `json:"nodes"`
	Roots      []string          `json:"roots"`
	ActivePath []string          `json:"active_path"`
}

// snapshotTreeLocked builds a treeSnapshot. MUST be called with s.mu held.
func (s *ChatSession) snapshotTreeLocked() *treeSnapshot {
	tr := s.tree
	out := &treeSnapshot{
		Nodes: make([]treeNodeSummary, 0, len(tr.Nodes)),
		Roots: append([]string(nil), tr.Roots...),
	}
	for _, n := range tr.Nodes {
		out.Nodes = append(out.Nodes, treeNodeSummary{
			ID:         n.ID,
			ParentID:   n.ParentID,
			UserText:   n.UserText,
			Children:   append([]string(nil), n.Children...),
			Status:     n.Status,
			Created:    n.Created,
			EntryCount: len(n.Entries),
			RetryCtx:   n.RetryCtx,
		})
	}
	for _, n := range tr.ActivePath() {
		out.ActivePath = append(out.ActivePath, n.ID)
	}
	return out
}

// snapshotTreeFromTree builds a treeSnapshot from a hydrated turntree.Tree
// without requiring a live ChatSession. Used by GET /api/chat/{id}/tree to
// serve past sessions from their persisted .chat.json — the Tree comes from
// LoadChatTreeFile, which the live-session path does not own. Mirrors
// snapshotTreeLocked but without a mutex (the caller is expected to hold one
// or be operating on an immutable on-disk snapshot).
func snapshotTreeFromTree(tr *turntree.Tree[transcriptEntry]) *treeSnapshot {
	out := &treeSnapshot{
		Nodes: make([]treeNodeSummary, 0, len(tr.Nodes)),
		Roots: append([]string(nil), tr.Roots...),
	}
	for _, n := range tr.Nodes {
		out.Nodes = append(out.Nodes, treeNodeSummary{
			ID:         n.ID,
			ParentID:   n.ParentID,
			UserText:   n.UserText,
			Children:   append([]string(nil), n.Children...),
			Status:     n.Status,
			Created:    n.Created,
			EntryCount: len(n.Entries),
			RetryCtx:   n.RetryCtx,
		})
	}
	for _, n := range tr.ActivePath() {
		out.ActivePath = append(out.ActivePath, n.ID)
	}
	return out
}

// SnapshotTree is the public, lock-acquiring wrapper around snapshotTreeLocked
// — used by the GET /api/chat/{id}/tree REST handler when the session is still
// live in memory. Past sessions go through snapshotTreeFromTree off disk.
func (s *ChatSession) SnapshotTree() *treeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotTreeLocked()
}

// ============================================================
// Resync helpers — broadcast after a tree mutation
// ============================================================

// snapshotTranscriptLocked is the under-lock variant of snapshotTranscript.
// Caller holds s.mu. Used by branch op paths that already hold the lock to
// avoid releasing + re-acquiring around resync construction.
func (s *ChatSession) snapshotTranscriptLocked() []transcriptEntry {
	return s.decorateActivePathLocked()
}

// transcriptCap limits how many derived transcript entries we send on the
// `attached` replay frame (see snapshotTranscript's doc comment in
// chat_session.go). Tree storage is unbounded; the cap protects the wire
// payload only. Declared here, next to decorateActivePathLocked below where
// it's actually enforced, rather than in chat_session.go where it's only
// declared — a review pass scoped to chat_session.go alone has repeatedly
// (3 rounds running) read the cross-file enforcement as "declared but never
// applied."
const transcriptCap = 200

// decorateActivePathLocked is the shared transcript-derivation core for
// both snapshotTranscript and the post-mutation broadcastTreeMutation
// path. Decorates each entry with turn_id; user entries additionally
// carry branch_index/branch_count so the inline UX can render the
// `‹n/m›` chip. Persisted entries (stored in Node.Entries) leave the
// branch fields blank — these are pure wire decoration. Caller holds
// s.mu.
//
// This is where transcriptCap (declared just above) is actually enforced —
// see its own doc comment for why that's worth stating explicitly.
func (s *ChatSession) decorateActivePathLocked() []transcriptEntry {
	path := s.tree.ActivePath()
	out := make([]transcriptEntry, 0, len(path)*4)
	for _, node := range path {
		if node.UserText != "" {
			idx, count := s.tree.SiblingIndex(node.ID)
			out = append(out, transcriptEntry{
				Kind:        "user",
				Text:        node.UserText,
				Timestamp:   node.Created.UTC().Format(time.RFC3339Nano),
				TurnID:      node.ID,
				BranchIndex: idx,
				BranchCount: count,
			})
		}
		for _, e := range node.Entries {
			e.TurnID = node.ID
			out = append(out, e)
		}
	}
	if len(out) > transcriptCap {
		out = out[len(out)-transcriptCap:]
	}
	return out
}

// broadcastTreeMutation snapshots the transcript + tree under the lock,
// persists, and fans out transcript_resync + tree_snapshot frames to all
// attached clients. The two-frame design lets the inline chat view rebuild
// its flat block list from transcript_resync while the Tree Viewer redraws
// from tree_snapshot — neither needs to consume the other's payload.
//
// Caller must hold s.mu. The lock is released for the network/disk side
// effects (broadcast, persist) once the in-memory snapshots are taken.
func (s *ChatSession) broadcastTreeMutationLocked() {
	transcript := s.snapshotTranscriptLocked()
	snap := s.snapshotTreeLocked()
	raw, gen, marshalErr := s.snapshotForPersistLocked()
	s.mu.Unlock()
	defer s.mu.Lock()

	s.broadcast(serverMsg{Type: "transcript_resync", Transcript: transcript})
	s.broadcast(serverMsg{Type: "tree_snapshot", Tree: snap})

	if marshalErr == nil {
		s.persistTreeRaw(raw, gen)
	}
}

// ============================================================
// Branch op methods — invoked from WS dispatch
// ============================================================

// ErrSessionGenerating is returned by branch ops invoked while a turn is
// still generating. Per the plan, branch ops first call Interrupt() and
// then mutate; this error is reserved for tests / future strict modes.
var ErrSessionGenerating = errors.New("session is generating; interrupt first")

// EditTurn creates a new sibling of targetID with newUserText and makes it
// the active branch at that level. Tree state is broadcast + persisted but
// no regeneration is triggered here — the WS dispatcher calls Submit
// separately if a regenerate is desired (PR D adds the "edit → auto-regen"
// composite flow; PR C keeps tree mutation and generation orthogonal).
func (s *ChatSession) EditTurn(targetID, newUserText string) error {
	if strings.TrimSpace(newUserText) == "" {
		return fmt.Errorf("edit_turn: empty text")
	}
	s.Interrupt()

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tree.Get(targetID); !ok {
		return fmt.Errorf("edit_turn: %w: %s", turntree.ErrUnknownNode, targetID)
	}
	if _, err := s.tree.AddSibling(uuid.NewString(), targetID, newUserText); err != nil {
		return fmt.Errorf("edit_turn: %w", err)
	}
	s.broadcastTreeMutationLocked()
	return nil
}

// RegenerateTurn creates a sibling of targetID with the SAME UserText as
// the target and makes it active. Used by the "try again with the same
// question" UI affordance. RetryCtx is left nil — per the user-driven /
// agent-driven rule, the user clicking regenerate is user-driven and the
// model should not be told "you got it wrong before" implicitly.
func (s *ChatSession) RegenerateTurn(targetID string) error {
	s.Interrupt()

	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.tree.Get(targetID)
	if !ok {
		return fmt.Errorf("regenerate: %w: %s", turntree.ErrUnknownNode, targetID)
	}
	if _, err := s.tree.AddSibling(uuid.NewString(), targetID, target.UserText); err != nil {
		return fmt.Errorf("regenerate: %w", err)
	}
	s.broadcastTreeMutationLocked()
	return nil
}

// SwitchBranch flips the active sibling at targetID's level by dir (±1).
// Idempotent at the boundaries — wraps cyclically per turntree semantics.
// No regeneration; tree state changes, transcript_resync reflects the new
// active path.
func (s *ChatSession) SwitchBranch(targetID string, dir int) error {
	s.Interrupt()

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.tree.SwitchToSibling(targetID, dir); err != nil {
		if errors.Is(err, turntree.ErrSiblingCount) {
			// One-of-one — broadcast nothing, surface success. The client
			// asked to switch but there's nowhere to go; not an error.
			return nil
		}
		return fmt.Errorf("switch_branch: %w", err)
	}
	s.broadcastTreeMutationLocked()
	return nil
}

// SetActivePath walks ancestors of leafTurnID setting ActiveChild at each
// parent so leafTurnID sits on the active path. Used by the Tree Viewer's
// "Make active" affordance.
func (s *ChatSession) SetActivePath(leafTurnID string) error {
	s.Interrupt()

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.tree.SetActivePath(leafTurnID); err != nil {
		return fmt.Errorf("set_active_path: %w", err)
	}
	s.broadcastTreeMutationLocked()
	return nil
}

// RequestTree sends a fresh tree_snapshot to all attached clients. Used on
// reconnect/late-attach where the client hasn't seen the snapshot yet.
// No mutation, no persistence.
func (s *ChatSession) RequestTree() {
	s.mu.Lock()
	snap := s.snapshotTreeLocked()
	s.mu.Unlock()
	s.broadcast(serverMsg{Type: "tree_snapshot", Tree: snap})
}

// ============================================================
// Fork — REST handler + copy logic
// ============================================================

// ForkMode discriminates how handleChatFork copies tree state into the new
// session.
type ForkMode string

const (
	// ForkActivePath copies only the root→target active path (ChatGPT
	// mental model: "start a fresh thread from here"). Sibling branches in
	// the source are dropped.
	ForkActivePath ForkMode = "path"
	// ForkSubtree copies the full subtree rooted at the active root, so
	// the fork is a true snapshot — user can keep exploring rejected
	// siblings in either the original or the fork independently.
	ForkSubtree ForkMode = "subtree"
)

// copyTreeFork returns a new tree containing either the active path up to
// truncateAtID (ForkActivePath) or the full forest (ForkSubtree). Either
// way, every Node is deep-copied so the fork mutates independently of src.
// It is the pure tree-copy core shared by the live-session fork
// (copyTreeForkLocked) and the past-session fork (handleChatFork's disk
// branch); the caller owns any locking around src.
//
// truncateAtID is the turn the user clicked "fork here" on; for path mode
// it's the last node included. Empty truncateAtID means use the active
// leaf, which makes "fork the current conversation as-is" the default.
func copyTreeFork(src *turntree.Tree[transcriptEntry], mode ForkMode, truncateAtID string) (*turntree.Tree[transcriptEntry], error) {
	dst := turntree.New[transcriptEntry]()

	switch mode {
	case ForkActivePath:
		path := src.ActivePath()
		if len(path) == 0 {
			return dst, nil
		}
		var stopAt int = len(path)
		if truncateAtID != "" {
			stopAt = -1
			for i, n := range path {
				if n.ID == truncateAtID {
					stopAt = i + 1
					break
				}
			}
			if stopAt < 0 {
				return nil, fmt.Errorf("fork: %w: %s not on active path", turntree.ErrUnknownNode, truncateAtID)
			}
		}
		parentID := ""
		for _, n := range path[:stopAt] {
			cloned, err := dst.AppendTurn(n.ID, n.UserText, parentID)
			if err != nil {
				return nil, fmt.Errorf("fork: clone path: %w", err)
			}
			cloned.Status = n.Status
			cloned.Created = n.Created
			cloned.Entries = append([]transcriptEntry(nil), n.Entries...)
			if n.RetryCtx != nil {
				rc := *n.RetryCtx
				cloned.RetryCtx = &rc
			}
			parentID = n.ID
		}

	case ForkSubtree:
		// Deep-copy every node + every root. Active state cloned too.
		for id, n := range src.Nodes {
			clone := &turntree.Node[transcriptEntry]{
				ID:       n.ID,
				ParentID: n.ParentID,
				UserText: n.UserText,
				Entries:  append([]transcriptEntry(nil), n.Entries...),
				Children: append([]string(nil), n.Children...),
				Status:   n.Status,
				Created:  n.Created,
			}
			if n.RetryCtx != nil {
				rc := *n.RetryCtx
				clone.RetryCtx = &rc
			}
			dst.Nodes[id] = clone
		}
		dst.Roots = append([]string(nil), src.Roots...)
		for k, v := range src.ActiveChild {
			dst.ActiveChild[k] = v
		}
		if truncateAtID != "" {
			if err := dst.SetActivePath(truncateAtID); err != nil {
				return nil, fmt.Errorf("fork: %w", err)
			}
		}

	default:
		return nil, fmt.Errorf("fork: unknown mode %q (want 'path' or 'subtree')", mode)
	}

	return dst, nil
}

// copyTreeForkLocked is the live-session wrapper around copyTreeFork. MUST
// be called with s.mu held — it reads s.tree.
func (s *ChatSession) copyTreeForkLocked(mode ForkMode, truncateAtID string) (*turntree.Tree[transcriptEntry], error) {
	return copyTreeFork(s.tree, mode, truncateAtID)
}

// forkOptionsLocked is the internal entry point used by handleChatFork.
// Caller holds s.mu on the SOURCE session; this method copies the tree
// shape and returns a new ChatSessionOptions ready for StartChatSession.
// The new session reuses the source's config and a fresh "chat-<uuid>"
// id (so both the original and the fork remain addressable in parallel).
func (s *ChatSession) forkOptionsLocked(mode ForkMode, truncateAtID string) (ChatSessionOptions, error) {
	clonedTree, err := s.copyTreeForkLocked(mode, truncateAtID)
	if err != nil {
		return ChatSessionOptions{}, err
	}
	return ChatSessionOptions{
		ConfigID:   s.configID,
		Cfg:        s.cfg,
		Workdir:    s.workdir,
		ResumeTree: clonedTree,
	}, nil
}

// forkPersistedOptions builds ChatSessionOptions for forking a chat session
// that is no longer live in memory (PR D-5.5). It resolves the source's
// branching tree and config from disk (resolvePersistedSession), copies the
// requested fork shape (copyTreeFork), and reinjects request env_vars so a
// fork of a config whose secrets were redacted at persist time still gets
// real API keys.
//
// The returned options describe a FRESH session (no ResumeID) seeded with
// the forked tree — the past-session mirror of forkOptionsLocked.
// BuildFunc / ForwardBus / SessionStore are left for the caller to fill.
func (m *ChatManager) forkPersistedOptions(sourceID string, mode ForkMode, truncateAtID string, envVars map[string]string) (ChatSessionOptions, error) {
	tree, _, configID, workdir, err := m.resolvePersistedSession(sourceID, "", envVars)
	if err != nil {
		return ChatSessionOptions{}, err
	}
	forked, err := copyTreeFork(tree, mode, truncateAtID)
	if err != nil {
		return ChatSessionOptions{}, err
	}
	cfg, configPath, err := m.configStore.GetWithEnv(configID, envVars)
	if err != nil {
		return ChatSessionOptions{}, fmt.Errorf("config not found: %w", err)
	}
	// The persisted YAML may carry "[REDACTED]" api keys and was captured
	// before the source session's workdir-to-tools wiring; both must be
	// (re)applied to the freshly-loaded cfg. The live-fork path skips this
	// because it reuses the source session's already-prepared cfg.
	applyRedactedKeyOverrides(cfg, envVars)
	applyWorkdirToTools(cfg, workdir)
	return ChatSessionOptions{
		ConfigID:   configID,
		ConfigPath: configPath,
		Cfg:        cfg,
		Workdir:    workdir,
		ResumeTree: forked,
	}, nil
}

// ============================================================
// HTTP handlers
// ============================================================

// handleChatFork — POST /api/chat/{id}/fork
// Body: {turn_id?: "...", mode?: "path"|"subtree", env_vars?: {...}}
// Default mode = "path"; turn_id defaults to the active leaf.
// Response: new ChatSessionMeta (200) or {"error": ...} (4xx/5xx).
//
// The source may be a live in-memory session OR a past session on disk
// (PR D-5.5): when it is not live, the branching tree + config are loaded
// from .chat.json / the JSONL event log. env_vars de-redacts API keys for
// a past session whose persisted YAML carries "[REDACTED]" masks.
func (s *SSEServer) handleChatFork(w http.ResponseWriter, r *http.Request, sourceID string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.chatManager == nil {
		http.Error(w, `{"error":"chat manager not configured"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		TurnID  string            `json:"turn_id,omitempty"`
		Mode    ForkMode          `json:"mode,omitempty"`
		EnvVars map[string]string `json:"env_vars,omitempty"`
	}
	// Don't gate the decode on r.ContentLength > 0: Go sets ContentLength to
	// -1 for a chunked-transfer body (or any request sent without a
	// Content-Length header), not just for a genuinely empty one — gating
	// on it silently skipped parsing a real body. Attempt the decode
	// unconditionally and treat io.EOF (no body at all) as "use defaults".
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Mode == "" {
		req.Mode = ForkActivePath
	}
	if req.Mode != ForkActivePath && req.Mode != ForkSubtree {
		jsonErrorResponse(w, fmt.Sprintf("fork mode must be 'path' or 'subtree', got %q", req.Mode), http.StatusBadRequest)
		return
	}

	// Build the fork's session options from the live source's in-memory
	// tree when it is still active, else by loading the source from disk.
	var (
		opts ChatSessionOptions
		err  error
	)
	if source := s.chatManager.Get(sourceID); source != nil {
		source.mu.Lock()
		opts, err = source.forkOptionsLocked(req.Mode, req.TurnID)
		source.mu.Unlock()
		if err != nil {
			jsonErrorResponse(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		opts, err = s.chatManager.forkPersistedOptions(sourceID, req.Mode, req.TurnID, req.EnvVars)
		if err != nil {
			status := http.StatusNotFound
			if errors.Is(err, turntree.ErrUnknownNode) {
				status = http.StatusBadRequest
			}
			jsonErrorResponse(w, err.Error(), status)
			return
		}
	}

	// Re-wire the new session's lifecycle inputs without going back through
	// ChatManager.Start (which would re-resolve the configID, re-apply
	// workdir-to-tools, etc.). For a fork the cfg is already in hand from
	// the source session; what we need is a fresh runner and ID.
	opts.BuildFunc = s.chatManager.buildFunc
	opts.ForwardBus = s.chatManager.hubBus
	if s.chatManager.sessionStore != nil {
		if ss, err := store.NewSessionStore(); err == nil {
			opts.SessionStore = ss
		}
	}
	sess, err := StartChatSession(r.Context(), opts)
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Register the fork in the manager so it's reachable by id, mirroring
	// what ChatManager.Start does at its tail.
	s.chatManager.mu.Lock()
	s.chatManager.sessions[sess.ID] = sess
	s.chatManager.mu.Unlock()

	// Persist the cloned tree once so the fork survives a restart from
	// turn zero — without this, a forked session that gets no further turns
	// would have no .chat.json on disk.
	sess.persistTree()

	json.NewEncoder(w).Encode(sess.Meta())
}

// handleGetTurn — GET /api/chat/{id}/turn/{turn_id}
// Returns the full turn node (id, parent_id, user_text, entries, status,
// created, retry_ctx) for one turn. Used by the Tree Viewer's preview pane.
//
// Two paths, mirroring handleChatTree (PR D-4):
//
//  1. Live in memory — chatManager.Get(id) returns a session; read the
//     node under the session mutex.
//  2. Past on disk    — load the persisted .chat.json via sessionStore +
//     LoadChatTreeFile and look up the turn in the hydrated tree.
//
// This lets the Sessions tab's Tree sub-tab show full turn previews for
// completed sessions, not just live ones.
func (s *SSEServer) handleGetTurn(w http.ResponseWriter, r *http.Request, sessionID, turnID string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.chatManager == nil {
		http.Error(w, `{"error":"chat manager not configured"}`, http.StatusServiceUnavailable)
		return
	}
	if sess := s.chatManager.Get(sessionID); sess != nil {
		sess.mu.Lock()
		node, ok := sess.tree.Get(turnID)
		if !ok {
			sess.mu.Unlock()
			http.Error(w, `{"error":"turn not found"}`, http.StatusNotFound)
			return
		}
		payload, err := json.Marshal(node)
		sess.mu.Unlock()
		if err != nil {
			jsonErrorResponse(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(payload)
		return
	}
	if s.sessionStore == nil {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	// PR D-5.6: prefer .chat.json; fall back to JSONL reconstruction so
	// per-turn previews work on past sessions that never persisted a
	// chat tree envelope (CLI runs, pre-PR-B/3 chats, STALE chats).
	var tree *turntree.Tree[transcriptEntry]
	if raw, lerr := s.sessionStore.LoadChatTree(sessionID); lerr == nil {
		f, perr := LoadChatTreeFile(raw)
		if perr != nil {
			jsonErrorResponse(w, perr.Error(), http.StatusInternalServerError)
			return
		}
		tree = f.Tree
	} else {
		events, evErr := s.sessionStore.GetSessionEvents(sessionID)
		if evErr != nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		tree = reconstructTreeFromEvents(events)
		if tree == nil {
			http.Error(w, `{"error":"session has no chat tree"}`, http.StatusNotFound)
			return
		}
	}
	node, ok := tree.Get(turnID)
	if !ok {
		http.Error(w, `{"error":"turn not found"}`, http.StatusNotFound)
		return
	}
	payload, err := json.Marshal(node)
	if err != nil {
		jsonErrorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(payload)
}
