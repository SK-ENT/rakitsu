// Package fs provides file system tool implementations.
// It supports read, write, list, and search operations with path restrictions.
package fs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
)

// DefaultMaxReadBytes caps how much of a file readFile loads into memory
// when the config leaves sandbox.resource_limits.max_output_bytes unset
// (0). A huge or special file (e.g. /dev/zero, a multi-GB log) would
// otherwise be fully buffered by ioutil.ReadFile before any cap applied,
// which is a memory + context-blowout DoS. Set max_output_bytes to -1 to
// disable the cap entirely.
const DefaultMaxReadBytes = 10 * 1024 * 1024 // 10MB

// Tool implements the Tool interface for file system operations
type Tool struct {
	name         string
	description  string
	operation    string   // "read", "read_image", "write", "list", "search"
	allowedPaths []string // Security: restrict file access
	workingDir   string   // resolve relative paths against this
	parameters   map[string]config.Parameter
	sandbox      *config.SandboxConfig
}

// NewTool creates a new FS tool from configuration
func NewTool(def *config.ToolDefinition) *Tool {
	allowedPaths := def.AllowedPaths
	if len(allowedPaths) == 0 {
		// Default to current directory only
		allowedPaths = []string{"."}
	}

	// Relative paths resolve against the process working directory unless the
	// config sets working_dir explicitly. Deliberately NOT defaulted to
	// allowedPaths[0]: agents are prompted with paths relative to where
	// `rakitsu run` was launched, and joining those onto a fence like
	// ["./workspace/"] silently produced doubled trees (workspace/workspace/…)
	// via writeFile's MkdirAll — see SK-ENT/rakitsu#28.
	workDir := def.WorkingDir

	return &Tool{
		name:         def.Name,
		description:  def.Description,
		operation:    def.Operation,
		allowedPaths: allowedPaths,
		workingDir:   workDir,
		parameters:   def.Parameters,
		sandbox:      def.Sandbox,
	}
}

// maxReadBytes returns the cap applied to readFile: the config's
// sandbox.resource_limits.max_output_bytes when set, else
// DefaultMaxReadBytes. A negative value disables the cap.
func (t *Tool) maxReadBytes() int {
	if t.sandbox == nil || t.sandbox.ResourceLimits.MaxOutputBytes == 0 {
		return DefaultMaxReadBytes
	}
	return t.sandbox.ResourceLimits.MaxOutputBytes
}

// GetName returns the tool's name
func (t *Tool) GetName() string {
	return t.name
}

// GetDescription returns the tool's description
func (t *Tool) GetDescription() string {
	return t.description
}

// GetParametersSchema returns the JSON Schema for parameters
func (t *Tool) GetParametersSchema() map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": make(map[string]interface{}),
		"required":   []string{},
	}

	props := schema["properties"].(map[string]interface{})
	required := []string{}

	for name, param := range t.parameters {
		prop := map[string]interface{}{
			"type":        param.Type,
			"description": param.Description,
		}
		if param.Default != nil {
			prop["default"] = param.Default
		}
		if len(param.Enum) > 0 {
			prop["enum"] = param.Enum
		}
		props[name] = prop

		if param.Required {
			required = append(required, name)
		}
	}

	schema["required"] = required
	return schema
}

// Execute performs the file system operation
func (t *Tool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	text, _, err := t.ExecuteContent(ctx, args)
	return text, err
}

// ExecuteContent implements tools.ContentTool. Only read_image returns a
// content block (the image); every other operation is text only.
func (t *Tool) ExecuteContent(ctx context.Context, args map[string]interface{}) (string, []llm.ContentBlock, error) {
	path, err := t.resolvePath(args)
	if err != nil {
		return "", nil, err
	}
	if t.operation == "read_image" {
		block, err := llm.LoadImageAttachment(path)
		if err != nil {
			return "", nil, err
		}
		block.Metadata["path"] = path
		size, _ := block.Metadata["size_bytes"].(int64)
		return fmt.Sprintf("Loaded image %s (%s, %d bytes).", path, block.MIMEType, size), []llm.ContentBlock{block}, nil
	}
	text, err := t.execute(ctx, path, args)
	return text, nil, err
}

