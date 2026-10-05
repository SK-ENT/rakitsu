// Package netsafe builds HTTP clients that refuse to reach internal network
// addresses unless the operator explicitly allowed them. The check happens at
// dial time on the resolved address, and the validated IP is dialed directly,
// so a hostname that resolves to a public address first and a private one
// later (DNS rebinding) cannot slip through.
package netsafe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Resolver is the subset of net.Resolver used for lookups (fakeable in tests).
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// RedirectPolicy selects how redirects are handled.
type RedirectPolicy int

const (
	// RedirectNone refuses every redirect (the zero value).
	RedirectNone RedirectPolicy = iota
	// RedirectSameOrigin follows redirects that keep scheme and host:port.
	RedirectSameOrigin
)

const maxRedirects = 5

// Options configures NewClient.
type Options struct {
	// Allow lists destinations the operator permits even if internal. Entries
	// are "host", "host:port", "ip", "ip:port", "[v6]:port" or a CIDR. An
	// entry without a port matches any port. A hostname entry unlocks private
	// ranges for that name but never cloud metadata, multicast or unspecified
	// addresses; list the IP or a CIDR to allow those.
	Allow     []string
	Resolver  Resolver
	Redirects RedirectPolicy
	// Timeout bounds the whole request including redirects and body read.
	// Zero or negative means no client-level deadline (the caller's context
	// still applies).
	Timeout time.Duration

	dial func(ctx context.Context, network, addr string) (net.Conn, error) // test hook
}

// ValidateURL accepts only http(s) URLs with a host and no userinfo.
func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("URL must use http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("URL has no host")
	}
	if u.User != nil {
		return nil, errors.New("URL must not contain credentials (userinfo)")
	}
	return u, nil
}

// NewClient returns a client enforcing the dial policy and redirect policy.
// Proxies are never used, because a proxy would bypass the dial check.
func NewClient(o Options) *http.Client {
	res := o.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	dial := o.dial
	if dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		dial = d.DialContext
	}
	allow := parseAllow(o.Allow)
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var ips []net.IP
			if ip := net.ParseIP(host); ip != nil {
				ips = []net.IP{ip}
			} else {
				as, err := res.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, a := range as {
					ips = append(ips, a.IP)
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("netsafe: no addresses for %q", host)
			}
			for _, ip := range ips {
				if !allow.permits(host, port, ip) {
					return nil, fmt.Errorf("netsafe: address %s for %q denied (internal or reserved range; not in the allow-list)", ip, addr)
				}
			}
			// Dial a validated IP, not the name; try each in turn.
			var lastErr error
			for _, ip := range ips {
				conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	c := &http.Client{Transport: tr, Timeout: o.Timeout}
	c.CheckRedirect = RedirectChecker(o.Redirects)
	return c
}

// RedirectChecker returns a CheckRedirect function for the policy. Any
// Authorization header is removed on every redirect it permits.
func RedirectChecker(p RedirectPolicy) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Authorization")
		if p == RedirectNone {
			return errors.New("redirects are not allowed")
		}
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		first := via[0].URL
		if req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
			return errors.New("cross-origin redirect refused")
		}
		return nil
	}
}

type allowList struct {
	prefixes []netip.Prefix
	entries  []allowEntry
}

type allowEntry struct {
	host string // lower-case; IP literal normalized
	ip   bool
	port string
}

func parseAllow(items []string) allowList {
	var l allowList
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if p, err := netip.ParsePrefix(it); err == nil {
			l.prefixes = append(l.prefixes, p.Masked())
			continue
		}
		host, port := it, ""
		if h, p, err := net.SplitHostPort(it); err == nil {
			host, port = h, p
		} else {
			host = strings.Trim(it, "[]")
		}
		e := allowEntry{host: strings.ToLower(host), port: port}
		if a, err := netip.ParseAddr(host); err == nil {
			e.host, e.ip = a.Unmap().String(), true
		}
		l.entries = append(l.entries, e)
	}
	return l
}

func (l allowList) permits(host, port string, ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !deniedByDefault(a) {
		return true
	}
	for _, p := range l.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	hardDeny := hardDenied(a)
	for _, e := range l.entries {
		if e.port != "" && e.port != port {
			continue
		}
		if e.ip {
			if e.host == a.String() {
				return true
			}
		} else if !hardDeny && e.host == strings.ToLower(host) {
			return true
		}
	}
	return false
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	metaV4    = netip.MustParseAddr("169.254.169.254")
	metaV6    = netip.MustParseAddr("fd00:ec2::254")
	reserved4 = netip.MustParsePrefix("240.0.0.0/4")
	bench4    = netip.MustParsePrefix("198.18.0.0/15")
	this4     = netip.MustParsePrefix("0.0.0.0/8")
)

// hardDenied addresses are never unlocked by a hostname allow entry.
func hardDenied(a netip.Addr) bool {
	return a == metaV4 || a == metaV6 || a.IsUnspecified() || a.IsMulticast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast()
}

func deniedByDefault(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || hardDenied(a) ||
		cgnat.Contains(a) || reserved4.Contains(a) || bench4.Contains(a) || this4.Contains(a)
}
