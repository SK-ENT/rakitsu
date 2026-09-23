package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// apiTokenEnv, when set in the serve process env, makes the control-plane
// endpoints capable of triggering code execution or disclosing local state
// require "Authorization: Bearer <token>". It is also the gate that permits
// binding a non-loopback host: without it, `serve --host 0.0.0.0` refuses to
// start (see requireBindAllowed) because that would expose an unauthenticated,
// RCE-capable API to the network.
//
// One token covers the whole control plane, including cross-session messaging
// (sessionMsgAuthorized accepts either this or RAKITSU_SESSION_MSG_TOKEN).
const apiTokenEnv = "RAKITSU_API_TOKEN"

// withinDir reports whether p is base itself or a genuine descendant of it.
// Anchored on the path separator, not a bare string prefix: a sibling
// directory that merely starts with the same string (e.g. base "/home/alice",
// p "/home/alice-evil") would satisfy strings.HasPrefix(p, base) even though
// it isn't actually under base. Both p and base must already be absolute
// (e.g. via filepath.Abs) — this does no cleaning of its own.
func withinDir(p, base string) bool {
	return p == base || strings.HasPrefix(p, base+string(os.PathSeparator))
}

// isLoopbackHost reports whether host binds only to the local machine.
// The empty host is NOT loopback: net/http turns ":port" into a listener on
// every interface, so `--host ""` must need a token like any network bind.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// RequireBindAllowed returns a non-nil error when binding the given host would
// expose the control plane to the network without an API token configured.
// Callers should treat a non-nil result as fatal.
func RequireBindAllowed(host string) error {
	if isLoopbackHost(host) {
		return nil
	}
	if os.Getenv(apiTokenEnv) == "" {
		return &bindRefusedError{host: host}
	}
	return nil
}

type bindRefusedError struct{ host string }

func (e *bindRefusedError) Error() string {
	return "refusing to bind non-loopback host " + e.host + " without " + apiTokenEnv +
		" set: this would expose an unauthenticated, code-execution-capable API to the network. " +
		"Set " + apiTokenEnv + "=<token> to enable network exposure, or bind localhost."
}

// TokenConfigured reports whether RAKITSU_API_TOKEN is set in this process's
// environment — i.e. whether AuthMiddleware is actively enforcing it on
// control-plane endpoints right now. Exported so callers outside this
// package (the A2A agent card builder) can advertise the same requirement a
// client would otherwise only discover by hitting a 401.
func TokenConfigured() bool {
	return os.Getenv(apiTokenEnv) != ""
}

// requiresAuth reports whether a request targets a control-plane endpoint that
// must be authenticated when an API token is configured.
//
// Default-deny: every path under the API, stream and
// protocol prefixes is gated unless it is one of the explicit public
// exceptions here. A handler wired onto the mux under /api/, /ws/, /events,
// /mcp or /a2a is therefore gated without touching this file; only adding a
// NEW public exception needs a change here, and it should be justified in a
// comment. Everything outside those prefixes is the embedded static SPA.
//
// What that means in practice once a token is set: the web UI (which cannot
// attach a bearer token to page navigations, EventSource or WebSocket
// upgrades) loses the history browser, config list and live event stream in
// addition to the run/chat controls that were already gated. That is
// deliberate — persisted sessions disclose queries, outputs and configs
// (which may embed provider API keys), and the previous allowlist left them
// open for UI compatibility. See docs/SECURITY.md.
func requiresAuth(r *http.Request) bool {
	p := r.URL.Path
	switch p {
	case "/health", "/api/status":
		// Liveness and version/boot-id only; nothing user- or run-specific.
		return false
	case "/.well-known/agent-card.json":
		// A2A discovery: the spec expects the card to be fetchable so a
		// client can learn what auth is required before authenticating, and
		// the card advertises the bearer requirement whenever a token is set.
		return false
	}
	// Cross-session messaging has its own gate (sessionMsgAuthorized, in
	// session_message.go) that accepts RAKITSU_SESSION_MSG_TOKEN as well as
	// the API token, so a client holding only the messaging token still
	// works. Requiring the API token here would lock those clients out; the
	// handler still refuses unauthenticated callers whenever either token is
	// configured. Only the exact {id}/message and {id}/inbox shapes are
	// deferred — every other /api/sessions/... path is gated below.
	if rest, ok := strings.CutPrefix(p, "/api/sessions/"); ok {
		if id, action, found := strings.Cut(rest, "/"); found && id != "" && (action == "message" || action == "inbox") {
			return false
		}
	}
	for _, prefix := range []string{"/api/", "/ws/", "/mcp", "/a2a"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return p == "/api" || p == "/events"
}

// MCPListenerHandler builds the handler for the standalone --mcp-port
// listener: the MCP server mounted at /mcp only, behind the same CORS and
// token middleware as the hub. MCPServer.ServeHTTP itself ignores the request
// path, so mounting it as the listener's root handler would answer — and
// execute tools for — any path; requiresAuth is default-deny, but the mux is
// what guarantees nothing other than /mcp can reach the handler regardless
// of how the auth policy evolves.
func MCPListenerHandler(bindHost string, mcp *MCPServer) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp)
	return GuardMiddleware(bindHost, CorsMiddleware(AuthMiddleware(mux)))
}

// GuardMiddleware blocks two browser-driven attacks CORS alone does not:
//
//   - Cross-site request forgery: CorsMiddleware only withholds response
//     headers, the request itself still runs. A page on any site could POST
//     a config to /api/configs/inline and start it via /api/run. So a
//     state-changing request carrying an Origin that is neither an allowed
//     localhost origin nor this server's own origin is refused. Non-browser
//     clients (the CLI hub forwarder, curl) send no Origin and are
//     unaffected.
//   - DNS rebinding: on a loopback bind, a request whose Host is not a
//     loopback name came through a rebound hostname, so it is refused.
func GuardMiddleware(bindHost string, next http.Handler) http.Handler {
	loopback := isLoopbackHost(bindHost)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if loopback && !isLoopbackHostHeader(r.Host) {
			http.Error(w, `{"error":"forbidden host"}`, http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if origin := r.Header.Get("Origin"); origin != "" && !isAllowedOrigin(origin) && !isSameOrigin(origin, r.Host) {
				http.Error(w, `{"error":"cross-origin request refused"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHostHeader reports whether a Host header (host or host:port)
// names the local machine.
func isLoopbackHostHeader(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return isLoopbackHost(host)
}

// isSameOrigin reports whether origin (scheme://host[:port]) names the host
// the request was sent to, i.e. the UI served by this server itself.
func isSameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// AuthMiddleware enforces the API token on control-plane endpoints when
// apiTokenEnv is set. With no token configured it is a pass-through: the
// loopback trust model applies and requireBindAllowed has already refused any
// non-loopback bind. Wrap this INSIDE CorsMiddleware so CORS preflight
// (OPTIONS) is answered before the token check.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv(apiTokenEnv)
		if token != "" && requiresAuth(r) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				jsonErrorResponse(w, "unauthorized: missing or invalid API token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
