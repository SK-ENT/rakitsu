package permission

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxPromptsPerMinute = 10
	rateWindow                 = time.Minute
	coalesceWindow             = 10 * time.Second
)

// GrantKey identifies an action class that a session grant covers. The
// empty key is never grantable.
type GrantKey string

// grantView is the read-only view of grants the checker sees.
type grantView interface{ Has(GrantKey) bool }

// SessionAllowed reports whether a request may be offered "allow for this
// session": never for risky classes (including unknown risk values),
// persistence points, unknown kinds, or a cli request whose binary identity
// was not captured.
func SessionAllowed(req Request) bool {
	if !req.Kind.known() || req.Risk != RiskNone || req.has(ReasonPersistencePoint) {
		return false
	}
	if req.Kind == KindCLIExec && req.Binary.IsZero() {
		return false
	}
	return true
}

// keyMaterial is the bound part of a grant key or fingerprint.
type keyMaterial struct {
	Kind        Kind
	Tool        string
	Boundary    string
	BinaryPath  string
	BinaryID    string
	BinarySize  int64
	BinaryMtime int64
	Command     []string
	Cwd         string
	Paths       []string
	EnvNames    []string
	Reasons     []string
	// Fingerprint-only fields (empty in grant keys).
	Argv []string
	Risk Risk
}

func sortedUnique(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	j := 0
	for i, s := range out {
		if i == 0 || s != out[j-1] {
			out[j] = s
			j++
		}
	}
	return out[:j]
}

// grantCommand is the command part of a grant key: the subcommand when
// argv[1] is a plain word ("git status --short" -> ["status"]), otherwise
// the exact argv[1:] (a leading flag may take a value, e.g. "git -C dir
// push", so a looser key could cover a different subcommand).
func grantCommand(argv []string) []string {
	if len(argv) < 2 {
		return []string{}
	}
	if !strings.HasPrefix(argv[1], "-") {
		return []string{argv[1]}
	}
	return append([]string{}, argv[1:]...)
}

func material(req Request) keyMaterial {
	m := keyMaterial{
		Kind:       req.Kind,
		Tool:       req.Tool,
		Boundary:   req.Boundary.Type,
		BinaryPath: req.BinaryPath,
		Command:    grantCommand(req.Argv),
		Cwd:        req.Cwd,
		Paths:      sortedUnique(req.Paths),
		EnvNames:   sortedUnique(req.EnvNames),
	}
	reasons := make([]string, len(req.Reasons))
	for i, r := range req.Reasons {
		reasons[i] = string(r)
	}
	m.Reasons = sortedUnique(reasons)
	if fi := req.Binary.info; fi != nil {
		m.BinaryID = fileIdentity(fi)
		m.BinarySize = fi.Size()
		m.BinaryMtime = fi.ModTime().UnixNano()
	}
	return m
}

// encode is injective: every string is Go-quoted (invalid UTF-8 bytes are
// escaped as \xNN, not replaced, unlike encoding/json) and every list
// carries its length, so no separator or byte trick can make two different
// actions share a key.
func encode(m keyMaterial) string {
	var b strings.Builder
	str := func(s string) {
		b.WriteString(strconv.Quote(s))
		b.WriteByte(' ')
	}
	list := func(l []string) {
		b.WriteString(strconv.Itoa(len(l)))
		b.WriteByte('[')
		for _, s := range l {
			str(s)
		}
		b.WriteString("] ")
	}
	str(string(m.Kind))
	str(m.Tool)
	str(m.Boundary)
	str(m.BinaryPath)
	str(m.BinaryID)
	str(strconv.FormatInt(m.BinarySize, 10))
	str(strconv.FormatInt(m.BinaryMtime, 10))
	list(m.Command)
	str(m.Cwd)
	list(m.Paths)
	list(m.EnvNames)
	list(m.Reasons)
	list(m.Argv)
	str(string(m.Risk))
	return b.String()
}

// KeyFor returns the session grant key of a request: kind, tool, boundary,
// binary path and identity, subcommand, cwd, offending paths, env names and
// reasons. It returns "" (never grantable) when SessionAllowed is false.
// Model-supplied fields (ToolCallID, Rationale) and the request ID are not
// part of the key.
func KeyFor(req Request) GrantKey {
	if !SessionAllowed(req) {
		return ""
	}
	return GrantKey(encode(material(req)))
}

// fingerprint identifies the exact action (full argv and risk), for
// coalescing repeated prompts. It exists for risky requests too.
func fingerprint(req Request) string {
	m := material(req)
	m.Argv = append([]string{}, req.Argv...)
	m.Risk = req.Risk
	return encode(m)
}

// SessionGrants holds the in-memory grants of one session, plus the prompt
// lock, recent denies and prompt rate window. Nothing is persisted.
type SessionGrants struct {
	mu           sync.Mutex
	keys         map[GrantKey]struct{}
	recentDenies map[string]time.Time
	prompts      []time.Time
	maxPerMinute int
	now          func() time.Time

	// promptLock serializes prompts for the session. It is a channel so a
	// waiter can give up when its context ends.
	promptLock chan struct{}
}

// NewSessionGrants returns empty grants. maxPromptsPerMinute <= 0 uses the
// default (10); now nil uses time.Now.
func NewSessionGrants(maxPromptsPerMinute int, now func() time.Time) *SessionGrants {
	if maxPromptsPerMinute <= 0 {
		maxPromptsPerMinute = defaultMaxPromptsPerMinute
	}
	if now == nil {
		now = time.Now
	}
	return &SessionGrants{
		keys:         map[GrantKey]struct{}{},
		recentDenies: map[string]time.Time{},
		maxPerMinute: maxPromptsPerMinute,
		now:          now,
		promptLock:   make(chan struct{}, 1),
	}
}

// Has reports whether key is granted. The empty key is never granted.
func (g *SessionGrants) Has(key GrantKey) bool {
	if g == nil || key == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.keys[key]
	return ok
}

// Add grants key for the rest of the session. The empty key is ignored.
func (g *SessionGrants) Add(key GrantKey) {
	if g == nil || key == "" {
		return
	}
	g.mu.Lock()
	g.keys[key] = struct{}{}
	g.mu.Unlock()
}

// lock takes the prompt lock, or returns false if ctx ends first.
func (g *SessionGrants) lock(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case g.promptLock <- struct{}{}:
		if ctx.Err() != nil { // cancelled while racing for the lock
			<-g.promptLock
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func (g *SessionGrants) unlock() { <-g.promptLock }

// takePromptSlot records a prompt if the per-minute limit allows it.
func (g *SessionGrants) takePromptSlot() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	kept := g.prompts[:0]
	for _, t := range g.prompts {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	g.prompts = kept
	if len(g.prompts) >= g.maxPerMinute {
		return false
	}
	g.prompts = append(g.prompts, now)
	return true
}

// recordDeny remembers a prompted deny for coalescing.
func (g *SessionGrants) recordDeny(fp string) {
	if fp == "" {
		return
	}
	g.mu.Lock()
	g.recentDenies[fp] = g.now()
	g.mu.Unlock()
}

// recentlyDenied reports whether fp was denied within the coalesce window.
func (g *SessionGrants) recentlyDenied(fp string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for k, t := range g.recentDenies {
		if now.Sub(t) >= coalesceWindow {
			delete(g.recentDenies, k)
		}
	}
	_, ok := g.recentDenies[fp]
	return ok
}
