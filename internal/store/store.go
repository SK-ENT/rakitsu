// Package store provides persistent session storage for agent execution traces.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/google/uuid"
)

// SessionStatus represents the final state of a session.
type SessionStatus string

const (
	SessionRunning SessionStatus = "running"
	SessionSuccess SessionStatus = "success"
	SessionError   SessionStatus = "error"
	SessionTimeout SessionStatus = "timeout"
	SessionStale   SessionStatus = "stale"
)

// SessionMeta holds metadata about a session.
type SessionMeta struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"project_id,omitempty"`
	Name        string        `json:"name"`
	Query       string        `json:"query"`
	ConfigPath  string        `json:"config_path"`
	ConfigYAML  string        `json:"config_yaml,omitempty"`
	Workdir     string        `json:"workdir,omitempty"`
	Agents      []string      `json:"agents"`
	StartTime   time.Time     `json:"start_time"`
	EndTime     *time.Time    `json:"end_time,omitempty"`
	Status      SessionStatus `json:"status"`
	TotalTokens int           `json:"total_tokens"`
	TotalEvents int           `json:"total_events"`
	DurationMs  int64         `json:"duration_ms"`
}

// SessionStore manages session persistence to ~/.rakitsu/sessions/.
type SessionStore struct {
	dir         string
	mu          sync.Mutex
	current     *SessionMeta
	file        *os.File
	eventCount  int
	totalTokens int
}

// NewSessionStore creates a session store at ~/.rakitsu/sessions/.
func NewSessionStore() (*SessionStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve home directory: %w", err)
	}

	dir := filepath.Join(home, ".rakitsu", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create sessions directory: %w", err)
	}

	return &SessionStore{dir: dir}, nil
}

// StartSession begins recording a new session.
func (s *SessionStore) StartSession(meta SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta.ID = uuid.New().String()
	meta.StartTime = time.Now()
	meta.Status = SessionRunning

	// Open JSONL file
	path := filepath.Join(s.dir, meta.ID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cannot create session file: %w", err)
	}

	// Write metadata as first line
	line, err := json.Marshal(meta)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		f.Close()
		return err
	}

	s.file = f
	s.current = &meta
	s.eventCount = 0
	s.totalTokens = 0

	// Append to sessions index
	return s.appendToIndex(meta)
}

// WriteEvent writes an event to the current session JSONL file.
func (s *SessionStore) WriteEvent(event telemetry.AgentEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		return
	}

	event.Payload = telemetry.RedactEventPayload(event.EventType, event.Payload)

	line, err := json.Marshal(event)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session store: marshal error: %v\n", err)
		return
	}

	if _, err := fmt.Fprintf(s.file, "%s\n", line); err != nil {
		fmt.Fprintf(os.Stderr, "session store: write error: %v\n", err)
		return
	}

	s.eventCount++
	if event.TokenUsage != nil {
		s.totalTokens += event.TokenUsage.TotalTokens
	}
}

// EndSession finalizes the current session with status and totals.
func (s *SessionStore) EndSession(status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		return
	}

	now := time.Now()
	s.current.EndTime = &now
	s.current.Status = status
	s.current.TotalEvents = s.eventCount
	s.current.TotalTokens = s.totalTokens
	s.current.DurationMs = now.Sub(s.current.StartTime).Milliseconds()

	if s.file != nil {
		// Write SESSION_END as the last JSONL line so consumers reading the
		// stream alone (without sessions.json) can determine final status from
		// the last line. Line-1 SessionMeta stays Status:"running" forever
		// because JSONL is append-only.
		if payload, err := json.Marshal(telemetry.SessionEndPayload{
			Status:      telemetry.SessionEndStatus(status),
			TotalEvents: s.eventCount,
			TotalTokens: s.totalTokens,
			DurationMs:  s.current.DurationMs,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "session store: SESSION_END payload marshal error: %v\n", err)
		} else {
			endEvent := telemetry.AgentEvent{
				ID:        uuid.New().String(),
				Timestamp: now,
				EventType: telemetry.EventSessionEnd,
				SessionID: s.current.ID,
				Payload:   payload,
			}
			if line, err := json.Marshal(endEvent); err != nil {
				fmt.Fprintf(os.Stderr, "session store: SESSION_END marshal error: %v\n", err)
			} else if _, err := fmt.Fprintf(s.file, "%s\n", line); err != nil {
				fmt.Fprintf(os.Stderr, "session store: SESSION_END write error: %v\n", err)
			}
		}

		s.file.Close()
		s.file = nil
	}

	// Update index with final metadata
	if err := s.updateIndex(*s.current); err != nil {
		fmt.Fprintf(os.Stderr, "session store: index update error: %v\n", err)
	}

	s.current = nil
}

// CurrentSessionID returns the ID of the active session, if any.
func (s *SessionStore) CurrentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return ""
	}
	return s.current.ID
}

// ListSessions returns all sessions, newest first.
func (s *SessionStore) ListSessions() ([]SessionMeta, error) {
	// readIndex is held under s.mu for its whole call, not just the
	// s.current.ID read afterward — StartSession/EndSession/DeleteSession
	// all write sessions.json under this same lock, and an unlocked read
	// landing mid-write could otherwise observe a truncated file.
	s.mu.Lock()
	sessions, err := s.readIndex()
	currentID := ""
	if s.current != nil {
		currentID = s.current.ID
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// Mark orphaned "running" sessions as stale (not the current active session)
	for i := range sessions {
		if sessions[i].Status == SessionRunning && sessions[i].ID != currentID {
			sessions[i].Status = SessionStale
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	return sessions, nil
}

// GetSession returns metadata for a single session. Applies the same
// orphaned-"running"-session reclassification as ListSessions, so a session
// left "running" by a crashed process (any session ID other than the
// current active one) reports as stale here too, instead of GetSession and
// ListSessions disagreeing about the same session's status.
func (s *SessionStore) GetSession(id string) (*SessionMeta, error) {
	// Same locked-readIndex reasoning as ListSessions above.
	s.mu.Lock()
	sessions, err := s.readIndex()
	currentID := ""
	if s.current != nil {
		currentID = s.current.ID
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	for i := range sessions {
		if sessions[i].ID == id {
			meta := sessions[i]
			if meta.Status == SessionRunning && meta.ID != currentID {
				meta.Status = SessionStale
			}
			return &meta, nil
		}
	}
	return nil, fmt.Errorf("session not found: %s", id)
}

// GetSessionEvents returns all agent execution events for a session by
// reading its JSONL file. The line-1 SessionMeta and the trailing SESSION_END
// marker are both excluded — the returned slice contains only events that
// were emitted via WriteEvent during agent execution. With this filter,
// len(events) == SessionEndPayload.TotalEvents.
func (s *SessionStore) GetSessionEvents(id string) ([]telemetry.AgentEvent, error) {
	path := filepath.Join(s.dir, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session file not found: %w", err)
	}
	defer f.Close()

	var events []telemetry.AgentEvent
	dec := json.NewDecoder(f)

	// Skip first object (session metadata line)
	var skip json.RawMessage
	if err := dec.Decode(&skip); err != nil {
		return events, nil // empty or unreadable file
	}

	for dec.More() {
		var event telemetry.AgentEvent
		if err := dec.Decode(&event); err != nil {
			continue // skip malformed entries
		}
		if event.EventType == telemetry.EventSessionEnd {
			continue // session-lifecycle marker, not an agent event
		}
		events = append(events, event)
	}

	return events, nil
}

// --- index helpers ---

func (s *SessionStore) indexPath() string {
	return filepath.Join(s.dir, "sessions.json")
}

func (s *SessionStore) readIndex() ([]SessionMeta, error) {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []SessionMeta
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("corrupt sessions index: %w", err)
	}
	return sessions, nil
}

func (s *SessionStore) lockPath() string {
	return s.indexPath() + ".lock"
}

func (s *SessionStore) writeIndex(sessions []SessionMeta) error {
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	// tmp+rename, same as WriteCheckpoint below — a plain os.WriteFile
	// truncates the file before writing the new content, so a reader
	// landing mid-write (even one holding s.mu, briefly, between the
	// truncate and the write completing) could observe a corrupt file.
	//
	// The tmp filename includes pid+timestamp rather than a fixed
	// ".tmp" suffix: two separate rakitsu processes both writing the
	// index around the same time (rakitsu serve + rakitsu run is the
	// documented workflow) previously raced to the *same* tmp path, and
	// their writes to that one shared file could interleave before
	// either rename happened — producing exactly the kind of torn,
	// invalid-JSON sessions.json this was found reproducing in practice
	// (a valid array immediately followed by a fragment of another
	// entry). A unique tmp name per writer removes that shared target
	// entirely, independent of the lockFile call in appendToIndex/
	// updateIndex below.
	path := s.indexPath()
	tmp := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write index: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write index: rename: %w", err)
	}
	return nil
}

// appendToIndex and updateIndex each read-modify-write the entire index.
// SessionStore's sync.Mutex only serializes goroutines within this one
// process; a second rakitsu process doing the same read-modify-write around
// the same time would otherwise see a stale read and clobber the first
// process's write (a lost update — a session silently vanishing from
// `rakitsu sessions`). lockFile takes a cross-process advisory lock around
// the whole cycle so concurrent processes serialize instead of racing.

func (s *SessionStore) appendToIndex(meta SessionMeta) error {
	return lockFile(s.lockPath(), func() error {
		sessions, err := s.readIndex()
		if err != nil {
			return err
		}
		sessions = append(sessions, meta)
		return s.writeIndex(sessions)
	})
}

func (s *SessionStore) updateIndex(meta SessionMeta) error {
	return lockFile(s.lockPath(), func() error {
		sessions, err := s.readIndex()
		if err != nil {
			return err
		}

		for i := range sessions {
			if sessions[i].ID == meta.ID {
				sessions[i] = meta
				return s.writeIndex(sessions)
			}
		}

		// Not found — append (shouldn't happen but safe fallback)
		sessions = append(sessions, meta)
		return s.writeIndex(sessions)
	})
}

// DeleteSession removes a session, its JSONL file, checkpoint file, and chat
// tree blob.
func (s *SessionStore) DeleteSession(id string) error {
	// Sanitize the id to prevent path traversal
	if strings.ContainsAny(id, "/\\..") {
		return fmt.Errorf("invalid session id")
	}

	// Remove JSONL file. Unlocked: nothing writes new content to an already-
	// ended session's .jsonl after EndSession finishes, so there's no
	// concurrent writer to race here.
	path := filepath.Join(s.dir, id+".jsonl")
	os.Remove(path)

	// Remove chat tree blob if present (best-effort) — like the checkpoint
	// file below, this is an out-of-band artifact keyed by session id that
	// isn't tracked in the JSONL or index, and would otherwise be left
	// orphaned on disk forever once the session itself is deleted.
	// Unlocked, and stays that way: SaveChatTree is deliberately lock-free
	// (its own doc comment — safe to call from any goroutine, independent
	// of the JSONL lifecycle) so a delete racing a concurrent SaveChatTree
	// for the same id can still leave a recreated .chat.json behind. s.mu
	// is a single store-wide mutex; making SaveChatTree take it to close
	// this would serialize every concurrent chat-tree save across every
	// live session, not just this one — out of scope for this fix.
	if chatPath, err := s.chatTreePath(id); err == nil {
		os.Remove(chatPath)
	}

	// Update index and remove the checkpoint file. Both locked so they're
	// properly serialized against a concurrent StartSession/EndSession
	// (which mutate sessions.json under s.mu via appendToIndex/updateIndex),
	// a concurrent DeleteSession, and — for the checkpoint file specifically
	// — a concurrent WriteCheckpoint (same mutex, held for its whole body).
	// Without holding the lock across the checkpoint removal too, a
	// WriteCheckpoint racing in the window between an unlocked os.Remove and
	// this Lock() call could recreate the file for a session that's mid-
	// deletion, orphaning it with no index entry pointing at it.
	s.mu.Lock()
	defer s.mu.Unlock()

	os.Remove(filepath.Join(s.dir, id+".checkpoint.json"))

	// If the deleted session is still the active one (a still-running
	// pipeline step, deleted out from under itself, before EndSession), drop
	// the stale pointer now — otherwise a later WriteCheckpoint recreates the
	// checkpoint file just removed above, and EndSession re-adds the session
	// to the index via updateIndex's append-if-not-found fallback. Also
	// close s.file here: WriteEvent only checks s.file == nil (not
	// s.current), so leaving it open would keep appending events into the
	// just-unlinked JSONL file, and EndSession's own close would never run
	// since it returns early on s.current == nil.
	if s.current != nil && s.current.ID == id {
		s.current = nil
		if s.file != nil {
			s.file.Close()
			s.file = nil
		}
	}

	// Same cross-process race as appendToIndex/updateIndex: this is a
	// read-modify-write of the whole index, so it needs the same
	// lockFile serialization against a concurrent process's own
	// appendToIndex/updateIndex/DeleteSession call.
	return lockFile(s.lockPath(), func() error {
		sessions, err := s.readIndex()
		if err != nil {
			return err
		}

		filtered := sessions[:0]
		for _, sess := range sessions {
			if sess.ID != id {
				filtered = append(filtered, sess)
			}
		}

		return s.writeIndex(filtered)
	})
}

// --- chat tree helpers ---

// chatTreePath resolves the on-disk path for a chat session's persisted
// branching turn history. Sanitizes the id against path-traversal — same
// guard DeleteSession/LoadCheckpoint use.
func (s *SessionStore) chatTreePath(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("invalid session id")
	}
	if strings.ContainsAny(id, "/\\..") {
		return "", fmt.Errorf("invalid session id")
	}
	return filepath.Join(s.dir, id+".chat.json"), nil
}

// SaveChatTree atomically writes a chat session's branching turn history
// blob to ~/.rakitsu/sessions/{id}.chat.json. Tmp+rename guarantees readers
// never see a half-written file. The bytes are opaque to the store; the
// server layer owns marshaling.
//
// Independent of the JSONL session lifecycle: safe to call without an
// active StartSession, and from any goroutine.
func (s *SessionStore) SaveChatTree(id string, raw []byte) error {
	path, err := s.chatTreePath(id)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("chat tree: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("chat tree: rename: %w", err)
	}
	return nil
}

// maxChatTreeSize caps how large a .chat.json blob LoadChatTree will
// read into memory. Defense in depth: a corrupt or accidentally
// unbounded tree (e.g. a producer bug that loops AppendTurn) should
// fail loud at load time instead of OOM'ing the serve process on
// resume. 64 MiB comfortably accommodates a 50k-turn conversation with
// 1 KB-average UserText + entries; well above any realistic chat.
const maxChatTreeSize = 64 << 20

// LoadChatTree reads the persisted chat tree blob for a session. The
// underlying os.ReadFile error is returned unwrapped on miss so callers
// can detect "no prior chat" via errors.Is(err, os.ErrNotExist). Files
// larger than maxChatTreeSize are rejected before any memory is
// allocated to read them.
func (s *SessionStore) LoadChatTree(id string) ([]byte, error) {
	path, err := s.chatTreePath(id)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxChatTreeSize {
		return nil, fmt.Errorf("chat tree: %s is %d bytes (exceeds %d-byte cap)", path, info.Size(), maxChatTreeSize)
	}
	return os.ReadFile(path)
}

// --- checkpoint helpers ---

// StoreCheckpointData is the on-disk JSON representation of pipeline checkpoint state.
type StoreCheckpointData struct {
	SessionID      string                     `json:"session_id"`
	Query          string                     `json:"query"`
	CompletedSteps []string                   `json:"completed_steps"`
	Results        map[string]StoreStepResult `json:"results"`
}

// StoreStepResult is the persisted form of one completed pipeline step.
type StoreStepResult struct {
	Name       string `json:"name"`
	Output     string `json:"output"`
	DurationNs int64  `json:"duration_ns"`
}

// WriteCheckpoint atomically writes (or overwrites) the checkpoint file for the
// current session. Safe to call after every pipeline step.
//
// Held for the whole write, not just the s.current.ID read: multiple
// pipeline steps can call this concurrently for the same session, and they
// all share the same ".tmp" path (see below), so without the lock two
// overlapping writes could stomp on each other's tmp file before either
// rename runs. This also serializes against DeleteSession, which now takes
// s.mu around its own checkpoint-file removal.
func (s *SessionStore) WriteCheckpoint(data StoreCheckpointData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		return fmt.Errorf("no active session")
	}

	b, err := json.Marshal(data)
	if err != nil {
		return err
	}

	// Tmp+rename — same atomic pattern as SaveChatTree — instead of a
	// direct os.WriteFile (which truncates then writes in place). Without
	// it, LoadCheckpoint — which reads by session id with no lock of its
	// own, since a checkpoint can be loaded for any session, not just the
	// current one — could observe a half-written file if it runs mid-write.
	path := filepath.Join(s.dir, s.current.ID+".checkpoint.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return fmt.Errorf("checkpoint: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}

// HasCheckpoint returns true when a resumable checkpoint file exists for the session.
func (s *SessionStore) HasCheckpoint(sessionID string) bool {
	if strings.ContainsAny(sessionID, "/\\..") {
		return false
	}
	path := filepath.Join(s.dir, sessionID+".checkpoint.json")
	_, err := os.Stat(path)
	return err == nil
}

// LoadCheckpoint reads the checkpoint file for the given session ID.
// Returns an error wrapping os.ErrNotExist if no checkpoint exists.
func (s *SessionStore) LoadCheckpoint(sessionID string) (*StoreCheckpointData, error) {
	if strings.ContainsAny(sessionID, "/\\..") {
		return nil, fmt.Errorf("invalid session id")
	}
	path := filepath.Join(s.dir, sessionID+".checkpoint.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("checkpoint not found for session %s: %w", sessionID, err)
	}
	var data StoreCheckpointData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("corrupt checkpoint for session %s: %w", sessionID, err)
	}
	return &data, nil
}
