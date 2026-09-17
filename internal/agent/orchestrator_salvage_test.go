package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/debug"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

// salvageFakeAgent is a Runner+SalvageReporter+OutcomeReporter test double.
// Per-call salvage state is driven by salvagedSchedule (one entry per Run
// call) — that's the strict "format adapter promoted reasoning" signal.
// Per-call unproductive state is driven by unproductiveSchedule — the
// broader unproductive signal (salvaged OR max_iter+empty OR success+empty).
// When unproductiveSchedule is nil, LastRunUnproductive() falls back to the
// salvage signal (preserves backwards compat: salvaged is always unproductive).
//
// Each test can shape its own pattern: e.g. salvagedSchedule=[false,false]
// with unproductiveSchedule=[true,true] models a worker that hits
// max_iter+empty twice (unproductive territory but not the old salvage-only narrow scope).
type salvageFakeAgent struct {
	name                 string
	answer               string
	salvagedSchedule     []bool
	unproductiveSchedule []bool
	callIdx              int32
	lastSalvaged         atomic.Bool
	lastUnproductive     atomic.Bool
	mu                   sync.Mutex
	callCount            int
}

func (f *salvageFakeAgent) GetName() string                              { return f.name }
func (f *salvageFakeAgent) GetRole() AgentRole                           { return RoleWorker }
func (f *salvageFakeAgent) GetTools() []string                           { return nil }
func (f *salvageFakeAgent) SetDebugController(dc *debug.DebugController) {}

func (f *salvageFakeAgent) Run(ctx context.Context, query string) (string, error) {
	idx := atomic.AddInt32(&f.callIdx, 1) - 1
	salvaged := false
	if int(idx) < len(f.salvagedSchedule) {
		salvaged = f.salvagedSchedule[int(idx)]
	}
	unproductive := salvaged // default: salvaged implies unproductive
	if f.unproductiveSchedule != nil && int(idx) < len(f.unproductiveSchedule) {
		unproductive = f.unproductiveSchedule[int(idx)]
	}
	f.lastSalvaged.Store(salvaged)
	f.lastUnproductive.Store(unproductive)
	f.mu.Lock()
	f.callCount++
	f.mu.Unlock()
	return f.answer, nil
}

func (f *salvageFakeAgent) LastRunSalvaged() bool {
	return f.lastSalvaged.Load()
}

func (f *salvageFakeAgent) LastRunUnproductive() bool {
	return f.lastUnproductive.Load()
}

func (f *salvageFakeAgent) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

// makeSalvageOrchestrator returns a bare-bones *Orchestrator with the given
// workers, suitable for calling DelegationTool.Execute directly. Skips the
// real supervisor LLM loop — we drive delegations explicitly to assert the
// gate semantics.
func makeSalvageOrchestrator(workers map[string]Runner, bus *telemetry.EventBus) *Orchestrator {
	return &Orchestrator{
		name:     "TestSupervisor",
		strategy: "Hierarchical",
		agents:   workers,
		eventBus: bus,
	}
}

