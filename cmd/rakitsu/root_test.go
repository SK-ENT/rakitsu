package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression: rootCmd.PersistentPreRunE must load a project-local
// .env from the current working directory before any subcommand runs,
// so a quickstart-generated project's saved key is available to `run`/
// `serve`/`doctor` without a manual export — but must never override a
// variable the real shell environment already set.
func TestRootPersistentPreRunE_LoadsDotEnvWithoutClobberingRealEnv(t *testing.T) {
	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE is nil — .env auto-load not wired up")
	}

	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origWd) }) //nolint:errcheck

	unsetenvForTest(t, "ROOT_DOTENV_TEST_UNSET")
	t.Setenv("ROOT_DOTENV_TEST_SET", "from-shell")

	envContent := "ROOT_DOTENV_TEST_UNSET=from-file\nROOT_DOTENV_TEST_SET=from-file\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("ROOT_DOTENV_TEST_UNSET") }) //nolint:errcheck

	if err := rootCmd.PersistentPreRunE(rootCmd, nil); err != nil {
		t.Fatalf("PersistentPreRunE returned an error: %v", err)
	}

	if got := os.Getenv("ROOT_DOTENV_TEST_UNSET"); got != "from-file" {
		t.Errorf("ROOT_DOTENV_TEST_UNSET = %q, want %q (loaded from .env)", got, "from-file")
	}
	if got := os.Getenv("ROOT_DOTENV_TEST_SET"); got != "from-shell" {
		t.Errorf("ROOT_DOTENV_TEST_SET = %q, want %q (real shell export must win)", got, "from-shell")
	}
}
