package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactWakeTickMasksCredentialsInDetailAndNote(t *testing.T) {
	p, _ := json.Marshal(WakeTickPayload{
		Tick: 1, Outcome: "alarm", Note: "token sk" + "-ABCDEF1234567890ABCDEF1234567890 leaked",
		Results: []WakeCheckResult{{Name: "f", Status: "alarm", Detail: "Bearer abcdef0123456789abcdef0123456789 in file"}},
	})
	out := RedactEventPayload(EventWakeTick, p)
	s := string(out)
	if strings.Contains(s, "sk"+"-ABCDEF1234567890ABCDEF1234567890") || strings.Contains(s, "abcdef0123456789abcdef0123456789") {
		t.Fatalf("credential-shaped text must be masked: %s", s)
	}
	var back WakeTickPayload
	if err := json.Unmarshal(out, &back); err != nil || back.Tick != 1 || back.Outcome != "alarm" {
		t.Fatalf("structure must survive redaction: %v %s", err, s)
	}
}

func TestRedactWakeEventsBadJSONReturnedUnchanged(t *testing.T) {
	in := json.RawMessage(`{not json`)
	for _, et := range []EventType{EventWakeTick, EventWakeEscalate} {
		if string(RedactEventPayload(et, in)) != string(in) {
			t.Fatalf("%s: undecodable payload must pass through unchanged", et)
		}
	}
}

func TestRedactWakeEscalateKeepsNumbers(t *testing.T) {
	p, _ := json.Marshal(WakeEscalatePayload{Tick: 7, Reason: "alarm: f", Level: "L2", SummaryChars: 494, SummaryCap: 1500})
	out := RedactEventPayload(EventWakeEscalate, p)
	var back WakeEscalatePayload
	_ = json.Unmarshal(out, &back)
	if back.SummaryChars != 494 || back.SummaryCap != 1500 || back.Tick != 7 {
		t.Fatalf("numeric fields must survive: %+v", back)
	}
}

func TestRedactWakeTickPreservesOrdinaryProse(t *testing.T) {
	// Verify that the redaction regex doesn't mask ordinary prose like
	// "bearer of" when it's not followed by a token-like string.
	p, _ := json.Marshal(WakeTickPayload{
		Tick:    1,
		Outcome: "quiet",
		Note:    "check reported success: bearer of good news, database reachable",
	})
	out := RedactEventPayload(EventWakeTick, p)
	s := string(out)
	if !strings.Contains(s, "good news") || !strings.Contains(s, "database reachable") {
		t.Fatalf("ordinary prose was masked: %s", s)
	}
}
