package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paupawsan/rakitsu/internal/scaffold"
	"github.com/spf13/cobra"
)

// ============================================================
// readLine — EOF vs blank-line
// ============================================================

// Regression: readLine used to return only a string, so a real blank line
// ("user pressed Enter to accept the default") and a closed/exhausted
// stdin ("no more input at all") were indistinguishable — both returned
// "". A non-interactive quickstart invocation (piped/redirected/closed
// stdin) would then silently walk every remaining prompt's default,
// including "Start web UI now? [Y/n]" (default Y), launching a real
// server with no terminal attached. readLine must report which case
// happened.

func TestReadLine_RealLine(t *testing.T) {
	line, ok := readLine(bufio.NewReader(strings.NewReader("hello\n")))
	if !ok {
		t.Fatal("expected ok=true for a real line")
	}
	if line != "hello" {
		t.Errorf("line = %q, want %q", line, "hello")
	}
}

func TestReadLine_BlankLine(t *testing.T) {
	// A real Enter press on an empty line — must still read as ok=true so
	// the caller applies its bracketed default, not an EOF abort.
	line, ok := readLine(bufio.NewReader(strings.NewReader("\nmore\n")))
	if !ok {
		t.Fatal("expected ok=true for a blank line followed by more input")
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}

func TestReadLine_ImmediateEOF(t *testing.T) {
	line, ok := readLine(bufio.NewReader(strings.NewReader("")))
	if ok {
		t.Fatal("expected ok=false on immediate EOF")
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}

func TestReadLine_LastLineNoTrailingNewline(t *testing.T) {
	// The final line of a script's stdin may have no trailing newline —
	// that's still real content, not an EOF-with-nothing-left.
	line, ok := readLine(bufio.NewReader(strings.NewReader("last")))
	if !ok {
		t.Fatal("expected ok=true — EOF carried real content")
	}
	if line != "last" {
		t.Errorf("line = %q, want %q", line, "last")
	}
}

// ============================================================
// runQuickstart — non-interactive stdin must abort, not cascade defaults
// ============================================================

func TestRunQuickstart_ImmediateEOFAborts(t *testing.T) {
	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close() // no data written — stdin is at EOF from the first read
	os.Stdin = r

	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd) //nolint:errcheck

	if err := runQuickstart(quickstartCmd, nil); err == nil {
		t.Fatal("expected an error on immediate EOF, got nil — quickstart must not " +
			"silently cascade through every remaining prompt's default (including " +
			"starting a live server) when stdin has no input")
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no project files created on immediate EOF, found: %v", entries)
	}
}

// ============================================================
// quickstartProviders — LiteLLM key/base_url guidance
// ============================================================

// Regression: LiteLLM had no key/base_url guidance at all in the wizard —
// needKey was false, so the Step 3 API-key check/prompt/warn flow (already
// working for openai/anthropic/gemini) never ran for it, and there was no
// equivalent step for base_url either. A user picking LiteLLM had no idea
// LITELLM_API_KEY/LITELLM_BASE_URL needed to be set before the generated
// config would work.

func TestQuickstartProviders_LiteLLMNeedsKeyAndBaseURL(t *testing.T) {
	p := findQuickstartProvider(t, "litellm")
	if !p.needKey || p.envVar != "LITELLM_API_KEY" {
		t.Errorf("litellm: needKey=%v envVar=%q, want needKey=true envVar=LITELLM_API_KEY", p.needKey, p.envVar)
	}
	if !p.needBaseURL || p.baseURLEnvVar != "LITELLM_BASE_URL" {
		t.Errorf("litellm: needBaseURL=%v baseURLEnvVar=%q, want needBaseURL=true baseURLEnvVar=LITELLM_BASE_URL", p.needBaseURL, p.baseURLEnvVar)
	}
}

// Ollama deliberately gets no base_url guidance: rakitsu already defaults
// it to http://localhost:11434/v1 at runtime (createLLMProvider) when
// unset, so prompting for it here would be guidance for a problem that
// doesn't exist for the common local case.
func TestQuickstartProviders_OllamaNoBaseURLGuidance(t *testing.T) {
	p := findQuickstartProvider(t, "ollama")
	if p.needBaseURL {
		t.Error("ollama: needBaseURL=true, want false — it already has a runtime default")
	}
}

func TestQuickstartProviders_OpenAIUnaffected(t *testing.T) {
	p := findQuickstartProvider(t, "openai")
	if !p.needKey || p.envVar != "OPENAI_API_KEY" {
		t.Errorf("openai: needKey=%v envVar=%q, want needKey=true envVar=OPENAI_API_KEY", p.needKey, p.envVar)
	}
	if p.needBaseURL {
		t.Error("openai: needBaseURL=true, want false")
	}
}

// Regression: os.WriteFile only applies the given permission mode when
// CREATING a file — if .env already exists (e.g. hand-created, or left
// over from before this feature), WriteFile happily truncates and
// rewrites its content but leaves its existing (looser) permission bits
// untouched. writeQuickstartEnv must not rely on that create-time
// semantics; it must enforce 0600 unconditionally.
func TestWriteQuickstartEnv_TightensLoosePermissionsOnExistingFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("OTHER_VAR=x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	prov := findQuickstartProvider(t, "openai")
	if err := writeQuickstartEnv(dir, prov, "sk-new", true, "", false); err != nil {
		t.Fatalf("writeQuickstartEnv: %v", err)
	}

	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf(".env mode = %o, want 0600 (must be tightened even when the file already existed)", perm)
	}
}

