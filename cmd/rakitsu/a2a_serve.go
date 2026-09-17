package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/server"
)

// ─── Server-side A2A JSON-RPC types (A2A v1.0.1, github.com/a2aproject/A2A) ──
//
// Modeled from specification/a2a.proto + docs/specification.md at tag
// v1.0.1 (the current release as of this writing; verified against main,
// structurally identical). JSON-RPC method names are PascalCase, matching
// the gRPC method names 1:1 (spec section 9.1) — NOT the "category/action"
// style ("tasks/send") of the pre-1.0 protocol this file used to speak.
// All JSON field names are camelCase; enums serialize as their proto
// SCREAMING_SNAKE_CASE names (spec section 5.5).

type srvA2ARequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type srvA2AResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *srvA2ARPCErr   `json:"error,omitempty"`
}

type srvA2ARPCErr struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Task states (proto enum TaskState). Rakitsu's handler is synchronous, so
// in practice it only ever produces TASK_STATE_COMPLETED/FAILED — the rest
// exist so the type system doesn't lie about what the real protocol models.
const (
	taskStateSubmitted     = "TASK_STATE_SUBMITTED"
	taskStateWorking       = "TASK_STATE_WORKING"
	taskStateCompleted     = "TASK_STATE_COMPLETED"
	taskStateFailed        = "TASK_STATE_FAILED"
	taskStateCanceled      = "TASK_STATE_CANCELED"
	taskStateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	taskStateRejected      = "TASK_STATE_REJECTED"
	taskStateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

func isTerminalTaskState(s string) bool {
	switch s {
	case taskStateCompleted, taskStateFailed, taskStateCanceled, taskStateRejected:
		return true
	}
	return false
}

const (
	roleUser  = "ROLE_USER"
	roleAgent = "ROLE_AGENT"
)

type srvA2AMessage struct {
	MessageID        string                 `json:"messageId"`
	ContextID        string                 `json:"contextId,omitempty"`
	TaskID           string                 `json:"taskId,omitempty"`
	Role             string                 `json:"role"`
	Parts            []srvA2APart           `json:"parts"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	Extensions       []string               `json:"extensions,omitempty"`
	ReferenceTaskIDs []string               `json:"referenceTaskIds,omitempty"`
}

// srvA2APart mirrors the proto `oneof content` (text/raw/url/data) — in
// ProtoJSON a oneof serializes as whichever single field is present, no
// "kind" discriminator. Rakitsu only ever populates Text today.
type srvA2APart struct {
	Text      string                 `json:"text,omitempty"`
	Raw       string                 `json:"raw,omitempty"` // base64; unused
	URL       string                 `json:"url,omitempty"`
	Data      interface{}            `json:"data,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Filename  string                 `json:"filename,omitempty"`
	MediaType string                 `json:"mediaType,omitempty"`
}

type srvA2ATask struct {
	ID        string                 `json:"id"`
	ContextID string                 `json:"contextId,omitempty"`
	Status    srvA2AStatus           `json:"status"`
	Artifacts []srvA2AArtifact       `json:"artifacts,omitempty"`
	History   []srvA2AMessage        `json:"history,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type srvA2AStatus struct {
	State     string         `json:"state"`
	Message   *srvA2AMessage `json:"message,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
}

type srvA2AArtifact struct {
	ArtifactID  string                 `json:"artifactId"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parts       []srvA2APart           `json:"parts"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// srvSendMessageResult mirrors SendMessageResponse's oneof — rakitsu's
// synchronous handler always creates a Task, never a bare Message reply.
type srvSendMessageResult struct {
	Task *srvA2ATask `json:"task,omitempty"`
}

type srvSendMessageParams struct {
	// Tenant carries the target agent name. Real A2A ties one AgentCard/
	// endpoint to one agent identity; rakitsu deliberately repurposes the
	// spec-sanctioned `tenant` field ("used for routing requests to a
	// specific agent... when multiple agents are served behind a single A2A
	// endpoint" — spec section 4.4, AgentInterface) to keep serving every
	// agent in a config from one /a2a endpoint.
	Tenant  string        `json:"tenant,omitempty"`
	Message srvA2AMessage `json:"message"`
}

type srvGetTaskParams struct {
	Tenant string `json:"tenant,omitempty"`
	ID     string `json:"id"`
}

type srvCancelTaskParams struct {
	Tenant string `json:"tenant,omitempty"`
	ID     string `json:"id"`
}

// ─── Agent Card (discovery) types ────────────────────────────────────────────

type srvAgentCard struct {
	Name                string               `json:"name"`
	Description         string               `json:"description"`
	SupportedInterfaces []srvAgentInterface  `json:"supportedInterfaces"`
	Version             string               `json:"version"`
	Capabilities        srvAgentCapabilities `json:"capabilities"`
	DefaultInputModes   []string             `json:"defaultInputModes"`
	DefaultOutputModes  []string             `json:"defaultOutputModes"`
	Skills              []srvAgentSkill      `json:"skills"`
	// SecuritySchemes/Security are populated only when RAKITSU_API_TOKEN is
	// configured — omitted entirely otherwise, so the card never claims an
	// auth requirement that doesn't exist. See buildAgentCard.
	SecuritySchemes map[string]srvSecurityScheme `json:"securitySchemes,omitempty"`
	Security        []map[string][]string        `json:"security,omitempty"`
}

// srvSecurityScheme is the OpenAPI-style HTTP-bearer scheme A2A's
// securitySchemes field expects — the only scheme rakitsu's /a2a ever
// requires (AuthMiddleware checks "Authorization: Bearer <token>").
type srvSecurityScheme struct {
	Type   string `json:"type"`
	Scheme string `json:"scheme"`
}

type srvAgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

type srvAgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

type srvAgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// ─── In-memory task store ────────────────────────────────────────────────────

// maxStoredA2ATasks bounds the store so a long-running `rakitsu serve`
// doesn't grow this unboundedly. Tasks are held in-memory only — a restart
// loses history, same as the rest of rakitsu's non-persistent server state.
const maxStoredA2ATasks = 500

type a2aTaskStore struct {
	mu    sync.Mutex
	tasks map[string]*srvA2ATask
	order []string
}

func newA2ATaskStore() *a2aTaskStore {
	return &a2aTaskStore{tasks: make(map[string]*srvA2ATask)}
}

// cloneTask deep-copies t via a marshal/unmarshal round trip. A shallow
// `cp := *t` only copies the outer struct — Artifacts, History,
// Status.Message, each artifact's Parts, and the Metadata maps would stay
// shared with the stored task, which is weaker than what put/get/
// cancelIfPossible's doc comments promise ("never the internal pointer,"
// "a concurrent write can never race with a caller reading... what get
// returned"). Nothing mutates a task after put today, so that gap isn't
// live yet — but the store exists for when async execution lands, and a
// shallow copy is exactly the part of it that wouldn't survive that change.
func cloneTask(t *srvA2ATask) *srvA2ATask {
	b, err := json.Marshal(t)
	if err != nil {
		// t was already produced by this package's own marshal-safe types;
		// a failure here means a bug in this file, not bad input.
		panic(fmt.Sprintf("a2a: cloneTask: marshal: %v", err))
	}
	var cp srvA2ATask
	if err := json.Unmarshal(b, &cp); err != nil {
		panic(fmt.Sprintf("a2a: cloneTask: unmarshal: %v", err))
	}
	return &cp
}

// put stores a copy of t, never the caller's pointer — so a caller mutating
// its own copy afterward can never reach into the store unsynchronized.
func (s *a2aTaskStore) put(t *srvA2ATask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := cloneTask(t)
	if _, exists := s.tasks[cp.ID]; !exists {
		s.order = append(s.order, cp.ID)
		if len(s.order) > maxStoredA2ATasks {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.tasks, oldest)
		}
	}
	s.tasks[cp.ID] = cp
}

// get returns a copy of the stored task, never the internal pointer — so a
// concurrent write can never race with a caller reading (e.g. marshaling)
// what get returned.
func (s *a2aTaskStore) get(id string) (*srvA2ATask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false
	}
	return cloneTask(t), true
}

