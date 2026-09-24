package chat

import (
	"testing"
	"time"
)

// TestTurnContext: --timeout in chat mode bounds each turn, and no
// timeout means no deadline.
func TestTurnContext(t *testing.T) {
	m := Model{turnTimeout: time.Minute}
	ctx, cancel := m.turnContext()
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("turnTimeout > 0: expected a per-turn deadline, got none")
	}

	m = Model{}
	ctx2, cancel2 := m.turnContext()
	defer cancel2()
	if _, ok := ctx2.Deadline(); ok {
		t.Fatal("no turnTimeout: expected no deadline, got one")
	}
}
