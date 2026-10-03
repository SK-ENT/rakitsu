package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// writeTestPNG writes a tiny valid PNG to path.
func writeTestPNG(t *testing.T, path string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	return buf.Bytes()
}

// writeTestJPEG writes a tiny valid JPEG to path.
func writeTestJPEG(t *testing.T, path string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{0, 255, 0, 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write JPEG fixture: %v", err)
	}
	return buf.Bytes()
}

// writeTestWAV writes a minimal valid WAV file to path. The WAV contains
// a single-frame mono PCM audio with no actual audio data (all zeros).
func writeTestWAV(t *testing.T, path string) []byte {
	t.Helper()
	// Minimal WAV structure:
	// - "RIFF" chunk header (4 bytes ID + 4 bytes size)
	// - "WAVE" format (4 bytes)
	// - "fmt " subchunk (4 bytes ID + 4 bytes size + format data)
	// - "data" subchunk (4 bytes ID + 4 bytes size + audio data)
	wav := []byte{
		// RIFF header
		0x52, 0x49, 0x46, 0x46, // "RIFF"
		0x24, 0x00, 0x00, 0x00, // chunk size (36 bytes for the rest)
		0x57, 0x41, 0x56, 0x45, // "WAVE"

		// fmt subchunk
		0x66, 0x6d, 0x74, 0x20, // "fmt "
		0x10, 0x00, 0x00, 0x00, // subchunk size (16 bytes)
		0x01, 0x00, // audio format (1 = PCM)
		0x01, 0x00, // channels (1 = mono)
		0x44, 0xac, 0x00, 0x00, // sample rate (44100 Hz)
		0x88, 0x58, 0x01, 0x00, // byte rate (44100 * 2)
		0x02, 0x00, // block align (2 bytes)
		0x10, 0x00, // bits per sample (16)

		// data subchunk
		0x64, 0x61, 0x74, 0x61, // "data"
		0x00, 0x00, 0x00, 0x00, // subchunk size (0 bytes of audio)
	}
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		t.Fatalf("write WAV fixture: %v", err)
	}
	return wav
}

func TestLoadImageAttachment_ValidPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.png")
	want := writeTestPNG(t, path)

	block, err := LoadImageAttachment(path)
	if err != nil {
		t.Fatalf("LoadImageAttachment() error = %v", err)
	}
	if block.Type != ContentTypeImage {
		t.Errorf("Type = %q, want %q", block.Type, ContentTypeImage)
	}
	if block.MIMEType != "image/png" {
		t.Errorf("MIMEType = %q, want image/png", block.MIMEType)
	}
	if block.Source.Kind != SourceKindBase64 {
		t.Errorf("Source.Kind = %q, want %q", block.Source.Kind, SourceKindBase64)
	}
	got, err := base64.StdEncoding.DecodeString(block.Source.Base64)
	if err != nil {
		t.Fatalf("decode Source.Base64: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded bytes do not match original file")
	}
}

func TestLoadImageAttachment_ValidJPEG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	writeTestJPEG(t, path)

	block, err := LoadImageAttachment(path)
	if err != nil {
		t.Fatalf("LoadImageAttachment() error = %v", err)
	}
	if block.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg", block.MIMEType)
	}
}

func TestLoadImageAttachment_OversizedFile(t *testing.T) {
	orig := MaxAttachmentBytes
	MaxAttachmentBytes = 100
	defer func() { MaxAttachmentBytes = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, "big.png")
	// Content past the 512-byte sniff window doesn't matter — the size
	// check runs before any read, so any 200-byte file trips it.
	if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 200), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := LoadImageAttachment(path)
	if err == nil {
		t.Fatal("LoadImageAttachment() error = nil, want a size-limit error")
	}
	// Regression: MaxAttachmentBytes/(1024*1024) used integer division,
	// which truncated any sub-1MB override to a misleading "0 MB" in the
	// message. The error must carry the real limit unambiguously.
	if strings.Contains(err.Error(), "0 MB attachment size limit") {
		t.Errorf("error message shows a misleading truncated limit: %v", err)
	}
	if !strings.Contains(err.Error(), "100 bytes") {
		t.Errorf("error message should state the exact byte limit, got: %v", err)
	}
}

