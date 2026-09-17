package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/chat"
)

// TestShutdownChatDrainsSendsBeforeCleanup is the safety-critical ordering.
// bubbletea launches Cmds detached and never waits on them, so exit can arrive
// with a directed Send mid-tool-call; running cleanup first closes the tool
// instances — MCP subprocesses included — that the send is holding. The order
// used to live only in the LIFO relationship between two defers, which nothing
// exercised: swapping them left every test green and fully restored the race.
func TestShutdownChatDrainsSendsBeforeCleanup(t *testing.T) {
	sends := &chat.InFlight{}
	ctx, _, done := sends.Begin(context.Background())

	var mu sync.Mutex
	var order []string
	record := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, what)
	}

	// A well-behaved send: it returns only once its context is cancelled.
	go func() {
		<-ctx.Done()
		record("send-returned")
		done()
	}()

	shutdownChat(sends, func() { record("cleanup") })

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "send-returned" || order[1] != "cleanup" {
		t.Fatalf("shutdown order = %v, want [send-returned cleanup] — cleanup must not close the tools a live send is holding", order)
	}
}

// TestShutdownChatIsBounded: a send that ignores its cancellation must not
// hang the terminal. The wait gives up and cleanup runs anyway.
func TestShutdownChatIsBounded(t *testing.T) {
	sends := &chat.InFlight{}
	_, _, _ = sends.Begin(context.Background()) // never calls done

	cleaned := make(chan struct{})
	go func() {
		shutdownChat(sends, func() { close(cleaned) })
	}()

	select {
	case <-cleaned:
	case <-time.After(sendDrainTimeout + 5*time.Second):
		t.Fatal("shutdown never completed — a send that ignores its context hung the exit path")
	}
}

// TestShutdownChatWithNoSendsStillCleansUp: the common case is a session that
// never opened a side chat at all.
func TestShutdownChatWithNoSendsStillCleansUp(t *testing.T) {
	var cleaned bool
	shutdownChat(&chat.InFlight{}, func() { cleaned = true })
	if !cleaned {
		t.Error("cleanup must run even when nothing was in flight")
	}
}
