package llm

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
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

	return ContentBlock{
		Type:     ContentTypeImage,
		MIMEType: mimeType,
		Source: &BlockSource{
			Kind:   SourceKindBase64,
			Base64: base64.StdEncoding.EncodeToString(data),
		},
		Metadata: map[string]any{"size_bytes": info.Size()},
	}, nil
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
