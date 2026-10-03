package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/secret"
)

func audioBlock(mime string, data []byte) llm.ContentBlock {
	return llm.ContentBlock{
		Type:     llm.ContentTypeAudio,
		MIMEType: mime,
		Source:   &llm.BlockSource{Kind: llm.SourceKindBase64, Base64: base64.StdEncoding.EncodeToString(data)},
	}
}

func TestTranscribe_SendsAudioAndReturnsText(t *testing.T) {
	var gotPath, gotModel, gotFilename string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
		} else {
			gotFilename = hdr.Filename
			gotBytes, _ = io.ReadAll(f)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello world"})
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name, configured, wantModel string
	}{
		{"default model", "", "whisper-1"},
		{"configured model", "my-proxy-stt", "my-proxy-stt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProvider(&llm.ProviderConfig{
				APIKey: secret.New("test"), BaseURL: srv.URL, Model: "gpt-4o-mini",
				TranscriptionModel: tc.configured,
			})
			text, err := p.Transcribe(context.Background(), audioBlock("audio/wave", []byte("RIFFfakewav")))
			if err != nil {
				t.Fatalf("Transcribe: %v", err)
			}
			if text != "hello world" {
				t.Errorf("text = %q, want %q", text, "hello world")
			}
			if gotPath != "/audio/transcriptions" {
				t.Errorf("path = %q, want /audio/transcriptions", gotPath)
			}
			if gotModel != tc.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tc.wantModel)
			}
			if gotFilename != "audio.wav" {
				t.Errorf("filename = %q, want audio.wav", gotFilename)
			}
			if string(gotBytes) != "RIFFfakewav" {
				t.Errorf("uploaded bytes = %q, want the decoded audio", gotBytes)
			}
		})
	}
}

func TestTranscribe_ServerErrorIsWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no such model","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: secret.New("test"), BaseURL: srv.URL})
	_, err := p.Transcribe(context.Background(), audioBlock("audio/mpeg", []byte("ID3")))
	if err == nil || !strings.Contains(err.Error(), `transcription with model "whisper-1" failed`) {
		t.Fatalf("err = %v, want wrapped transcription error", err)
	}
}

func TestTranscribe_RejectsNonAudioBlock(t *testing.T) {
	p := NewProvider(&llm.ProviderConfig{APIKey: secret.New("test"), BaseURL: "http://127.0.0.1:1"})
	img := audioBlock("image/png", []byte("x"))
	img.Type = llm.ContentTypeImage
	if _, err := p.Transcribe(context.Background(), img); err == nil {
		t.Fatal("expected an error for a non-audio block")
	}
}

// Compile-time check that the OpenAI-compatible provider is a transcriber.
var _ llm.AudioTranscriber = (*Provider)(nil)

// Compile-time check that the OpenAI-compatible provider can prepare input
// for the FallbackProvider audio path.
var _ llm.InputPreparer = (*Provider)(nil)

func TestPrepareInput_TranscribesAudioBlockToText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "hello world"})
	}))
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: secret.New("test"), BaseURL: srv.URL})
	block := audioBlock("audio/wav", []byte("RIFFfakewav"))
	block.Metadata = map[string]any{"filename": "memo.wav", "size_bytes": int64(11)}
	history := []llm.Message{{Role: "user", Content: []llm.ContentBlock{block}}}

	prepared, err := p.PrepareInput(context.Background(), history)
	if err != nil {
		t.Fatalf("PrepareInput: %v", err)
	}
	if len(prepared) != 1 || len(prepared[0].Content) != 1 {
		t.Fatalf("unexpected prepared shape: %+v", prepared)
	}
	got := prepared[0].Content[0]
	if got.Type != llm.ContentTypeText {
		t.Errorf("Type = %v, want ContentTypeText", got.Type)
	}
	if got.Text != "[transcript of memo.wav]: hello world" {
		t.Errorf("Text = %q", got.Text)
	}
	// Original history must not be mutated.
	if history[0].Content[0].Type != llm.ContentTypeAudio {
		t.Errorf("original history was mutated: %+v", history[0].Content[0])
	}
}

func TestPrepareInput_CachesTranscriptAcrossCalls(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "cached text"})
	}))
	defer srv.Close()

	p := NewProvider(&llm.ProviderConfig{APIKey: secret.New("test"), BaseURL: srv.URL})
	block := audioBlock("audio/wav", []byte("samebytes"))
	history := []llm.Message{{Role: "user", Content: []llm.ContentBlock{block}}}

	if _, err := p.PrepareInput(context.Background(), history); err != nil {
		t.Fatalf("first PrepareInput: %v", err)
	}
	if _, err := p.PrepareInput(context.Background(), history); err != nil {
		t.Fatalf("second PrepareInput: %v", err)
	}
	if hits != 1 {
		t.Errorf("transcription endpoint hit %d times, want 1 (cache miss)", hits)
	}
}

func TestPrepareInput_NonBase64AudioSourceErrors(t *testing.T) {
	p := NewProvider(&llm.ProviderConfig{APIKey: secret.New("test"), BaseURL: "http://127.0.0.1:1"})
	block := llm.ContentBlock{Type: llm.ContentTypeAudio, MIMEType: "audio/wav", Source: &llm.BlockSource{Kind: llm.SourceKindURL, URL: "https://example.com/a.wav"}}
	history := []llm.Message{{Role: "user", Content: []llm.ContentBlock{block}}}

	if _, err := p.PrepareInput(context.Background(), history); err == nil {
		t.Fatal("expected an error for a non-base64 audio source")
	}
}

func TestPrepareInput_NoAudioBlocksIsNoop(t *testing.T) {
	p := NewProvider(&llm.ProviderConfig{APIKey: secret.New("test"), BaseURL: "http://127.0.0.1:1"})
	history := []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: llm.ContentTypeText, Text: "hi"}}}}

	prepared, err := p.PrepareInput(context.Background(), history)
	if err != nil {
		t.Fatalf("PrepareInput: %v", err)
	}
	if prepared[0].Content[0].Text != "hi" {
		t.Errorf("unexpected mutation of text-only history: %+v", prepared)
	}
}