// Regression: the write used to be a full truncate, not a merge —
// rerunning quickstart into a project whose .env already has entries (a
// different provider's key, hand-added variables) silently deleted
// everything not part of this run's freshly typed values.
func TestWriteQuickstartEnv_PreservesUnrelatedExistingEntries(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("OTHER_VAR=keep-me\n"), 0600); err != nil {
		t.Fatal(err)
	}

	prov := findQuickstartProvider(t, "openai")
	if err := writeQuickstartEnv(dir, prov, "sk-new", true, "", false); err != nil {
		t.Fatalf("writeQuickstartEnv: %v", err)
	}

	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "OTHER_VAR=keep-me") {
		t.Errorf(".env content = %q, must preserve unrelated existing entries", content)
	}
	if !strings.Contains(string(content), "OPENAI_API_KEY=sk-new") {
		t.Errorf(".env content = %q, missing the newly written key", content)
	}
}

// Regression: rewriting an already-present key must replace its line
// in place, not append a duplicate.
func TestWriteQuickstartEnv_ReplacesExistingKeyInPlace(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=sk-old\nOTHER_VAR=keep-me\n"), 0600); err != nil {
		t.Fatal(err)
	}

	prov := findQuickstartProvider(t, "openai")
	if err := writeQuickstartEnv(dir, prov, "sk-new", true, "", false); err != nil {
		t.Fatalf("writeQuickstartEnv: %v", err)
	}

	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "sk-old") {
		t.Errorf(".env content = %q, old value must be replaced, not left alongside the new one", content)
	}
	if !strings.Contains(string(content), "OPENAI_API_KEY=sk-new") {
		t.Errorf(".env content = %q, missing the newly written key", content)
	}
	if !strings.Contains(string(content), "OTHER_VAR=keep-me") {
		t.Errorf(".env content = %q, must preserve unrelated existing entries", content)
	}
}

