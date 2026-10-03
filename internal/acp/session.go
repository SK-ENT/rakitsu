// Package acp implements an ACP (Agent Client Protocol) server.
// ACP is JSON-RPC 2.0 over stdio, adopted by JetBrains, Zed, Cursor, and others
// as a standard protocol for invoking coding agents from IDEs.
package acp

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// historyMaxTurns bounds how many prior turns are threaded into the next
	// session/prompt call. Mirrors internal/chat/history.go's identical
	// constant for the interactive chat TUI — kept as a separate, ACP-local
	// copy rather than a shared import: that package pulls in the
	// bubbletea/lipgloss TUI dependency tree, which a headless protocol
	// server has no other reason to depend on.
	historyMaxTurns = 10
	// historyMaxResponseChars truncates a long stored response so an
	// enormous prior answer doesn't blow the next turn's context window.
	historyMaxResponseChars = 2000
)

// Session tracks one ACP session: a conversation identity a client can send
// multiple session/prompt turns against. Unlike the old agent/run model
// (one Session = one run, created together with that run's cancel func and
// polled to completion via agent/status), real ACP sessions outlive any
// single turn — session/new mints the identity before any prompt exists, and
// a session/prompt can be sent against it more than once. So a Session's job
// here is narrower: hold the *current* turn's cancel func (so a
// session/cancel notification has something to call), remember whether that
// turn was cancelled (so the turn's own completion path can tell a
// cancellation-caused error apart from a real failure), and accumulate prior
// turns' query/response text — executeConfig (the RunFunc every turn calls)
// has no native multi-turn API, so composeQuery below is what makes several
// session/prompt calls against one session feel like a continuous
// conversation instead of independent, context-free runs.
type Session struct {
	ID string

	mu        sync.Mutex
	cancel    context.CancelFunc // set for the duration of the in-flight session/prompt call; nil when idle
	cancelled bool               // sticky for the current/just-finished turn; cleared by the next startTurn
	history   []promptTurn       // recorded session/prompt turns, oldest first
	conv      *sessionConv       // settings.memory.conversation state; nil until first engaged turn (see conversation.go)
	pins      int                // lookups held by in-progress handlePrompt calls; pinned sessions are never evicted
	lastUsed  time.Time          // last add/get/touch; drives sessionMap eviction
}

// promptTurn is one session/prompt request/response pair, recorded so a
// later turn on the same session can see what was already discussed.
type promptTurn struct {
	query    string
	response string
}

func newSession(id string) *Session {
	return &Session{ID: id}
}

func (s *Session) touch(t time.Time) {
	s.mu.Lock()
	s.lastUsed = t
	s.mu.Unlock()
}

// activity reports when the session was last used and whether a prompt is
// in flight.
func (s *Session) activity() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed, s.cancel != nil || s.pins > 0
}

// startTurn attempts to arm the session for a new in-flight session/prompt
// call, clearing any cancelled flag left over from a prior turn — a
// session/cancel aimed at an already-finished turn must not bleed into the
// next one. Returns false, changing nothing, if a turn is already in flight:
// real ACP sessions serve one prompt turn at a time, and the caller must
// reject an overlapping session/prompt rather than let a second startTurn
// silently overwrite the first turn's cancel func (which endTurn could then
// nil out from under the wrong turn).
func (s *Session) startTurn(cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return false
	}
	s.cancel = cancel
	s.cancelled = false
	return true
}

// endTurn disarms the cancel func once the in-flight session/prompt call has
// returned — a session/cancel arriving after this point has nothing to call.
func (s *Session) endTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
}