func TestLoadImageAttachment_UnsupportedMIME(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\n%moredata"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := LoadImageAttachment(path)
	if err == nil {
		t.Fatal("LoadImageAttachment() error = nil, want an unsupported-content-type error")
	}
}

func TestLoadImageAttachment_NonexistentPath(t *testing.T) {
	_, err := LoadImageAttachment("/nonexistent/path/does-not-exist.png")
	if err == nil {
		t.Fatal("LoadImageAttachment() error = nil, want a stat error")
	}
}

func TestLoadImageAttachment_Directory(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadImageAttachment(dir)
	if err == nil {
		t.Fatal("LoadImageAttachment() error = nil, want a directory rejection")
	}
}

func TestLoadImageAttachment_NonRegularFile(t *testing.T) {
	// Create a named pipe (FIFO) to test non-regular file rejection
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "fifo")

	// mkfifo via syscall
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("cannot create FIFO on this platform: %v", err)
	}

	_, err := LoadImageAttachment(fifoPath)
	if err == nil {
		t.Fatal("LoadImageAttachment() error = nil, want a non-regular-file rejection")
	}
}

// Tests for LoadAudioAttachment

func TestLoadAudioAttachment_ValidWAV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wav")
	want := writeTestWAV(t, path)

	block, err := LoadAudioAttachment(path)
	if err != nil {
		t.Fatalf("LoadAudioAttachment() error = %v", err)
	}
	if block.Type != ContentTypeAudio {
		t.Errorf("Type = %q, want %q", block.Type, ContentTypeAudio)
	}
	if block.MIMEType != "audio/wave" {
		t.Errorf("MIMEType = %q, want audio/wave", block.MIMEType)
	}
	if block.Source.Kind != SourceKindBase64 {
		t.Errorf("Source.Kind = %q, want %q", block.Source.Kind, SourceKindBase64)
	}
	got, err := base64.StdEncoding.DecodeString(block.Source.Base64)
	if err != nil {
		t.Fatalf("decode Source.Base64: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded bytes do not match original file")
	}
}

// http.DetectContentType recognizes the WebM/EBML container but not
// whether its track is audio-only, so it always reports "video/webm" —
// never "audio/webm". A real .webm voice memo must still be accepted here.
func TestLoadAudioAttachment_WebM_SniffedAsVideoWebM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memo.webm")
	// The EBML header ID, confirmed to sniff as "video/webm" via
	// net/http.DetectContentType.
	data := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0x02, 0x03, 0x04}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	block, err := LoadAudioAttachment(path)
	if err != nil {
		t.Fatalf("LoadAudioAttachment() error = %v", err)
	}
	if block.MIMEType != "audio/webm" {
		t.Errorf("MIMEType = %q, want audio/webm (normalized from the sniffed video/webm)", block.MIMEType)
	}
}

func TestLoadAudioAttachment_OversizedFile(t *testing.T) {
	orig := MaxAttachmentBytes
	MaxAttachmentBytes = 100
	defer func() { MaxAttachmentBytes = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, "big.wav")
	// Write a file larger than the limit
	if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 200), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := LoadAudioAttachment(path)
	if err == nil {
		t.Fatal("LoadAudioAttachment() error = nil, want a size-limit error")
	}
	if !strings.Contains(err.Error(), "100 bytes") {
		t.Errorf("error message should state the exact byte limit, got: %v", err)
	}
}

func TestLoadAudioAttachment_UnsupportedMIME(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\n%moredata"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := LoadAudioAttachment(path)
	if err == nil {
		t.Fatal("LoadAudioAttachment() error = nil, want an unsupported-content-type error")
	}
}

