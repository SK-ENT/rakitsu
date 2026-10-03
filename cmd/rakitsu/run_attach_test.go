package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// Minimal headers that http.DetectContentType recognizes.
var (
	pngFixture = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00")
	wavFixture = []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x44\xac\x00\x00\x88\x58\x01\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
)

func writeFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeProvider is an LLMProvider with no transcription support.
type fakeProvider struct{}

func (fakeProvider) Generate(context.Context, string, []llm.Message, []llm.ToolDefinition) (*llm.GenerateResult, error) {
	return nil, errors.New("not used")
}
func (fakeProvider) GetName() string  { return "fake" }
func (fakeProvider) GetModel() string { return "fake" }

// fakeTranscriber adds llm.AudioTranscriber and records its calls.
type fakeTranscriber struct {
	fakeProvider
	calls int
	text  string
	err   error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, b llm.ContentBlock) (string, error) {
	f.calls++
	if b.Type != llm.ContentTypeAudio {
		return "", errors.New("not audio")
	}
	return f.text, f.err
}

func TestLoadAttachments_RoutesImageAndAudio(t *testing.T) {
	img := writeFixture(t, "a.png", pngFixture)
	wav := writeFixture(t, "memo.wav", wavFixture)

	blocks, err := loadAttachments([]string{img, wav}, "openai", "a")
	if err != nil {
		t.Fatalf("loadAttachments: %v", err)
	}
	if len(blocks) != 2 || blocks[0].Type != llm.ContentTypeImage || blocks[1].Type != llm.ContentTypeAudio {
		t.Fatalf("got block types %v, want [image audio]", blockTypes(blocks))
	}
}

func TestLoadAttachments_AnthropicRejectsAudioButNotImage(t *testing.T) {
	img := writeFixture(t, "a.png", pngFixture)
	wav := writeFixture(t, "memo.wav", wavFixture)

	if _, err := loadAttachments([]string{img}, "anthropic", "a"); err != nil {
		t.Fatalf("image on anthropic: %v", err)
	}
	_, err := loadAttachments([]string{wav}, "anthropic", "claude-agent")
	if err == nil || !strings.Contains(err.Error(), "does not accept audio input") || !strings.Contains(err.Error(), `"claude-agent"`) {
		t.Fatalf("audio on anthropic: err = %v, want a clear audio-not-supported error", err)
	}
}

func TestLoadAttachments_RejectsUnsupportedType(t *testing.T) {
	txt := writeFixture(t, "notes.txt", []byte("just some text"))
	if _, err := loadAttachments([]string{txt}, "openai", "a"); err == nil {
		t.Fatal("expected an error for a text file")
	}
}

func TestTranscribeAudioAttachments(t *testing.T) {
	img := llm.ContentBlock{Type: llm.ContentTypeImage, MIMEType: "image/png"}
	audio := llm.ContentBlock{
		Type: llm.ContentTypeAudio, MIMEType: "audio/wave",
		Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: "UklGRg=="},
		Metadata: map[string]any{"size_bytes": int64(4)},
	}
	paths := []string{"/x/a.png", "/x/memo.wav"}

	t.Run("transcriber replaces audio with a tagged text block", func(t *testing.T) {
		tr := &fakeTranscriber{text: "buy milk"}
		out, err := transcribeAudioAttachments(context.Background(), tr, "litellm", "a", []llm.ContentBlock{img, audio}, paths)
		if err != nil {
			t.Fatal(err)
		}
		if out[0].Type != llm.ContentTypeImage {
			t.Errorf("image block changed: %v", out[0].Type)
		}
		if out[1].Type != llm.ContentTypeText || out[1].Text != "[transcript of memo.wav]: buy milk" {
			t.Errorf("audio block = %+v, want tagged transcript text", out[1])
		}
		if out[1].Metadata["transcribed_from"] != "audio/wave" {
			t.Errorf("metadata = %v, want transcribed_from audio/wave", out[1].Metadata)
		}
	})

	t.Run("gemini keeps native audio and never transcribes", func(t *testing.T) {
		tr := &fakeTranscriber{text: "unused"}
		out, err := transcribeAudioAttachments(context.Background(), tr, "gemini", "a", []llm.ContentBlock{audio}, paths[1:])
		if err != nil {
			t.Fatal(err)
		}
		if tr.calls != 0 || out[0].Type != llm.ContentTypeAudio {
			t.Errorf("gemini: calls=%d type=%v, want 0 calls and native audio", tr.calls, out[0].Type)
		}
	})

	t.Run("provider without transcription errors, no silent drop", func(t *testing.T) {
		_, err := transcribeAudioAttachments(context.Background(), fakeProvider{}, "codex", "a", []llm.ContentBlock{audio}, paths[1:])
		if err == nil || !strings.Contains(err.Error(), "cannot transcribe audio") {
			t.Fatalf("err = %v, want a cannot-transcribe error", err)
		}
	})

	t.Run("transcription failure is surfaced", func(t *testing.T) {
		tr := &fakeTranscriber{err: errors.New("endpoint down")}
		_, err := transcribeAudioAttachments(context.Background(), tr, "openai", "a", []llm.ContentBlock{audio}, paths[1:])
		if err == nil || !strings.Contains(err.Error(), "endpoint down") {
			t.Fatalf("err = %v, want the transcription error", err)
		}
	})

	t.Run("images only: provider never consulted", func(t *testing.T) {
		out, err := transcribeAudioAttachments(context.Background(), fakeProvider{}, "codex", "a", []llm.ContentBlock{img}, paths[:1])
		if err != nil || len(out) != 1 || out[0].Type != llm.ContentTypeImage {
			t.Fatalf("out=%v err=%v, want the image untouched", blockTypes(out), err)
		}
	})
}

