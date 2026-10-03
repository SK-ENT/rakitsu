package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type Options struct {
	Clock      Clock
	HTTPClient *http.Client
	Getenv     func(string) string
	Queue      int
	OnFailed   func(sink string, err error)
}
type message struct {
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Time     time.Time `json:"time"`
}
type sink struct {
	cfg               config.AlertSink
	url               string
	client            *http.Client
	window            time.Time
	count, suppressed int
}
type Notifier struct {
	mu       sync.Mutex
	queue    []message
	capacity int
	closed   bool
	wake     chan struct{}
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	clock    Clock
	secrets  []string
	sinks    []*sink
	cfg      config.WakeAlerts
	failed   func(string, error)
}

func severity(s string) int {
	switch s {
	case "info":
		return 1
	case "warn":
		return 2
	case "critical":
		return 3
	}
	return 0
}
func allowed(u *url.URL, hosts []string) bool {
	h := strings.ToLower(u.Hostname())
	if h == "" || u.User != nil {
		return false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1")) {
		return false
	}
	for _, a := range hosts {
		if h == a {
			return true
		}
	}
	return false
}

// NewNotifier disables invalid sinks while retaining all valid sinks. Errors never include URLs.
func NewNotifier(cfg config.WakeAlerts, secrets []string, opts Options) (*Notifier, error) {
	cfg.Sinks = append([]config.AlertSink(nil), cfg.Sinks...)
	cfg.ApplyDefaults()
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Queue <= 0 {
		opts.Queue = 128
	}
	if cfg.RatePerHour <= 0 {
		cfg.RatePerHour = 20
	}
	if cfg.Retry.MaxAttempts <= 0 {
		cfg.Retry.MaxAttempts = 3
	}
	if cfg.Retry.BackoffSeconds <= 0 {
		cfg.Retry.BackoffSeconds = 5
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifier{capacity: opts.Queue, wake: make(chan struct{}, 1), done: make(chan struct{}), ctx: ctx, cancel: cancel, clock: opts.Clock, secrets: append([]string(nil), secrets...), cfg: cfg, failed: opts.OnFailed}
	var errs []error
	for _, c := range cfg.Sinks {
		raw := opts.Getenv(c.URLEnv)
		u, e := url.Parse(raw)
		if raw == "" {
			errs = append(errs, fmt.Errorf("sink %q disabled: environment variable %s is unset", c.Name, c.URLEnv))
			continue
		}
		if e != nil || !allowed(u, c.AllowHosts) {
			errs = append(errs, fmt.Errorf("sink %q disabled: invalid or disallowed URL in %s", c.Name, c.URLEnv))
			continue
		}
		if (c.Type != "webhook" && c.Type != "ntfy") || severity(c.MinSeverity) == 0 {
			errs = append(errs, fmt.Errorf("sink %q disabled: invalid type or severity", c.Name))
			continue
		}
		client := http.Client{}
		if opts.HTTPClient != nil {
			client = *opts.HTTPClient
		}
		client.Timeout = 10 * time.Second
		host := strings.ToLower(u.Hostname())
		hosts := append([]string(nil), c.AllowHosts...)
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || strings.ToLower(req.URL.Hostname()) != host || !allowed(req.URL, hosts) {
				return errors.New("alert redirect refused")
			}
			return nil
		}
		n.sinks = append(n.sinks, &sink{cfg: c, url: raw, client: &client})
	}
	go n.run()
	return n, errors.Join(errs...)
}

// Send never waits for delivery. A full queue discards its oldest message.
func (n *Notifier) Send(s, title, body string) error {
	if severity(s) == 0 {
		return errors.New("invalid alert severity")
	}
	m := message{s, Truncate(Redact(title, n.secrets), 200), Truncate(Redact(body, n.secrets), 2000), n.clock.Now()}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return errors.New("notifier closed")
	}
	if len(n.queue) == n.capacity {
		copy(n.queue, n.queue[1:])
		n.queue = n.queue[:len(n.queue)-1]
	}
	n.queue = append(n.queue, m)
	select {
	case n.wake <- struct{}{}:
	default:
	}
	return nil
}
func (n *Notifier) Close() { n.mu.Lock(); n.closed = true; n.cancel(); n.mu.Unlock(); <-n.done }
func (n *Notifier) run() {
	defer close(n.done)
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-n.wake:
		}
		for {
			n.mu.Lock()
			if n.closed || len(n.queue) == 0 {
				n.mu.Unlock()
				break
			}
			m := n.queue[0]
			n.queue = n.queue[1:]
			n.mu.Unlock()
			for _, s := range n.sinks {
				if severity(m.Severity) < severity(s.cfg.MinSeverity) {
					continue
				}
				now := n.clock.Now()
				if s.window.IsZero() || now.Sub(s.window) >= time.Hour {
					s.window = now
					s.count = 0
					if s.suppressed > 0 {
						summary := message{s.cfg.MinSeverity, "Alerts suppressed", fmt.Sprintf("%d alerts suppressed", s.suppressed), now}
						s.suppressed = 0
						n.deliver(s, summary)
						s.count++
					}
				}
				if s.count >= n.cfg.RatePerHour {
					s.suppressed++
					continue
				}
				s.count++
				n.deliver(s, m)
			}
		}
	}
}
func (n *Notifier) deliver(s *sink, m message) {
	ctx, cancel := context.WithTimeout(n.ctx, 60*time.Second)
	defer cancel()
	start := n.clock.Now()
	var payload []byte
	var err error
	if s.cfg.Type == "ntfy" {
		payload = []byte(m.Body)
	} else {
		payload, err = json.Marshal(m)
	}
	if err != nil || len(payload) > 8192 {
		if n.failed != nil {
			n.failed(s.cfg.Name, errors.New("alert payload exceeds 8KB"))
		}
		return
	}
	backoff := time.Duration(n.cfg.Retry.BackoffSeconds) * time.Second
	for attempt := 0; attempt < n.cfg.Retry.MaxAttempts; attempt++ {
		if ctx.Err() != nil || n.clock.Now().Sub(start) >= 60*time.Second {
			err = errors.New("alert delivery deadline exceeded")
			break
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if s.cfg.Type == "ntfy" {
			req.Header.Set("Content-Type", "text/plain; charset=utf-8")
			req.Header.Set("Title", strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Title))
			req.Header.Set("Priority", fmt.Sprint(severity(m.Severity)+2))
		}
		resp, e := s.client.Do(req)
		retry := true
		if e != nil {
			err = errors.New("alert HTTP delivery failed")
		} else {
			_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
			resp.Body.Close()
			_ = readErr
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("alert HTTP status %d", resp.StatusCode)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				retry = false
			}
		}
		if !retry || attempt+1 == n.cfg.Retry.MaxAttempts {
			break
		}
		remaining := 60*time.Second - n.clock.Now().Sub(start)
		if backoff >= remaining {
			break
		}
		if e = n.clock.Sleep(ctx, backoff); e != nil {
			err = errors.New("alert retry interrupted")
			break
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
	if n.failed != nil && err != nil {
		n.failed(s.cfg.Name, err)
	}
}