// cancelIfPossible atomically checks and, if the task isn't already
// terminal, cancels it — under one lock acquisition, unlike a get-then-put
// pair, which would let a concurrent cancel race the mutation. alreadyTerminal
// distinguishes "found but not cancelable" from "not found" so the caller
// can return the right JSON-RPC error.
func (s *a2aTaskStore) cancelIfPossible(id string) (task *srvA2ATask, found bool, alreadyTerminal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false, false
	}
	if isTerminalTaskState(t.Status.State) {
		return cloneTask(t), true, true
	}
	t.Status = srvA2AStatus{State: taskStateCanceled, Timestamp: time.Now().UTC().Format(time.RFC3339)}
	return cloneTask(t), true, false
}

// ─── subConfigForAgent ────────────────────────────────────────────────────────

// subConfigForAgent returns a stripped config containing only the named agent
// and the global tool definitions. Used by the A2A handler to run a single
// remote agent without starting an orchestrator.
func subConfigForAgent(cfg *config.Config, agentName string) (*config.Config, error) {
	for _, a := range cfg.Agents {
		if a.Name == agentName {
			return &config.Config{
				Name:     cfg.Name,
				Settings: cfg.Settings,
				Tools:    cfg.Tools,
				Agents:   []config.AgentDefinition{a},
			}, nil
		}
	}
	return nil, fmt.Errorf("agent %q not found in config", agentName)
}

