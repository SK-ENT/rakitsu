package llm

import (
	"os"
	"path/filepath"
	"strings"
)

var scanAttachExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".mp3": true, ".wav": true, ".m4a": true, ".ogg": true, ".flac": true, ".webm": true,
}

// ScanQueryForAttachments returns absolute paths of local image/audio files
// named in query. It is best-effort and never errors: tokens that do not
// resolve to a regular file within baseDir (after symlink resolution), exceed
// MaxAttachmentBytes, look like URLs, or lack a known extension are skipped.
// It only stats (never opens), so FIFOs and devices cannot block it. Intended
// for the local CLI only; do not call it on remotely supplied queries.
func ScanQueryForAttachments(query, baseDir string) []string {
	root, err := filepath.Abs(baseDir)
	if err != nil {
		return nil
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.Fields(query) {
		tok = strings.Trim(tok, "\"'`")
		tok = strings.TrimRight(tok, ".,!?;:)\"'`")
		tok = strings.TrimLeft(tok, "(\"'`")
		if tok == "" || strings.Contains(tok, "://") || !scanAttachExts[strings.ToLower(filepath.Ext(tok))] {
			continue
		}
		p := tok
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxAttachmentBytes {
			continue
		}
		if !seen[resolved] {
			seen[resolved] = true
			out = append(out, resolved)
		}
	}
	return out
}