// requestCancel calls the current turn's cancel func, if one is in flight,
// and marks the session cancelled either way.
func (s *Session) requestCancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancelled = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wasCancelled reports whether session/cancel was called for the current (or
// just-finished) turn.
func (s *Session) wasCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

// composeQuery returns query prefixed with this session's recorded prior
// turns, in the same "Previous conversation: ... Current question: ..."
// shape internal/chat/history.go's buildQueryWithHistory uses for the
// interactive chat TUI — the format that's already proven to work with the
// agents in this codebase. Returns query unchanged when there's no history
// yet (the common case: a session's first turn).
func (s *Session) composeQuery(query string) string {
	s.mu.Lock()
	turns := append([]promptTurn(nil), s.history...) // copy out under lock
	s.mu.Unlock()

	if len(turns) == 0 {
		return query
	}

	var sb strings.Builder
	sb.WriteString("Previous conversation:\n")
	for _, t := range turns {
		if t.query != "" {
			sb.WriteString("User: ")
			sb.WriteString(t.query)
			sb.WriteString("\n")
		}
		if t.response != "" {
			sb.WriteString("Assistant: ")
			sb.WriteString(t.response)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nCurrent question:\n")
	sb.WriteString(query)
	return sb.String()
}

// recordTurn appends this turn's original (uncomposed) query and its
// response to the session's history, so a later session/prompt can see it
// via composeQuery. Called for every outcome — success, error, cancelled,
// or panic — not just success: without something recorded for a failed or
// cancelled turn, the next turn's composed history has a silent gap the
// model can't account for (mirrors internal/chat/model.go's identical
// reasoning for always recording an assistant entry, error placeholder
// included).
//
// Trims to historyMaxTurns and truncates both query and response to
// historyMaxResponseChars right here at store time, so the
// untruncated/unbounded text isn't retained at all: composeQuery only ever
// reads what's already bounded, rather than re-deriving the bound on every
// call from an unbounded slice. query gets the same bound as response
// since it's replayed into every one of the next historyMaxTurns prompts
// otherwise — a pasted diff in the query is exactly as capable of blowing
// the context window as a long model answer.
func (s *Session) recordTurn(query, response string) {
	query = truncateHistoryText(query)
	response = truncateHistoryText(response)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, promptTurn{query: query, response: response})
	if len(s.history) > historyMaxTurns {
		s.history = s.history[len(s.history)-historyMaxTurns:]
	}
}

// truncateHistoryText bounds s to historyMaxResponseChars runes (not bytes —
// CJK/emoji text is multi-byte per rune, and slicing on bytes can cut
// mid-rune, corrupting the string). A cheap byte-length check short-circuits
// the common case of a short string with no []rune allocation: every rune is
// at least one byte, so len(s) <= historyMaxResponseChars in bytes already
// guarantees the rune count is within the limit too.
func truncateHistoryText(s string) string {
	if len(s) <= historyMaxResponseChars {
		return s
	}
	if rs := []rune(s); len(rs) > historyMaxResponseChars {
		s = string(rs[:historyMaxResponseChars]) + "...[truncated]"
	}
	return s
}

const (
	// defaultMaxSessions caps how many sessions one serve process retains.
	defaultMaxSessions = 256
	// defaultSessionIdleTTL is how long a session may sit unused before it
	// becomes evictable.
	defaultSessionIdleTTL = 24 * time.Hour
)

// sessionMap is a thread-safe registry of known sessions with bounded
// retention. Eviction is lazy (runs inside add, no background goroutine):
// sessions idle longer than idleTTL go first, then, if still over
// maxSessions, the least recently used idle ones. A session with an
// in-flight prompt is never evicted, so the cap can be exceeded temporarily
// when every session is busy. maxSessions/idleTTL <= 0 disable that bound.
type sessionMap struct {
	mu          sync.Mutex
	data        map[string]*Session
	now         func() time.Time
	maxSessions int
	idleTTL     time.Duration
}

func newSessionMap() *sessionMap {
	return &sessionMap{
		data:        make(map[string]*Session),
		now:         time.Now,
		maxSessions: defaultMaxSessions,
		idleTTL:     defaultSessionIdleTTL,
	}
}

func (m *sessionMap) add(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	s.touch(now)
	m.data[s.ID] = s
	m.evictLocked(now, s.ID)
}

// get returns the session and refreshes its idle clock.
func (m *sessionMap) get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.data[id]
	if ok {
		s.touch(m.now())
	}
	return s, ok
}

// acquire is get plus a pin taken under the same map lock, so no eviction can
// slip between lookup and the session being marked in flight by startTurn.
// The caller must call release (typically via defer) when done. Returns nil,
// nil if the session does not exist.
func (m *sessionMap) acquire(id string) (*Session, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.data[id]
	if !ok {
		return nil, nil
	}
	now := m.now()
	s.mu.Lock()
	s.pins++
	s.lastUsed = now
	s.mu.Unlock()
	var once sync.Once
	return s, func() {
		once.Do(func() {
			s.mu.Lock()
			s.pins--
			s.mu.Unlock()
		})
	}
}

func (m *sessionMap) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.data)
}

// evictLocked drops expired, then excess, idle sessions. keep is never
// evicted (the session just added). Caller holds m.mu.
func (m *sessionMap) evictLocked(now time.Time, keep string) {
	if m.idleTTL > 0 {
		for id, s := range m.data {
			if id == keep {
				continue
			}
			if last, busy := s.activity(); !busy && now.Sub(last) > m.idleTTL {
				delete(m.data, id)
			}
		}
	}
	for m.maxSessions > 0 && len(m.data) > m.maxSessions {
		var victim string
		var oldest time.Time
		for id, s := range m.data {
			if id == keep {
				continue
			}
			last, busy := s.activity()
			if busy {
				continue
			}
			if victim == "" || last.Before(oldest) {
				victim, oldest = id, last
			}
		}
		if victim == "" {
			return // everything left is in flight
		}
		delete(m.data, victim)
	}
}
