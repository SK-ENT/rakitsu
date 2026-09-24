package agent

// a tool that returns an image (tools.ContentTool) must reach the
// model as an image block on the tool-result message. `vision: false` gets a
// one-line note instead, never a silent drop. `vision` unset is auto: the
// image is sent, and a model that rejects it falls back to the note.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/telemetry"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

type imageTool struct{ *mockTool }

func (t imageTool) ExecuteContent(ctx context.Context, args map[string]interface{}) (string, []llm.ContentBlock, error) {
	return "Loaded image shot.png (image/png, 4 bytes).", []llm.ContentBlock{{
		Type:     llm.ContentTypeImage,
		MIMEType: "image/png",
		Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "iVBORw=="},
		Metadata: map[string]any{"size_bytes": int64(4)},
	}}, nil
}

var _ tools.ContentTool = imageTool{}

func boolPtr(b bool) *bool { return &b }

func runImageToolAgent(t *testing.T, vision *bool) (*sequenceProvider, llm.Message) {
	t.Helper()
	bus := telemetry.NewEventBus(64)
	provider := newSequenceProvider(
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "shot.png"})),
		stopResponse("a screenshot"),
	)
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Tools: []string{"read_image"}, Vision: vision}
	reg := tools.NewToolRegistry()
	reg.RegisterTool(imageTool{newMockTool("read_image", "unused")})
	ag := NewAgent(def, provider, reg, bus, nil)

	if _, err := ag.Run(context.Background(), "explain shot.png"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", provider.callCount())
	}
	hist := provider.getCall(1).History
	for _, m := range hist {
		if m.Role == "tool" {
			return provider, m
		}
	}
	t.Fatal("no tool message in the second call's history")
	return nil, llm.Message{}
}

func countImages(m llm.Message) int {
	n := 0
	for _, b := range m.Content {
		if b.Type == llm.ContentTypeImage {
			n++
		}
	}
	return n
}

func TestAgent_ToolImage_VisionAgentGetsImageBlock(t *testing.T) {
	for name, v := range map[string]*bool{"vision: true": boolPtr(true), "vision unset (auto)": nil} {
		_, msg := runImageToolAgent(t, v)
		if !strings.Contains(msg.AsText(), "Loaded image shot.png") {
			t.Errorf("%s: tool text = %q, want the tool's summary line", name, msg.AsText())
		}
		if countImages(msg) != 1 {
			t.Fatalf("%s: tool message has %d image blocks, want 1", name, countImages(msg))
		}
	}
}

func TestAgent_ToolImage_VisionFalseGetsNote(t *testing.T) {
	_, msg := runImageToolAgent(t, boolPtr(false))
	if msg.HasNonTextContent() {
		t.Fatal("vision: false must not send image blocks to the model")
	}
	if !strings.Contains(msg.AsText(), "[image omitted: image/png") {
		t.Errorf("tool text = %q, want an image-omitted note", msg.AsText())
	}
}

// textOnlyProvider acts like a model without vision: any request carrying an
// image fails with Ollama's real error text.
type textOnlyProvider struct {
	*sequenceProvider
	mu       sync.Mutex
	rejected int
}

func (p *textOnlyProvider) Generate(ctx context.Context, sp string, h []llm.Message, td []llm.ToolDefinition) (*llm.GenerateResult, error) {
	for _, m := range h {
		if m.HasNonTextContent() {
			p.mu.Lock()
			p.rejected++
			p.mu.Unlock()
			return nil, errors.New(`error, status code: 400, message: {"error":{"code":400,"message":"Multimodal data provided, but model does not support multimodal requests."}}`)
		}
	}
	return p.sequenceProvider.Generate(ctx, sp, h, td)
}

// TestAgent_ToolImage_AutoFallsBackOnTextOnlyModel: in auto mode a model
// that rejects images must not break the run. The images become notes, the
// call is retried once, and later tool images in the run skip straight to
// the note (no second rejection).
func TestAgent_ToolImage_AutoFallsBackOnTextOnlyModel(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	provider := &textOnlyProvider{sequenceProvider: newSequenceProvider(
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "a.png"})),
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "b.png"})),
		stopResponse("I can't see images with this model."),
	)}
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Tools: []string{"read_image"}}
	reg := tools.NewToolRegistry()
	reg.RegisterTool(imageTool{newMockTool("read_image", "unused")})
	ag := NewAgent(def, provider, reg, bus, nil)

	out, err := ag.Run(context.Background(), "explain a.png and b.png")
	if err != nil {
		t.Fatalf("Run: %v — auto mode must recover from the rejection", err)
	}
	if out != "I can't see images with this model." {
		t.Errorf("output = %q", out)
	}
	if provider.rejected != 1 {
		t.Errorf("provider rejected %d requests, want exactly 1", provider.rejected)
	}
	last := provider.getCall(provider.callCount() - 1).History
	var notes int
	for _, m := range last {
		if m.HasNonTextContent() {
			t.Fatalf("final call still carries non-text content: %+v", m)
		}
		if m.Role == "tool" && strings.Contains(m.AsText(), "[image omitted: image/png") {
			notes++
		}
	}
	if notes != 2 {
		t.Errorf("final call has %d image-omitted notes, want 2 (both tool results)", notes)
	}
}

