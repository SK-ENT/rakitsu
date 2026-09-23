package server

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// ConfigEntry represents a discovered or uploaded config.
type ConfigEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Agents      []string `json:"agents"`
	Tools       []string `json:"tools"`
	Strategy    string   `json:"strategy,omitempty"`
	Interactive bool     `json:"interactive,omitempty"` // YAML `interactive: true` — drives UI chat gate
}

// ConfigStore manages config discovery and uploads.
type ConfigStore struct {
	mu          sync.RWMutex
	searchPaths []string
	tempDir     string
	entries     map[string]string // id -> absolute file path
}

// NewConfigStore creates a config store scanning the given directories.
func NewConfigStore(searchPaths []string) (*ConfigStore, error) {
	tempDir := filepath.Join(os.TempDir(), "rakitsu-configs")
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return nil, err
	}
	// The path is fixed and os.TempDir() may be shared (/tmp on Linux), so
	// another local user could have created it first — as a symlink, or a
	// directory they own — and redirect or read uploaded configs. Refuse
	// anything but a real directory owned by us, and make it private.
	if err := checkPrivateDir(tempDir); err != nil {
		return nil, err
	}
	return &ConfigStore{
		searchPaths: searchPaths,
		tempDir:     tempDir,
		entries:     make(map[string]string),
	}, nil
}

// List scans search paths for valid rakitsu YAML configs.
func (cs *ConfigStore) List() ([]ConfigEntry, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	var entries []ConfigEntry
	seen := map[string]bool{}

	for _, dir := range cs.searchPaths {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			// Skip conventional modular subdirs — these contain fragment YAMLs, not standalone configs
			if info.IsDir() {
				base := filepath.Base(path)
				if base == "agents" || base == "tools" || base == "skills" || base == "prompts" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true

			cfg, loadErr := config.Load(path)
			if loadErr != nil {
				return nil // skip invalid configs
			}

			id := pathID(path)
			entry := buildEntry(id, path, cfg)
			entries = append(entries, entry)
			cs.entries[id] = path
			return nil
		})
	}

	// Include uploaded configs from temp dir
	filepath.Walk(cs.tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if seen[path] {
			return nil
		}
		seen[path] = true

		cfg, loadErr := config.Load(path)
		if loadErr != nil {
			return nil
		}

		id := pathID(path)
		entry := buildEntry(id, "(uploaded) "+filepath.Base(path), cfg)
		entries = append(entries, entry)
		cs.entries[id] = path
		return nil
	})

	return entries, nil
}