// filterBlocked drains all collected events and returns the
// WorkerRedelegationBlockedPayload entries decoded in order.
func filterBlocked(t *testing.T, events []telemetry.AgentEvent) []telemetry.WorkerRedelegationBlockedPayload {
	t.Helper()
	var out []telemetry.WorkerRedelegationBlockedPayload
	for _, e := range events {
		if e.EventType != telemetry.EventWorkerRedelegationBlocked {
			continue
		}
		var p telemetry.WorkerRedelegationBlockedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode WorkerRedelegationBlockedPayload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// TestOrchestrator_FirstSalvageAllowsOneRetry: worker returns salvaged on
// call 1, clean on call 2. Both calls go through. No blocked event.
func TestOrchestrator_FirstSalvageAllowsOneRetry(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	worker := &salvageFakeAgent{name: "W", answer: "result", salvagedSchedule: []bool{true, false}}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	events := collectEvents(bus, func() {
		for i := 0; i < 2; i++ {
			out, err := dt.Execute(context.Background(), map[string]interface{}{"task": "go"})
			if err != nil {
				t.Fatalf("call %d returned error: %v", i+1, err)
			}
			if strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
				t.Fatalf("call %d unexpectedly blocked: %s", i+1, out)
			}
		}
	})

	if worker.Calls() != 2 {
		t.Errorf("worker.Calls() = %d, want 2", worker.Calls())
	}

	blocked := filterBlocked(t, events)
	if len(blocked) != 0 {
		t.Errorf("expected 0 WORKER_REDELEGATION_BLOCKED events, got %d: %+v", len(blocked), blocked)
	}
}

// TestOrchestrator_TwoSalvagesBlocksThird: worker returns salvaged on both
// call 1 and call 2. Third call is blocked: returns the [DELEGATION BLOCKED]
// marker, worker is not invoked, and a WORKER_REDELEGATION_BLOCKED event
// fires with PostSalvageCount=1 (1 post-salvage delegation already happened
// before the block — the second call).
func TestOrchestrator_TwoSalvagesBlocksThird(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	worker := &salvageFakeAgent{name: "W", answer: "result", salvagedSchedule: []bool{true, true, false}}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	events := collectEvents(bus, func() {
		// Call 1: salvaged. Allowed.
		if _, err := dt.Execute(context.Background(), map[string]interface{}{"task": "go"}); err != nil {
			t.Fatalf("call 1: %v", err)
		}
		// Call 2: post-salvage retry. Allowed (cap=1). Returns salvaged again.
		if _, err := dt.Execute(context.Background(), map[string]interface{}{"task": "go"}); err != nil {
			t.Fatalf("call 2: %v", err)
		}
		// Call 3: cap reached. BLOCKED.
		out, err := dt.Execute(context.Background(), map[string]interface{}{"task": "go"})
		if err != nil {
			t.Fatalf("call 3: %v", err)
		}
		if !strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
			t.Errorf("call 3 expected [DELEGATION BLOCKED] prefix, got %q", out)
		}
	})

	if worker.Calls() != 2 {
		t.Errorf("worker.Calls() = %d, want 2 (third call should be blocked before invocation)", worker.Calls())
	}

	blocked := filterBlocked(t, events)
	if len(blocked) != 1 {
		t.Fatalf("expected 1 WORKER_REDELEGATION_BLOCKED event, got %d: %+v", len(blocked), blocked)
	}
	got := blocked[0]
	if got.BlockedWorker != "W" {
		t.Errorf("BlockedWorker = %q, want %q", got.BlockedWorker, "W")
	}
	if got.PostSalvageCount != 1 {
		t.Errorf("PostSalvageCount = %d, want 1", got.PostSalvageCount)
	}
	if got.FromAgent != "TestSupervisor" {
		t.Errorf("FromAgent = %q, want %q", got.FromAgent, "TestSupervisor")
	}
}

// TestOrchestrator_BlockedReturnsToolOutputNotError: the blocked path must
// return (msg, nil) so the supervisor ReAct loop continues normally. Verifies
// the err is nil and the msg shape is informative.
func TestOrchestrator_BlockedReturnsToolOutputNotError(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	worker := &salvageFakeAgent{name: "W", answer: "r", salvagedSchedule: []bool{true, true}}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "go"})
	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "go"})
	out, err := dt.Execute(context.Background(), map[string]interface{}{"task": "go"})

	if err != nil {
		t.Fatalf("blocked path must return nil error, got %v", err)
	}
	if !strings.Contains(out, "DELEGATION BLOCKED") {
		t.Errorf("blocked output missing marker: %q", out)
	}
	if !strings.Contains(out, "W") {
		t.Errorf("blocked output missing worker name: %q", out)
	}
	// Output should encourage alternative strategy — substring check on the
	// canonical phrase so a future copy-edit gets caught:
	if !strings.Contains(out, "Pick a different worker") {
		t.Errorf("blocked output missing supervisor guidance: %q", out)
	}
}

// TestOrchestrator_DifferentWorkerStillAllowedAfterBlock: worker X is
// blocked; the supervisor delegates to worker Y. Y goes through normally.
// Verifies per-worker (not per-supervisor) scope.
func TestOrchestrator_DifferentWorkerStillAllowedAfterBlock(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	workerX := &salvageFakeAgent{name: "X", answer: "rx", salvagedSchedule: []bool{true, true}}
	workerY := &salvageFakeAgent{name: "Y", answer: "ry", salvagedSchedule: []bool{false}}
	orch := makeSalvageOrchestrator(map[string]Runner{"X": workerX, "Y": workerY}, bus)

	// Both delegation tools share one run-scoped state, same as
	// registerDelegationTools() wires them within a single runReAct() call —
	// the per-worker scope comes from the map key, not a separate state
	// object per worker.
	state := &orchestratorRunState{}
	dtX := &DelegationTool{agent: workerX, eventBus: bus, fromAgent: orch.name, runState: state}
	dtY := &DelegationTool{agent: workerY, eventBus: bus, fromAgent: orch.name, runState: state}

	// Burn X's salvage budget.
	_, _ = dtX.Execute(context.Background(), map[string]interface{}{"task": "x1"})
	_, _ = dtX.Execute(context.Background(), map[string]interface{}{"task": "x2"})

	// Y must still be invocable.
	out, err := dtY.Execute(context.Background(), map[string]interface{}{"task": "y1"})
	if err != nil {
		t.Fatalf("Y delegation errored unexpectedly: %v", err)
	}
	if strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
		t.Fatalf("Y unexpectedly blocked: %s", out)
	}
	if workerY.Calls() != 1 {
		t.Errorf("workerY.Calls() = %d, want 1", workerY.Calls())
	}
}

