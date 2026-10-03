package format

import (
	"strings"
	"testing"
)

func qwenFinalize(t *testing.T, content string, allowed ...string) Result {
	t.Helper()
	a := ToolCallTagInline{}
	state := NewState()
	state.SetAllowedTools(allowed)
	a.ApplyFull(state, RawMessage{Content: content, FinishReason: "stop"})
	return a.Finalize(state)
}

// Real sample from a reported failure (note: closing tag only, no opening <tool_call>).
const issue425Sample = "I'll review the sample.go file for bugs, security issues, and best practices.\n\n" +
	"<function=read_file>\n<parameter=path>\nsample.go\n</parameter>\n</function>\n</tool_call>"

func TestQwenXML_Issue425Sample(t *testing.T) {
	r := qwenFinalize(t, issue425Sample, "read_file")
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool calls = %+v", r.ToolCalls)
	}
	if got := r.ToolCalls[0].Arguments["path"]; got != "sample.go" {
		t.Errorf("path = %q", got)
	}
	if r.FinishReason != "tool_calls" {
		t.Errorf("finish = %q", r.FinishReason)
	}
	if strings.Contains(r.Content, "<function") || strings.Contains(r.Content, "tool_call>") {
		t.Errorf("content still has markup: %q", r.Content)
	}
	if !strings.HasPrefix(r.Content, "I'll review") {
		t.Errorf("prose lost: %q", r.Content)
	}
}

func TestQwenXML_WrappedAndMultiple(t *testing.T) {
	c := "<tool_call><function=a><parameter=x>1</parameter></function></tool_call>\n" +
		"<tool_call><function=b><parameter=y>2</parameter><parameter=z>3</parameter></function></tool_call>"
	r := qwenFinalize(t, c, "a", "b")
	if len(r.ToolCalls) != 2 || r.ToolCalls[0].Name != "a" || r.ToolCalls[1].Name != "b" {
		t.Fatalf("calls = %+v", r.ToolCalls)
	}
	if r.ToolCalls[0].ID == r.ToolCalls[1].ID {
		t.Error("ids must be unique")
	}
	if r.ToolCalls[1].Arguments["z"] != "3" {
		t.Errorf("args = %+v", r.ToolCalls[1].Arguments)
	}
	if r.Content != "" {
		t.Errorf("content = %q", r.Content)
	}
}

func TestQwenXML_ParamWithAngleBrackets(t *testing.T) {
	c := "<function=write_file><parameter=path>a.go</parameter><parameter=content>\nif a < b && c > d { x := []int{1} }\n<div>hi</div>\n</parameter></function>"
	r := qwenFinalize(t, c, "write_file")
	if len(r.ToolCalls) != 1 {
		t.Fatalf("calls = %+v", r.ToolCalls)
	}
	want := "if a < b && c > d { x := []int{1} }\n<div>hi</div>"
	if got := r.ToolCalls[0].Arguments["content"]; got != want {
		t.Errorf("content = %q want %q", got, want)
	}
}

func TestQwenXML_UnknownToolStaysText(t *testing.T) {
	r := qwenFinalize(t, issue425Sample, "other_tool")
	if len(r.ToolCalls) != 0 || r.FinishReason == "tool_calls" {
		t.Fatalf("unknown tool must not be parsed: %+v", r.ToolCalls)
	}
	if !strings.Contains(r.Content, "<function=read_file>") {
		t.Errorf("text must be preserved: %q", r.Content)
	}
}

func TestQwenXML_NoAllowedToolsStaysText(t *testing.T) {
	r := qwenFinalize(t, issue425Sample)
	if len(r.ToolCalls) != 0 {
		t.Fatalf("no registry => no parse: %+v", r.ToolCalls)
	}
}

func TestQwenXML_MixedKnownUnknown(t *testing.T) {
	c := "<function=good><parameter=a>1</parameter></function>\n<function=evil><parameter=a>1</parameter></function>"
	r := qwenFinalize(t, c, "good")
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "good" {
		t.Fatalf("calls = %+v", r.ToolCalls)
	}
	if !strings.Contains(r.Content, "<function=evil>") {
		t.Errorf("unknown call must remain as text: %q", r.Content)
	}
}