// ─── a2aHandlerFunc ───────────────────────────────────────────────────────────

// a2aRunFunc runs the given sub-config against query and returns its result
// text. In production this wraps executeConfig (see serve.go); tests inject
// a stub so the handler can be exercised without a real LLM provider.
type a2aRunFunc func(ctx context.Context, cfg *config.Config, query string) (string, error)

// a2aHandlerFunc returns an http.HandlerFunc implementing the real A2A
// JSON-RPC binding (SendMessage, GetTask, CancelTask) over a single /a2a
// endpoint, dispatching to the agent named by params.tenant.
//
// Chat mode is explicitly *not* honored here. A2A is an RPC protocol — peers
// call agents expecting a one-shot result. Even if the target config declares
// `interactive: true`, this handler skips runInteractive / ChatHost and goes
// straight to run (executeConfig in production). Do NOT change this without
// auditing every caller in the agent graph — an interactive sub-agent
// mid-pipeline would block the whole RPC on a non-existent human prompt.
func a2aHandlerFunc(cfg *config.Config, run a2aRunFunc) http.HandlerFunc {
	store := newA2ATaskStore()

	return func(w http.ResponseWriter, r *http.Request) {
		writeResult := func(id json.RawMessage, result interface{}) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(srvA2AResponse{JSONRPC: "2.0", ID: id, Result: result}) //nolint:errcheck
		}
		writeErr := func(id json.RawMessage, code int, msg string) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(srvA2AResponse{ //nolint:errcheck
				JSONRPC: "2.0", ID: id,
				Error: &srvA2ARPCErr{Code: code, Message: msg},
			})
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req srvA2ARequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeErr(nil, -32700, "Invalid JSON payload")
			return
		}

		switch req.Method {
		case "SendMessage":
			handleA2ASendMessage(r.Context(), cfg, run, store, req, writeResult, writeErr)
		case "GetTask":
			handleA2AGetTask(store, req, writeResult, writeErr)
		case "CancelTask":
			handleA2ACancelTask(store, req, writeResult, writeErr)
		default:
			writeErr(req.ID, -32601, "Method not found")
		}
	}
}