// TestOrchestrator_NewRunResetsSalvageState: block worker X in turn 1; a
// fresh orchestratorRunState (what runReAct allocates at the start of every
// Run) backs turn 2's delegation tool; first delegation to X in turn 2 goes
// through unblocked.
func TestOrchestrator_NewRunResetsSalvageState(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	worker := &salvageFakeAgent{name: "W", answer: "r", salvagedSchedule: []bool{true, true, false, false}}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	// Turn 1: salvage twice, then block.
	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "t1a"})
	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "t1b"})
	out, _ := dt.Execute(context.Background(), map[string]interface{}{"task": "t1c"})
	if !strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
		t.Fatalf("turn 1 third call should be blocked, got %q", out)
	}

	// Simulate a new Run: runReAct allocates a brand-new orchestratorRunState
	// (and therefore a brand-new DelegationTool) at the top of every call —
	// nothing carries over from turn 1's state.
	dt2 := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	// Turn 2: delegation should now go through.
	out2, err := dt2.Execute(context.Background(), map[string]interface{}{"task": "t2"})
	if err != nil {
		t.Fatalf("turn 2 call: %v", err)
	}
	if strings.HasPrefix(out2, "[DELEGATION BLOCKED]") {
		t.Fatalf("turn 2 unexpectedly blocked, got %q", out2)
	}
}

// ============================================================================
// Broader-unproductive tests — the cap from salvaged to any unproductive
// outcome. The fake's unproductiveSchedule decouples LastRunUnproductive()
// from LastRunSalvaged(); when unproductiveSchedule has true with
// salvagedSchedule false at the same index, that models a max_iter+empty
// or success+empty terminal status that the original salvage-only predicate
// would have missed.
// ============================================================================

// TestOrchestrator_TwoMaxIterEmptyBlocksThird: worker reports
// LastRunUnproductive()=true (but LastRunSalvaged()=false) on calls 1 and 2 —
// modeling the max_iterations+empty cascade this guards against.
// Third call must be blocked: returns [DELEGATION BLOCKED], no agent.Run
// invocation, and a WORKER_REDELEGATION_BLOCKED event fires with
// Reason="post-unproductive cap reached".
func TestOrchestrator_TwoMaxIterEmptyBlocksThird(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	// salvagedSchedule is all-false (this is NOT the salvage path).
	// unproductiveSchedule fires on the first two calls (max_iter+empty shape).
	worker := &salvageFakeAgent{
		name:                 "BackendAuditor",
		answer:               "[max_iterations reached — no response produced]",
		salvagedSchedule:     []bool{false, false, false},
		unproductiveSchedule: []bool{true, true, false},
	}
	orch := makeSalvageOrchestrator(map[string]Runner{"BackendAuditor": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	events := collectEvents(bus, func() {
		// Call 1: unproductive. Allowed.
		if _, err := dt.Execute(context.Background(), map[string]interface{}{"task": "audit"}); err != nil {
			t.Fatalf("call 1: %v", err)
		}
		// Call 2: post-unproductive retry. Allowed (cap=1). Still unproductive.
		if _, err := dt.Execute(context.Background(), map[string]interface{}{"task": "audit"}); err != nil {
			t.Fatalf("call 2: %v", err)
		}
		// Call 3: cap reached. BLOCKED.
		out, err := dt.Execute(context.Background(), map[string]interface{}{"task": "audit"})
		if err != nil {
			t.Fatalf("call 3: %v", err)
		}
		if !strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
			t.Errorf("call 3 expected [DELEGATION BLOCKED] prefix, got %q", out)
		}
	})

	if worker.Calls() != 2 {
		t.Errorf("worker.Calls() = %d, want 2 (third should be blocked before invocation)", worker.Calls())
	}

	blocked := filterBlocked(t, events)
	if len(blocked) != 1 {
		t.Fatalf("expected 1 WORKER_REDELEGATION_BLOCKED event, got %d: %+v", len(blocked), blocked)
	}
	got := blocked[0]
	if got.BlockedWorker != "BackendAuditor" {
		t.Errorf("BlockedWorker = %q, want BackendAuditor", got.BlockedWorker)
	}
	if got.PostSalvageCount != 1 {
		t.Errorf("PostSalvageCount = %d, want 1 (one post-unproductive delegation before the block)", got.PostSalvageCount)
	}
	if got.Reason != "post-unproductive cap reached" {
		t.Errorf("Reason = %q, want %q", got.Reason, "post-unproductive cap reached")
	}
}

// TestOrchestrator_SuccessEmptyTreatedAsUnproductive: worker returns clean-
// looking content but reports LastRunUnproductive()=true (a "success with
// empty answer" shape — common with reasoning models that put the answer
// into ThinkingContent and leave finalAnswer == ""). The gate must still
// engage; the unproductive signal doesn't discriminate among the three
// unproductive shapes (salvaged / max_iter+empty / success+empty).
func TestOrchestrator_SuccessEmptyTreatedAsUnproductive(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	worker := &salvageFakeAgent{
		name:                 "W",
		answer:               "", // empty answer is exactly the success-empty signal
		salvagedSchedule:     []bool{false, false, false},
		unproductiveSchedule: []bool{true, true, false},
	}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	events := collectEvents(bus, func() {
		_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "q1"})
		_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "q2"})
		out, _ := dt.Execute(context.Background(), map[string]interface{}{"task": "q3"})
		if !strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
			t.Errorf("third call should be blocked under success-empty cascade, got %q", out)
		}
	})

	blocked := filterBlocked(t, events)
	if len(blocked) != 1 {
		t.Errorf("expected 1 blocked event for success-empty cascade, got %d", len(blocked))
	}
}