func TestQwenXML_Malformed(t *testing.T) {
	cases := map[string]string{
		"unterminated function":  "<function=read_file><parameter=path>x</parameter>",
		"unterminated parameter": "<function=read_file><parameter=path>x</function>",
		"empty name":             "<function=><parameter=a>1</parameter></function>",
		"bad name chars":         "<function=read file;rm><parameter=a>1</parameter></function>",
		"junk between params":    "<function=read_file>garbage<parameter=a>1</parameter></function>",
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := qwenFinalize(t, c, "read_file", "")
			if len(r.ToolCalls) != 0 {
				t.Errorf("malformed must not parse: %+v", r.ToolCalls)
			}
			if r.Content == "" {
				t.Error("content must be preserved")
			}
		})
	}
}

func TestQwenXML_NativeToolCallsWin(t *testing.T) {
	a := ToolCallTagInline{}
	state := NewState()
	state.SetAllowedTools([]string{"read_file"})
	a.ApplyDelta(state, RawDelta{Content: issue425Sample})
	a.ApplyDelta(state, RawDelta{ToolCalls: []RawDeltaToolCall{{Index: 0, ID: "c1", Name: "read_file", Arguments: `{"path":"n.go"}`}}})
	a.ApplyDelta(state, RawDelta{FinishReason: "tool_calls"})
	r := a.Finalize(state)
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].ID != "c1" {
		t.Fatalf("native call must be kept alone: %+v", r.ToolCalls)
	}
}

func TestQwenXML_SizeLimits(t *testing.T) {
	big := "<function=read_file><parameter=path>" + strings.Repeat("a", maxToolCallXMLBytes) + "</parameter></function>"
	if r := qwenFinalize(t, big, "read_file"); len(r.ToolCalls) != 0 {
		t.Error("oversized content must not be parsed")
	}
	var b strings.Builder
	for i := 0; i < maxToolCallXMLCalls+5; i++ {
		b.WriteString("<function=t><parameter=a>1</parameter></function>")
	}
	if r := qwenFinalize(t, b.String(), "t"); len(r.ToolCalls) != 0 {
		t.Errorf("too many calls must not be parsed, got %d", len(r.ToolCalls))
	}
}

func TestContainsUnparsedToolCall(t *testing.T) {
	for _, c := range []string{issue425Sample, "x <tool_call>{}", "</tool_call>", "<function=a>"} {
		if !ContainsUnparsedToolCall(c) {
			t.Errorf("should detect: %q", c)
		}
	}
	for _, c := range []string{"", "plain answer", "use <function> generics", "a < b"} {
		if ContainsUnparsedToolCall(c) {
			t.Errorf("false positive: %q", c)
		}
	}
}

func TestQwenXML_MixedWithLegacyJSON(t *testing.T) {
	c := "intro\n<tool_call>{\"name\":\"a\",\"arguments\":{\"x\":\"1\"}}</tool_call>\n" +
		"<function=b><parameter=y>2</parameter></function>\n" +
		"<function=evil><parameter=y>2</parameter></function>"
	r := qwenFinalize(t, c, "a", "b")
	if len(r.ToolCalls) != 2 || r.ToolCalls[0].Name != "a" || r.ToolCalls[1].Name != "b" {
		t.Fatalf("calls = %+v", r.ToolCalls)
	}
	if r.ToolCalls[0].ID == r.ToolCalls[1].ID {
		t.Errorf("ids must be unique: %q", r.ToolCalls[0].ID)
	}
	if strings.Contains(r.Content, "<function=b>") || strings.Contains(r.Content, "<tool_call>") {
		t.Errorf("converted blocks must be stripped: %q", r.Content)
	}
	if !strings.Contains(r.Content, "<function=evil>") {
		t.Errorf("unknown block must stay text: %q", r.Content)
	}
}