// Regression: a duplicate existing line for the same key (hand-edited
// file, or an artifact of some other tool) must not survive the merge
// with its stale value — only the fresh value should remain, once.
func TestWriteQuickstartEnv_DropsDuplicateExistingKeyLines(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=sk-old-1\nOTHER_VAR=keep-me\nOPENAI_API_KEY=sk-old-2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	prov := findQuickstartProvider(t, "openai")
	if err := writeQuickstartEnv(dir, prov, "sk-new", true, "", false); err != nil {
		t.Fatalf("writeQuickstartEnv: %v", err)
	}

	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(content), "OPENAI_API_KEY="); got != 1 {
		t.Errorf(".env content = %q, want exactly 1 OPENAI_API_KEY= line, got %d", content, got)
	}
	if strings.Contains(string(content), "sk-old") {
		t.Errorf(".env content = %q, no stale value should survive", content)
	}
	if !strings.Contains(string(content), "OTHER_VAR=keep-me") {
		t.Errorf(".env content = %q, must preserve unrelated existing entries", content)
	}
}

// Both a typed key and a typed base_url (e.g. LiteLLM) exercise
// mergeEnvLines's multi-key path and the deterministic-order append.
func TestWriteQuickstartEnv_BothKeyAndBaseURLTyped(t *testing.T) {
	dir := t.TempDir()

	prov := findQuickstartProvider(t, "litellm")
	if err := writeQuickstartEnv(dir, prov, "sk-new", true, "https://proxy.example/v1", true); err != nil {
		t.Fatalf("writeQuickstartEnv: %v", err)
	}

	envPath := filepath.Join(dir, ".env")
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "LITELLM_API_KEY=sk-new") {
		t.Errorf(".env content = %q, missing the API key", content)
	}
	if !strings.Contains(string(content), "LITELLM_BASE_URL=https://proxy.example/v1") {
		t.Errorf(".env content = %q, missing the base_url", content)
	}
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf(".env mode = %o, want 0600 on a freshly created file too", perm)
	}
}

// Regression: writeQuickstartEnv's temp file (created via os.CreateTemp +
// os.Rename for an atomic, correctly-permissioned write) must itself be
// gitignored — if the process dies between create and rename, a leftover
// .env.tmp-* holding the same secret should never be git-addable either.
func TestWriteQuickstartEnv_GitignoresTempFilePattern(t *testing.T) {
	dir := t.TempDir()

	prov := findQuickstartProvider(t, "openai")
	if err := writeQuickstartEnv(dir, prov, "sk-new", true, "", false); err != nil {
		t.Fatalf("writeQuickstartEnv: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), ".env.tmp-*") {
		t.Errorf(".gitignore = %q, missing .env.tmp-* pattern for the write's temp file", content)
	}
}

// Security: if ensureGitignoreHasEnv can't run, writeQuickstartEnv must
// never have written the secret-bearing .env at all — not write it and
// then merely report the gitignore failure. Otherwise a user sees
// quickstart report an error and reasonably assumes nothing was written,
// while an ungitignored .env sits on disk ready to be committed. Found
// by the automated PR review on paupawsan/rakitsu#86.
func TestWriteQuickstartEnv_NeverWritesEnvIfGitignoreCannotBeEnsured(t *testing.T) {
	dir := t.TempDir()
	// Make .gitignore a directory so ensureGitignoreHasEnv's os.WriteFile
	// fails outright — a stand-in for any reason it might (permissions,
	// read-only filesystem, disk full).
	if err := os.Mkdir(filepath.Join(dir, ".gitignore"), 0755); err != nil {
		t.Fatal(err)
	}

	prov := findQuickstartProvider(t, "openai")
	err := writeQuickstartEnv(dir, prov, "sk-should-never-land", true, "", false)
	if err == nil {
		t.Fatal("writeQuickstartEnv succeeded despite .gitignore being unwritable — want an error")
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Errorf(".env exists after a reported failure — secret was written before gitignore protection was confirmed (stat err: %v)", statErr)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, ".env.tmp-*"))
	if len(entries) != 0 {
		t.Errorf("leftover temp file(s) %v after a reported failure — secret's temp file was written before gitignore protection was confirmed", entries)
	}
}

func findQuickstartProvider(t *testing.T, id string) quickstartProvider {
	t.Helper()
	for _, p := range quickstartProviders {
		if p.id == id {
			return p
		}
	}
	t.Fatalf("provider %q not found in quickstartProviders", id)
	return quickstartProvider{}
}

