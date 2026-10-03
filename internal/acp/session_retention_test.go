package acp

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestMap(maxSessions int, idle time.Duration) (*sessionMap, *fakeClock) {
	clk := &fakeClock{now: time.Unix(1_000_000, 0)}
	m := newSessionMap()
	m.now = clk.Now
	m.maxSessions = maxSessions
	m.idleTTL = idle
	return m, clk
}

func TestSessionMap_CapEvictsLeastRecentlyUsedIdle(t *testing.T) {
	m, clk := newTestMap(3, 0)
	for i := 0; i < 3; i++ {
		m.add(newSession(fmt.Sprintf("s%d", i)))
		clk.Advance(time.Second)
	}
	m.get("s0") // s0 is now most recently used; s1 is the oldest
	clk.Advance(time.Second)
	m.add(newSession("s3"))

	if _, ok := m.get("s1"); ok {
		t.Error("s1 (least recently used) should have been evicted")
	}
	for _, id := range []string{"s0", "s2", "s3"} {
		if _, ok := m.get(id); !ok {
			t.Errorf("%s should still be present", id)
		}
	}
	if m.len() != 3 {
		t.Errorf("len = %d, want 3", m.len())
	}
}

func TestSessionMap_IdleTTLEvictsOnAdd(t *testing.T) {
	m, clk := newTestMap(100, time.Hour)
	m.add(newSession("old"))
	clk.Advance(30 * time.Minute)
	m.add(newSession("mid"))
	clk.Advance(31 * time.Minute) // old idle 61m, mid idle 31m
	m.add(newSession("new"))

	if _, ok := m.get("old"); ok {
		t.Error("old should be evicted after exceeding idle TTL")
	}
	if _, ok := m.get("mid"); !ok {
		t.Error("mid is within TTL and must stay")
	}
}

func TestSessionMap_NeverEvictsInFlightSession(t *testing.T) {
	m, clk := newTestMap(2, time.Minute)
	busy := newSession("busy")
	m.add(busy)
	if !busy.startTurn(func() {}) {
		t.Fatal("startTurn failed")
	}
	clk.Advance(time.Hour) // far past TTL
	m.add(newSession("a"))
	m.add(newSession("b")) // over cap: only idle ones may go

	if _, ok := m.get("busy"); !ok {
		t.Fatal("in-flight session must never be evicted")
	}
	busy.endTurn()
}

func TestSessionMap_AllBusyOvershootsCapRatherThanEvicting(t *testing.T) {
	m, _ := newTestMap(2, 0)
	for i := 0; i < 3; i++ {
		s := newSession(fmt.Sprintf("s%d", i))
		m.add(s)
		s.startTurn(func() {})
	}
	if m.len() != 3 {
		t.Errorf("len = %d, want 3 (all in flight, none evictable)", m.len())
	}
}

func TestSessionMap_GetRefreshesIdleClock(t *testing.T) {
	m, clk := newTestMap(100, time.Hour)
	m.add(newSession("s"))
	clk.Advance(50 * time.Minute)
	m.get("s")
	clk.Advance(50 * time.Minute) // 100m since add, 50m since use
	m.add(newSession("other"))
	if _, ok := m.get("s"); !ok {
		t.Error("recently used session must survive TTL sweep")
	}
}

func TestSessionMap_DefaultsBounded(t *testing.T) {
	m := newSessionMap()
	if m.maxSessions <= 0 || m.idleTTL <= 0 {
		t.Errorf("defaults must bound the map, got max=%d ttl=%v", m.maxSessions, m.idleTTL)
	}
}

// Regression: handlePrompt looks a session up long before startTurn marks it
// busy. In that gap a concurrent add() at cap, with every other session busy,
// must not pick the just-fetched session as its only victim.
func TestSessionMap_AcquirePinsSessionBeforeStartTurn(t *testing.T) {
	m, clk := newTestMap(2, 0)
	busy := newSession("busy")
	m.add(busy)
	busy.startTurn(func() {})
	clk.Advance(time.Second)
	m.add(newSession("target"))

	sess, release := m.acquire("target") // fetched, startTurn not yet called
	if sess == nil {
		t.Fatal("acquire returned nil for an existing session")
	}
	clk.Advance(time.Second)
	m.add(newSession("newcomer")) // over cap; only unpinned idle sessions may go

	if _, ok := m.get("target"); !ok {
		t.Fatal("pinned session was evicted in the lookup-to-startTurn gap")
	}
	release()
	clk.Advance(time.Second)
	m.add(newSession("another"))
	if _, ok := m.get("target"); ok {
		t.Error("after release the session is an ordinary eviction candidate again")
	}
}

func TestSessionMap_AcquireUnknown(t *testing.T) {
	m, _ := newTestMap(2, 0)
	if s, _ := m.acquire("nope"); s != nil {
		t.Error("expected nil for unknown session")
	}
}