func handleA2ASendMessage(
	ctx context.Context,
	cfg *config.Config,
	run a2aRunFunc,
	store *a2aTaskStore,
	req srvA2ARequest,
	writeResult func(json.RawMessage, interface{}),
	writeErr func(json.RawMessage, int, string),
) {
	var params srvSendMessageParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeErr(req.ID, -32602, "Invalid parameters: "+err.Error())
		return
	}
	if params.Tenant == "" {
		writeErr(req.ID, -32602, "Invalid parameters: tenant (target agent name) is required")
		return
	}
	if len(params.Message.Parts) == 0 || params.Message.Parts[0].Text == "" {
		writeErr(req.ID, -32602, "Invalid parameters: message.parts[0].text is required")
		return
	}
	if len(params.Message.Parts) > 1 {
		// Only Parts[0] is ever read below. Silently ignoring the rest
		// would mean a successful-looking task computed from a fraction of
		// what the caller sent — reject explicitly instead, same as TaskID
		// continuation right below.
		writeErr(req.ID, -32004, "Unsupported operation: only a single message part is supported yet")
		return
	}
	if params.Message.TaskID != "" {
		writeErr(req.ID, -32004, "Unsupported operation: continuing an existing task is not supported yet")
		return
	}
	if params.Message.ContextID != "" {
		// Grouping tasks under a client-chosen context is part of
		// multi-turn continuation, out of scope alongside TaskID above —
		// but silently substituting a fresh contextId (as this used to do)
		// gives the caller no signal its grouping didn't take, unlike the
		// clean rejection TaskID gets. Same treatment here.
		writeErr(req.ID, -32004, "Unsupported operation: continuing an existing context is not supported yet")
		return
	}

	subCfg, err := subConfigForAgent(cfg, params.Tenant)
	if err != nil {
		writeErr(req.ID, -32602, "Invalid parameters: "+err.Error())
		return
	}

	query := params.Message.Parts[0].Text
	result, execErr := run(ctx, subCfg, query)

	taskID := uuid.New().String()
	contextID := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	task := &srvA2ATask{
		ID:        taskID,
		ContextID: contextID,
		History:   []srvA2AMessage{params.Message},
	}
	if execErr != nil {
		task.Status = srvA2AStatus{
			State:     taskStateFailed,
			Timestamp: now,
			Message: &srvA2AMessage{
				MessageID: uuid.New().String(), ContextID: contextID, TaskID: taskID,
				Role: roleAgent, Parts: []srvA2APart{{Text: execErr.Error()}},
			},
		}
	} else {
		task.Status = srvA2AStatus{State: taskStateCompleted, Timestamp: now}
		task.Artifacts = []srvA2AArtifact{{ArtifactID: uuid.New().String(), Parts: []srvA2APart{{Text: result}}}}
	}

	store.put(task)
	writeResult(req.ID, srvSendMessageResult{Task: task})
}

func handleA2AGetTask(
	store *a2aTaskStore,
	req srvA2ARequest,
	writeResult func(json.RawMessage, interface{}),
	writeErr func(json.RawMessage, int, string),
) {
	var params srvGetTaskParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeErr(req.ID, -32602, "Invalid parameters: "+err.Error())
		return
	}
	if params.ID == "" {
		writeErr(req.ID, -32602, "Invalid parameters: id is required")
		return
	}
	task, ok := store.get(params.ID)
	if !ok {
		writeErr(req.ID, -32001, "Task not found")
		return
	}
	writeResult(req.ID, task)
}