func TestAgent_ToolImage_ExplicitVisionDoesNotFallBack(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	provider := &textOnlyProvider{sequenceProvider: newSequenceProvider(
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "a.png"})),
	)}
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Tools: []string{"read_image"}, Vision: boolPtr(true)}
	reg := tools.NewToolRegistry()
	reg.RegisterTool(imageTool{newMockTool("read_image", "unused")})
	ag := NewAgent(def, provider, reg, bus, nil)

	if _, err := ag.Run(context.Background(), "explain a.png"); err == nil {
		t.Fatal("vision: true on a text-only model must surface the provider error")
	}
}

func TestIsImageUnsupportedError(t *testing.T) {
	yes := []string{
		"Multimodal data provided, but model does not support multimodal requests.",
		"Invalid content type. image_url is only supported by certain models.",
		"this model does not support image input",
	}
	no := []string{"413 Request Entity Too Large", "rate limit exceeded", "invalid api key"}
	for _, s := range yes {
		if !isImageUnsupportedError(errors.New(s)) {
			t.Errorf("isImageUnsupportedError(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isImageUnsupportedError(errors.New(s)) {
			t.Errorf("isImageUnsupportedError(%q) = true, want false", s)
		}
	}
}

// Found in review: the auto-vision fallback lasts for one run only. A later
// run (next chat turn, or after a /model swap to a vision model) must try
// sending images again.
func TestAgent_ToolImage_FallbackResetsNextRun(t *testing.T) {
	bus := telemetry.NewEventBus(64)
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Tools: []string{"read_image"}}
	reg := tools.NewToolRegistry()
	reg.RegisterTool(imageTool{newMockTool("read_image", "unused")})

	textOnly := &textOnlyProvider{sequenceProvider: newSequenceProvider(
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "a.png"})),
		stopResponse("can't see it"),
	)}
	ag := NewAgent(def, textOnly, reg, bus, nil)
	if _, err := ag.Run(context.Background(), "explain a.png"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !ag.visionRejected.Load() {
		t.Fatal("first run should have recorded the rejection")
	}

	capable := newSequenceProvider(
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "a.png"})),
		stopResponse("a screenshot"),
	)
	ag.SetLLMProvider(capable, "sequence", "mock")
	if _, err := ag.Run(context.Background(), "explain a.png again"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, m := range capable.getCall(1).History {
		if m.Role == "tool" && countImages(m) == 1 {
			return
		}
	}
	t.Fatal("second run did not send the image — the fallback leaked across runs")
}

// Found in review: tool images accumulate in history and are re-sent every
// iteration, so the total must be bounded. Over the budget, the oldest
// images become notes and the newest are kept.
func TestAgent_ToolImage_HistoryBudgetDropsOldest(t *testing.T) {
	old := maxToolImageHistoryBytes
	maxToolImageHistoryBytes = 10 // each test image is 8 base64 bytes
	defer func() { maxToolImageHistoryBytes = old }()

	bus := telemetry.NewEventBus(64)
	provider := newSequenceProvider(
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "a.png"})),
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "b.png"})),
		toolCallResponse("", tc("read_image", map[string]interface{}{"query": "c.png"})),
		stopResponse("done"),
	)
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Tools: []string{"read_image"}}
	reg := tools.NewToolRegistry()
	reg.RegisterTool(imageTool{newMockTool("read_image", "unused")})
	ag := NewAgent(def, provider, reg, bus, nil)
	if _, err := ag.Run(context.Background(), "explain a, b and c"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var tools []llm.Message
	for _, m := range provider.getCall(3).History {
		if m.Role == "tool" {
			tools = append(tools, m)
		}
	}
	if len(tools) != 3 {
		t.Fatalf("got %d tool messages, want 3", len(tools))
	}
	for i, wantImages := range []int{0, 0, 1} {
		if got := countImages(tools[i]); got != wantImages {
			t.Errorf("tool message %d has %d images, want %d (only the newest fits the budget)", i, got, wantImages)
		}
		if wantImages == 0 && !strings.Contains(tools[i].AsText(), "[image omitted: image/png") {
			t.Errorf("tool message %d = %q, want an image-omitted note", i, tools[i].AsText())
		}
	}
}

