package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// RunFunc is the signature of executeConfig from cmd/rakitsu/run.go.
// The ACP server calls this to run an agent pipeline.
// The registrar callback (if non-nil) registers agents for mid-run debug attach.
type RunFunc func(
	ctx context.Context,
	cfg *config.Config,
	eventBus *telemetry.EventBus,
	debugCtrl *debug.DebugController,
	query string,
	registrar func([]debug.Attachable),
) (string, error)

// ─── JSON-RPC types ───────────────────────────────────────────────────────────

// acpRequest.ID and acpResponse.ID are json.RawMessage, not *int: JSON-RPC 2.0
// permits a request id to be a string, a number, or null. A client using
// string ids (common in JSON-RPC client libraries) would otherwise fail
// json.Unmarshal entirely for that line, routing a well-formed request into
// the "malformed JSON" branch and losing the id needed to correlate the
// error. json.RawMessage round-trips whatever shape the client sent.
type acpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type acpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ─── Server ───────────────────────────────────────────────────────────────────

// protocolVersion is ACP's PROTOCOL_VERSION — a plain integer, not a semver
// string. Verified against @zed-industries/agent-client-protocol@0.4.5's
// dist/schema.d.ts (the human-readable docs site paraphrase got this wrong).
const protocolVersion = 1

// defaultPromptTimeout bounds how long a single session/prompt turn may run
// when the pinned config leaves settings.execution.timeout_seconds unset.
// Real ACP's PromptRequest carries no timeout field (unlike the old
// agent/run's timeout_sec), so this is a fixed server-side default rather
// than a per-request client override — but a configured value still wins,
// see promptTimeout below.
const defaultPromptTimeout = 300 * time.Second

// promptTimeout resolves the duration that bounds one session/prompt turn.
// Precedence: an explicit override (the process's --timeout flag, see
// cmd/rakitsu/acp.go) beats settings.execution.timeout_seconds, which beats
// defaultPromptTimeout — mirroring cmd/rakitsu/run.go's resolveTimeoutSeconds
// flag-over-config precedence, since ACP has no per-request timeout field to
// carry this instead (real ACP's PromptRequest has none).
//
// override, when non-nil, came from an explicit --timeout flag and is used
// as-is with run.go's <=0-disables convention (contextWithOptionalTimeout):
// the flag has no "unset" value of its own to distinguish from an intentional
// 0, so there is no default-substitution step here.
//
// Without an override, the config field applies: 0 (unset) falls back to
// defaultPromptTimeout, a positive value is used as-is, and a negative value
// means no timeout at all (ok=false — the caller must not apply a deadline).
// YAML's zero-value can't distinguish "unset" from "explicit 0" the way a
// cobra flag can, hence the different treatment of 0 between the two paths.
func promptTimeout(cfg *config.Config, override *int) (d time.Duration, ok bool) {
	if override != nil {
		if *override <= 0 {
			return 0, false
		}
		return time.Duration(*override) * time.Second, true
	}
	switch sec := cfg.Settings.Execution.TimeoutSeconds; {
	case sec == 0:
		return defaultPromptTimeout, true
	case sec < 0:
		return 0, false
	default:
		return time.Duration(sec) * time.Second, true
	}
}

// Server is the ACP stdio server. It reads JSON-RPC requests from in,
// executes agent pipelines via runFunc, and writes JSON-RPC responses/
// notifications to out. cfg is the single config pinned at process startup
// (see cmd/rakitsu/acp.go) — real ACP's session/new has no config-path field,
// so every session this process serves runs against the same cfg.
type Server struct {
	runFunc         RunFunc
	cfg             *config.Config
	timeoutOverride *int // explicit --timeout flag value, if any; see promptTimeout
	in              io.Reader
	out             io.Writer
	mu              sync.Mutex // protects writes to out
	sessions        *sessionMap
	wg              sync.WaitGroup // tracks all in-flight dispatch work

	shutdownMu sync.Mutex // guards stopped, serializing it against wg.Add in the reader loop
	stopped    bool       // set before wg.Wait(); see Run's comment
}

// NewServer creates an ACP server that reads from os.Stdin and writes to
// os.Stdout, running every session against cfg.
// Use NewServerWithIO for testing.
func NewServer(cfg *config.Config, runFunc RunFunc) *Server {
	return &Server{
		cfg:      cfg,
		runFunc:  runFunc,
		sessions: newSessionMap(),
	}
}

