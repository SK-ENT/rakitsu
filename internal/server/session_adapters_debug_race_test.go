package server

import (
	"testing"

	"github.com/SK-ENT/rakitsu/internal/debug"
)

// lockCheckingRunner is a test double that, on the detach path (dc == nil),
// checks whether the adapter's dbgMu is still held at the moment
// SetDebugController is called. Deterministic and single-threaded — no
// goroutine racing needed, since the property under test ("is the lock
// still held during this call") doesn't depend on OS scheduling timing the
// way reproducing the actual interleaved bug via concurrent goroutines
// would (that window is narrow enough that raw concurrent stress testing
// didn't reliably reproduce it — see the removed iteration-based attempt).
type lockCheckingRunner struct {
	fakeRunner
	adapter              *chatSessionAdapter
	sawUnlockedDuringSet bool
}

func (r *lockCheckingRunner) SetDebugController(dc *debug.DebugController) {
	if dc == nil {
		// TryLock succeeding means dbgMu was NOT held by the caller —
		// exactly the window a concurrent AttachDebugger could slip into.
		if r.adapter.dbgMu.TryLock() {
			r.sawUnlockedDuringSet = true
			r.adapter.dbgMu.Unlock()
		}
	}
}

// TestChatSessionAdapter_DetachDebugger_HoldsLockAcrossSetDebugController
// regression-guards: DetachDebugger cleared a.dc and released dbgMu BEFORE
// calling SetDebugController(nil), instead of doing both under the same
// lock like AttachDebugger does. A concurrent AttachDebugger could fully
// complete (a.dc = newDC, session wired to newDC) inside that window, and
// Detach's now-delayed SetDebugController(nil) would run last — wiping the
// session's live controller back to nil while a.dc still (correctly, from
// the adapter's own bookkeeping) points at newDC.
func TestChatSessionAdapter_DetachDebugger_HoldsLockAcrossSetDebugController(t *testing.T) {
	lr := &lockCheckingRunner{fakeRunner: fakeRunner{name: "a1"}}
	sess := &ChatSession{runner: lr}
	adapter := &chatSessionAdapter{sess: sess}
	lr.adapter = adapter

	if _, err := adapter.AttachDebugger(nil); err != nil {
		t.Fatalf("AttachDebugger: %v", err)
	}
	if err := adapter.DetachDebugger(); err != nil {
		t.Fatalf("DetachDebugger: %v", err)
	}

	if lr.sawUnlockedDuringSet {
		t.Error("DetachDebugger called SetDebugController(nil) after releasing dbgMu — a concurrent AttachDebugger could attach a new controller in that window and have it silently wiped out")
	}
}