func TestResolveAgentProviderType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.Providers = map[string]config.ProviderDefinition{
		"claude-fast": {Type: "Anthropic"},
	}
	cases := []struct {
		def  config.AgentDefinition
		want string
	}{
		{config.AgentDefinition{}, "openai"},
		{config.AgentDefinition{Provider: "gemini"}, "gemini"},
		{config.AgentDefinition{Provider: "claude-fast"}, "anthropic"},
	}
	for _, c := range cases {
		if got := resolveAgentProviderType(cfg, &c.def); got != c.want {
			t.Errorf("provider %q: got %q, want %q", c.def.Provider, got, c.want)
		}
	}
}

func blockTypes(bs []llm.ContentBlock) []llm.ContentType {
	out := make([]llm.ContentType, len(bs))
	for i, b := range bs {
		out[i] = b.Type
	}
	return out
}

func autoAttachCfg(vision bool) *config.Config {
	return &config.Config{Agents: []config.AgentDefinition{{Name: "a", Vision: &vision}}}
}

func TestAutoAttachFromQuery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.png"), pngFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	q := "what is in real.png?"

	paths, blocks := autoAttachFromQuery(autoAttachCfg(true), q, dir, nil, nil, false)
	if len(paths) != 1 || len(blocks) != 1 {
		t.Fatalf("want 1 auto attachment, got %v / %d", paths, len(blocks))
	}

	// Explicit --attach of the same file plus a mention attaches once.
	explicit := filepath.Join(dir, "real.png")
	eb, err := loadAttachments([]string{explicit}, "openai", "a")
	if err != nil {
		t.Fatal(err)
	}
	paths, blocks = autoAttachFromQuery(autoAttachCfg(true), q, dir, []string{explicit}, eb, false)
	if len(paths) != 1 || len(blocks) != 1 {
		t.Fatalf("duplicate attached twice: %v / %d", paths, len(blocks))
	}

	// Not applicable: no vision, dry-run, interactive. Never an error.
	for name, c := range map[string]struct {
		cfg *config.Config
		dry bool
	}{
		"no vision":   {autoAttachCfg(false), false},
		"dry run":     {autoAttachCfg(true), true},
		"interactive": {func() *config.Config { c := autoAttachCfg(true); c.Interactive = true; return c }(), false},
	} {
		if p, b := autoAttachFromQuery(c.cfg, q, dir, nil, nil, c.dry); len(p) != 0 || len(b) != 0 {
			t.Errorf("%s: attached %v", name, p)
		}
	}

	// A mentioned file that fails to load (bad content) is skipped, not fatal.
	if err := os.WriteFile(filepath.Join(dir, "bad.png"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, _ := autoAttachFromQuery(autoAttachCfg(true), "see bad.png", dir, nil, nil, false); len(p) != 0 {
		t.Errorf("bad file attached: %v", p)
	}
}