// resolvePath returns the call's path argument, resolved against the working
// directory and checked against allowed_paths.
func (t *Tool) resolvePath(args map[string]interface{}) (string, error) {
	// Get path argument — fall back to first allowed path if omitted
	pathVal, ok := args["path"]
	pathStr := ""
	if ok && pathVal != nil {
		pathStr = fmt.Sprintf("%v", pathVal)
	}
	if pathStr == "" || pathStr == "<nil>" {
		if len(t.allowedPaths) > 0 {
			pathStr = t.allowedPaths[0]
		} else {
			return "", fmt.Errorf("path argument required")
		}
	}
	path := pathStr

	// Resolve relative paths against working directory
	if !filepath.IsAbs(path) && t.workingDir != "" {
		path = filepath.Join(t.workingDir, path)
	}

	// Validate path is within allowed paths
	if !t.isPathAllowed(path) {
		return "", &PathNotAllowedError{
			Path:    path,
			Allowed: t.allowedPaths,
		}
	}
	return path, nil
}

// execute runs a text operation on an already-resolved, allowed path.
func (t *Tool) execute(ctx context.Context, path string, args map[string]interface{}) (string, error) {
	switch t.operation {
	case "read":
		return t.readFile(ctx, path)
	case "write":
		content, ok := args["content"].(string)
		if !ok {
			return "", fmt.Errorf("content argument required for write operation")
		}
		mode, _ := args["mode"].(string)
		return t.writeFile(ctx, path, content, mode)
	case "list":
		pattern, _ := args["pattern"].(string)
		if pattern == "" {
			pattern = "*"
		}
		return t.listDir(ctx, path, pattern)
	case "search":
		pattern, _ := args["pattern"].(string)
		if pattern == "" {
			// No content pattern — fall back to listing all files
			return t.listDir(ctx, path, "*")
		}
		filePattern, _ := args["file_pattern"].(string)
		if filePattern == "" {
			filePattern = "*"
		}
		return t.searchFiles(ctx, path, pattern, filePattern)
	default:
		return "", fmt.Errorf("unknown operation: %s", t.operation)
	}
}

// absAllowed returns an allowed_paths entry as an absolute path. When the
// tool's working_dir is absolute (what `rakitsu run --workdir` sets), a
// relative fence is anchored on it, so allowed_paths: ["."] means "the
// workdir" rather than "wherever rakitsu was launched from". A relative
// working_dir is itself relative to the process cwd (SK-ENT/rakitsu#28),
// so relative fences keep resolving against the cwd in that case, as before.
func (t *Tool) absAllowed(allowed string) (string, error) {
	if !filepath.IsAbs(allowed) && filepath.IsAbs(t.workingDir) {
		allowed = filepath.Join(t.workingDir, allowed)
	}
	return filepath.Abs(allowed)
}

