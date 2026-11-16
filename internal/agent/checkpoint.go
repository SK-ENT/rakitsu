package agent

import (
	"github.com/SK-ENT/rakitsu/internal/decision"
	"time"
)

// CheckpointData holds serialized pipeline progress for resume support.
// It lives in the agent package to avoid a circular import with store.
type CheckpointData struct {
	SessionID      string
	Query          string
	CompletedSteps []string
	Results        map[string]CheckpointStep
}

// CheckpointStep is the persisted form of a completed pipeline step.
type CheckpointStep struct {
	Decision *decision.Result `json:",omitempty"`
	Name     string
	Output   string
	Duration time.Duration
}

// historyCheckpoint records the conversation state at the start of a ReAct
// iteration so runtime self-correction (config.RollbackConfig) can rewind to
// it. Because conversation history grows append-only within a Run, a rewind
// is a slice truncation to historyLen — no deep copy is needed. lastResponse
// is snapshotted too so a rolled-back iteration's stray thought does not leak
// into the max_iterations graceful fallback.
type historyCheckpoint struct {
	iteration    int
	historyLen   int
	lastResponse string
}

// buildCheckpoint constructs a CheckpointData from the current PipelineContext.
// prior may be nil (first checkpoint) or the loaded checkpoint (resume mode —
// preserves SessionID).
func buildCheckpoint(pctx *PipelineContext, prior *CheckpointData) CheckpointData {
	pctx.mu.RLock()
	defer pctx.mu.RUnlock()

	cp := CheckpointData{
		Query:   pctx.Query,
		Results: make(map[string]CheckpointStep, len(pctx.Results)),
	}
	if prior != nil {
		cp.SessionID = prior.SessionID
	}

	for _, r := range pctx.Results {
		if r.Error == nil {
			cp.CompletedSteps = append(cp.CompletedSteps, r.Name)
			cp.Results[r.Name] = CheckpointStep{
				Name:     r.Name,
				Output:   r.Output,
				Duration: r.Duration,
				Decision: r.Decision,
			}
		}
	}
	return cp
}
