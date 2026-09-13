package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/paupawsan/rakitsu/internal/scaffold"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var quickstartCmd = &cobra.Command{
	Use:   "quickstart",
	Short: "Interactive wizard to create and run your first agent project",
	Long: `Creates a new rakitsu project with an interactive wizard.
No YAML knowledge required — pick a template, enter your API key, and run.

Steps:
  1. Pick a template (Chat Bot, Code Reviewer, Research Team, etc.)
  2. Pick a provider (OpenAI, Anthropic, Ollama, LiteLLM)
  3. Enter API key / base URL (or detect from environment)
  4. Generate project in ./rakitsu-project/
  5. Start the web UI

Example:
  rakitsu quickstart`,
	RunE: runQuickstart,
}

func init() {
	rootCmd.AddCommand(quickstartCmd)
}

// quickstartProvider describes one provider choice in the wizard: its
// display name, the scaffold provider id, and what credentials it needs
// guidance for. envVar/needKey drive the Step 3 API-key check/prompt/warn
// flow; baseURLEnvVar/needBaseURL drive the analogous step for providers
// with no fixed endpoint (LiteLLM). Ollama also needs a base_url in
// principle, but rakitsu already defaults it to http://localhost:11434/v1
// at runtime (createLLMProvider) when unset, so it's left without
// guidance here — there's nothing to warn the user about for the common
// local case.
type quickstartProvider struct {
	name          string
	id            string
	envVar        string
	needKey       bool
	baseURLEnvVar string
	needBaseURL   bool
}

var quickstartProviders = []quickstartProvider{
	{name: "OpenAI", id: "openai", envVar: "OPENAI_API_KEY", needKey: true},
	{name: "Anthropic", id: "anthropic", envVar: "ANTHROPIC_API_KEY", needKey: true},
	{name: "Google Gemini", id: "gemini", envVar: "GEMINI_API_KEY", needKey: true},
	{name: "Ollama (local)", id: "ollama"},
	{name: "LiteLLM (proxy)", id: "litellm", envVar: "LITELLM_API_KEY", needKey: true, baseURLEnvVar: "LITELLM_BASE_URL", needBaseURL: true},
	{name: "Codex (ChatGPT subscription, needs `codex login`)", id: "codex"},
}

