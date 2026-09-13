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

// Load reads KEY=VALUE lines from path and sets each one via os.Setenv,
// skipping blank lines, "#" comments, and malformed lines (no "="). It
// never overrides a variable already present in the environment — a real
// shell export always wins over the file. A missing file is a silent
// no-op: not every project has a .env, and callers should be able to call
// Load unconditionally.
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
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		os.Setenv(key, value) //nolint:errcheck
	}
	return scanner.Err()
}
