package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandSessionsDir expands ${VAR} / ${VAR:-default} references and a leading
// "~" in a sessions directory setting.
func expandSessionsDir(p string) string {
	out, _ := expandSessionsDirChecked(p)
	return out
}

// expandSessionsDirChecked also reports whether any ${VAR} reference
// resolved to empty, which would silently turn "${X}/s" into "/s".
func expandSessionsDirChecked(p string) (string, bool) {
	unresolved := false
	p = strings.TrimSpace(p)
	p = os.Expand(p, func(name string) string {
		var v string
		if i := strings.Index(name, ":-"); i != -1 {
			v = getEnvWithDefault(name[:i], name[i+2:])
		} else {
			v = lookupEnv(name)
		}
		if v == "" {
			unresolved = true
		}
		return v
	})
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, p[1:])
		}
	}
	return p, unresolved
}

// validateSessionsDir checks settings.sessions_dir. Empty means "unset".
func validateSessionsDir(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("must not be blank")
	}
	exp, unresolved := expandSessionsDirChecked(raw)
	if exp == "" || unresolved {
		return fmt.Errorf("%q references an unset or empty environment variable", raw)
	}
	if !filepath.IsAbs(exp) {
		return fmt.Errorf("%q must be an absolute path after ~ and ${VAR} expansion (got %q)", raw, exp)
	}
	return nil
}

// ResolveSessionsDir picks the session directory: flag > settings.sessions_dir
// > "" (empty = the store's default, ~/.rakitsu/sessions). A flag value may be
// relative and is made absolute; a settings value must already be absolute.
// cfg may be nil.
func ResolveSessionsDir(flag string, cfg *Config) (string, error) {
	if flag != "" {
		exp := expandSessionsDir(flag)
		if exp == "" {
			return "", fmt.Errorf("--sessions-dir %q expands to an empty path", flag)
		}
		abs, err := filepath.Abs(exp)
		if err != nil {
			return "", fmt.Errorf("--sessions-dir: %w", err)
		}
		return abs, nil
	}
	if cfg == nil || cfg.Settings.SessionsDir == "" {
		return "", nil
	}
	if err := validateSessionsDir(cfg.Settings.SessionsDir); err != nil {
		return "", fmt.Errorf("settings.sessions_dir: %w", err)
	}
	return filepath.Clean(expandSessionsDir(cfg.Settings.SessionsDir)), nil
}
