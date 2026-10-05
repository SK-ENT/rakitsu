package netsafe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver struct {
	answers [][]string // one answer set per call; last repeats
	calls   atomic.Int32
}

func (f *fakeResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	i := int(f.calls.Add(1)) - 1
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	var out []net.IPAddr
	for _, s := range f.answers[i] {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

func TestDeniedAddresses(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "fd12::1",
		"169.254.169.254", "fd00:ec2::254", "fe80::1", "100.64.0.1", "224.0.0.1", "ff02::1", "0.0.0.0", "::",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1"} {
		c := NewClient(Options{})
		_, err := c.Get("http://" + net.JoinHostPort(a, "9") + "/")
		if err == nil || !strings.Contains(err.Error(), "denied") {
			t.Errorf("%s: want denied error, got %v", a, err)
		}
	}
}

func TestPublicAddressAllowed(t *testing.T) {
	var dialed string
	c := NewClient(Options{dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		return nil, errors.New("stop")
	}})
	_, _ = c.Get("http://93.184.216.34:8080/")
	if dialed != "93.184.216.34:8080" {
		t.Fatalf("dialed %q", dialed)
	}
}

func TestRebindingRejectedAtDialTime(t *testing.T) {
	r := &fakeResolver{answers: [][]string{{"93.184.216.34"}, {"10.0.0.5"}}}
	var dialed []string
	c := NewClient(Options{Resolver: r, dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, errors.New("stop")
	}})
	_, err1 := c.Get("http://rebind.test/")
	if err1 == nil || strings.Contains(err1.Error(), "denied") {
		t.Fatalf("first: %v", err1)
	}
	_, err2 := c.Get("http://rebind.test/")
	if err2 == nil || !strings.Contains(err2.Error(), "denied") {
		t.Fatalf("second: %v", err2)
	}
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:80" {
		t.Fatalf("dialed %v", dialed)
	}
}

func TestMixedAnswerRejected(t *testing.T) {
	r := &fakeResolver{answers: [][]string{{"93.184.216.34", "127.0.0.1"}}}
	c := NewClient(Options{Resolver: r})
	if _, err := c.Get("http://mixed.test/"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("got %v", err)
	}
}

func TestAllowedLoopbackHostPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	resp, err := NewClient(Options{Allow: []string{host}}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// A different loopback port is not allowed.
	if _, err := NewClient(Options{Allow: []string{"127.0.0.1:1"}}).Get(srv.URL); err == nil {
		t.Fatal("other port must be denied")
	}
	// CIDR allow works.
	resp, err = NewClient(Options{Allow: []string{"127.0.0.0/8"}}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestMetadataNeedsExplicitIP(t *testing.T) {
	r := &fakeResolver{answers: [][]string{{"169.254.169.254"}}}
	c := NewClient(Options{Resolver: r, Allow: []string{"meta.test:80"}})
	if _, err := c.Get("http://meta.test/"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("hostname allow must not unlock metadata: %v", err)
	}
}

func TestNoRedirectsByDefault(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	c := NewClient(Options{Allow: []string{"127.0.0.0/8"}})
	if _, err := c.Get(redir.URL); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestSameOriginRedirectStripsAuthAndRefusesOthers(t *testing.T) {
	var gotAuth atomic.Value
	gotAuth.Store("unset")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotAuth.Store("other") }))
	defer other.Close()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			gotAuth.Store(r.Header.Get("Authorization"))
		case "/cross":
			http.Redirect(w, r, other.URL, http.StatusFound)
		}
	}))
	defer srv.Close()
	c := NewClient(Options{Allow: []string{"127.0.0.0/8"}, Redirects: RedirectSameOrigin})
	req, _ := http.NewRequest("GET", srv.URL+"/start", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth.Load() != "" {
		t.Fatalf("token forwarded on redirect: %q", gotAuth.Load())
	}
	gotAuth.Store("unset")
	if _, err := c.Get(srv.URL + "/cross"); err == nil {
		t.Fatal("cross-origin redirect followed")
	}
	if gotAuth.Load() != "unset" {
		t.Fatal("other origin was contacted")
	}
}

func TestRedirectToPrivateIPRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1:81/", http.StatusFound)
	}))
	defer srv.Close()
	c := NewClient(Options{Allow: []string{strings.TrimPrefix(srv.URL, "http://")}, Redirects: RedirectSameOrigin})
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("redirect to private IP followed")
	}
}

func TestOverallDeadlineCoversBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("a"))
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	c := NewClient(Options{Allow: []string{"127.0.0.0/8"}, Timeout: 200 * time.Millisecond})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 10)
	var rerr error
	for rerr == nil {
		_, rerr = resp.Body.Read(buf)
	}
	if rerr.Error() == "EOF" {
		t.Fatal("body read not bounded by deadline")
	}
}

func TestValidateURL(t *testing.T) {
	for _, bad := range []string{"http://user:pw@example.com/", "http://user@example.com/", "ftp://example.com/", "file:///etc/passwd", "http:///x", "://x"} {
		if _, err := ValidateURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ValidateURL("https://example.com:8443/a?b=1"); err != nil {
		t.Fatal(err)
	}
}

func TestDialFallsBackAcrossValidatedAddresses(t *testing.T) {
	r := &fakeResolver{answers: [][]string{{"93.184.216.34", "93.184.216.35"}}}
	var dialed []string
	c := NewClient(Options{Resolver: r, dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, errors.New("stop")
	}})
	_, _ = c.Get("http://multi.test/")
	if len(dialed) != 2 || dialed[1] != "93.184.216.35:80" {
		t.Fatalf("dialed %v", dialed)
	}
}
