// Package dotenv loads KEY=VALUE pairs from a project-local .env file into
// the process environment, the standard pattern used by quickstart-generated
// projects to persist a freshly typed API key across sessions without ever
// committing it to git or hardcoding it into the YAML config.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

// dangerousVars are dynamic-linker and shell/interpreter-hijack variables
// that Load must never set from a file, even when nothing already exports
// them. Load runs automatically for ANY directory rakitsu is invoked in
// (wired into rootCmd.PersistentPreRunE) — unlike a typical dotenv loader
// only ever called by code a developer wrote for their own project, a
// cloned repo, downloaded example, or shared directory here can carry a
// hostile .env. Without this guard, a variable like LD_PRELOAD or
// BASH_ENV set this way would be inherited by any subprocess rakitsu's
// own tools later spawn (internal/tools/cli, internal/tools/fs) —
// arbitrary code execution the moment rakitsu runs from that directory.
// Not exhaustive by design (no enumerated list of "dangerous env vars"
// ever is) — it covers the well-known dynamic-loader and shell-startup
// hijack vectors across Linux/macOS/BSD, and the module/library-path or
// startup-options variables for the scripting/build runtimes (Python,
// Ruby, Node, Perl, Java) and version-control tooling (Git) that
// rakitsu's own tools might end up invoking as a subprocess.
var dangerousVars = map[string]bool{
	"LD_PRELOAD":            true,
	"LD_LIBRARY_PATH":       true,
	"LD_AUDIT":              true,
	"DYLD_INSERT_LIBRARIES": true,
	"DYLD_LIBRARY_PATH":     true,
	"DYLD_FRAMEWORK_PATH":   true,
	"BASH_ENV":              true,
	"ENV":                   true,
	"ZDOTDIR":               true,
	"IFS":                   true,
	"PS4":                   true,
	"PERL5LIB":              true,
	"PERLLIB":               true,
	"PYTHONPATH":            true,
	"PYTHONSTARTUP":         true,
	"NODE_OPTIONS":          true,
	"NODE_PATH":             true,
	"RUBYOPT":               true,
	"RUBYLIB":               true,
	"GEM_PATH":              true,
	"GEM_HOME":              true,
	// GIT_SSH and GIT_SSH_COMMAND are two generations of the same knob —
	// Git's older, single-executable-path form and its newer, full-command
	// form — either one picks what Git runs in place of ssh. GIT_SSH was
	// missing from the first version of this list, caught by review.
	"GIT_SSH":           true,
	"GIT_SSH_COMMAND":   true,
	"SSH_ASKPASS":       true,
	"GCONV_PATH":        true,
	"JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS":     true,
	"CLASSPATH":         true,
}

// Load reads KEY=VALUE lines from path and sets each one via os.Setenv,
// skipping blank lines, "#" comments, and malformed lines (no "="). It
// never overrides a variable already present in the environment — a real
// shell export always wins over the file — and never sets a name in
// dangerousVars regardless (see its doc comment). A missing file is a
// silent no-op: not every project has a .env, and callers should be able
// to call Load unconditionally.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" || dangerousVars[key] {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		os.Setenv(key, value) //nolint:errcheck
	}
	return scanner.Err()
}