// isPathAllowed checks if a path is within allowed directories.
// Resolves symlinks to prevent traversal via symlink chains.
func (t *Tool) isPathAllowed(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	// Resolve symlinks to get the real path.
	// Walk up the path until we find an existing ancestor to resolve.
	realPath := resolvePathWithSymlinks(absPath)

	for _, allowed := range t.allowedPaths {
		absAllowed, err := t.absAllowed(allowed)
		if err != nil {
			continue
		}
		realAllowed := strings.TrimRight(resolvePathWithSymlinks(absAllowed), string(filepath.Separator))
		if realPath == realAllowed || strings.HasPrefix(realPath, realAllowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvePathWithSymlinks resolves symlinks for existing path components.
// For paths that don't fully exist yet (e.g., write targets), it resolves
// the deepest existing ancestor and appends the remaining components.
func resolvePathWithSymlinks(absPath string) string {
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return resolved
	}

	// Walk up until we find an existing directory
	dir := absPath
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		resolved, err = filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
	}
	return absPath // fallback to original
}

// readFile reads the contents of a file, capped at maxReadBytes so a huge
// or special file (e.g. /dev/zero, a multi-GB log) cannot be fully
// buffered into memory before any limit applies.
func (t *Tool) readFile(ctx context.Context, path string) (string, error) {
	// Check if file exists
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot access file: %w", err)
	}

	// Check if it's a directory
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory, not a file")
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	defer f.Close()

	maxBytes := t.maxReadBytes()
	if maxBytes < 0 {
		content, err := io.ReadAll(f)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		return string(content), nil
	}

	// Read one byte past the cap so we can tell a capped read from a file
	// that happens to be exactly maxBytes long, without ever buffering
	// more than maxBytes+1 bytes regardless of the file's real size.
	// Guard the +1 against overflow: max_output_bytes is a config value an
	// operator could set to math.MaxInt64, and int64(maxBytes)+1 wrapping
	// negative would make LimitReader return EOF immediately, truncating
	// every read to empty instead of applying the (effectively unlimited)
	// cap.
	lookahead := int64(maxBytes)
	if lookahead < math.MaxInt64 {
		lookahead++
	}
	content, err := io.ReadAll(io.LimitReader(f, lookahead))
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	if len(content) > maxBytes {
		return string(content[:maxBytes]) + fmt.Sprintf("\n... [truncated, file is %d+ bytes, max_output_bytes cap is %d]", info.Size(), maxBytes), nil
	}
	return string(content), nil
}

// writeFile writes content to a file
func (t *Tool) writeFile(ctx context.Context, path, content, mode string) (string, error) {
	// Default mode is "write" (overwrite)
	if mode == "" {
		mode = "write"
	}

	// Create parent directories if needed
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
			return "", fmt.Errorf("failed to create directory: %w", mkErr)
		}
	}

	var err error
	switch mode {
	case "write":
		err = ioutil.WriteFile(path, []byte(content), 0644)
	case "append":
		var f *os.File
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return "", fmt.Errorf("failed to open file: %w", err)
		}
		defer f.Close()
		_, err = f.WriteString(content)
	default:
		return "", fmt.Errorf("unknown write mode: %s", mode)
	}

	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

