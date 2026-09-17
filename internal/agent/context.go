package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Injection detection patterns
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+instructions`),
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?above\s+instructions`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?previous`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+a\b`),
	regexp.MustCompile(`(?i)new\s+instructions?:`),
	regexp.MustCompile(`(?i)^system:\s`),
	regexp.MustCompile(`(?i)^user:\s`),
	regexp.MustCompile(`(?i)^assistant:\s`),
}

// ContextMonitor manages conversation context for the ReAct loop.
// It is a pure Go component — no LLM calls, deterministic, no injection surface.
type ContextMonitor struct {
	mu                    sync.Mutex
	strategy              string
	windowSize            int
	keepRecent            int
	maxToolOutput         int
	fenceOutputs          bool
	autoFullThreshold     int     // auto: switch to sliding_window above this message count
	autoCompressThreshold int     // auto: switch to step_log above this message count
	lastApplied           string  // auto: last strategy applied by buildAuto
	tokenPressure         float64 // current 0.0–1.0 pressure ratio; 0 = unknown
	budgetThreshold       float64 // pressure to trigger sliding_window (default 0.75)
	retrievalThreshold    float64 // pressure to trigger step_log (default 0.90)

	// retrieval (optional — set via SetRetriever)
	retriever     ContextRetriever
	retrievalTopK int
}

// NewContextMonitor creates a ContextMonitor from config.
// Zero-value config → "full" strategy with no fencing/truncation (current behavior).
func NewContextMonitor(cfg config.ContextConfig) *ContextMonitor {
	strategy := cfg.Strategy
	if strategy == "" {
		strategy = "full"
	}
	keepRecent := cfg.KeepRecent
	if keepRecent <= 0 && (strategy == "step_log" || strategy == "auto") {
		keepRecent = 3
	}
	windowSize := cfg.WindowSize
	if windowSize <= 0 && (strategy == "sliding_window" || strategy == "auto") {
		windowSize = 20
	}
	autoFull := cfg.AutoFullThreshold
	if autoFull <= 0 {
		autoFull = 30
	}
	autoCompress := cfg.AutoCompressThreshold
	if autoCompress <= 0 {
		autoCompress = 60
	}
	budgetThreshold := cfg.ContextBudgetThreshold
	if budgetThreshold <= 0 {
		budgetThreshold = 0.75
	}
	retrievalThreshold := cfg.ContextRetrievalThreshold
	if retrievalThreshold <= 0 {
		retrievalThreshold = 0.90
	}
	return &ContextMonitor{
		strategy:              strategy,
		windowSize:            windowSize,
		keepRecent:            keepRecent,
		maxToolOutput:         cfg.MaxToolOutput,
		fenceOutputs:          cfg.FenceOutputs,
		autoFullThreshold:     autoFull,
		autoCompressThreshold: autoCompress,
		budgetThreshold:       budgetThreshold,
		retrievalThreshold:    retrievalThreshold,
	}
}

// SetRetriever wires a ContextRetriever into the monitor.
// When set, retrieval is injected during BuildHistory whenever strategy != "full".
func (cm *ContextMonitor) SetRetriever(r ContextRetriever, topK int) {
	if topK <= 0 {
		topK = 5
	}
	cm.mu.Lock()
	cm.retriever = r
	cm.retrievalTopK = topK
	cm.mu.Unlock()
}

// IsFullStrategy returns true when the monitor is using the default "full" strategy
// with no truncation or fencing — meaning it has zero effect on behavior.
func (cm *ContextMonitor) IsFullStrategy() bool {
	return cm.strategy == "full" && cm.maxToolOutput <= 0 && !cm.fenceOutputs
}

// NeedsStepLog returns true when the strategy requires an external step log.
func (cm *ContextMonitor) NeedsStepLog() bool {
	return cm.strategy == "step_log" || cm.strategy == "sliding_window" || cm.strategy == "auto"
}

