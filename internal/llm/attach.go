package llm

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MaxAttachmentBytes is the hard size cap for a single --attach file in
// this phase. There is no provider File API fallback path yet (that's
// later-phase scope), so exceeding this is a hard error, not a
// size-triggered upload. A var, not a const, so tests can shrink it
// instead of allocating a real 20MB fixture file.
var MaxAttachmentBytes int64 = 20 * 1024 * 1024

// allowedImageMIMETypes is the intersection of what OpenAI, Anthropic, and
// Gemini all accept for inline image content. Anthropic's four-value
// media-type enum is the tightest of the three, so it sets the ceiling.
var allowedImageMIMETypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// IsSupportedImageMIME reports whether every provider accepts this image
// type inline (see allowedImageMIMETypes).
func IsSupportedImageMIME(mimeType string) bool {
	return allowedImageMIMETypes[mimeType]
}

// allowedAudioMIMETypes is the set of audio MIME types supported for inline
// audio content. Includes both canonical types and aliases detected by
// net/http.DetectContentType (e.g. "application/ogg" for OGG Vorbis).
var allowedAudioMIMETypes = map[string]bool{
	"audio/mpeg":      true, // MP3
	"audio/wave":      true, // WAV (canonical)
	"audio/wav":       true, // WAV (alternative alias)
	"audio/ogg":       true, // OGG Vorbis
	"application/ogg": true, // OGG (what stdlib's DetectContentType returns)
	"audio/flac":      true, // FLAC
	"audio/mp4":       true, // M4A/AAC
	"audio/webm":      true, // WebM
}

// IsSupportedAudioMIME reports whether a MIME type is accepted for inline
// audio content (see allowedAudioMIMETypes).
func IsSupportedAudioMIME(mimeType string) bool {
	return allowedAudioMIMETypes[mimeType]
}

