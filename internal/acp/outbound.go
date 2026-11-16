package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ─── Outbound (agent → client) requests ───────────────────────────────────────
//
// ACP is bidirectional: the agent also sends requests to the client (for
// example session/request_permission) and the client answers them with
// JSON-RPC responses on stdin. This file holds that path:
//
//   - callClient sends a request with an id from our own "rk-N" string
//     namespace and waits for the matching response.
//   - routeResponse is called by the reader loop for every inbound response.
//     It looks the id up in the pending map; unknown, late, duplicate and
//     pre-emptive ids are dropped silently (never answered).
//   - Every pending call ends exactly once: on its answer, its ctx
//     (deadline/cancel), session/cancel for its session (failPending), or the
//     connection closing (closeOutbound). None of these ever yields a result
//     except a real answer, so a caller that maps "error" to "deny" fails closed.

var (
	// errClientDisconnected: stdin reached EOF/error or Run's ctx ended, so no
	// answer can ever arrive.
	errClientDisconnected = errors.New("acp: client disconnected")
	// errSessionCancelled: the client sent session/cancel for the call's session.
	errSessionCancelled = errors.New("acp: session cancelled")
)

// Error lets a client's JSON-RPC error answer travel as a Go error.
func (e *acpError) Error() string {
	return fmt.Sprintf("acp client error %d: %s", e.Code, e.Message)
}

// inboundEnvelope is one parsed inbound line: a request, a notification, or a
// response to one of our outbound requests. Result and Error stay raw so a
// malformed answer still parses and can be failed closed by routeResponse
// instead of being turned into a parse-error reply.
type inboundEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// hasError reports an "error" member other than an explicit null.
func (e *inboundEnvelope) hasError() bool {
	return e.Error != nil && string(e.Error) != "null"
}

// isResponse: no method, an id, and a result or error. A "result": null still
// counts (it unmarshals to the non-nil RawMessage "null").
func (e *inboundEnvelope) isResponse() bool {
	return e.Method == "" && e.ID != nil && (e.Result != nil || e.hasError())
}

type callOutcome struct {
	result json.RawMessage
	err    error
}

// pendingCall is one outbound request waiting for its answer. done is
// buffered 1 and written at most once, by whichever path removed the call
// from Server.pending under outMu, so no writer ever blocks.
type pendingCall struct {
	sessionID string
	done      chan callOutcome
}

type outboundRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// maxDropLogs bounds the stderr lines written for dropped responses, so a
// client flooding stray responses cannot flood stderr too.
const maxDropLogs = 20

// callClient sends method/params to the client as a JSON-RPC request and
// waits for its answer. It returns the raw result, or an error: *acpError
// for a client error answer, ctx.Err() on deadline/cancel,
// errSessionCancelled, errClientDisconnected, or a decode error. It never
// returns a nil error without a real answer.
//
// The request is written on its own goroutine so a client that stopped
// reading stdout cannot hold the caller past its deadline. That writer skips
// the write if the call already ended while it waited for the output lock.
func (s *Server) callClient(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	if sessionID == "" {
		return nil, errors.New("acp: outbound call without a session id")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("acp: encode %s params: %w", method, err)
		}
		rawParams = b
	}

	pc := &pendingCall{sessionID: sessionID, done: make(chan callOutcome, 1)}
	s.outMu.Lock()
	if s.outClosed {
		s.outMu.Unlock()
		return nil, errClientDisconnected
	}
	s.outSeq++
	id := "rk-" + strconv.FormatUint(s.outSeq, 10)
	if s.pending == nil {
		s.pending = make(map[string]*pendingCall)
	}
	s.pending[id] = pc
	s.outMu.Unlock()

	line, err := json.Marshal(outboundRequest{JSONRPC: "2.0", ID: id, Method: method, Params: rawParams})
	if err != nil { // cannot happen: every field is a string or pre-encoded JSON
		s.removePending(id)
		return nil, err
	}
	go s.writeIfPending(id, append(line, '\n'))

	select {
	case out := <-pc.done:
		return out.result, out.err
	case <-ctx.Done():
		s.removePending(id)
		return nil, ctx.Err()
	}
}

// writeIfPending writes an outbound request line unless its call has already
// ended (deadline, cancel, disconnect) while this goroutine waited for the
// output lock — a request nobody waits for must not reach the client.
// Lock order: s.mu, then s.outMu. No path takes them the other way round.
func (s *Server) writeIfPending(id string, line []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outMu.Lock()
	_, live := s.pending[id]
	s.outMu.Unlock()
	if !live {
		return
	}
	s.out.Write(line) //nolint:errcheck
}

func (s *Server) removePending(id string) {
	s.outMu.Lock()
	delete(s.pending, id)
	s.outMu.Unlock()
}

// routeResponse delivers an inbound response to its pending call. It runs
// inline on the reader goroutine and never blocks. It never writes to the
// client: JSON-RPC forbids replying to a response.
func (s *Server) routeResponse(env inboundEnvelope) {
	var id string
	if err := json.Unmarshal(env.ID, &id); err != nil {
		// Not a string id, so not one of ours (we only issue "rk-N").
		s.logDroppedResponse()
		return
	}
	s.outMu.Lock()
	pc, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.outMu.Unlock()
	if !ok {
		s.logDroppedResponse()
		return
	}

	if env.hasError() {
		var rpcErr acpError
		if err := json.Unmarshal(env.Error, &rpcErr); err != nil {
			pc.done <- callOutcome{err: fmt.Errorf("acp: malformed client error answer: %w", err)}
			return
		}
		pc.done <- callOutcome{err: &rpcErr}
		return
	}
	pc.done <- callOutcome{result: env.Result}
}

// logDroppedResponse writes one fixed stderr line per dropped response, up
// to maxDropLogs. The client's id is not echoed (it is untrusted text).
func (s *Server) logDroppedResponse() {
	switch n := s.droppedResponses.Add(1); {
	case n < maxDropLogs:
		fmt.Fprintln(os.Stderr, "rakitsu acp: dropped a response with no matching outbound request")
	case n == maxDropLogs:
		fmt.Fprintln(os.Stderr, "rakitsu acp: dropped a response with no matching outbound request (further drops not logged)")
	}
}

// failPending ends every pending call of one session with err. An empty
// sessionID matches nothing (it must never mean "all sessions").
func (s *Server) failPending(sessionID string, err error) {
	if sessionID == "" {
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	for id, pc := range s.pending {
		if pc.sessionID == sessionID {
			delete(s.pending, id)
			pc.done <- callOutcome{err: err}
		}
	}
}

// closeOutbound ends every pending call with err and makes every later
// callClient fail immediately with errClientDisconnected. Run calls it
// before waiting for in-flight turns, so a turn blocked on an answer cannot
// hold Run open after the client is gone.
func (s *Server) closeOutbound(err error) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.outClosed = true
	for id, pc := range s.pending {
		delete(s.pending, id)
		pc.done <- callOutcome{err: err}
	}
}