func runQuickstart(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("  Welcome to Rakitsu — The Agent IDE")
	fmt.Println("  =====================================")
	fmt.Println()

	// Step 1: Pick template
	templates := []struct {
		name string
		id   string
		desc string
	}{
		{"Chat Bot", "llm-chat", "Simple conversational assistant"},
		{"Code Reviewer", "code-review", "Multi-agent code review pipeline"},
		{"Research Team", "web-research", "Research agent with reasoning"},
		{"Dev Team", "dev-team", "Planner → Developer → Reviewer pipeline"},
		{"Data Analysis", "data-analysis", "Data analyst with CLI + file tools"},
		{"QA Pipeline", "qa-pipeline", "Test generator → Validator pipeline"},
		{"RAG Assistant", "rag-assistant", "Search knowledge base + synthesise"},
	}

	fmt.Println("  Pick a template:")
	fmt.Println()
	for i, t := range templates {
		fmt.Printf("    %d. %-18s %s\n", i+1, t.name, t.desc)
	}
	fmt.Println()
	fmt.Print("  Enter number [1]: ")
	choice, err := mustReadLine(reader)
	if err != nil {
		return err
	}
	idx := 0
	if choice != "" {
		n, err := strconv.Atoi(strings.TrimSpace(choice))
		if err != nil || n < 1 || n > len(templates) {
			return fmt.Errorf("invalid choice: %s", choice)
		}
		idx = n - 1
	}
	tmpl := templates[idx]
	fmt.Printf("  → %s\n\n", tmpl.name)

	// Step 2: Pick provider
	providers := quickstartProviders

	fmt.Println("  Pick a provider:")
	fmt.Println()
	for i, p := range providers {
		status := ""
		if p.envVar != "" {
			if os.Getenv(p.envVar) != "" {
				status = " (key detected)"
			}
		}
		fmt.Printf("    %d. %s%s\n", i+1, p.name, status)
	}
	fmt.Println()
	fmt.Print("  Enter number [1]: ")
	pChoice, err := mustReadLine(reader)
	if err != nil {
		return err
	}
	pIdx := 0
	if pChoice != "" {
		n, err := strconv.Atoi(strings.TrimSpace(pChoice))
		if err != nil || n < 1 || n > len(providers) {
			return fmt.Errorf("invalid choice: %s", pChoice)
		}
		pIdx = n - 1
	}
	prov := providers[pIdx]
	fmt.Printf("  → %s\n\n", prov.name)

	// Step 3: API key. If already in the environment, use it as-is. If
	// not, prompt with masked input (no terminal echo) and remember the
	// value — it gets written to the generated project's .env file once
	// we know absDir (Step 4), which rakitsu auto-loads at startup
	// (internal/dotenv, wired into rootCmd.PersistentPreRunE). It is also
	// os.Setenv'd immediately so an auto-started `serve` later in this
	// same process (Step 5) has it right away, without waiting on the
	// .env round-trip.
	apiKey := ""
	apiKeyTyped := false
	if prov.needKey {
		if existing := os.Getenv(prov.envVar); existing != "" {
			fmt.Printf("  API key detected from $%s\n\n", prov.envVar)
		} else {
			fmt.Printf("  Enter %s API key: ", prov.name)
			line, err := readSecretLine(reader)
			if err != nil {
				return err
			}
			apiKey = strings.TrimSpace(line)
			if apiKey == "" {
				fmt.Printf("  Warning: no API key provided. Set $%s before running.\n\n", prov.envVar)
			} else {
				os.Setenv(prov.envVar, apiKey) //nolint:errcheck
				apiKeyTyped = true
			}
		}
	}

	// Step 3b: Base URL — same detect/prompt/persist shape as the API key
	// above, for providers with no fixed endpoint (LiteLLM).
	baseURL := ""
	baseURLTyped := false
	if prov.needBaseURL {
		if existing := os.Getenv(prov.baseURLEnvVar); existing != "" {
			fmt.Printf("  Base URL detected from $%s\n\n", prov.baseURLEnvVar)
		} else {
			fmt.Printf("  Enter %s base URL (e.g. https://your-proxy-host/v1): ", prov.name)
			line, err := mustReadLine(reader)
			if err != nil {
				return err
			}
			baseURL = strings.TrimSpace(line)
			if baseURL == "" {
				fmt.Printf("  Warning: no base URL provided. Set $%s before running.\n\n", prov.baseURLEnvVar)
			} else {
				os.Setenv(prov.baseURLEnvVar, baseURL) //nolint:errcheck
				baseURLTyped = true
			}
		}
	}

	// Step 4: Generate project
	projectDir := "rakitsu-project"
	fmt.Printf("  Project directory [%s]: ", projectDir)
	dirChoice, err := mustReadLine(reader)
	if err != nil {
		return err
	}
	if dirChoice != "" {
		projectDir = strings.TrimSpace(dirChoice)
	}

	absDir, _ := filepath.Abs(projectDir)
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return fmt.Errorf("cannot create directory: %w", err)
	}

	// Step 4b: Single file or modular?
	fmt.Print("  Project structure — modular (agents/, tools/ dirs) or single YAML? [M/s]: ")
	structLine, err := mustReadLine(reader)
	if err != nil {
		return err
	}
	structChoice := strings.ToLower(strings.TrimSpace(structLine))
	useModular := structChoice != "s" && structChoice != "single"

	// Generate config using scaffold
	preset, ok := scaffold.Presets[tmpl.id]
	if !ok {
		return fmt.Errorf("unknown template: %s", tmpl.id)
	}
	model := scaffold.DefaultModel(prov.id)
	data := scaffold.TemplateData{
		Provider:   prov.id,
		Model:      model,
		APIKeyEnv:  scaffold.APIKeyEnvVar(prov.id),
		BaseURLEnv: scaffold.BaseURLEnvVar(prov.id),
	}
	files, err := scaffold.Render(preset, data, useModular)
	if err != nil {
		return fmt.Errorf("scaffold error: %w", err)
	}

	// Step 4c: refuse to silently clobber an already-scaffolded project.
	// Re-running quickstart into an existing project dir would otherwise
	// overwrite already-edited config/agent files with no warning.
	existing := existingQuickstartFiles(files, absDir, useModular)
	if len(existing) > 0 {
		fmt.Printf("\n  %d file(s) already exist in %s and would be overwritten:\n", len(existing), absDir)
		for _, p := range existing {
			fmt.Printf("    %s\n", p)
		}
		fmt.Print("  Overwrite? [y/N]: ")
		confirmLine, err := mustReadLine(reader)
		if err != nil {
			return err
		}
		confirm := strings.ToLower(strings.TrimSpace(confirmLine))
		if confirm != "y" && confirm != "yes" {
			return fmt.Errorf("aborted: refusing to overwrite existing project files")
		}
	}

	// Write files
	var configPath string
	if useModular {
		for relPath, content := range files {
			fullPath := filepath.Join(absDir, relPath)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return fmt.Errorf("cannot create directory: %w", err)
			}
			if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("cannot write %s: %w", relPath, err)
			}
		}
		configPath = filepath.Join(absDir, "config.yaml")
		fmt.Println()
		fmt.Printf("  Project created at: %s\n", absDir)
		fmt.Printf("  Files: %d (%s)\n\n", len(files), "modular layout")
	} else {
		var yaml string
		for _, content := range files {
			yaml = content
			break
		}
		configPath = filepath.Join(absDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
			return fmt.Errorf("cannot write config: %w", err)
		}
		fmt.Println()
		fmt.Printf("  Project created at: %s\n", absDir)
		fmt.Printf("  Config: %s\n\n", configPath)
	}

	// Step 4d: persist any freshly typed key/base_url into a project-local
	// .env, which rakitsu loads automatically at startup (internal/dotenv
	// via rootCmd.PersistentPreRunE). This is what makes a typed value
	// actually useful beyond this one process — without it, a later
	// `rakitsu run` from a new terminal would have nothing to read.
	if apiKeyTyped || baseURLTyped {
		if err := writeQuickstartEnv(absDir, prov, apiKey, apiKeyTyped, baseURL, baseURLTyped); err != nil {
			return fmt.Errorf("cannot write .env: %w", err)
		}
		fmt.Printf("  Saved to %s (gitignored, loaded automatically by rakitsu)\n\n", filepath.Join(absDir, ".env"))
	}

	// Step 5: Start serve
	fmt.Print("  Start web UI now? [Y/n]: ")
	startLine, err := mustReadLine(reader)
	if err != nil {
		return err
	}
	startChoice := strings.ToLower(strings.TrimSpace(startLine))
	if startChoice != "n" && startChoice != "no" {
		fmt.Println()
		fmt.Println("  Starting Rakitsu web UI...")
		fmt.Println("  Open http://localhost:9100 in your browser")
		fmt.Println()

		// serve's ConfigStore only scans ".", "./examples", "./configs"
		// relative to the process's working directory, and generated
		// configs use allowed_paths like "./" (resolved against cwd too).
		// Without this chdir, serve keeps running from wherever quickstart
		// was invoked, so the project just created in absDir is invisible
		// in the web UI's config list.
		if err := os.Chdir(absDir); err != nil {
			return fmt.Errorf("cannot switch to project directory: %w", err)
		}

		// Try to open browser
		go openBrowser("http://localhost:9100")

		// Run serve with the project directory. serveCmd is defined with
		// Run (not RunE) — invoke that directly; calling the nil RunE
		// segfaults.
		serveCmd.Flags().Set("port", "9100") //nolint:errcheck
		os.Args = []string{"rakitsu", "serve"}
		serveCmd.Run(serveCmd, []string{})
		return nil
	}

	fmt.Println()
	fmt.Println("  To start later, run:")
	fmt.Printf("    cd %s && rakitsu serve\n", projectDir)
	fmt.Println()
	return nil
}

