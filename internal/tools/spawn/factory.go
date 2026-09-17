// Package spawn implements the spawn_agent tool: runtime subagent fan-out.
// The tool builds nothing itself — cmd/rakitsu injects a Factory closure that
// owns provider/registry wiring, keeping this package free of package-main
// dependencies (same pattern as chathost.NewInvokeTool).
package spawn

import (
	"context"
	"fmt"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/agent"
)

// Spec describes one requested child agent.
type Spec struct {
	Task          string // the child's prompt (required)
	TemplateAgent string // YAML agent name used as template; "" = ad-hoc worker
	Name          string // unique instance name, already reserved by the tool
	Parent        string // spawning agent's instance name
	Depth         int    // child's depth (parent depth + 1)
}

// Factory builds a runnable child for a Spec. cleanup must be safe to call
// exactly once and is always called after the child finishes (or fails to build).
type Factory func(ctx context.Context, spec Spec) (child agent.Runner, cleanup func(), err error)

// RunState is shared by every spawn tool instance of one run: a run-global
// concurrency semaphore plus the unique-name counter.
type RunState struct {
	sem      chan struct{}
	mu       sync.Mutex
	names    map[string]int
	reserved map[string]bool
}

// NewRunState creates run-shared spawn state with the given concurrency cap.
// reservedNames are the config's own agent/template names — a generated
// "<base>-N" name is skipped if it collides with one of these, since agent
// names carry no reserved-suffix convention (a config can legitimately
// define an agent literally named "Researcher-1").
func NewRunState(maxConcurrent int, reservedNames []string) *RunState {
	reserved := make(map[string]bool, len(reservedNames))
	for _, n := range reservedNames {
		reserved[n] = true
	}
	return &RunState{
		sem:      make(chan struct{}, maxConcurrent),
		names:    make(map[string]int),
		reserved: reserved,
	}
}

// uniqueName reserves the next instance name for base. Always suffixed
// ("base-1", "base-2", …), skipping any candidate that collides with a
// configured agent/template name, so a spawned child can never collide with
// one in name-keyed tree views.
func (s *RunState) uniqueName(base string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		s.names[base]++
		candidate := fmt.Sprintf("%s-%d", base, s.names[base])
		if !s.reserved[candidate] {
			return candidate
		}
	}
}
