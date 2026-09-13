package dotenv

import (
	"os"
	"path/filepath"
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