// unsetenvForTest clears an env var for the duration of the test and
// restores its original value (or absence) afterward — t.Setenv cannot
// unset a variable, only set it to a value.
func unsetenvForTest(t *testing.T, key string) {
	t.Helper()
	if orig, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { os.Setenv(key, orig) }) //nolint:errcheck
	} else {
		t.Cleanup(func() { os.Unsetenv(key) }) //nolint:errcheck
	}
	os.Unsetenv(key) //nolint:errcheck
}

// runQuickstartWithInput drives runQuickstart with the given stdin lines
// (each already including its own line ending) and returns its error.
func runQuickstartWithInput(t *testing.T, input string) error {
	t.Helper()
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString(input) //nolint:errcheck
		w.Close()
	}()
	os.Stdin = r

	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origWd) }) //nolint:errcheck

	return runQuickstart(quickstartCmd, nil)
}

// Regression: quickstart never prompts for an API key or base_url
// to be typed — a typed value can't be persisted anywhere useful (it would
// only live in this one process, never reaching a later `rakitsu run` in a
// new shell), so the wizard only detects-or-warns. Selecting LiteLLM
// (provider 5) with neither env var set must consume zero extra stdin
// lines and complete without prompting.
func TestRunQuickstart_LiteLLM_PromptsForKeyAndBaseURLWhenUnset(t *testing.T) {
	unsetenvForTest(t, "LITELLM_API_KEY")
	unsetenvForTest(t, "LITELLM_BASE_URL")

	// template(blank) provider(5=litellm) apikey(blank) baseurl(blank)
	// dir(blank) structure(blank) start-web-ui(n)
	err := runQuickstartWithInput(t, "\n5\n\n\n\n\nn\n")
	if err != nil {
		t.Fatalf("runQuickstart returned an error: %v", err)
	}
}

// Regression: a freshly typed key/base_url must be persisted
// to the generated project's .env (mode 0600) so a later `rakitsu run`
// from a new shell can pick it up via internal/dotenv — os.Setenv alone
// only reaches this one process. A .gitignore entry must also exist so
// the secret is never accidentally committed.
func TestRunQuickstart_TypedKeyAndBaseURLPersistToDotEnv(t *testing.T) {
	unsetenvForTest(t, "LITELLM_API_KEY")
	unsetenvForTest(t, "LITELLM_BASE_URL")

	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origWd) }) //nolint:errcheck

	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		// template(blank) provider(5=litellm) apikey(typed) baseurl(typed)
		// dir(myproj) structure(blank) start-web-ui(n)
		w.WriteString("\n5\nsk-typed-key\nhttps://typed.example/v1\nmyproj\n\nn\n") //nolint:errcheck
		w.Close()
	}()
	os.Stdin = r

	if err := runQuickstart(quickstartCmd, nil); err != nil {
		t.Fatalf("runQuickstart returned an error: %v", err)
	}

	envPath := filepath.Join(dir, "myproj", ".env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf(".env not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf(".env mode = %o, want 0600", perm)
	}
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "LITELLM_API_KEY=sk-typed-key") {
		t.Errorf(".env content = %q, missing LITELLM_API_KEY", content)
	}
	if !strings.Contains(string(content), "LITELLM_BASE_URL=https://typed.example/v1") {
		t.Errorf(".env content = %q, missing LITELLM_BASE_URL", content)
	}

	gitignore, err := os.ReadFile(filepath.Join(dir, "myproj", ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore not written: %v", err)
	}
	if !strings.Contains(string(gitignore), ".env") {
		t.Errorf(".gitignore = %q, missing .env entry", gitignore)
	}
}

