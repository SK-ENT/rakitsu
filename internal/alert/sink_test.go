package alert

import (
	"context"
	"encoding/json"
	"github.com/SK-ENT/rakitsu/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.advance(d)
	return nil
}
func (f *fakeClock) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }
func testConfig() config.WakeAlerts {
	return config.WakeAlerts{RatePerHour: 2, Retry: config.AlertRetry{MaxAttempts: 3, BackoffSeconds: 1}, Sinks: []config.AlertSink{{Name: "test", Type: "webhook", URLEnv: "ALERT_TEST_URL", AllowHosts: []string{"127.0.0.1"}, MinSeverity: "warn"}}}
}
func wait(t *testing.T, ch <-chan message) message {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("delivery timed out")
		return message{}
	}
}
func TestSinkValidation(t *testing.T) {
	for _, raw := range []string{"", "https://example.com/a"} {
		n, err := NewNotifier(testConfig(), nil, Options{Getenv: func(string) string { return raw }})
		if err == nil || !strings.Contains(err.Error(), "ALERT_TEST_URL") {
			t.Fatalf("missing safe validation error: %v", err)
		}
		n.Close()
	}
}
func TestSinkDelivery(t *testing.T) {
	ch := make(chan message, 10)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var m message
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Error(err)
		}
		ch <- m
		w.WriteHeader(204)
	}))
	defer srv.Close()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	n, err := NewNotifier(testConfig(), []string{"private-value"}, Options{Clock: clock, Getenv: func(string) string { return srv.URL }})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	n.Send("info", "filtered", "filtered")
	n.Send("warn", "private-value", "private-value")
	m := wait(t, ch)
	if strings.Contains(m.Title+m.Body, "private-value") || !strings.Contains(m.Body, "[redacted]") {
		t.Fatalf("not redacted: %+v", m)
	}
	n.Send("critical", "second", "second")
	wait(t, ch)
	n.Send("warn", "third", "third")
	// A marker delivered to no sink is not sufficient to synchronize the worker;
	// inspect the worker-owned counters only after stopping it in the separate cap test.
	if err := n.Send("bad", "", ""); err == nil {
		t.Fatal("invalid severity accepted")
	}
}
func TestRetryAndSuccess(t *testing.T) {
	for _, status := range []int{500, 204, 400} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var count atomic.Int32
			ch := make(chan message, 10)
			failed := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(status); ch <- message{} }))
			defer srv.Close()
			n, err := NewNotifier(testConfig(), nil, Options{Clock: &fakeClock{now: time.Now()}, Getenv: func(string) string { return srv.URL }, OnFailed: func(_ string, e error) { failed <- e }})
			if err != nil {
				t.Fatal(err)
			}
			n.Send("warn", "title", "body")
			expected := int32(1)
			if status == 500 {
				expected = 3
			}
			for i := int32(0); i < expected; i++ {
				wait(t, ch)
			}
			if status != 204 {
				select {
				case <-failed:
				case <-time.After(3 * time.Second):
					t.Fatal("no failure callback")
				}
			}
			n.Close()
			if count.Load() != expected {
				t.Fatalf("requests=%d want %d", count.Load(), expected)
			}
		})
	}
}
func TestRateCapAndSummary(t *testing.T) {
	ch := make(chan message, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		json.NewDecoder(r.Body).Decode(&m)
		ch <- m
	}))
	defer srv.Close()
	clock := &fakeClock{now: time.Now()}
	n, _ := NewNotifier(testConfig(), nil, Options{Clock: clock, Getenv: func(string) string { return srv.URL }})
	defer n.Close()
	// Invoke the rate-limited worker with a barrier: a custom clock blocks only
	// after the suppressed message has been accounted for by polling under no race.
	n.Send("warn", "one", "")
	wait(t, ch)
	n.Send("warn", "two", "")
	wait(t, ch)
	// Stop the worker before inspecting its state, then exercise the next window
	// with a fresh worker over the same sink state.
	n.Send("warn", "three", "")
	// Queue a barrier through the clock is unnecessary: wait until queue is empty,
	// then Close joins the worker before inspecting counters.
	deadline := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		empty := len(n.queue) == 0
		n.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue not consumed")
		}
		time.Sleep(time.Millisecond)
	}
	n.Close()
	if n.sinks[0].count != 2 || n.sinks[0].suppressed != 1 {
		t.Fatalf("cap state: %+v", n.sinks[0])
	}
	clock.advance(time.Hour)
	n.mu.Lock()
	n.ctx, n.cancel = context.WithCancel(context.Background())
	n.closed = false
	n.done = make(chan struct{})
	n.mu.Unlock()
	go n.run()
	n.Send("warn", "next", "")
	summary := wait(t, ch)
	if summary.Body != "1 alerts suppressed" {
		t.Fatalf("summary: %+v", summary)
	}
	wait(t, ch)
}
func TestRedirectHostRefused(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1), 302)
	}))
	defer source.Close()
	failed := make(chan error, 1)
	n, _ := NewNotifier(testConfig(), nil, Options{Clock: &fakeClock{now: time.Now()}, Getenv: func(string) string { return source.URL }, OnFailed: func(_ string, e error) { failed <- e }})
	defer n.Close()
	n.Send("warn", "", "")
	select {
	case <-failed:
	case <-time.After(3 * time.Second):
		t.Fatal("no redirect failure")
	}
	if reached.Load() != 0 {
		t.Fatal("redirect followed")
	}
}
