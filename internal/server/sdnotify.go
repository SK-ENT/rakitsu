package server

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

var sdGetenv = os.Getenv

// sdNotify sends a notification to systemd, if a notification socket is configured.
func sdNotify(state string) error {
	socket := sdGetenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}

// watchdogInterval returns half the configured watchdog timeout.
func watchdogInterval() time.Duration {
	usec, err := strconv.ParseInt(sdGetenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || usec <= 0 || usec > int64((1<<63-1)/time.Microsecond) {
		return 0
	}
	return time.Duration(usec) * time.Microsecond / 2
}

// RunSDNotify announces readiness and sends watchdog notifications while healthy.
// Notification errors are best-effort and do not stop the loop.
func RunSDNotify(ctx context.Context, healthy func() bool) {
	if sdGetenv("NOTIFY_SOCKET") == "" {
		return
	}
	_ = sdNotify("READY=1")
	interval := watchdogInterval()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			if healthy() {
				_ = sdNotify("WATCHDOG=1")
			}
		}
	}
}