// Regression: when both env vars are already detected from the real
// environment, nothing should be written to .env — that value is already
// available everywhere and duplicating it into a file is unnecessary.
func TestRunQuickstart_DetectedKeyDoesNotWriteDotEnv(t *testing.T) {
	t.Setenv("LITELLM_API_KEY", "sk-test-key")
	t.Setenv("LITELLM_BASE_URL", "https://example.invalid/v1")

	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origWd) }) //nolint:errcheck

	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString("\n5\nmyproj\n\nn\n") //nolint:errcheck
		w.Close()
	}()
	os.Stdin = r

	if err := runQuickstart(quickstartCmd, nil); err != nil {
		t.Fatalf("runQuickstart returned an error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "myproj", ".env")); !os.IsNotExist(err) {
		t.Errorf(".env should not be written for env-detected values, stat err = %v", err)
	}
}

// Regression: when both env vars ARE already set, the wizard must detect
// them and skip the prompts entirely — consuming zero extra stdin lines.
// This guards against exactly the off-by-one-prompt class of bug this
// feature's own manual testing tripped over (a miscounted line silently
// shifts every later answer, including "Start web UI now?").
func TestRunQuickstart_LiteLLM_DetectsKeyAndBaseURLWhenSet(t *testing.T) {
	t.Setenv("LITELLM_API_KEY", "sk-test-key")
	t.Setenv("LITELLM_BASE_URL", "https://example.invalid/v1")

	// template(blank) provider(5=litellm) dir(blank) structure(blank)
	// start-web-ui(n) — no key/base_url lines, both are pre-detected.
	err := runQuickstartWithInput(t, "\n5\n\n\nn\n")
	if err != nil {
		t.Fatalf("runQuickstart returned an error: %v", err)
	}
}

// Codex needs neither key nor base_url, so the wizard must not prompt for
// them, and the generated config must leave the model to ~/.codex/config.toml.
func TestRunQuickstart_Codex_NoKeyPromptAndNoModel(t *testing.T) {
	// template(blank) provider(6=codex) dir(blank) structure(blank) start-web-ui(n)
	err := runQuickstartWithInput(t, "\n6\n\n\nn\n")
	if err != nil {
		t.Fatalf("runQuickstart returned an error: %v", err)
	}
	if scaffold.DefaultModel("codex") != "" {
		t.Fatalf("codex must not get a hardcoded default model")
	}
	if scaffold.APIKeyEnvVar("codex") != "" {
		t.Fatalf("codex must not get an API key env var")
	}
}

// TestServeCmd_DefinesRun guards the quickstart → serve hand-off.
// runQuickstart starts the web UI by calling serveCmd.Run directly. If
// serveCmd is ever switched to RunE-only, serveCmd.Run becomes nil and
// `rakitsu quickstart` segfaults on its final step.
func TestServeCmd_DefinesRun(t *testing.T) {
	if serveCmd.Run == nil {
		t.Fatal("serveCmd.Run is nil — runQuickstart invokes serveCmd.Run directly; " +
			"serve must keep a Run handler (not RunE-only) or quickstart will panic")
	}
}

// Regression: the "Start web UI now?" auto-launch used to call serveCmd.Run
// from whatever directory quickstart itself was invoked in, never the
// project directory it just created. serve's ConfigStore only scans ".",
// "./examples", "./configs" relative to its own working directory (see
// cmd/rakitsu/serve.go), so the freshly generated project was invisible in
// the web UI's config list unless the chosen project dir happened to be
// ".". Guards that the auto-launch chdirs into the project directory
// first. Stubs serveCmd.Run so no real server starts.
func TestRunQuickstart_StartWebUI_ChdirsIntoProjectDir(t *testing.T) {
	origStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = origStdin })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// template(blank) provider(4=ollama, no key/base_url prompts)
	// dir(blank) structure(blank) start-web-ui(blank=default Y)
	go func() {
		w.WriteString("\n4\n\n\n\n") //nolint:errcheck
		w.Close()
	}()
	os.Stdin = r

	baseDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(baseDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origWd) }) //nolint:errcheck

	origRun := serveCmd.Run
	var gotWd string
	serveCmd.Run = func(cmd *cobra.Command, args []string) {
		gotWd, _ = os.Getwd()
	}
	t.Cleanup(func() { serveCmd.Run = origRun })

	if err := runQuickstart(quickstartCmd, nil); err != nil {
		t.Fatalf("runQuickstart returned an error: %v", err)
	}

	wantWd, err := filepath.EvalSymlinks(filepath.Join(baseDir, "rakitsu-project"))
	if err != nil {
		t.Fatalf("resolving expected project dir: %v", err)
	}
	gotWdResolved, err := filepath.EvalSymlinks(gotWd)
	if err != nil {
		t.Fatalf("resolving serve's working dir %q: %v", gotWd, err)
	}
	if gotWdResolved != wantWd {
		t.Errorf("serve launched from %q, want project dir %q", gotWdResolved, wantWd)
	}
}