// SetTimeoutOverride pins an explicit --timeout value (see cmd/rakitsu/acp.go)
// that beats settings.execution.timeout_seconds for every session/prompt turn
// this server handles. seconds <= 0 disables the deadline entirely, matching
// cmd/rakitsu/run.go's --timeout flag convention.
func (s *Server) SetTimeoutOverride(seconds int) {
	s.timeoutOverride = &seconds
}

// NewServerWithIO creates an ACP server with injected I/O (for testing).
func NewServerWithIO(cfg *config.Config, runFunc RunFunc, in io.Reader, out io.Writer) *Server {
	return &Server{
		cfg:      cfg,
		runFunc:  runFunc,
		in:       in,
		out:      out,
		sessions: newSessionMap(),
	}
}

// Run blocks, reading newline-delimited JSON-RPC requests until in is closed
// or ctx is cancelled. It returns only once every request dispatched before
// shutdown — including in-flight session/prompt turns, which now run
// synchronously inside their own dispatch goroutine rather than spawning a
// second tracked goroutine — has actually finished, so a caller can safely
// treat Run returning as "safe to shut down." No new request is dispatched
// once shutdown begins.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if s.in == nil {
		s.in = in
	}
	if s.out == nil {
		s.out = out
	}

	// The read loop runs on its own goroutine so Run can select on ctx.Done()
	// instead of blocking forever on a synchronous read when in stays open.
	readDone := make(chan error, 1)
	go func() {
		// bufio.Reader.ReadString has no fixed per-line size ceiling, unlike
		// the bufio.Scanner + fixed buffer this replaces: a single line over
		// that buffer's size (plausible for session/prompt's prompt field,
		// which could carry a pasted diff) made Scan() return false
		// permanently with bufio.ErrTooLong — Scanner cannot recover after
		// that, so the loop exited and Run returned silently, dropping every
		// subsequent request for the life of the process.
		reader := bufio.NewReader(s.in)
		for {
			line, err := reader.ReadString('\n')
			trimmed := bytesTrimNewline(line)
			if len(trimmed) > 0 {
				var req acpRequest
				if jsonErr := json.Unmarshal([]byte(trimmed), &req); jsonErr != nil {
					// Malformed JSON — parse errors are reported with an
					// explicit JSON "null" id per the JSON-RPC 2.0 spec, not
					// suppressed the way a true notification's absent id is
					// (see writeResponse/writeError).
					s.writeError(json.RawMessage("null"), -32700, "parse error")
				} else {
					// s.wg.Add must never happen concurrently with or after
					// s.wg.Wait() below — sync.WaitGroup's own docs call that
					// undefined behavior. shutdownMu serializes this check
					// against Run setting s.stopped before it calls Wait, so
					// either this Add is guaranteed to happen before that
					// Wait, or stopped is already true and this request is
					// dropped instead of dispatched.
					s.shutdownMu.Lock()
					if s.stopped {
						s.shutdownMu.Unlock()
					} else {
						s.wg.Add(1)
						s.shutdownMu.Unlock()
						go func() {
							defer s.wg.Done()
							s.dispatch(ctx, req)
						}()
					}
				}
			}
			if err != nil {
				if err == io.EOF {
					readDone <- nil
				} else {
					readDone <- err
				}
				return
			}
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case runErr = <-readDone:
	}

	// Stop accepting new dispatches before waiting — see the Add-site
	// comment above for why this ordering is what makes s.wg.Wait() safe.
	// The reader goroutine itself may still be blocked on the next
	// reader.ReadString call after Run returns (nothing closes s.in here);
	// that's fine, since it can no longer dispatch anything once stopped.
	s.shutdownMu.Lock()
	s.stopped = true
	s.shutdownMu.Unlock()

	// Wait for every request dispatched before shutdown — including an
	// in-flight session/prompt turn, which runs inline inside dispatch now —
	// to actually finish before returning.
	s.wg.Wait()
	return runErr
}

// bytesTrimNewline strips a trailing "\n" and, before that, "\r" — matching
// bufio.Scanner's ScanLines behavior for CRLF input — without allocating a
// new string via strings.TrimRight for the common case.
func bytesTrimNewline(line string) string {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line
}

// ─── Dispatch ─────────────────────────────────────────────────────────────────

func (s *Server) dispatch(ctx context.Context, req acpRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "session/new":
		s.handleNewSession(req)
	case "session/prompt":
		s.handlePrompt(ctx, req)
	case "session/cancel":
		s.handleCancel(req)
	default:
		s.writeError(req.ID, -32601, fmt.Sprintf("method not found: %q", req.Method))
	}
}

// ─── initialize ───────────────────────────────────────────────────────────────