// TestOrchestrator_MixedSalvageAndMaxIterCounted: a worker that salvages
// once then hits max_iter+empty once still has its third call blocked. The
// cap accumulates across different unproductive shapes (both flip the same
// unproductiveWorkers[w] marker; the count increments on every subsequent
// call once that marker is set, regardless of which specific shape fired).
func TestOrchestrator_MixedSalvageAndMaxIterCounted(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	worker := &salvageFakeAgent{
		name: "W",
		// Call 1: salvaged.
		// Call 2: max_iter+empty — not salvaged, but unproductive.
		// Call 3: any — must be blocked.
		answer:               "r",
		salvagedSchedule:     []bool{true, false, false},
		unproductiveSchedule: []bool{true, true, false},
	}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "a"})
	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "b"})
	out, _ := dt.Execute(context.Background(), map[string]interface{}{"task": "c"})

	if !strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
		t.Errorf("third call after mixed salvage+max_iter should be blocked, got %q", out)
	}
	if worker.Calls() != 2 {
		t.Errorf("worker.Calls() = %d, want 2", worker.Calls())
	}
}

// TestOrchestrator_LastRunUnproductivePreferredOverSalvaged: when a Runner
// implements both SalvageReporter and OutcomeReporter (as *Agent does),
// DelegationTool must read LastRunUnproductive() (the broader signal) rather
// than LastRunSalvaged() (the narrower one). A worker that's unproductive
// without being salvaged must still trigger the cap.
func TestOrchestrator_LastRunUnproductivePreferredOverSalvaged(t *testing.T) {
	bus := telemetry.NewEventBus(64)

	// salvagedSchedule is all-false. If the orchestrator wrongly read
	// LastRunSalvaged() instead of LastRunUnproductive(), the cap would never
	// arm and call 3 would pass through. Verifying the OutcomeReporter signal
	// is preferred over SalvageReporter at the DelegationTool.Execute site.
	worker := &salvageFakeAgent{
		name:                 "W",
		answer:               "r",
		salvagedSchedule:     []bool{false, false, false},
		unproductiveSchedule: []bool{true, true, false},
	}
	orch := makeSalvageOrchestrator(map[string]Runner{"W": worker}, bus)
	dt := &DelegationTool{agent: worker, eventBus: bus, fromAgent: orch.name, runState: &orchestratorRunState{}}

	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "a"})
	_, _ = dt.Execute(context.Background(), map[string]interface{}{"task": "b"})
	out, err := dt.Execute(context.Background(), map[string]interface{}{"task": "c"})

	if err != nil {
		t.Fatalf("third call returned error: %v", err)
	}
	if !strings.HasPrefix(out, "[DELEGATION BLOCKED]") {
		t.Errorf("OutcomeReporter signal not preferred — cap didn't arm; got %q", out)
	}
}

