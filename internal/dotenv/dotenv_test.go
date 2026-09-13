package dotenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_SetsUnsetVars(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("DOTENV_TEST_A") }) //nolint:errcheck
	os.Unsetenv("DOTENV_TEST_A")                       //nolint:errcheck

	path := writeTemp(t, "DOTENV_TEST_A=hello\n")
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_A"); got != "hello" {
		t.Errorf("DOTENV_TEST_A = %q, want %q", got, "hello")
	}
}

// The whole point of a .env loader: a real shell export must always win.
// Loading a .env must never clobber a variable the environment already has.
func TestLoad_NeverOverridesExistingEnv(t *testing.T) {
	t.Setenv("DOTENV_TEST_B", "from-shell")

	path := writeTemp(t, "DOTENV_TEST_B=from-file\n")
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_B"); got != "from-shell" {
		t.Errorf("DOTENV_TEST_B = %q, want %q (real env must win)", got, "from-shell")
	}
}

func TestLoad_SkipsCommentsAndBlankLines(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("DOTENV_TEST_C") }) //nolint:errcheck
	os.Unsetenv("DOTENV_TEST_C")                       //nolint:errcheck

	path := writeTemp(t, "# a comment\n\nDOTENV_TEST_C=value\n  # indented comment\n")
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_C"); got != "value" {
		t.Errorf("DOTENV_TEST_C = %q, want %q", got, "value")
	}
}

func TestLoad_StripsQuotesAndWhitespace(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("DOTENV_TEST_D") }) //nolint:errcheck
	os.Unsetenv("DOTENV_TEST_D")                       //nolint:errcheck

	path := writeTemp(t, `DOTENV_TEST_D = "quoted value"`+"\n")
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_D"); got != "quoted value" {
		t.Errorf("DOTENV_TEST_D = %q, want %q", got, "quoted value")
	}
}

// A missing .env is normal — not every project has one. Load must be a
// silent no-op, not an error, so callers can unconditionally call it.
func TestLoad_MissingFileIsNoOp(t *testing.T) {
	err := Load(filepath.Join(t.TempDir(), "does-not-exist.env"))
	if err != nil {
		t.Fatalf("Load on a missing file should be a no-op, got error: %v", err)
	}
}

func TestLoad_IgnoresMalformedLines(t *testing.T) {
	path := writeTemp(t, "not-a-valid-line\nDOTENV_TEST_E=ok\n")
	t.Cleanup(func() { os.Unsetenv("DOTENV_TEST_E") }) //nolint:errcheck
	os.Unsetenv("DOTENV_TEST_E")                       //nolint:errcheck

	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_E"); got != "ok" {
		t.Errorf("DOTENV_TEST_E = %q, want %q", got, "ok")
	}
}

// Security: Load runs automatically for ANY directory a user runs rakitsu
// in (wired into rootCmd.PersistentPreRunE) — unlike a typical dotenv
// loader only ever invoked by code the developer themselves wrote for
// their own project. A cloned repo, a downloaded example, or a shared
// directory can carry a hostile .env; without a guard, Load would happily
// set dynamic-linker/shell-hijack variables (LD_PRELOAD, BASH_ENV, ...)
// that any later subprocess rakitsu's own tools spawn (internal/tools/cli,
// internal/tools/fs) would inherit — arbitrary code execution the moment
// rakitsu is run from that directory. Found by the automated PR review on
// paupawsan/rakitsu#86.

func TestLoad_RefusesToSetKnownDangerousVars(t *testing.T) {
	dangerous := []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT",
		"DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "DYLD_FRAMEWORK_PATH",
		"BASH_ENV", "ENV", "ZDOTDIR", "IFS", "PS4",
		"PERL5LIB", "PERLLIB", "PYTHONPATH", "PYTHONSTARTUP",
		"NODE_OPTIONS", "NODE_PATH", "RUBYOPT", "RUBYLIB",
		"GEM_PATH", "GEM_HOME",
		// GIT_SSH is Git's older single-executable-path form of the same
		// knob GIT_SSH_COMMAND provides — missed in the first version of
		// this list, caught by the automated review on this same PR.
		"GIT_SSH", "GIT_SSH_COMMAND",
		"SSH_ASKPASS", "GCONV_PATH",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH",
	}

	var lines strings.Builder
	for _, name := range dangerous {
		t.Cleanup(func(n string) func() { return func() { os.Unsetenv(n) } }(name)) //nolint:errcheck
		os.Unsetenv(name)                                                           //nolint:errcheck
		fmt.Fprintf(&lines, "%s=malicious\n", name)
	}

	path := writeTemp(t, lines.String())
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	for _, name := range dangerous {
		if got := os.Getenv(name); got != "" {
			t.Errorf("%s = %q, want unset — this variable must never be settable from a project-local .env", name, got)
		}
	}
}

// A dangerous variable already legitimately exported by the real shell
// must be left completely untouched — the block only ever applies to the
// FILE trying to introduce one that wasn't already there.
func TestLoad_DoesNotUnsetAnAlreadyExportedDangerousVar(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/legit/shell/export.so")

	path := writeTemp(t, "LD_PRELOAD=malicious\n")
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("LD_PRELOAD"); got != "/legit/shell/export.so" {
		t.Errorf("LD_PRELOAD = %q, want the shell's own value untouched", got)
	}
}

// The block must not collide with an ordinary provider credential that
// simply happens to share no relation to the denylist.
func TestLoad_StillSetsOrdinaryVarsAlongsideABlockedOne(t *testing.T) {
	t.Cleanup(func() {
		os.Unsetenv("LD_PRELOAD")           //nolint:errcheck
		os.Unsetenv("DOTENV_TEST_ORDINARY") //nolint:errcheck
	})
	os.Unsetenv("LD_PRELOAD")           //nolint:errcheck
	os.Unsetenv("DOTENV_TEST_ORDINARY") //nolint:errcheck

	path := writeTemp(t, "LD_PRELOAD=malicious\nDOTENV_TEST_ORDINARY=fine\n")
	if err := Load(path); err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if got := os.Getenv("LD_PRELOAD"); got != "" {
		t.Errorf("LD_PRELOAD = %q, want unset", got)
	}
	if got := os.Getenv("DOTENV_TEST_ORDINARY"); got != "fine" {
		t.Errorf("DOTENV_TEST_ORDINARY = %q, want %q", got, "fine")
	}
}