type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AuthMethods       []authMethod      `json:"authMethods"`
}

// agentCapabilities is returned all-zero/false in v1: no session resumption
// (loadSession), no non-text prompt content, no MCP-server forwarding.
type agentCapabilities struct {
	LoadSession        bool               `json:"loadSession"`
	PromptCapabilities promptCapabilities `json:"promptCapabilities"`
	McpCapabilities    mcpCapabilities    `json:"mcpCapabilities"`
}

type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type mcpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type authMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (s *Server) handleInitialize(req acpRequest) {
	s.writeResponse(req.ID, initializeResult{
		ProtocolVersion:   protocolVersion,
		AgentCapabilities: agentCapabilities{},
		AuthMethods:       []authMethod{}, // no auth for a local stdio agent
	})
}

// ─── session/new ────────────────────────────────────────────────────────────

// newSessionParams mirrors real ACP's NewSessionRequest. cwd and mcpServers
// are accepted (required by the client-side schema) but not used in v1 —
// rakitsu's tool set and working directory come from the config pinned at
// server startup (cmd/rakitsu/acp.go), not from the session.
type newSessionParams struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

func (s *Server) handleNewSession(req acpRequest) {
	var params newSessionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	// Silently ignoring these could mean opening project B in the client
	// while the pinned config's fs tools resolve against project A, with
	// nothing in the transcript hinting at the mismatch — logging at least
	// makes it diagnosable.
	if params.CWD != "" {
		fmt.Fprintf(os.Stderr, "rakitsu acp: session/new cwd %q ignored — file access follows the config pinned at startup, not the session\n", params.CWD)
	}
	if len(params.MCPServers) > 0 {
		fmt.Fprintf(os.Stderr, "rakitsu acp: session/new mcpServers (%d) ignored — client-supplied MCP servers aren't wired in v1; use the config's own tools: mcp_server entries instead\n", len(params.MCPServers))
	}

	sessionID := uuid.New().String()
	s.sessions.add(newSession(sessionID))
	s.writeResponse(req.ID, newSessionResult{SessionID: sessionID})
}

// ─── session/prompt ───────────────────────────────────────────────────────────

// contentBlock is a (deliberately partial) ACP ContentBlock. v1 only reads
// Type and Text — every other field (data/mimeType/uri/resource/...) is
// unused because a non-"text" block is rejected before those fields would
// matter (see handlePrompt).
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

// sessionUpdateParams is the session/update notification envelope. Update
// holds one of the sessionUpdate-discriminated payload structs below.
type sessionUpdateParams struct {
	SessionID string      `json:"sessionId"`
	Update    interface{} `json:"update"`
}