func handleA2ACancelTask(
	store *a2aTaskStore,
	req srvA2ARequest,
	writeResult func(json.RawMessage, interface{}),
	writeErr func(json.RawMessage, int, string),
) {
	var params srvCancelTaskParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeErr(req.ID, -32602, "Invalid parameters: "+err.Error())
		return
	}
	if params.ID == "" {
		writeErr(req.ID, -32602, "Invalid parameters: id is required")
		return
	}
	// The terminal-state check and the cancel itself happen under one lock
	// (a2aTaskStore.cancelIfPossible) rather than a get-then-put pair, so a
	// concurrent CancelTask on the same task can't race the mutation.
	// Unreachable in practice today — a2aHandlerFunc runs SendMessage
	// synchronously, so a task is always already terminal by the time it's
	// stored — but kept correct for when/if async execution lands.
	task, found, alreadyTerminal := store.cancelIfPossible(params.ID)
	if !found {
		writeErr(req.ID, -32001, "Task not found")
		return
	}
	if alreadyTerminal {
		writeErr(req.ID, -32002, "Task not cancelable: already in a terminal state")
		return
	}
	writeResult(req.ID, task)
}

// ─── agentCardHandlerFunc ─────────────────────────────────────────────────────

// agentCardHandlerFunc serves the Agent Card at GET /.well-known/agent-card.json
// (spec section 8.2), the route a real A2A client uses to discover rakitsu at
// all. Every agent in cfg is listed as an AgentSkill; a caller addresses one by
// setting SendMessage's `tenant` param to that skill's id (the agent name).
//
// fallbackURL is only used when a request somehow has no Host (malformed or
// synthetic) — otherwise the advertised URL is derived from the incoming
// request's Host header, not fallbackURL. `--host` values that don't
// describe a reachable client-facing address (`localhost` reached from
// elsewhere, or a wildcard bind like `0.0.0.0`) would otherwise get baked
// into the card verbatim; a real client's own request necessarily carries
// the address it actually used to reach this server, which is always the
// right answer regardless of how rakitsu was told to bind.
func agentCardHandlerFunc(cfg *config.Config, fallbackURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		selfURL := fallbackURL
		if r.Host != "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			selfURL = fmt.Sprintf("%s://%s/a2a", scheme, r.Host)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(buildAgentCard(cfg, selfURL, server.TokenConfigured())) //nolint:errcheck
	}
}

// buildAgentCard assembles the advertised card. authRequired is passed in
// (rather than read from the environment here) so buildAgentCard stays a
// pure function of its inputs — server.TokenConfigured() is the one place
// that actually reads RAKITSU_API_TOKEN.
func buildAgentCard(cfg *config.Config, selfURL string, authRequired bool) srvAgentCard {
	skills := make([]srvAgentSkill, 0, len(cfg.Agents))
	for _, a := range cfg.Agents {
		skills = append(skills, srvAgentSkill{
			ID:          a.Name,
			Name:        a.Name,
			Description: fmt.Sprintf("Rakitsu agent %q. Address it by setting the request's \"tenant\" field to %q.", a.Name, a.Name),
			Tags:        []string{"rakitsu"},
		})
	}

	name := cfg.Name
	if name == "" {
		name = "Rakitsu"
	}
	desc := cfg.Description
	if desc == "" {
		desc = fmt.Sprintf("Rakitsu multi-agent config %q, exposed over A2A.", name)
	}
	version := cfg.Version
	if version == "" {
		version = "1.0.0"
	}

	card := srvAgentCard{
		Name:        name,
		Description: desc,
		Version:     version,
		SupportedInterfaces: []srvAgentInterface{
			// ProtocolVersion names the Agent2Agent *wire schema* this server
			// implements (a2aproject/A2A, spec v1.0.1) — unrelated to the
			// config's own Version above, which is this agent's own release.
			{URL: selfURL, ProtocolBinding: "JSONRPC", ProtocolVersion: "1.0.1"},
		},
		Capabilities:       srvAgentCapabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             skills,
	}

	// Only advertise the requirement when it's real — RAKITSU_API_TOKEN
	// unset means AuthMiddleware is a pass-through, and a card claiming
	// bearer auth in that case would mislead a client into sending a
	// credential nothing checks.
	if authRequired {
		card.SecuritySchemes = map[string]srvSecurityScheme{
			"bearerAuth": {Type: "http", Scheme: "bearer"},
		}
		card.Security = []map[string][]string{{"bearerAuth": {}}}
	}

	return card
}