// readLine reads one line from stdin, trimming the trailing line ending.
// ok is false only when stdin was already at EOF with nothing left to read
// — a real terminal never produces that (Enter always terminates a line),
// so it means quickstart is being driven non-interactively (piped/
// redirected/closed stdin) and the caller must abort rather than silently
// substituting every remaining prompt's bracketed default.
func readLine(reader *bufio.Reader) (line string, ok bool) {
	s, err := reader.ReadString('\n')
	if s == "" && err != nil {
		return "", false
	}
	return strings.TrimRight(s, "\r\n"), true
}

// errNonInteractive is returned by mustReadLine when stdin runs out
// mid-wizard. Its bracketed-default prompts (Overwrite? [y/N], Start web
// UI now? [Y/n]) must never be silently answered by an absent terminal.
var errNonInteractive = errors.New("quickstart needs an interactive terminal — stdin closed with more input expected; run it from a real shell, or use 'rakitsu scaffold <use-case>' for non-interactive project generation")

// mustReadLine wraps readLine for call sites that cannot proceed on EOF.
func mustReadLine(reader *bufio.Reader) (string, error) {
	line, ok := readLine(reader)
	if !ok {
		return "", errNonInteractive
	}
	return line, nil
}

// readSecretLine reads one line of sensitive input (an API key) without
// echoing it to the terminal, so it can't be shoulder-surfed or left
// sitting in terminal scrollback. It only does so when stdin is actually
// an interactive terminal (term.IsTerminal) — a piped/redirected stdin
// (tests, scripted input) has no TTY to mask against, so it falls back to
// the plain reader in that case.
func readSecretLine(reader *bufio.Reader) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println() // ReadPassword swallows the Enter keypress's newline
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return mustReadLine(reader)
}

