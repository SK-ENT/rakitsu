package agent

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SK-ENT/rakitsu/internal/config"
)

type retrievalPassage struct {
	Path      string  `json:"path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
}

type retrievalResult struct {
	Passages []retrievalPassage `json:"passages"`
}

func executeRetrievalStep(ctx context.Context, step config.PipelineStep, query string) (retrievalResult, error) {
	if step.TimeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSec)*time.Second)
		defer cancel()
	}
	spec := step.Retrieval
	if err := spec.Validate(); err != nil {
		return retrievalResult{}, err
	}
	rootPath := os.Getenv("RAKITSU_RETRIEVAL_ROOT")
	if !filepath.IsAbs(rootPath) {
		return retrievalResult{}, fmt.Errorf("set RAKITSU_RETRIEVAL_ROOT to an absolute corpus directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return retrievalResult{}, fmt.Errorf("cannot open configured retrieval root")
	}
	defer root.Close()

	index := NewRetriever(config.RetrievalConfig{}, nil)
	passages := make(map[string]retrievalPassage)
	var entries, remaining int
	remaining = 1 << 20
	err = fs.WalkDir(root.FS(), spec.Directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("cannot read retrieval corpus")
		}
		entries++
		if entries > 2048 {
			return fmt.Errorf("retrieval corpus exceeds 2048 entries")
		}
		if name != spec.Directory && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if name == spec.Directory {
			return fmt.Errorf("retrieval directory must be a directory")
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".md", ".markdown", ".txt":
		default:
			return nil
		}
		file, err := root.Open(filepath.FromSlash(name))
		if err != nil {
			return fmt.Errorf("cannot open retrieval document")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			file.Close()
			return fmt.Errorf("retrieval document must be a regular file")
		}
		const maxFile = 64 << 10
		data, err := io.ReadAll(io.LimitReader(file, int64(min(remaining, maxFile))+1))
		file.Close()
		if err != nil || len(data) > remaining || len(data) > maxFile {
			return fmt.Errorf("retrieval corpus exceeds its read limits (64 KiB per file, 1 MiB total)")
		}
		remaining -= len(data)
		if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return fmt.Errorf("retrieval documents must be UTF-8 text")
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for start := 0; start < len(lines); {
			end := start
			size := 0
			for end < len(lines) && end-start < 8 && size+len(lines[end])+1 <= 1024 {
				size += len(lines[end]) + 1
				end++
			}
			if end == start {
				return fmt.Errorf("retrieval document contains a line longer than 1023 bytes")
			}
			text := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
			if text != "" {
				id := fmt.Sprintf("%s:%d", name, start+1)
				passages[id] = retrievalPassage{Path: name, StartLine: start + 1, EndLine: end, Text: text}
				index.Index(id, text, SegmentMeta{})
			}
			start = end
		}
		return nil
	})
	if err != nil {
		return retrievalResult{}, err
	}
	topK := spec.TopK
	if topK == 0 {
		topK = 3
	}
	result := retrievalResult{Passages: make([]retrievalPassage, 0, topK)}
	for _, match := range index.Query(query, topK) {
		passage := passages[match.ID]
		passage.Score = match.Score
		result.Passages = append(result.Passages, passage)
	}
	return result, nil
}