// TestOrchestrator_RegisterDelegationTools_IsolatesConcurrentRunState
// regression-guards finding 18: per-Run worker-result/unproductive tracking
// used to live directly on the shared *Orchestrator, guarded by mutexes —
// correct for one Run at a time, but the same *Orchestrator can be Run
// concurrently (e.g. a nested orchestrator reachable through two independent
// delegation paths). Two concurrent runReAct() calls sharing that state
// would corrupt each other: one Run's unproductive-worker tracking would
// silently arm the block gate for the other Run, and vice versa.
//
// A second gap surfaced later (rung 05's follow-up review): registering both
// runs' delegation tools into one shared, Orchestrator-lifetime registry has
// the same failure mode one level up — RegisterTool overwrites same-name
// entries, so Run B's registration replaces Run A's delegate_to_W tool
// object, and Run A's supervisor ends up executing Run B's tool (looked up
// by name at call time) even though the state objects themselves were
// correctly isolated. registerDelegationTools() now takes both the run's
// registry and its state explicitly, so this wires two independent
// registry+state pairs against the same Orchestrator — matching exactly
// what runReAct() allocates per call — drives both concurrently, and
// asserts neither run's tracking nor its tool registration leaks into the
// other's.
func TestOrchestrator_RegisterDelegationTools_IsolatesConcurrentRunState(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	worker := &salvageFakeAgent{
		name:   "W",
		answer: "r",
		// Every call reports unproductive, regardless of which goroutine's
		// call lands on which schedule index — keeps the assertions below
		// deterministic under concurrent interleaving.
		salvagedSchedule:     []bool{true, true, true, true, true, true},
		unproductiveSchedule: []bool{true, true, true, true, true, true},
	}
	orch := &Orchestrator{
		name:       "Sup",
		agentNames: []string{"W"},
		agents:     map[string]Runner{"W": worker},
		eventBus:   bus,
	}

	// Two independent registry+state pairs, as runReAct() would allocate
	// fresh for two concurrent Run() calls.
	registryA := tools.NewToolRegistry()
	registryB := tools.NewToolRegistry()
	stateA := &orchestratorRunState{}
	stateB := &orchestratorRunState{}

	orch.registerDelegationTools(registryA, stateA)
	orch.registerDelegationTools(registryB, stateB)

	toolAIface := registryA.GetTool("delegate_to_w")
	toolA, ok := toolAIface.(*DelegationTool)
	if !ok {
		t.Fatalf("GetTool returned %T, want *DelegationTool", toolAIface)
	}
	if toolA.runState != stateA {
		t.Fatalf("tool registered via registerDelegationTools(registryA, stateA) has a different runState — not wired to the state passed in")
	}

	toolBIface := registryB.GetTool("delegate_to_w")
	toolB, ok := toolBIface.(*DelegationTool)
	if !ok {
		t.Fatalf("GetTool returned %T, want *DelegationTool", toolBIface)
	}
	if toolB.runState != stateB {
		t.Fatalf("tool registered via registerDelegationTools(registryB, stateB) has a different runState — not wired to the state passed in")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = toolA.Execute(context.Background(), map[string]interface{}{"task": "a1"})
		_, _ = toolA.Execute(context.Background(), map[string]interface{}{"task": "a2"})
	}()
	go func() {
		defer wg.Done()
		_, _ = toolB.Execute(context.Background(), map[string]interface{}{"task": "b1"})
	}()
	wg.Wait()

	if !stateA.isRedelegationBlocked("W") {
		t.Error("Run A: worker W should be blocked after two unproductive delegations under state A")
	}
	if stateB.isRedelegationBlocked("W") {
		t.Error("Run B: made only one delegation under state B and must not be blocked — a leak from Run A's tracking would fail this")
	}

	// The regression this fix closes: even after both runs have registered,
	// each registry must still resolve delegate_to_w to its own run's tool
	// object — proving the registries themselves, not just the state, are
	// isolated. Before the fix there was only one shared registry, so this
	// property couldn't even be expressed.
	if got := registryA.GetTool("delegate_to_w").(*DelegationTool); got.runState != stateA {
		t.Error("registryA's delegate_to_w resolves to the wrong run's state — registries are not isolated")
	}
	if got := registryB.GetTool("delegate_to_w").(*DelegationTool); got.runState != stateB {
		t.Error("registryB's delegate_to_w resolves to the wrong run's state — registries are not isolated")
	}
}