func (s *Server) handlePrompt(ctx context.Context, req acpRequest) {
	var params promptParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if params.SessionID == "" {
		s.writeError(req.ID, -32602, "sessionId is required")
		return
	}
	if len(params.Prompt) == 0 {
		s.writeError(req.ID, -32602, "prompt is required")
		return
	}

	sess, ok := s.sessions.get(params.SessionID)
	if !ok {
		s.writeError(req.ID, -32001, "session not found: "+params.SessionID)
		return
	}

	textParts := make([]string, 0, len(params.Prompt))
	for _, block := range params.Prompt {
		if block.Type != "text" {
			s.writeError(req.ID, -32602, fmt.Sprintf(
				"unsupported content block type %q: only \"text\" is supported in this version", block.Type))
			return
		}
		textParts = append(textParts, block.Text)
	}
	query := strings.Join(textParts, "\n\n")
	composedQuery := sess.composeQuery(query)

	var runCtx context.Context
	var cancel context.CancelFunc
	if d, hasTimeout := promptTimeout(s.cfg, s.timeoutOverride); hasTimeout {
		runCtx, cancel = context.WithTimeout(ctx, d)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	if !sess.startTurn(cancel) {
		cancel()
		s.writeError(req.ID, -32000, "session busy: a session/prompt turn is already in flight for session "+params.SessionID)
		return
	}

	eventBus := telemetry.NewEventBus(1024)
	eventCh := eventBus.Subscribe()

	// sawMessageChunk tracks whether any agent_message_chunk notification
	// was actually sent during this turn. Only the drain goroutine below
	// writes it; cleanupEvents's <-drainDone (called before result is used
	// below) establishes a happens-before edge, so reading it afterward
	// needs no atomic/lock of its own.
	sawMessageChunk := false

	// Drain events → session/update notifications. drainDone signals once
	// the range loop has finished flushing everything Unsubscribe's
	// channel-close left buffered, so the final response is only written
	// after the client has already seen every session/update that causally
	// preceded it — on BOTH the normal and panic paths below.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for event := range eventCh {
			if update, ok := sessionUpdateFromEvent(event); ok {
				if event.EventType == telemetry.EventTokenChunk {
					sawMessageChunk = true
				}
				s.writeNotification("session/update", sessionUpdateParams{
					SessionID: params.SessionID,
					Update:    update,
				})
			}
		}
	}()
	var cleanupOnce sync.Once
	cleanupEvents := func() {
		cleanupOnce.Do(func() {
			eventBus.Unsubscribe(eventCh)
			<-drainDone
		})
	}

	// Any panic in s.runFunc — the full agent/orchestrator/provider/tool
	// execution stack — must not escape this call: unrecovered, it would
	// crash the whole ACP process, taking down every other concurrently
	// in-flight session on the same stdio connection. internal/server/runner.go
	// guards the same RunFunc call point the same way. cleanupEvents is
	// called explicitly here rather than left to a defer at this call site: a
	// plain defer registered before s.runFunc would run AFTER this recover
	// defer during panic unwind (defers unwind LIFO — this one, registered
	// first, runs last), so the error response below would be written before
	// the drain finished flushing every preceding session/update, breaking
	// the very ordering this cleanup exists to guarantee.
	defer func() {
		if p := recover(); p != nil {
			cleanupEvents()
			cancel()
			sess.endTurn()
			sess.recordTurn(query, fmt.Sprintf("[error: agent panic: %v]", p))
			s.writeError(req.ID, -32000, fmt.Sprintf("agent panic: %v", p))
		}
	}()

	result, runErr := s.runFunc(runCtx, s.cfg, eventBus, nil, composedQuery, nil)

	cleanupEvents()
	cancel()
	wasCancelled := sess.wasCancelled()
	sess.endTurn()

	switch {
	case runErr != nil && wasCancelled:
		// Cancellation isn't a failure — real ACP has no generic "error"
		// StopReason, only this one for the cancel case.
		sess.recordTurn(query, "[cancelled]")
		s.writeResponse(req.ID, promptResult{StopReason: "cancelled"})
	case runErr != nil:
		// No StopReason value fits a generic failure — signal it as a
		// request error instead, same as the panic-recovery path above.
		sess.recordTurn(query, fmt.Sprintf("[error: %v]", runErr))
		s.writeError(req.ID, -32000, runErr.Error())
	default:
		// v1 has no ACP field for the final answer text separate from the
		// agent_message_chunk notifications already streamed during the run,
		// so on the streaming path result is already fully surfaced and
		// only needs recording into session history (composeQuery is the
		// consumer, not the ACP response).
		//
		// But streaming isn't guaranteed: model_config.no_stream_tools with
		// tools present, or any provider that isn't an llm.StreamingProvider,
		// skips generateWithStreaming entirely — the only EventTokenChunk
		// emit site — so sawMessageChunk is false and the client would
		// otherwise see zero content. Emit result as one synthetic final
		// chunk in that case; the streaming path never hits this, so
		// nothing is ever duplicated.
		if !sawMessageChunk && result != "" {
			s.writeNotification("session/update", sessionUpdateParams{
				SessionID: params.SessionID,
				Update: agentMessageChunkUpdate{
					SessionUpdate: "agent_message_chunk",
					Content:       contentBlock{Type: "text", Text: result},
				},
			})
		}
		sess.recordTurn(query, result)
		s.writeResponse(req.ID, promptResult{StopReason: "end_turn"})
	}
}

// ─── session/cancel ───────────────────────────────────────────────────────────

// session/cancel is a true JSON-RPC notification per the ACP spec: the
// client sends it with no "id" and expects no response, ever — not even an
// error for an unknown session. handleCancel therefore never calls
// writeResponse/writeError.
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

func (s *Server) handleCancel(req acpRequest) {
	var params cancelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return
	}
	if sess, ok := s.sessions.get(params.SessionID); ok {
		sess.requestCancel()
	}
}

// ─── event → session/update mapping ────────────────────────────────────────────

// toolCallPayload backs both the "tool_call" and "tool_call_update"
// sessionUpdate variants, which share the same field set in the ACP schema
// (tool_call requires Title; tool_call_update leaves everything but
// ToolCallID optional — omitempty covers that difference here).
type toolCallPayload struct {
	SessionUpdate string                 `json:"sessionUpdate"`
	ToolCallID    string                 `json:"toolCallId"`
	Title         string                 `json:"title,omitempty"`
	Status        string                 `json:"status,omitempty"`
	RawInput      map[string]interface{} `json:"rawInput,omitempty"`
	RawOutput     map[string]interface{} `json:"rawOutput,omitempty"`
}

type agentMessageChunkUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       contentBlock `json:"content"`
}

type agentThoughtChunkUpdate struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       contentBlock `json:"content"`
}

type planEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type planUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Entries       []planEntry `json:"entries"`
}

// sessionUpdateFromEvent converts a rakitsu telemetry event into an ACP
// session/update payload, following the same switch-on-EventType +
// json.Unmarshal(event.Payload, &p) pattern already used in
// internal/chat/bridge.go's convertEvent. ok is false for event types with
// no ACP counterpart (the large majority — AGENT_START/END, ERROR,
// REFLECTION_*, PIPELINE_*, DEBUG_*, MEMORY_*, etc.); those still reach the
// regular telemetry/hub stream, just not the ACP client.
func sessionUpdateFromEvent(event telemetry.AgentEvent) (interface{}, bool) {
	switch event.EventType {
	case telemetry.EventTokenChunk:
		var p telemetry.TokenChunkPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, false
		}
		return agentMessageChunkUpdate{
			SessionUpdate: "agent_message_chunk",
			Content:       contentBlock{Type: "text", Text: p.Text},
		}, true

	case telemetry.EventReasoningChunk:
		var p telemetry.ReasoningChunkPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, false
		}
		return agentThoughtChunkUpdate{
			SessionUpdate: "agent_thought_chunk",
			Content:       contentBlock{Type: "text", Text: p.Text},
		}, true

	case telemetry.EventToolCallStart:
		var p telemetry.ToolCallStartPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, false
		}
		return toolCallPayload{
			SessionUpdate: "tool_call",
			ToolCallID:    p.ToolCallID,
			Title:         p.ToolName,
			Status:        "pending",
			RawInput:      p.Arguments,
		}, true

	case telemetry.EventToolCallEnd:
		var p telemetry.ToolCallEndPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, false
		}
		status := "completed"
		if p.Error != "" {
			status = "failed"
		}
		rawOutput := map[string]interface{}{"output": p.Output}
		// ExitCode is meaningless for non-process tools (fs, mcp_server, a2a,
		// memory) — the payload's own `omitempty` says as much, but that's
		// lost once re-encoded into this map literal. A nonzero value is
		// unambiguous evidence of a real process exit; zero is ambiguous
		// (could be a genuine successful exit, or just "not a process"), so
		// only include the key when it's unambiguous — avoids asserting
		// "exited successfully" for a tool that never ran a process at all.
		if p.ExitCode != 0 {
			rawOutput["exitCode"] = p.ExitCode
		}
		return toolCallPayload{
			SessionUpdate: "tool_call_update",
			ToolCallID:    p.ToolCallID,
			Status:        status,
			RawOutput:     rawOutput,
		}, true

	case telemetry.EventThoughtEnd:
		var p telemetry.ThoughtEndPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			return nil, false
		}
		if len(p.Plan) == 0 {
			return nil, false
		}
		entries := make([]planEntry, len(p.Plan))
		for i, line := range p.Plan {
			entries[i] = planEntry{Content: line, Priority: "medium", Status: "pending"}
		}
		return planUpdate{SessionUpdate: "plan", Entries: entries}, true

	default:
		return nil, false
	}
}

// ─── I/O helpers ──────────────────────────────────────────────────────────────

// writeResponse writes a JSON-RPC response for id, unless id is nil.
// acpRequest.ID unmarshals to nil exactly when the request's "id" member was
// absent — i.e. a JSON-RPC 2.0 Notification, which the spec forbids ever
// responding to. This is distinct from an explicit `"id": null`, which
// unmarshals to the non-nil json.RawMessage("null") and still gets a
// response, per spec.
func (s *Server) writeResponse(id json.RawMessage, result interface{}) {
	if id == nil {
		return
	}
	s.writeLine(acpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

// writeError mirrors writeResponse's notification check — see its comment.
// A caller that must respond despite not having parsed an id (e.g. a JSON
// parse error) passes an explicit json.RawMessage("null") rather than nil.
func (s *Server) writeError(id json.RawMessage, code int, msg string) {
	if id == nil {
		return
	}
	s.writeLine(acpResponse{JSONRPC: "2.0", ID: id, Error: &acpError{Code: code, Message: msg}})
}

func (s *Server) writeNotification(method string, params interface{}) {
	s.writeLine(acpNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) writeLine(v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(b) //nolint:errcheck
}