// writeQuickstartEnv persists a freshly typed API key/base_url to
// <absDir>/.env (KEY=VALUE lines, mode 0600 — readable only by the current
// user) and ensures <absDir>/.gitignore excludes it, so the secret is
// never accidentally committed. Only called when at least one value was
// actually typed (never for values already detected from the environment
// — those are already available everywhere and don't need duplicating).
func writeQuickstartEnv(absDir string, prov quickstartProvider, apiKey string, apiKeyTyped bool, baseURL string, baseURLTyped bool) error {
	var b strings.Builder
	if apiKeyTyped {
		fmt.Fprintf(&b, "%s=%s\n", prov.envVar, apiKey)
	}
	if baseURLTyped {
		fmt.Fprintf(&b, "%s=%s\n", prov.baseURLEnvVar, baseURL)
	}
	if err := os.WriteFile(filepath.Join(absDir, ".env"), []byte(b.String()), 0600); err != nil {
		return err
	}
	return ensureGitignoreHasEnv(absDir)
}

// ensureGitignoreHasEnv appends a ".env" entry to <absDir>/.gitignore,
// creating the file if it doesn't exist yet and leaving it untouched if
// ".env" is already listed (e.g. quickstart run twice into the same dir).
func ensureGitignoreHasEnv(absDir string) error {
	path := filepath.Join(absDir, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == ".env" {
			return nil
		}
	}
	content := string(existing)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += ".env\n"
	return os.WriteFile(path, []byte(content), 0644)
}

// existingQuickstartFiles returns the absolute paths, under absDir, of any
// rendered scaffold file that already exists on disk. Used to warn before
// a quickstart re-run silently overwrites an already-scaffolded project.
func existingQuickstartFiles(files map[string]string, absDir string, useModular bool) []string {
	var existing []string
	if useModular {
		for relPath := range files {
			if _, err := os.Stat(filepath.Join(absDir, relPath)); err == nil {
				existing = append(existing, filepath.Join(absDir, relPath))
			}
		}
	} else if _, err := os.Stat(filepath.Join(absDir, "config.yaml")); err == nil {
		existing = append(existing, filepath.Join(absDir, "config.yaml"))
	}
	return existing
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Run()
}
