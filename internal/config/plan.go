package config

import (
	"fmt"

	"github.com/SK-ENT/rakitsu/internal/permission"
)

// Plan reports whether the configuration requests read-only tool execution.
func (s Settings) Plan() bool {
	return s.Focus.Mode == permission.FocusPlan
}

// PlanFileOperation is the shared allowlist for read-only filesystem operations.
func PlanFileOperation(operation string) bool {
	switch operation {
	case "read", "list", "search":
		return true
	default:
		return false
	}
}

// ValidatePlan rejects capabilities without a read-only execution path before
// any tool constructors can start processes or contact external services.
func (c *Config) ValidatePlan() error {
	if !c.Settings.Plan() {
		return nil
	}
	if len(c.Agents) != 1 || c.Orchestrator != nil || len(c.Orchestrators) != 0 ||
		len(c.Workflows) != 0 || c.ForceDelegation || (c.InteractiveOverlay != nil && *c.InteractiveOverlay) {
		return fmt.Errorf("plan focus requires one direct agent without orchestration or delegation")
	}
	if c.Settings.Memory.Enabled || c.Settings.Spawn.Enabled || c.Settings.SessionMsg.Enabled ||
		c.Settings.Wake.Enabled || len(c.Monitors) != 0 {
		return fmt.Errorf("plan focus does not allow memory, spawning, session messaging, wake, or monitors")
	}
	check := func(definitions []ToolDefinition) error {
		for _, definition := range definitions {
			if definition.Type != "fs" || !PlanFileOperation(definition.Operation) {
				return fmt.Errorf("plan focus does not allow tool %q (%s/%s); use filesystem read, list, or search", definition.Name, definition.Type, definition.Operation)
			}
		}
		return nil
	}
	if err := check(c.Tools); err != nil {
		return err
	}
	return check(c.Agents[0].ToolsInline)
}
