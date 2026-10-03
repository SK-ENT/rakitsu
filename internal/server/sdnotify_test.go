package server

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestSDNotify(t *testing.T) {
	for _, tc := range []struct {
		name     string
		healthy  bool
		watchdog string
	}{
		{"ready", true, ""},
		{"healthy", true, "40000"},
		{"unhealthy", false, "40000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep Unix socket paths below the platform's sockaddr_un limit.
			t.Setenv("TMPDIR", "/tmp")
			path := filepath.Join(t.TempDir(), "n")
			conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			old := sdGetenv
			sdGetenv = func(key string) string {
				switch key {
				case "NOTIFY_SOCKET":
					return path
				case "WATCHDOG_USEC":
					return tc.watchdog
				default:
					return ""
				}
			}
			defer func() { sdGetenv = old }()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunSDNotify(ctx, func() bool { return tc.healthy })
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("RunSDNotify did not stop after cancellation")
				}
			}()
			read := func(timeout time.Duration) (string, error) {
				t.Helper()
				if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 128)
				n, _, err := conn.ReadFromUnix(buf)
				return string(buf[:n]), err
			}
			if got, err := read(time.Second); err != nil || got != "READY=1" {
				t.Fatalf("read readiness = %q, %v; want READY=1", got, err)
			}
			if tc.healthy && tc.watchdog != "" {
				for i := 0; i < 3; i++ {
					if got, err := read(time.Second); err != nil || got != "WATCHDOG=1" {
						t.Fatalf("read watchdog = %q, %v; want WATCHDOG=1", got, err)
					}
				}
			} else {
				got, err := read(120 * time.Millisecond)
				if e, ok := err.(net.Error); !ok || !e.Timeout() {
					t.Fatalf("unexpected notification %q or error %v; want timeout", got, err)
				}
			}
		})
	}
}

func TestSDNotifyUnset(t *testing.T) {
	old := sdGetenv
	sdGetenv = func(string) string { return "" }
	defer func() { sdGetenv = old }()
	if err := sdNotify("READY=1"); err != nil {
		t.Fatal(err)
	}
	RunSDNotify(context.Background(), func() bool {
		t.Fatal("health checked without NOTIFY_SOCKET")
		return false
	})
	if got := watchdogInterval(); got != 0 {
		t.Fatalf("watchdogInterval() = %v; want 0", got)
	}
}

func TestSDNotifyInterval(t *testing.T) {
	old := sdGetenv
	defer func() { sdGetenv = old }()
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0}, {"invalid", 0}, {"-1", 0}, {"0", 0},
		{"9223372036854775807", 0}, {"40000", 20 * time.Millisecond},
	} {
		sdGetenv = func(string) string { return tc.value }
		if got := watchdogInterval(); got != tc.want {
			t.Errorf("watchdogInterval(%q) = %v; want %v", tc.value, got, tc.want)
		}
	}
}