// LoadImageAttachment reads a local image file at path, content-sniffs its
// MIME type, and returns it as a base64-encoded ContentTypeImage
// ContentBlock ready to append to a user message.
//
// path is a local filesystem path only — remote URLs and provider File API
// uploads are out of scope for this phase.
func LoadImageAttachment(path string) (ContentBlock, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if info.IsDir() {
		return ContentBlock{}, fmt.Errorf("%s is a directory, not a file", path)
	}
	// A named pipe or device reports size 0 and would then block or read
	// without end.
	if !info.Mode().IsRegular() {
		return ContentBlock{}, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxAttachmentBytes {
		return ContentBlock{}, fmt.Errorf("%s is %d bytes, exceeds the %.2f MB (%d bytes) attachment size limit", path, info.Size(), float64(MaxAttachmentBytes)/(1024*1024), MaxAttachmentBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer f.Close()
	// Bounded read: a file that grew after the size check still can't
	// exceed the limit.
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachmentBytes+1))
	if err != nil {
		return ContentBlock{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if int64(len(data)) > MaxAttachmentBytes {
		return ContentBlock{}, fmt.Errorf("%s grew past the %.2f MB (%d bytes) attachment size limit while being read", path, float64(MaxAttachmentBytes)/(1024*1024), MaxAttachmentBytes)
	}

	mimeType := sniffMIMEType(data)
	if !allowedImageMIMETypes[mimeType] {
		return ContentBlock{}, fmt.Errorf("%s has unsupported content type %q — only image/jpeg, image/png, image/gif, image/webp are supported", path, mimeType)
	}

	data, mimeType = ShrinkImage(data, mimeType)
	return ContentBlock{
		Type:     ContentTypeImage,
		MIMEType: mimeType,
		Source: &BlockSource{
			Kind:   SourceKindBase64,
			Base64: base64.StdEncoding.EncodeToString(data),
		},
		Metadata: map[string]any{"size_bytes": int64(len(data))},
	}, nil
}

// LoadAudioAttachment reads a local audio file at path, content-sniffs its
// MIME type, and returns it as a base64-encoded ContentTypeAudio
// ContentBlock ready to append to a user message.
//
// path is a local filesystem path only — remote URLs and provider File API
// uploads are out of scope for this phase.
//
// MIME type detection:
// http.DetectContentType reliably sniffs common audio headers (WAV, MP3 with
// ID3 tag, OGG Vorbis). For formats it doesn't recognize (raw MP3, FLAC, M4A),
// it returns "application/octet-stream"; LoadAudioAttachment then checks the
// file extension as a fallback (.mp3 → audio/mpeg, .flac → audio/flac, .m4a
// → audio/mp4).
func LoadAudioAttachment(path string) (ContentBlock, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if info.IsDir() {
		return ContentBlock{}, fmt.Errorf("%s is a directory, not a file", path)
	}
	// A named pipe or device reports size 0 and would then block or read
	// without end.
	if !info.Mode().IsRegular() {
		return ContentBlock{}, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxAttachmentBytes {
		return ContentBlock{}, fmt.Errorf("%s is %d bytes, exceeds the %.2f MB (%d bytes) attachment size limit", path, info.Size(), float64(MaxAttachmentBytes)/(1024*1024), MaxAttachmentBytes)
	}

	f, err := os.Open(path)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer f.Close()
	// Bounded read: a file that grew after the size check still can't
	// exceed the limit.
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachmentBytes+1))
	if err != nil {
		return ContentBlock{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if int64(len(data)) > MaxAttachmentBytes {
		return ContentBlock{}, fmt.Errorf("%s grew past the %.2f MB (%d bytes) attachment size limit while being read", path, float64(MaxAttachmentBytes)/(1024*1024), MaxAttachmentBytes)
	}

	mimeType := normalizeAudioMIME(sniffMIMEType(data), path)

	if !allowedAudioMIMETypes[mimeType] {
		return ContentBlock{}, fmt.Errorf("%s has unsupported content type %q — supported audio types are: audio/mpeg (MP3), audio/wave (WAV), audio/ogg (OGG), audio/flac (FLAC), audio/mp4 (M4A), audio/webm (WebM)", path, mimeType)
	}

	return ContentBlock{
		Type:     ContentTypeAudio,
		MIMEType: mimeType,
		Source: &BlockSource{
			Kind:   SourceKindBase64,
			Base64: base64.StdEncoding.EncodeToString(data),
		},
		Metadata: map[string]any{"size_bytes": info.Size(), "filename": filepath.Base(path)},
	}, nil
}

// normalizeAudioMIME maps what http.DetectContentType reports for an audio
// file onto the audio MIME types in allowedAudioMIMETypes:
//   - WebM: the stdlib recognizes the EBML container but not whether it holds
//     only audio, so it always reports "video/webm".
//   - M4A: the stdlib reports any ISO-BMFF ("ftyp") file as "video/mp4", so a
//     .m4a extension is trusted to mean the audio-only variant.
//   - Raw MP3 (no ID3 tag), FLAC: not recognized at all
//     ("application/octet-stream"), so the extension decides.
//
// Only called once the caller already treats the file as audio (or is
// deciding whether it is), never for image validation.
func normalizeAudioMIME(sniffed, path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch sniffed {
	case "video/webm":
		return "audio/webm"
	case "video/mp4":
		if ext == ".m4a" {
			return "audio/mp4"
		}
	case "application/octet-stream":
		switch ext {
		case ".mp3":
			return "audio/mpeg"
		case ".flac":
			return "audio/flac"
		case ".m4a":
			return "audio/mp4"
		}
	}
	return sniffed
}

// SniffAttachmentKind reports whether the local file at path is a supported
// image (ContentTypeImage) or audio (ContentTypeAudio) attachment, reading
// only its first 512 bytes. Anything else is an error. It lets --attach
// route a file to LoadImageAttachment or LoadAudioAttachment, which do the
// full size and type validation.
func SniffAttachmentKind(path string) (ContentType, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", path)
	}
	// A named pipe or device would block on read.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, 512))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	sniffed := sniffMIMEType(head)
	if allowedImageMIMETypes[sniffed] {
		return ContentTypeImage, nil
	}
	if allowedAudioMIMETypes[normalizeAudioMIME(sniffed, path)] {
		return ContentTypeAudio, nil
	}
	return "", fmt.Errorf("%s has unsupported content type %q — --attach accepts images (jpeg, png, gif, webp) and audio (mp3, wav, ogg, flac, m4a, webm)", path, sniffed)
}

// sniffMIMEType detects a file's type from its leading bytes via
// net/http.DetectContentType — the stdlib's actual content-sniffing
// function (the mime package only maps file extensions; there is no
// mime.DetectContentType). It never errors: an unrecognized format falls
// back to "application/octet-stream", which allowedImageMIMETypes then
// rejects with a clear message.
func sniffMIMEType(data []byte) string {
	n := len(data)
	if n > 512 {
		n = 512
	}
	return http.DetectContentType(data[:n])
}