// listDir lists the contents of a directory
func (t *Tool) listDir(ctx context.Context, path, pattern string) (string, error) {
	// Check if directory exists
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cannot access directory: %w", err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}

	// Read directory contents
	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	// Filter by pattern and format output
	var lines []string
	for _, entry := range entries {
		matched, err := filepath.Match(pattern, entry.Name())
		if err != nil {
			continue
		}
		if matched {
			typeStr := "file"
			if entry.IsDir() {
				typeStr = "dir"
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%d", typeStr, entry.Name(), entry.Size()))
		}
	}

	if len(lines) == 0 {
		return "No files found matching pattern: " + pattern, nil
	}

	return strings.Join(lines, "\n"), nil
}

// searchFiles searches for files under basePath whose contents contain
// contentPattern. Every candidate is opened through an os.Root anchored at
// the allowed path containing basePath, so a symlink planted inside the
// fence that points outside it is refused at open time by the kernel-level
// root check — not by a pre-check that a rename could race — and the walk
// itself never descends a symlinked directory.
//
// The allowed-path check in searchRoot and os.OpenRoot are both pathname
// lookups, so a rename between them could anchor the root somewhere else.
// They are bound together by inode: basePath is pinned as a handle before
// anything is resolved by name, the search target is then opened once
// through the root as its own handle, and the search proceeds only if that
// handle is the same file as the pinned one — and only through that
// handle, never by re-resolving basePath. A swap of the allowed directory
// (or a parent of it) at any point after the pin therefore fails closed.
func (t *Tool) searchFiles(ctx context.Context, basePath, contentPattern, filePattern string) (string, error) {
	// O_NONBLOCK because the allowed-path check has not run yet: basePath is
	// still caller-chosen and may point outside the fence. Opening a FIFO
	// blocks until a writer arrives, and ctx does not reach this open, so
	// without it a named pipe would hang this goroutine before searchRoot
	// could reject the path.
	base, err := os.OpenFile(basePath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer base.Close()
	baseInfo, err := base.Stat()
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	if mode := baseInfo.Mode(); !mode.IsDir() && !mode.IsRegular() {
		return "", fmt.Errorf("search failed: %s is not a regular file or directory", basePath)
	}

	rootDir, rel, err := t.searchRoot(basePath)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer root.Close()
	moved := fmt.Errorf("search failed: %s changed while its allowed-path check was running", basePath)

	var results []string
	if !baseInfo.IsDir() {
		// Single-file search: open it through the root and match on that
		// handle only.
		f, err := root.Open(rel)
		if err != nil {
			return "", fmt.Errorf("search failed: %w", err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !os.SameFile(info, baseInfo) {
			return "", moved
		}
		if matched, _ := filepath.Match(filePattern, info.Name()); matched {
			if content, ok := readRegular(f); ok && strings.Contains(string(content), contentPattern) {
				results = append(results, basePath)
			}
		}
		return formatSearchResults(results, contentPattern), nil
	}

	sub, err := root.OpenRoot(rel)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	defer sub.Close()
	anchored, err := sub.Stat(".")
	if err != nil || !os.SameFile(anchored, baseInfo) {
		return "", moved
	}

	err = fs.WalkDir(sub.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // Skip entries we can't access
		}
		matched, err := filepath.Match(filePattern, d.Name())
		if err != nil || !matched {
			return nil
		}
		f, err := sub.Open(p) // refuses any symlink component resolving outside sub
		if err != nil {
			return nil
		}
		content, ok := readRegular(f)
		f.Close()
		if ok && strings.Contains(string(content), contentPattern) {
			results = append(results, filepath.Join(basePath, filepath.FromSlash(p)))
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}
	return formatSearchResults(results, contentPattern), nil
}

func formatSearchResults(results []string, contentPattern string) string {
	if len(results) == 0 {
		return "No files found containing: " + contentPattern
	}
	return strings.Join(results, "\n")
}

// searchRoot returns the symlink-resolved allowed directory that contains
// basePath, and basePath's position under it in io/fs form ("." for the
// root itself).
func (t *Tool) searchRoot(basePath string) (string, string, error) {
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", "", err
	}
	realBase := resolvePathWithSymlinks(absBase)
	sep := string(filepath.Separator)
	for _, allowed := range t.allowedPaths {
		absAllowed, err := t.absAllowed(allowed)
		if err != nil {
			continue
		}
		realAllowed := resolvePathWithSymlinks(absAllowed)
		trimmed := strings.TrimRight(realAllowed, sep)
		if realBase != trimmed && !strings.HasPrefix(realBase, trimmed+sep) {
			continue
		}
		if trimmed == "" {
			trimmed = sep // allowed path is the filesystem root
		}
		rel, err := filepath.Rel(trimmed, realBase)
		if err != nil {
			continue
		}
		return trimmed, filepath.ToSlash(rel), nil
	}
	return "", "", &PathNotAllowedError{Path: basePath, Allowed: t.allowedPaths}
}

// readRegular returns f's content when it is a regular file.
func readRegular(f *os.File) ([]byte, bool) {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, false
	}
	return data, true
}

// PathNotAllowedError is returned when a path is not in allowed paths
type PathNotAllowedError struct {
	Path    string
	Allowed []string
}

func (e *PathNotAllowedError) Error() string {
	return fmt.Sprintf("path '%s' is not in allowed paths: %v", e.Path, e.Allowed)
}