// Upload saves YAML content to a temp file and validates it.
func (cs *ConfigStore) Upload(content []byte, filename string) (*ConfigEntry, error) {
	// Ensure temp dir exists (macOS cleans /tmp on reboot)
	os.MkdirAll(cs.tempDir, 0700)
	id := randomID()
	tempPath := filepath.Join(cs.tempDir, id+"-"+filename)
	if err := os.WriteFile(tempPath, content, 0600); err != nil {
		return nil, err
	}

	cfg, err := config.Load(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	entry := buildEntry(id, "(uploaded) "+filename, cfg)

	cs.mu.Lock()
	cs.entries[id] = tempPath
	cs.mu.Unlock()

	return &entry, nil
}

// Inline saves inline YAML string as a temp config.
//
// ID derivation:
//   - If the YAML has a `project_id`, that is the ID — uploads from the same
//     Builder project overwrite the same entry rather than piling up as
//     duplicates when the user clicks Chat repeatedly.
//   - Otherwise a content hash keys the entry.
func (cs *ConfigStore) Inline(yaml string) (*ConfigEntry, error) {
	content := []byte(yaml)
	// Ensure temp dir exists (macOS cleans /tmp on reboot)
	os.MkdirAll(cs.tempDir, 0700)

	// First, parse to check for project_id.
	tmp := filepath.Join(cs.tempDir, "inline-probe.yaml")
	if err := os.WriteFile(tmp, content, 0600); err != nil {
		return nil, err
	}
	probeCfg, err := config.Load(tmp)
	os.Remove(tmp)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	var id string
	if probeCfg.ProjectID != "" {
		id = "pid-" + sanitizeIDPart(probeCfg.ProjectID)
	} else {
		h := sha256.Sum256(content)
		id = hex.EncodeToString(h[:8])
	}
	filename := id + "-inline.yaml"
	tempPath := filepath.Join(cs.tempDir, filename)

	// If already exists with same content, just return the entry.
	if existing, err := os.ReadFile(tempPath); err == nil && string(existing) == yaml {
		cfg, err := config.Load(tempPath)
		if err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
		entry := buildEntry(id, "(inline) config", cfg)
		cs.mu.Lock()
		cs.entries[id] = tempPath
		cs.mu.Unlock()
		return &entry, nil
	}

	// Overwrite (project_id path) or create (hash path).
	if err := os.WriteFile(tempPath, content, 0600); err != nil {
		return nil, err
	}
	cfg, err := config.Load(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	entry := buildEntry(id, "(inline) config", cfg)
	cs.mu.Lock()
	cs.entries[id] = tempPath
	cs.mu.Unlock()
	return &entry, nil
}

// UploadZip extracts a zip archive containing a modular config (config.yaml + agents/, tools/, etc.)
func (cs *ConfigStore) UploadZip(data []byte) (*ConfigEntry, error) {
	id := randomID()
	extractDir := filepath.Join(cs.tempDir, id)
	if err := os.MkdirAll(extractDir, 0700); err != nil {
		return nil, err
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		os.RemoveAll(extractDir)
		return nil, fmt.Errorf("invalid zip: %w", err)
	}

	// Extract files, stripping common prefix if all files share one. The
	// per-file cap alone doesn't bound a zip of many small entries, so the
	// file count and total extracted size are capped too.
	const (
		maxZipFiles      = 1000
		maxZipTotalBytes = 50 << 20
	)
	if len(reader.File) > maxZipFiles {
		os.RemoveAll(extractDir)
		return nil, fmt.Errorf("zip has %d entries, max %d", len(reader.File), maxZipFiles)
	}
	var total int64
	prefix := zipCommonPrefix(reader.File)
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		relPath := strings.TrimPrefix(f.Name, prefix)
		if relPath == "" || strings.Contains(relPath, "..") {
			continue
		}
		dest := filepath.Join(extractDir, relPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			continue
		}
		n, _ := io.Copy(out, io.LimitReader(rc, 2<<20)) // 2MB per file limit
		out.Close()
		rc.Close()
		if total += n; total > maxZipTotalBytes {
			os.RemoveAll(extractDir)
			return nil, fmt.Errorf("zip extracts to more than %d bytes", maxZipTotalBytes)
		}
	}

	// Find the main config file
	configPath := filepath.Join(extractDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = filepath.Join(extractDir, "config.yml")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			os.RemoveAll(extractDir)
			return nil, fmt.Errorf("zip must contain config.yaml at root")
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		os.RemoveAll(extractDir)
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	entry := buildEntry(id, "(uploaded zip)", cfg)

	cs.mu.Lock()
	cs.entries[id] = configPath
	cs.mu.Unlock()

	return &entry, nil
}

// zipCommonPrefix returns the shared directory prefix of all zip entries.
func zipCommonPrefix(files []*zip.File) string {
	if len(files) == 0 {
		return ""
	}
	prefix := filepath.Dir(files[0].Name) + "/"
	for _, f := range files[1:] {
		for !strings.HasPrefix(f.Name, prefix) {
			prefix = filepath.Dir(strings.TrimSuffix(prefix, "/")) + "/"
			if prefix == "./" || prefix == "/" {
				return ""
			}
		}
	}
	if prefix == "./" {
		return ""
	}
	return prefix
}

// Get loads a full config by ID.
func (cs *ConfigStore) Get(id string) (*config.Config, string, error) {
	return cs.GetWithEnv(id, nil)
}

// Delete removes a config entry. Only applies to uploaded/inline configs that
// live under the temp dir — on-disk configs from the scanned search paths
// (examples/, configs/) are read-only and return an error.
func (cs *ConfigStore) Delete(id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	path, ok := cs.entries[id]
	if !ok {
		return fmt.Errorf("config not found: %s", id)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	tempAbs, _ := filepath.Abs(cs.tempDir)
	if tempAbs == "" || !withinDir(abs, tempAbs) {
		return fmt.Errorf("config %q is on-disk; remove the file to delete it", id)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove: %w", err)
	}
	delete(cs.entries, id)
	return nil
}

// GetWithEnv loads a config with run-scoped env var overrides.
func (cs *ConfigStore) GetWithEnv(id string, envVars map[string]string) (*config.Config, string, error) {
	cs.mu.RLock()
	path, ok := cs.entries[id]
	cs.mu.RUnlock()

	if !ok {
		return nil, "", fmt.Errorf("config not found: %s", id)
	}
	var cfg *config.Config
	var err error
	if len(envVars) > 0 {
		cfg, err = config.LoadWithEnv(path, envVars)
	} else {
		cfg, err = config.Load(path)
	}
	if err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}

// Cleanup removes temp uploaded configs.
// FileRefRoots returns the directories config file references may resolve
// into when configs are loaded through this store: the scanned search paths
// and the upload directory. See config.SetFileRefRoots.
func (cs *ConfigStore) FileRefRoots() []string {
	return append(append([]string{}, cs.searchPaths...), cs.tempDir)
}

func (cs *ConfigStore) Cleanup() {
	os.RemoveAll(cs.tempDir)
}

func buildEntry(id, path string, cfg *config.Config) ConfigEntry {
	entry := ConfigEntry{
		ID:     id,
		Name:   cfg.Name,
		Path:   path,
		Agents: []string{},
		Tools:  []string{},
	}
	for _, a := range cfg.Agents {
		entry.Agents = append(entry.Agents, a.Name)
	}
	for _, t := range cfg.Tools {
		entry.Tools = append(entry.Tools, t.Name)
	}
	if cfg.Orchestrator != nil {
		entry.Strategy = cfg.Orchestrator.Strategy
	}
	entry.Interactive = cfg.Interactive
	return entry
}

func pathID(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(h[:6])
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sanitizeIDPart keeps alnum/dash/underscore; collapses anything else to '-'.
// Used for project_id-derived config IDs that also become filenames.
func sanitizeIDPart(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}
