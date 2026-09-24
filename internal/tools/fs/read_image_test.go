package fs

// the read_image operation hands the model the image itself, not text.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SK-ENT/rakitsu/internal/llm"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

func writePNG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func TestReadImage_ReturnsImageBlock(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot one.png") // spaces, like macOS screenshots
	writePNG(t, img)

	var ct tools.ContentTool = newFSTool("read_image", []string{dir})
	text, blocks, err := ct.ExecuteContent(context.Background(), map[string]interface{}{"path": img})
	if err != nil {
		t.Fatalf("ExecuteContent: %v", err)
	}
	if !strings.Contains(text, "image/png") {
		t.Errorf("text = %q, want it to name the MIME type", text)
	}
	if len(blocks) != 1 || blocks[0].Type != llm.ContentTypeImage || blocks[0].MIMEType != "image/png" {
		t.Fatalf("blocks = %+v, want one image/png block", blocks)
	}
	if blocks[0].Source == nil || blocks[0].Source.Base64 == "" {
		t.Fatal("image block has no base64 payload")
	}
}

func TestReadImage_OutsideFenceDenied(t *testing.T) {
	allowed := t.TempDir()
	other := t.TempDir()
	img := filepath.Join(other, "x.png")
	writePNG(t, img)

	_, _, err := newFSTool("read_image", []string{allowed}).ExecuteContent(context.Background(), map[string]interface{}{"path": img})
	var pna *PathNotAllowedError
	if !errors.As(err, &pna) {
		t.Fatalf("err = %v, want PathNotAllowedError", err)
	}
}

func TestReadImage_NonImageRejected(t *testing.T) {
	dir := t.TempDir()
	txt := writeTemp(t, dir, "just text")
	if _, _, err := newFSTool("read_image", []string{dir}).ExecuteContent(context.Background(), map[string]interface{}{"path": txt}); err == nil {
		t.Fatal("expected an error for a non-image file")
	}
}

// Text-only callers (MCP /mcp export, A2A) get the summary line, no payload.
func TestReadImage_ExecuteIsTextOnly(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "x.png")
	writePNG(t, img)

	out, err := newFSTool("read_image", []string{dir}).Execute(context.Background(), map[string]interface{}{"path": img})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "image/png") {
		t.Errorf("out = %q, want the summary line", out)
	}
}

// Other operations keep returning text only through ExecuteContent.
func TestExecuteContent_TextOperationsHaveNoBlocks(t *testing.T) {
	dir := t.TempDir()
	f := writeTemp(t, dir, "hello")
	text, blocks, err := newFSTool("read", []string{dir}).ExecuteContent(context.Background(), map[string]interface{}{"path": f})
	if err != nil || !strings.Contains(text, "hello") || len(blocks) != 0 {
		t.Fatalf("read via ExecuteContent = (%q, %d blocks, %v), want text only", text, len(blocks), err)
	}
}
