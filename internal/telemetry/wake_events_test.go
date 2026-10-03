package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWakeEventPayloadJSON(t *testing.T) {
	if EventWakeTick != "WAKE_TICK" || EventWakeEscalate != "WAKE_ESCALATE" {
		t.Fatal("event names changed")
	}
	b, err := json.Marshal(WakeEscalatePayload{Tick: 3, Reason: "alarm", Level: "L2", SummaryChars: 10, SummaryCap: 1500})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"tick", "reason", "level", "summary_chars", "summary_cap"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing json key %q in %s", k, b)
		}
	}
}

func TestWakeTaskEventsJSONAndRedaction(t *testing.T) {
	if EventWakeTaskStart != "WAKE_TASK_START" || EventWakeTaskEnd != "WAKE_TASK_END" {
		t.Fatal("event names changed")
	}
	p, _ := json.Marshal(WakeTaskPayload{TaskID: "t1", Config: "alert-handler", Status: "done",
		Args:    map[string]string{"severity": "high"},
		Summary: "token sk" + "-ABCDEF1234567890ABCDEF1234567890 leaked", Reason: "Bearer " + "abcdef0123456789abcdef0123456789"})
	for _, et := range []EventType{EventWakeTaskStart, EventWakeTaskEnd} {
		out := string(RedactEventPayload(et, p))
		if strings.Contains(out, "sk"+"-ABCDEF1234567890ABCDEF1234567890") || strings.Contains(out, "abcdef0123456789abcdef0123456789") {
			t.Fatalf("%s: credential-shaped text must be masked: %s", et, out)
		}
		var back WakeTaskPayload
		if err := json.Unmarshal([]byte(out), &back); err != nil || back.TaskID != "t1" || back.Args["severity"] != "high" {
			t.Fatalf("%s: structure must survive: %v %s", et, err, out)
		}
	}
}