// Regression: re-running quickstart into an already-scaffolded directory
// used to overwrite existing files with no warning. existingQuickstartFiles
// is the pure detection logic behind the confirm-before-overwrite prompt.

func TestExistingQuickstartFiles_ModularDetectsConflict(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"config.yaml": "new", "agents/assistant.md": "new"}

	got := existingQuickstartFiles(files, dir, true)
	if len(got) != 1 || got[0] != filepath.Join(dir, "config.yaml") {
		t.Errorf("existingQuickstartFiles = %v, want just config.yaml flagged", got)
	}
}

func TestExistingQuickstartFiles_ModularNoConflictOnFreshDir(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"config.yaml": "new", "agents/assistant.md": "new"}

	got := existingQuickstartFiles(files, dir, true)
	if len(got) != 0 {
		t.Errorf("existingQuickstartFiles = %v, want none on a fresh directory", got)
	}
}

func TestExistingQuickstartFiles_SingleFileDetectsConflict(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got := existingQuickstartFiles(map[string]string{"llm-chat.yaml": "new"}, dir, false)
	if len(got) != 1 || got[0] != filepath.Join(dir, "config.yaml") {
		t.Errorf("existingQuickstartFiles = %v, want config.yaml flagged", got)
	}
}

// ============================================================
// shouldMaskSecretInput — buffered-reader race (readSecretLine bug)
//
// readSecretLine masks input via term.ReadPassword, which reads
// directly off the raw stdin fd — bypassing the shared bufio.Reader
// every other prompt in the wizard reads through. If the user pastes
// ahead (answers several prompts' worth of input before being asked),
// bytes for the secret answer can already be sitting in the buffered
// reader, invisible to a raw fd read: ReadPassword would then either
// hang waiting for new terminal input, or a later prompt would
// consume what was meant to be the secret. shouldMaskSecretInput is
// the pure decision extracted from readSecretLine so this can be unit
// tested without a real pty (term.IsTerminal(os.Stdin) is always false
// under `go test`, so readSecretLine's own terminal branch can't be
// exercised directly here).

func TestShouldMaskSecretInput_TrueOnATerminalWithNothingBuffered(t *testing.T) {
	if !shouldMaskSecretInput(true, 0) {
		t.Error("shouldMaskSecretInput(true, 0) = false, want true — the common case: a real terminal, nothing pasted ahead")
	}
}

func TestShouldMaskSecretInput_FalseWhenNotATerminal(t *testing.T) {
	if shouldMaskSecretInput(false, 0) {
		t.Error("shouldMaskSecretInput(false, 0) = true, want false — piped/redirected stdin has no TTY to mask against")
	}
}

func TestShouldMaskSecretInput_FalseWhenInputWasPastedAhead(t *testing.T) {
	// The exact bug: on a real terminal, but the buffered reader
	// already holds bytes (a paste-ahead) — masking here would read
	// past those bytes via the raw fd instead of consuming them,
	// hanging or misordering later answers.
	if shouldMaskSecretInput(true, 12) {
		t.Error("shouldMaskSecretInput(true, 12) = true, want false — must defer to the buffered reader when it already has pending bytes")
	}
}
