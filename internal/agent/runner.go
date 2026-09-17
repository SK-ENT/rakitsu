package agent

import (
	"context"

	"github.com/SK-ENT/rakitsu/internal/debug"
)

// Runner is the common interface for executable entities in the orchestration graph.
// Both Agent and Orchestrator implement Runner, enabling nested orchestration
// where a parent orchestrator can delegate to either agents or sub-orchestrators.
type Runner interface {
	Run(ctx context.Context, query string) (string, error)
	GetName() string
	GetRole() AgentRole
	GetTools() []string
	SetDebugController(dc *debug.DebugController)
}
