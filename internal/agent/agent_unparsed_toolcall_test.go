package agent

import (
	"strings"
	"testing"
)

// A final answer that still contains a raw tool-call block must
// not be reported as success.
func TestAgent_UnparsedToolCallInFinalAnswer_Fails(t *testing.T) {
	txt := "I'll read it.\n\n<function=noop>\n<parameter=path>\nsample.go\n</parameter>\n</function>\n</tool_call>"
	out, err, unproductive := drive(t, "unparsed-toolcall", 3, 0, stopResponse(txt))
	if err == nil || !strings.Contains(err.Error(), "unparsed tool call") {
		t.Fatalf("err = %v, want unparsed tool call error", err)
	}
	if !strings.Contains(out, "<function=noop>") {
		t.Errorf("raw output should still be returned for debugging: %q", out)
	}
	if !unproductive {
		t.Error("run must be flagged unproductive")
	}
}

func TestAgent_PlainFinalAnswer_StillSucceeds(t *testing.T) {
	out, err, _ := drive(t, "plain-final", 3, 0, stopResponse("all done, a < b"))
	if err != nil || out != "all done, a < b" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// A leftover unparsed block must fail the turn even when the same response
// also carries structured tool calls.
func TestAgent_UnparsedToolCallAlongsideStructuredCalls_Fails(t *testing.T) {
	txt := "go\n<function=ghost>\n<parameter=a>\n1\n</parameter>\n</function>"
	resp := toolCallResponse(txt, tc("noop", map[string]interface{}{"query": "x"}))
	_, err, _ := drive(t, "unparsed-with-calls", 3, 0, resp, stopResponse("done"))
	if err == nil || !strings.Contains(err.Error(), "unparsed tool call") {
		t.Fatalf("err = %v", err)
	}
}
