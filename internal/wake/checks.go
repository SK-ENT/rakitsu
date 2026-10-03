// Wake-up timer built-in check implementations (file, http).
package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

type CheckFunc func(ctx context.Context, c config.WakeCheck) Observation

// NewBuiltinChecks builds the real checks. workdir confines file checks. hc may be nil.
func NewBuiltinChecks(cfg config.WakeConfig, workdir string, hc *http.Client) CheckFunc {
	if hc == nil {
		hc = &http.Client{}
	}
	return func(ctx context.Context, c config.WakeCheck) Observation {
		switch c.Type {
		case "file_mtime", "file_contains":
			return checkFile(c, workdir)
		case "http_status", "http_json":
			return checkHTTP(ctx, c, hc, cfg.Allow.URLHosts)
		default:
			return Observation{Err: errors.New("unknown check type")}
		}
	}
}

func checkFile(c config.WakeCheck, workdir string) Observation {
	var path string
	if filepath.IsAbs(c.Path) {
		path = c.Path
	} else {
		path = filepath.Join(workdir, c.Path)
	}
	path = filepath.Clean(path)

	// Resolve symlinks
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil && !os.IsNotExist(err) {
		return Observation{Err: fmt.Errorf("symlink resolution: %w", err)}
	}
	if os.IsNotExist(err) {
		// File doesn't exist, return as Exists=false without error
		return Observation{Exists: false}
	}

	// Resolve workdir for comparison
	resolvedWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return Observation{Err: fmt.Errorf("workdir resolution: %w", err)}
	}

	// Check that resolved path is within workdir
	if !isPathUnder(resolved, resolvedWorkdir) {
		return Observation{Err: errors.New("path escapes workdir")}
	}

	st, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return Observation{Exists: false}
		}
		return Observation{Err: fmt.Errorf("stat: %w", err)}
	}

	switch c.Type {
	case "file_mtime":
		return Observation{Exists: true, ModTime: st.ModTime()}
	case "file_contains":
		file, err := os.Open(resolved)
		if err != nil {
			return Observation{Err: fmt.Errorf("open: %w", err)}
		}
		defer file.Close()
		limited := io.LimitReader(file, 64*1024)
		data, err := io.ReadAll(limited)
		if err != nil {
			return Observation{Err: fmt.Errorf("read: %w", err)}
		}
		contains := strings.Contains(string(data), c.Contains)
		return Observation{Exists: true, Contains: contains}
	}
	return Observation{Exists: true}
}

func isPathUnder(path, base string) bool {
	if path == base {
		return true
	}
	if !strings.HasPrefix(path, base) {
		return false
	}
	// Check that the next character is a separator
	if len(path) > len(base) && path[len(base)] == filepath.Separator {
		return true
	}
	return false
}

func checkHTTP(ctx context.Context, c config.WakeCheck, hc *http.Client, allowedHosts []string) Observation {
	// Validate URL
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Observation{Err: errors.New("invalid URL")}
	}

	// Check host is allowed
	if !hostAllowed(allowedHosts, u.Host) {
		return Observation{Err: errors.New("host not allowed")}
	}

	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.URL, nil)
	if err != nil {
		return Observation{Err: fmt.Errorf("request creation: %w", err)}
	}

	// Create a custom client with redirect checking
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 2 {
				return errors.New("too many redirects")
			}
			if !hostAllowed(allowedHosts, req.URL.Host) {
				return errors.New("redirect to disallowed host")
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return Observation{Err: fmt.Errorf("http error: %w", err)}
	}
	defer resp.Body.Close()

	// Re-check host
	if !hostAllowed(allowedHosts, resp.Request.URL.Host) {
		return Observation{Err: errors.New("final host not allowed")}
	}

	if c.Type == "http_status" {
		return Observation{HTTPStatus: resp.StatusCode}
	}

	// http_json
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Observation{Err: fmt.Errorf("status %d", resp.StatusCode)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return Observation{Err: fmt.Errorf("read body: %w", err)}
	}

	if len(body) > 64*1024 {
		return Observation{Err: errors.New("body exceeds 64 KiB")}
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return Observation{Err: fmt.Errorf("unmarshal JSON: %w", err)}
	}

	num := getFieldAsNumber(data, c.Field)
	if num == nil {
		return Observation{Err: errors.New("field is not a number")}
	}

	return Observation{Number: num}
}

func hostAllowed(allowed []string, host string) bool {
	for _, a := range allowed {
		if a == host {
			return true
		}
	}
	return false
}

func getFieldAsNumber(data any, dotted string) *float64 {
	parts := strings.Split(dotted, ".")
	current := data

	for _, part := range parts {
		if m, ok := current.(map[string]any); ok {
			current = m[part]
		} else {
			return nil
		}
	}

	if f, ok := current.(float64); ok {
		return &f
	}
	return nil
}
