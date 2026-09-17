package server

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/SK-ENT/rakitsu/internal/turntree"
)

// chatTreeSchemaVersion is the version tag stamped onto every persisted
// .chat.json file. Loaders reject blobs from a newer schema so a downgrade
// fails loudly instead of silently dropping unknown fields. Bump only when
// the on-disk shape changes in a way readers must discriminate — additive
// fields with sensible zero-value defaults don't require a bump.
const chatTreeSchemaVersion = 1

// ChatTreeFile is the on-disk envelope for a chat session's branching turn
// history. It wraps the generic turntree.Tree[transcriptEntry] with the
// session identity metadata needed to rehydrate a ChatSession after a
// `rakitsu serve` restart, and is the unit that Save/Load operate on.
type ChatTreeFile struct {
	SchemaVersion int                             `json:"schema_version"`
	SessionID     string                          `json:"session_id"`
	ConfigID      string                          `json:"config_id,omitempty"`
	AgentName     string                          `json:"agent_name,omitempty"`
	Model         string                          `json:"model,omitempty"`
	Created       time.Time                       `json:"created"`
	Tree          *turntree.Tree[transcriptEntry] `json:"tree"`
}

// marshalChatTreeLocked serializes the session's authoritative branching
// turn history into a ChatTreeFile blob. MUST be called with s.mu held so
// the tree snapshot is internally consistent.
func (s *ChatSession) marshalChatTreeLocked() ([]byte, error) {
	f := ChatTreeFile{
		SchemaVersion: chatTreeSchemaVersion,
		SessionID:     s.ID,
		ConfigID:      s.configID,
		AgentName:     s.agentName,
		Model:         s.modelLabel,
		Created:       s.created,
		Tree:          s.tree,
	}
	return json.Marshal(f)
}

// LoadChatTreeFile parses a persisted .chat.json blob, validates its
// schema_version, and returns the envelope with tree maps hydrated. A
// missing tree (e.g. a placeholder save before the first turn) is replaced
// with an empty tree so callers can use the result unconditionally.
func LoadChatTreeFile(raw []byte) (*ChatTreeFile, error) {
	var f ChatTreeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("chat tree: parse: %w", err)
	}
	if f.SchemaVersion > chatTreeSchemaVersion {
		return nil, fmt.Errorf("chat tree: unsupported schema_version %d (max %d)", f.SchemaVersion, chatTreeSchemaVersion)
	}
	if f.Tree == nil {
		f.Tree = turntree.New[transcriptEntry]()
	}
	f.Tree.Hydrate()
	return &f, nil
}

// persistTree snapshots the session's tree under s.mu and writes the blob
// to the session store outside the lock so disk I/O does not stall other
// session operations. Best-effort: a marshal or write failure is logged to
// stderr but does not abort the in-memory session.
//
// Called after every terminal mutation that should survive a restart — turn
// finalize here, and branch ops via broadcastTreeMutationLocked
// (chat_branch.go). Both funnel their actual disk write through
// persistTreeRaw so the two independent snapshot-then-write paths can't
// land out of order on disk.
func (s *ChatSession) persistTree() {
	if s.sessionStore == nil {
		return
	}
	s.mu.Lock()
	raw, gen, err := s.snapshotForPersistLocked()
	s.mu.Unlock()
	if err != nil {
		log.Printf("chat session %s: marshal tree: %v", s.ID, err)
		return
	}
	s.persistTreeRaw(raw, gen)
}

// snapshotForPersistLocked marshals the tree and bumps the persistence
// generation counter, both under s.mu — so two concurrent mutations
// (e.g. a branch op racing a turn finalize) get a strictly increasing
// generation sequence matching the actual order their mutations happened
// in, not the order their eventual disk writes happen to complete in.
// Caller holds s.mu.
func (s *ChatSession) snapshotForPersistLocked() ([]byte, uint64, error) {
	raw, err := s.marshalChatTreeLocked()
	s.persistSeq++
	return raw, s.persistSeq, err
}

// persistTreeRaw writes raw to the session store, but only if gen is newer
// than the last generation actually written — an older, now-stale snapshot
// arriving after a newer one (because its disk I/O simply took longer) is
// dropped instead of silently reverting the on-disk state. Safe to call
// from either persistTree or broadcastTreeMutationLocked; persistMu
// serializes the read-check-write of persistGen across both callers.
func (s *ChatSession) persistTreeRaw(raw []byte, gen uint64) {
	if s.sessionStore == nil {
		return
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if gen <= s.persistGen {
		return
	}
	if err := s.sessionStore.SaveChatTree(s.ID, raw); err != nil {
		log.Printf("chat session %s: persist tree: %v", s.ID, err)
		return
	}
	s.persistGen = gen
}