func TestLoadAudioAttachment_MP3WithoutID3_FallbackByExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.mp3")

	// MP3 MPEG sync header (without ID3 tag, so DetectContentType returns
	// "application/octet-stream"). LoadAudioAttachment should fall back to
	// checking the .mp3 extension.
	mp3Data := []byte{
		0xFF, 0xFB, // MPEG sync word
		0x90, 0x00, // MPEG layer 3 info
	}
	if err := os.WriteFile(path, mp3Data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	block, err := LoadAudioAttachment(path)
	if err != nil {
		t.Fatalf("LoadAudioAttachment() error = %v", err)
	}
	if block.Type != ContentTypeAudio {
		t.Errorf("Type = %q, want %q", block.Type, ContentTypeAudio)
	}
	if block.MIMEType != "audio/mpeg" {
		t.Errorf("MIMEType = %q, want audio/mpeg (via extension fallback)", block.MIMEType)
	}
}

func TestLoadAudioAttachment_FLACWithoutMagic_FallbackByExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.flac")

	// FLAC header (which DetectContentType doesn't recognize as audio/flac)
	flacData := []byte{
		0x66, 0x4C, 0x61, 0x43, // "fLaC"
		0x80, 0x00, 0x00, 0x00, // streaminfo metadata block
	}
	if err := os.WriteFile(path, flacData, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	block, err := LoadAudioAttachment(path)
	if err != nil {
		t.Fatalf("LoadAudioAttachment() error = %v", err)
	}
	if block.MIMEType != "audio/flac" {
		t.Errorf("MIMEType = %q, want audio/flac (via extension fallback)", block.MIMEType)
	}
}

func TestLoadAudioAttachment_NonRegularFile(t *testing.T) {
	// Create a named pipe (FIFO) to test non-regular file rejection
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "fifo")

	// mkfifo via syscall
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Skipf("cannot create FIFO on this platform: %v", err)
	}

	_, err := LoadAudioAttachment(fifoPath)
	if err == nil {
		t.Fatal("LoadAudioAttachment() error = nil, want a non-regular-file rejection")
	}
}

// m4aHeader is an ISO-BMFF "ftyp" box, which http.DetectContentType reports
// as "video/mp4" regardless of the file being audio-only.
var m4aHeader = append([]byte{0, 0, 0, 0x20}, []byte("ftypM4A \x00\x00\x02\x00isomiso2M4A mp41")...)

func TestLoadAudioAttachment_M4A_SniffedAsVideoMP4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memo.m4a")
	if err := os.WriteFile(path, m4aHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	block, err := LoadAudioAttachment(path)
	if err != nil {
		t.Fatalf("LoadAudioAttachment: %v", err)
	}
	if block.MIMEType != "audio/mp4" {
		t.Errorf("MIMEType = %q, want audio/mp4", block.MIMEType)
	}
}

func TestSniffAttachmentKind(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	writeTestPNG(t, png)
	wav := filepath.Join(dir, "a.wav")
	writeTestWAV(t, wav)
	m4a := filepath.Join(dir, "a.m4a")
	mp4 := filepath.Join(dir, "a.mp4")
	txt := filepath.Join(dir, "a.txt")
	for p, data := range map[string][]byte{m4a: m4aHeader, mp4: m4aHeader, txt: []byte("hello")} {
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		path    string
		want    ContentType
		wantErr bool
	}{
		{png, ContentTypeImage, false},
		{wav, ContentTypeAudio, false},
		{m4a, ContentTypeAudio, false},
		{mp4, "", true}, // same bytes, but no .m4a extension: treated as video
		{txt, "", true},
		{dir, "", true},
		{filepath.Join(dir, "missing.png"), "", true},
	}
	for _, c := range cases {
		got, err := SniffAttachmentKind(c.path)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got (%q, %v), want (%q, err=%v)", filepath.Base(c.path), got, err, c.want, c.wantErr)
		}
	}
}

func TestSniffAttachmentKind_NonRegularFile(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe.wav")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	if _, err := SniffAttachmentKind(fifo); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want not-a-regular-file", err)
	}
}