// SetTokenPressure updates the monitor with the current token pressure ratio.
// inputTokens is the prompt token count from the last LLM call;
// maxContextTokens is the model's context window size (must be > 0).
func (cm *ContextMonitor) SetTokenPressure(inputTokens, maxContextTokens int) {
	if maxContextTokens <= 0 {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.tokenPressure = float64(inputTokens) / float64(maxContextTokens)
}

// Pressure returns the current token pressure ratio (0.0–1.0).
// Returns 0 when no pressure data has been set yet.
func (cm *ContextMonitor) Pressure() float64 {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.tokenPressure
}

// AppliedStrategy returns the strategy that was most recently applied.
// For non-auto strategies, this is the configured strategy.
// For "auto", this is the strategy selected on the last BuildHistory call.
func (cm *ContextMonitor) AppliedStrategy() string {
	if cm.strategy != "auto" {
		return cm.strategy
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.lastApplied
}

// BuildHistory constructs the message history to send to the LLM.
// For "full" strategy, returns the raw history unchanged.
// For other strategies, builds compact context from the step log.
// Returns the history and the number of retrieved segments injected (0 when retrieval is off or strategy is "full").
func (cm *ContextMonitor) BuildHistory(stepLog *StepLog, rawHistory []llm.Message) ([]llm.Message, int) {
	var history []llm.Message
	switch cm.strategy {
	case "sliding_window":
		history = cm.buildSlidingWindow(rawHistory)
	case "step_log":
		history = cm.buildStepLog(stepLog)
	case "auto":
		history = cm.buildAuto(stepLog, rawHistory)
	default: // "full"
		return rawHistory, 0
	}

	// Inject retrieved context when strategy is not "full" and a retriever is configured.
	cm.mu.Lock()
	retriever := cm.retriever
	topK := cm.retrievalTopK
	cm.mu.Unlock()

	if retriever == nil || stepLog == nil {
		return history, 0
	}

	segments := retriever.Query(stepLog.Query, topK)
	if len(segments) == 0 {
		return history, 0
	}

	block := BuildRetrievalBlock(segments)
	injected := llm.NewTextMessage("user", block)

	// Insert after the first message (the user query) so the LLM sees:
	// [query] → [retrieved context] → [strategy-compressed history]
	result := make([]llm.Message, 0, len(history)+1)
	if len(history) > 0 {
		result = append(result, history[0])
	}
	result = append(result, injected)
	if len(history) > 1 {
		result = append(result, history[1:]...)
	}

	return result, len(segments)
}

// buildAuto selects a strategy based on token pressure (primary) or message count (fallback).
// Token pressure takes priority when maxContextTokens is known (pressure > 0).
// When pressure is unknown (0), message count thresholds are used as before.
func (cm *ContextMonitor) buildAuto(stepLog *StepLog, rawHistory []llm.Message) []llm.Message {
	cm.mu.Lock()
	pressure := cm.tokenPressure
	budgetT := cm.budgetThreshold
	retrievalT := cm.retrievalThreshold
	cm.mu.Unlock()

	var strategy string
	if pressure > 0 && budgetT > 0 {
		// Token pressure known — use it as primary signal
		switch {
		case pressure >= retrievalT:
			strategy = "step_log"
		case pressure >= budgetT:
			strategy = "sliding_window"
		default:
			strategy = "full"
		}
	} else {
		// Pressure unknown — fall back to message count thresholds
		n := len(rawHistory)
		switch {
		case n < cm.autoFullThreshold:
			strategy = "full"
		case n < cm.autoCompressThreshold:
			strategy = "sliding_window"
		default:
			strategy = "step_log"
		}
	}

	cm.mu.Lock()
	cm.lastApplied = strategy
	cm.mu.Unlock()

	switch strategy {
	case "sliding_window":
		return cm.buildSlidingWindow(rawHistory)
	case "step_log":
		return cm.buildStepLog(stepLog)
	default:
		return rawHistory
	}
}

// truncateUTF8 truncates s to at most maxBytes bytes, snapping backward to the
// nearest rune boundary so the result is always valid UTF-8. A byte-offset
// slice like s[:n] can otherwise land mid-codepoint and corrupt the string.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// SanitizeOutput applies fencing and truncation to a tool output before it enters the LLM context.
func (cm *ContextMonitor) SanitizeOutput(toolName, callID, output string) string {
	// Truncate first (before fencing, so fence markers aren't counted toward limit)
	if cm.maxToolOutput > 0 && len(output) > cm.maxToolOutput {
		truncated := truncateUTF8(output, cm.maxToolOutput)
		output = truncated + fmt.Sprintf("\n... [truncated, %d chars omitted]", len(output)-len(truncated))
	}

	// Fence
	if cm.fenceOutputs {
		output = fmt.Sprintf("<tool_output name=%q call_id=%q>\n%s\n</tool_output>", toolName, callID, output)
	}

	return output
}

// DetectInjection scans output for suspicious prompt injection patterns.
// Returns a list of warnings. Non-blocking — the caller decides how to handle them.
func (cm *ContextMonitor) DetectInjection(output string) []string {
	var warnings []string
	for _, pat := range injectionPatterns {
		if loc := pat.FindStringIndex(output); loc != nil {
			// Extract a snippet around the match for context
			start := loc[0]
			end := loc[1]
			if end-start > 100 {
				end = start + 100
			}
			warnings = append(warnings, fmt.Sprintf("suspicious pattern detected: %q", output[start:end]))
		}
	}
	return warnings
}

// buildSlidingWindow keeps the query + last N turns (snapped to turn boundaries).
// A "turn" is an assistant message + its tool result messages.
func (cm *ContextMonitor) buildSlidingWindow(rawHistory []llm.Message) []llm.Message {
	if len(rawHistory) <= cm.windowSize {
		return rawHistory
	}

	// Find turn boundaries: each turn spans from just after the previous
	// turn (or the query) through an assistant message and its trailing
	// tool messages. Anchoring start at prevEnd — rather than at the
	// assistant index itself — folds in any standalone message preceding
	// that assistant reply (e.g. a retry/corrective "user" message injected
	// mid-loop) instead of silently dropping it.
	type turn struct {
		start, end int // indices into rawHistory
	}
	var turns []turn
	prevEnd := 1 // index 0 is the user query, handled separately below
	for i := 1; i < len(rawHistory); i++ {
		if rawHistory[i].Role == "assistant" {
			t := turn{start: prevEnd, end: i + 1}
			// Include following tool messages
			for t.end < len(rawHistory) && rawHistory[t.end].Role == "tool" {
				t.end++
			}
			turns = append(turns, t)
			prevEnd = t.end
			i = t.end - 1 // skip past tool messages
		}
	}
	// A trailing corrective message with no assistant reply yet (e.g. the
	// directive injected right before the next retry) has no turn of its
	// own — capture it as a final turn so it isn't dropped.
	if prevEnd < len(rawHistory) {
		turns = append(turns, turn{start: prevEnd, end: len(rawHistory)})
	}

	// Keep the first message (user query) + last N turns worth of messages
	result := []llm.Message{rawHistory[0]} // user query

	// Calculate how many turns fit in the window. The single most recent
	// turn is always admitted regardless of budget — otherwise a turn
	// alone larger than windowSize (e.g. many parallel tool results in one
	// iteration) would break out before keepFrom ever advances, discarding
	// the entire conversation instead of keeping a partial window.
	msgCount := 1 // the query
	keepFrom := len(turns)
	for i := len(turns) - 1; i >= 0; i-- {
		turnMsgs := turns[i].end - turns[i].start
		if i != len(turns)-1 && msgCount+turnMsgs > cm.windowSize {
			break
		}
		msgCount += turnMsgs
		keepFrom = i
	}

	for i := keepFrom; i < len(turns); i++ {
		result = append(result, rawHistory[turns[i].start:turns[i].end]...)
	}

	return result
}

// buildStepLog builds compact context from the step log.
// Structure: [query] + [execution context summary] + [recent steps as messages]
func (cm *ContextMonitor) buildStepLog(stepLog *StepLog) []llm.Message {
	if stepLog == nil || len(stepLog.Steps) == 0 {
		return []llm.Message{llm.NewTextMessage("user", stepLog.Query)}
	}

	totalSteps := len(stepLog.Steps)
	recentStart := totalSteps - cm.keepRecent
	if recentStart < 0 {
		recentStart = 0
	}

	var result []llm.Message

	// 1. Original query
	result = append(result, llm.NewTextMessage("user", stepLog.Query))

	// 2. Execution context (summary of old steps)
	if recentStart > 0 {
		var sb strings.Builder
		sb.WriteString("## Previous Steps\n")
		for i := 0; i < recentStart; i++ {
			s := &stepLog.Steps[i]
			sb.WriteString(cm.summarizeStep(i+1, s))
			sb.WriteByte('\n')
		}
		sb.WriteString(fmt.Sprintf("\n## Current State\nCompleted %d of %d recorded steps. Showing last %d in detail.\n",
			totalSteps, totalSteps, totalSteps-recentStart))
		result = append(result, llm.NewTextMessage("user", sb.String()))
	}

	// 3. Recent steps as full messages
	for i := recentStart; i < totalSteps; i++ {
		s := &stepLog.Steps[i]

		// Assistant message (thought + tool calls)
		result = append(result, llm.Message{
			Role:      "assistant",
			Content:   []llm.ContentBlock{{Type: llm.ContentTypeText, Text: s.Thought}},
			ToolCalls: s.ToolCalls,
		})

		// Tool results
		for _, tr := range s.ToolResults {
			output := cm.SanitizeOutput(tr.ToolName, tr.CallID, tr.RawOutput)
			if tr.Error != "" {
				output = fmt.Sprintf("ERROR: %s\n%s", tr.Error, output)
			}
			result = append(result, llm.Message{
				Role:       "tool",
				Content:    []llm.ContentBlock{{Type: llm.ContentTypeText, Text: output}},
				ToolCallID: tr.CallID,
				Name:       tr.ToolName,
				Metadata:   tr.Metadata,
			})
		}

		// Reflection
		if s.Reflection != "" {
			result = append(result, llm.NewTextMessage("assistant", fmt.Sprintf("[Reflection] %s", s.Reflection)))
		}
	}

	return result
}

// summarizeStep generates a one-line programmatic summary of a step.
func (cm *ContextMonitor) summarizeStep(num int, s *Step) string {
	// Thought excerpt (first 100 chars)
	thought := s.Thought
	if len(thought) > 100 {
		thought = thought[:100] + "..."
	}
	thought = strings.ReplaceAll(thought, "\n", " ")

	// Tool names
	var toolNames []string
	for _, tr := range s.ToolResults {
		toolNames = append(toolNames, tr.ToolName)
	}
	tools := strings.Join(toolNames, ", ")
	if tools == "" {
		tools = "(none)"
	}

	// Status
	status := "ok"
	for _, tr := range s.ToolResults {
		if tr.Error != "" {
			status = "error"
			break
		}
	}

	return fmt.Sprintf("Step %d: %s | Tools: %s | Status: %s", num, thought, tools, status)
}
