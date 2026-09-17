package gemini

import (
	"encoding/base64"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"google.golang.org/genai"
)

// TestParseResponse_EmptyCandidatesReportsContentFilter is a regression
// test: an empty Candidates slice (the response was blocked before
// generation started) must not be reported as an indistinguishable normal
// "stop".
func TestParseResponse_EmptyCandidatesReportsContentFilter(t *testing.T) {
	p := &Provider{}
	result := p.parseResponse(&genai.GenerateContentResponse{})
	if result.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "content_filter")
	}
}

// TestParseResponse_SafetyBlockedReportsContentFilter is a regression test:
// a blocked/unexpected finish reason (SAFETY, RECITATION, OTHER, ...) must
// not fall through to a normal "stop".
func TestParseResponse_SafetyBlockedReportsContentFilter(t *testing.T) {
	p := &Provider{}
	result := p.parseResponse(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{FinishReason: genai.FinishReasonSafety},
		},
	})
	if result.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "content_filter")
	}
}

// TestParseResponse_TotalTokensIncludesThinkingTokens is a regression test:
// TotalTokens used to be PromptTokenCount + CandidatesTokenCount only,
// silently dropping ThoughtsTokenCount/ToolUsePromptTokenCount and
// undercounting cost for extended-thinking Gemini 3.x models. It must now
// match the SDK's own authoritative sum, and OutputTokens must fold in
// thinking tokens too (they're billed as output), matching how the
// OpenAI/Anthropic providers already report a single output figure that
// includes reasoning tokens.
func TestParseResponse_TotalTokensIncludesThinkingTokens(t *testing.T) {
	p := &Provider{}
	result := p.parseResponse(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{FinishReason: genai.FinishReasonStop, Content: &genai.Content{Parts: []*genai.Part{{Text: "42"}}}},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        10,
			CandidatesTokenCount:    5,
			ThoughtsTokenCount:      20,
			ToolUsePromptTokenCount: 3,
			TotalTokenCount:         38,
		},
	})
	if result.TokenUsage == nil {
		t.Fatal("TokenUsage is nil, want populated")
	}
	if result.TokenUsage.TotalTokens != 38 {
		t.Errorf("TotalTokens = %d, want 38 (the SDK's own sum)", result.TokenUsage.TotalTokens)
	}
	if result.TokenUsage.OutputTokens != 25 {
		t.Errorf("OutputTokens = %d, want 25 (candidates 5 + thoughts 20)", result.TokenUsage.OutputTokens)
	}
}

// TestParseResponseAndBuildContents_ThoughtSignatureRoundTripsForTextOnlyTurn
// is a regression test: ThoughtSignature was only ever captured on
// FunctionCall parts. A text-only turn (no tool call) silently dropped its
// signature, which Gemini 3.x requires preserving for replay.
func TestParseResponseAndBuildContents_ThoughtSignatureRoundTripsForTextOnlyTurn(t *testing.T) {
	p := &Provider{}
	sig := []byte{1, 2, 3, 4}
	result := p.parseResponse(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				FinishReason: genai.FinishReasonStop,
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: "the answer is 42", ThoughtSignature: sig}},
				},
			},
		},
	})
	gotSig, ok := result.ProviderMetadata["thought_signature"].(string)
	if !ok {
		t.Fatal("ProviderMetadata[\"thought_signature\"] not set for a text-only part")
	}
	if gotSig != base64.StdEncoding.EncodeToString(sig) {
		t.Errorf("captured signature = %q, want %q", gotSig, base64.StdEncoding.EncodeToString(sig))
	}

	// Round-trip: rebuilding an assistant message that carries this
	// signature in its Metadata must re-attach it to the built Part.
	history := []llm.Message{
		{
			Role:     "assistant",
			Content:  []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "the answer is 42"}},
			Metadata: map[string]interface{}{"thought_signature": gotSig},
		},
	}
	contents := p.buildContents(history)
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("buildContents() = %+v, want one content with one part", contents)
	}
	if string(contents[0].Parts[0].ThoughtSignature) != string(sig) {
		t.Errorf("rebuilt part ThoughtSignature = %v, want %v", contents[0].Parts[0].ThoughtSignature, sig)
	}
}

// TestBuildContentsToolResponseIsPlainString locks down the M1.5 phase 0
// fix at buildContents' "tool" branch. FunctionResponse.Response is
// map[string]any, which the Go compiler happily accepts a []ContentBlock
// value into — unlike every other call site in this file, a regression
// here would compile clean and silently send the wrong payload shape to
// Gemini instead of failing the build. This test is the only thing that
// catches that class of bug now.
func TestBuildContentsToolResponseIsPlainString(t *testing.T) {
	p := &Provider{}
	history := []llm.Message{
		{
			Role:       "tool",
			Content:    []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "42 widgets found"}},
			ToolCallID: "call_1",
			Name:       "count_widgets",
		},
	}

	contents := p.buildContents(history)
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("buildContents() = %+v, want exactly one content with one part", contents)
	}

	fr := contents[0].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("tool message did not produce a FunctionResponse part")
	}

	output, ok := fr.Response["output"].(string)
	if !ok {
		t.Fatalf("FunctionResponse.Response[\"output\"] = %#v (%T), want a plain string", fr.Response["output"], fr.Response["output"])
	}
	if output != "42 widgets found" {
		t.Errorf("output = %q, want %q", output, "42 widgets found")
	}
}

// TestBuildContentsUserAndAssistantText covers the ordinary text-only path
// (the only shape phase 0 can produce) for the user/assistant branches.
func TestBuildContentsUserAndAssistantText(t *testing.T) {
	p := &Provider{}
	history := []llm.Message{
		llm.NewTextMessage("user", "what is 2+2?"),
		llm.NewTextMessage("assistant", "4"),
	}

	contents := p.buildContents(history)
	if len(contents) != 2 {
		t.Fatalf("buildContents() = %d contents, want 2: %+v", len(contents), contents)
	}
	if contents[0].Role != "user" || len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "what is 2+2?" {
		t.Errorf("user content = %+v", contents[0])
	}
	if contents[1].Role != "model" || len(contents[1].Parts) != 1 || contents[1].Parts[0].Text != "4" {
		t.Errorf("assistant content = %+v", contents[1])
	}
}

// TestBuildContentsUserWithImage covers the new image-block path a
// --attach image takes.
func TestBuildContentsUserWithImage(t *testing.T) {
	p := &Provider{}
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47} // arbitrary bytes, not a real PNG — buildContents doesn't validate content
	history := []llm.Message{
		{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: llm.ContentTypeText, Text: "what is this?"},
				{
					Type:     llm.ContentTypeImage,
					MIMEType: "image/png",
					Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: base64.StdEncoding.EncodeToString(imgBytes)},
				},
			},
		},
	}

	contents := p.buildContents(history)

	if len(contents) != 1 || len(contents[0].Parts) != 2 {
		t.Fatalf("buildContents() = %+v, want one content with two parts", contents)
	}
	if contents[0].Parts[0].Text != "what is this?" {
		t.Errorf("Parts[0].Text = %q, want %q", contents[0].Parts[0].Text, "what is this?")
	}
	inline := contents[0].Parts[1].InlineData
	if inline == nil {
		t.Fatal("Parts[1].InlineData = nil, want an inline data part")
	}
	if inline.MIMEType != "image/png" {
		t.Errorf("InlineData.MIMEType = %q, want image/png", inline.MIMEType)
	}
	if string(inline.Data) != string(imgBytes) {
		t.Errorf("InlineData.Data = %v, want %v", inline.Data, imgBytes)
	}
}
