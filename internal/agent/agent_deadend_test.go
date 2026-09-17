package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
)

// TestJudgeDeadEnd_UnicodeExpandingUpperCaseDoesNotPanic regression-guards
// finding 26: judgeDeadEnd located "DEAD_END" in a strings.ToUpper(verdict)
// copy but then sliced the ORIGINAL verdict string at that same byte
// offset. strings.ToUpper can change a string's byte length (e.g. 'ɐ'
// U+0250, 2 bytes, uppercases to 'Ɑ' U+2C6D, 3 bytes) — when that happens
// before the DEAD_END marker, the index found in the uppercased copy no
// longer lines up with the original, and slicing verdict at that offset
// can run past the end of the string and panic ("slice bounds out of
// range").
func TestJudgeDeadEnd_UnicodeExpandingUpperCaseDoesNotPanic(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	// 'ɐ' (U+0250) uppercases to 'Ɑ' (U+2C6D), one byte longer — three of
	// them ahead of DEAD_END push the uppercased-copy index 3 bytes past
	// the end of the (shorter) original string.
	verdict := "ɐɐɐDEAD_END"
	provider := newSequenceProvider(llm.GenerateResult{Response: verdict})
	ag := newTestAgent("judge", provider, bus)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("judgeDeadEnd panicked: %v", r)
		}
	}()

	deadEnd, reason, _, _ := ag.judgeDeadEnd(context.Background(), nil, 0)
	if !deadEnd {
		t.Errorf("expected deadEnd=true for verdict %q", verdict)
	}
	if reason == "" {
		t.Error("expected a non-empty reason")
	}
}

// TestJudgeDeadEnd_ExtractsReasonAfterMarker verifies the ordinary ASCII
// path still extracts the trailing reason text correctly.
func TestJudgeDeadEnd_ExtractsReasonAfterMarker(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	provider := newSequenceProvider(llm.GenerateResult{Response: "VERDICT: DEAD_END — wrong file edited"})
	ag := newTestAgent("judge", provider, bus)

	deadEnd, reason, _, _ := ag.judgeDeadEnd(context.Background(), nil, 0)
	if !deadEnd {
		t.Fatal("expected deadEnd=true")
	}
	if !strings.Contains(reason, "wrong file edited") {
		t.Errorf("reason = %q, want it to contain %q", reason, "wrong file edited")
	}
}

// TestJudgeDeadEnd_ProgressVerdict verifies a PROGRESS verdict reports
// deadEnd=false.
func TestJudgeDeadEnd_ProgressVerdict(t *testing.T) {
	bus := telemetry.NewEventBus(16)
	provider := newSequenceProvider(llm.GenerateResult{Response: "VERDICT: PROGRESS"})
	ag := newTestAgent("judge", provider, bus)

	deadEnd, _, _, _ := ag.judgeDeadEnd(context.Background(), nil, 0)
	if deadEnd {
		t.Error("expected deadEnd=false for a PROGRESS verdict")
	}
}