// Found in review: the budget must also cover tool images already in the
// history a run is given (e.g. a replayed transcript), not only ones added
// during the run — even when the model answers without calling a tool.
func TestAgent_ToolImage_HistoryBudgetCoversPriorHistory(t *testing.T) {
	old := maxToolImageHistoryBytes
	maxToolImageHistoryBytes = 10 // each test image is 8 base64 bytes
	defer func() { maxToolImageHistoryBytes = old }()

	img := llm.ContentBlock{Type: llm.ContentTypeImage, MIMEType: "image/png",
		Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "iVBORw=="}}
	prior := []llm.Message{llm.NewTextMessage("user", "look at a and b")}
	for _, id := range []string{"1", "2"} {
		prior = append(prior,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: "read_image"}}},
			llm.Message{Role: "tool", ToolCallID: id, Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "Loaded"}, img}})
	}
	prior = append(prior, llm.NewTextMessage("assistant", "two images"))

	bus := telemetry.NewEventBus(64)
	provider := newSequenceProvider(stopResponse("done"))
	ag := newE2EAgent("viewer", provider, bus)
	if _, err := ag.RunWithHistory(context.Background(), "and now?", prior); err != nil {
		t.Fatalf("RunWithHistory: %v", err)
	}
	var images int
	for _, m := range provider.getCall(0).History {
		images += countImages(m)
	}
	if images != 1 {
		t.Fatalf("first request carries %d images, want 1 (budget applied to prior history)", images)
	}
}

// `vision: false` means never send — including tool images already in
// the history a run starts with (e.g. a replayed transcript).
func TestAgent_ToolImage_VisionFalseStripsStartingHistory(t *testing.T) {
	img := llm.ContentBlock{Type: llm.ContentTypeImage, MIMEType: "image/png",
		Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "iVBORw=="}}
	prior := []llm.Message{
		llm.NewTextMessage("user", "look at a"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "1", Name: "read_image"}}},
		{Role: "tool", ToolCallID: "1", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "Loaded"}, img}},
		llm.NewTextMessage("assistant", "an image"),
	}
	provider := newSequenceProvider(stopResponse("done"))
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Vision: boolPtr(false)}
	ag := NewAgent(def, provider, tools.NewToolRegistry(), telemetry.NewEventBus(64), nil)
	if _, err := ag.RunWithHistory(context.Background(), "and now?", prior); err != nil {
		t.Fatalf("RunWithHistory: %v", err)
	}
	for _, m := range provider.getCall(0).History {
		if m.HasNonTextContent() {
			t.Fatalf("vision: false sent an image from the starting history: %+v", m)
		}
		if m.Role == "tool" && !strings.Contains(m.AsText(), "[image omitted: image/png") {
			t.Errorf("tool message = %q, want an image-omitted note", m.AsText())
		}
	}
}

// Images a user attached earlier in the transcript are covered too,
// not only tool results.
func userImageHistory() []llm.Message {
	img := llm.ContentBlock{Type: llm.ContentTypeImage, MIMEType: "image/png",
		Source: &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "iVBORw=="}}
	return []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "what is this?"}, img}},
		llm.NewTextMessage("assistant", "a screenshot"),
	}
}

func TestAgent_VisionFalseStripsUserImagesInHistory(t *testing.T) {
	provider := newSequenceProvider(stopResponse("done"))
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x", Vision: boolPtr(false)}
	ag := NewAgent(def, provider, tools.NewToolRegistry(), telemetry.NewEventBus(64), nil)
	if _, err := ag.RunWithHistory(context.Background(), "and now?", userImageHistory()); err != nil {
		t.Fatalf("RunWithHistory: %v", err)
	}
	h := provider.getCall(0).History
	for _, m := range h {
		if m.HasNonTextContent() {
			t.Fatalf("vision: false sent a user image from the starting history: %+v", m)
		}
	}
	if got := h[0].AsText(); !strings.Contains(got, "what is this?") || !strings.Contains(got, "[image omitted: image/png") {
		t.Errorf("user message = %q, want its text plus an image-omitted note", got)
	}
}

func TestAgent_AutoVisionFallbackCoversUserImages(t *testing.T) {
	provider := &textOnlyProvider{sequenceProvider: newSequenceProvider(stopResponse("no images here"))}
	def := &config.AgentDefinition{Name: "viewer", SystemPrompt: "x"}
	ag := NewAgent(def, provider, tools.NewToolRegistry(), telemetry.NewEventBus(64), nil)
	out, err := ag.RunWithHistory(context.Background(), "and now?", userImageHistory())
	if err != nil {
		t.Fatalf("RunWithHistory: %v — auto mode must recover from the rejection", err)
	}
	if out != "no images here" || provider.rejected != 1 {
		t.Errorf("out = %q, rejected = %d; want the answer after exactly 1 rejection", out, provider.rejected)
	}
}
