// Package main — `rakitsu doctor` subcommand.
//
// Diagnoses common silent-misconfiguration failures by exercising the same
// loading path `rakitsu run` would use, plus reachability + catalog probes
// against declared providers.
//
// Checks:
//   - Provider reachability + auth (curl /v1/models with declared key/url)
//   - Model-in-catalog (each agent.model must appear in its provider's listing)
//   - Env var resolution (which ${VAR} references the config makes; which
//     are set, unset, or empty)
//
// Exit codes:
//
//	0   all checks passed
//	1   one or more warnings (config will likely run but with degraded behavior)
//	2   one or more errors (config will likely fail to run)
//
// Out of scope for MVP (queued as follow-ups):
//   - Effective-config layer dump (top-N knobs × 5 layers)
//   - Timeout coordination report
//   - Workspace permissions check
//   - Tool sandbox sanity check
//   - Recent session health survey
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/llm"
	codexProvider "github.com/SK-ENT/rakitsu/internal/llm/codex"
)

// Severity controls the symbol in front of each report line and contributes
// to the final exit code.
type severity int

const (
	sevOK severity = iota
	sevInfo
	sevWarn
	sevErr
)

type finding struct {
	sev   severity
	label string // short tag, e.g. "Provider reachability"
	subj  string // subject, e.g. "litellm"
	msg   string // free-form explanation; may include `→ suggestion`
}

func (f finding) String() string {
	var sym, color string
	switch f.sev {
	case sevOK:
		sym, color = "[OK]   ", colorGreen
	case sevInfo:
		sym, color = "[INFO] ", colorCyan
	case sevWarn:
		sym, color = "[WARN] ", colorYellow
	case sevErr:
		sym, color = "[ERR]  ", colorRed
	}
	if useColor {
		sym = color + sym + colorReset
	}
	if f.subj != "" {
		return fmt.Sprintf("%s%-26s %s: %s", sym, f.label, f.subj, f.msg)
	}
	return fmt.Sprintf("%s%-26s %s", sym, f.label, f.msg)
}

// Color codes for the doctor output. Disabled by default; enabled when stdout
// is a TTY and NO_COLOR is unset.
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
)

var useColor bool

func init() {
	useColor = isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == ""
	rootCmd.AddCommand(doctorCmd)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

var doctorCmd = &cobra.Command{
	Use:   "doctor [config.yaml]",
	Short: "Diagnose common rakitsu misconfiguration failures",
	Long: `Diagnose common rakitsu misconfiguration failures.

doctor exercises the same config-loading path 'rakitsu run' would use and
probes each declared provider for reachability + model catalog membership.
It catches silent failures that would otherwise show up as confusing
agent-iteration errors midway through a run.

Checks performed (MVP):
  - Provider reachability + auth
  - Model in provider catalog
  - Env var resolution (${VAR} references in YAML)

Exit codes:
  0   all checks passed
  1   one or more warnings
  2   one or more errors

If config path is omitted, ./agent.yaml is used.`,
	Example: `  rakitsu doctor examples/single/02-single-agent/config.yaml
  rakitsu doctor                            # uses ./agent.yaml
  rakitsu doctor --json config.yaml         # machine-readable output`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDoctor,
}

var doctorJSON bool

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Emit findings as JSON instead of pretty text")
}

func runDoctor(cmd *cobra.Command, args []string) error {
	configPath := "agent.yaml"
	if len(args) == 1 {
		configPath = args[0]
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	var findings []finding

	// Step 1 — raw YAML read for env-var-reference detection. Must happen
	// before config.Load() because Load() resolves the references and we
	// want to report on the raw shape.
	raw, err := os.ReadFile(abs)
	if err != nil {
		findings = append(findings, finding{
			sev:   sevErr,
			label: "Config file",
			subj:  abs,
			msg:   fmt.Sprintf("cannot read: %v", err),
		})
		return emit(findings)
	}
	findings = append(findings, finding{
		sev:   sevOK,
		label: "Config file",
		subj:  abs,
		msg:   fmt.Sprintf("%d bytes", len(raw)),
	})

	// Step 2 — env var resolution check.
	findings = append(findings, checkEnvVarReferences(raw)...)

	// Step 3 — load config through the real loader so we honor the same
	// precedence and validation rules `rakitsu run` does.
	cfg, err := config.Load(abs)
	if err != nil {
		findings = append(findings, finding{
			sev:   sevErr,
			label: "Config parse",
			subj:  filepath.Base(abs),
			msg:   err.Error(),
		})
		return emit(findings)
	}
	findings = append(findings, finding{
		sev:   sevOK,
		label: "Config parse",
		subj:  filepath.Base(abs),
		msg:   fmt.Sprintf("%d agents, %d tools, default_provider=%q", len(cfg.Agents), len(cfg.Tools), cfg.Settings.DefaultProvider),
	})

	// Step 4 — provider reachability + auth.
	ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
	defer cancel()
	reach := checkProviderReachability(ctx, cfg)
	findings = append(findings, reach.findings...)

	// Step 5 — model in catalog.
	findings = append(findings, checkModelsInCatalog(cfg, reach.catalogs)...)

	return emit(findings)
}

// ============================================================
// Check 1: Env var references
// ============================================================

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-[^}]*)?\}`)

func checkEnvVarReferences(raw []byte) []finding {
	matches := envRefRe.FindAllSubmatch(raw, -1)
	if len(matches) == 0 {
		return []finding{{
			sev:   sevInfo,
			label: "Env var refs",
			msg:   "config makes no ${VAR} references",
		}}
	}
	// Dedup names; track first-seen order so output is stable.
	seen := make(map[string]bool)
	var names []string
	for _, m := range matches {
		name := string(m[1])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	var out []finding
	var unset, empty, set []string
	for _, n := range names {
		v, ok := os.LookupEnv(n)
		switch {
		case !ok:
			unset = append(unset, n)
		case v == "":
			empty = append(empty, n)
		default:
			set = append(set, n)
		}
	}
	if len(set) > 0 {
		out = append(out, finding{
			sev:   sevOK,
			label: "Env var refs (set)",
			msg:   strings.Join(set, ", "),
		})
	}
	if len(empty) > 0 {
		out = append(out, finding{
			sev:   sevWarn,
			label: "Env var refs (empty)",
			msg:   strings.Join(empty, ", ") + " → set in .env or shell before run",
		})
	}
	if len(unset) > 0 {
		out = append(out, finding{
			sev:   sevErr,
			label: "Env var refs (unset)",
			msg:   strings.Join(unset, ", ") + " → set in .env or shell; values will resolve to empty otherwise",
		})
	}
	return out
}

// ============================================================
// Check 2: Provider reachability + auth
// ============================================================

type reachabilityResult struct {
	findings []finding
	// catalogs[providerName] = list of model IDs from /v1/models
	catalogs map[string][]string
}

func checkProviderReachability(ctx context.Context, cfg *config.Config) reachabilityResult {
	res := reachabilityResult{catalogs: make(map[string][]string)}
	for name, p := range cfg.Settings.Providers {
		// Only probe OpenAI-compatible endpoints (litellm, ollama, openai,
		// nvidia-via-openai). Native Anthropic + Gemini use different model-
		// list shapes; deferred to a follow-up.
		ptype := strings.ToLower(p.Type)
		if ptype == "codex" {
			f, slugs := checkCodex(ctx, name, p)
			res.findings = append(res.findings, f...)
			if len(slugs) > 0 {
				res.catalogs[name] = slugs
			}
			continue
		}
		if ptype != "openai" && ptype != "litellm" && ptype != "ollama" && ptype != "" {
			res.findings = append(res.findings, finding{
				sev:   sevInfo,
				label: "Provider type",
				subj:  name,
				msg:   fmt.Sprintf("type=%q; reachability/catalog checks skipped (only OpenAI-compatible probed in MVP)", p.Type),
			})
			continue
		}
		baseURL := strings.TrimRight(p.BaseURL, "/")
		if baseURL == "" {
			res.findings = append(res.findings, finding{
				sev:   sevWarn,
				label: "Provider reachability",
				subj:  name,
				msg:   "no base_url set; assuming compiled default — skipping live probe",
			})
			continue
		}
		// /v1/models is the OpenAI-spec catalog endpoint.
		modelsURL := baseURL + "/models"
		if !strings.HasSuffix(baseURL, "/v1") {
			modelsURL = baseURL + "/v1/models"
		}
		req, err := http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
		if err != nil {
			res.findings = append(res.findings, finding{
				sev:   sevErr,
				label: "Provider reachability",
				subj:  name,
				msg:   fmt.Sprintf("build request failed: %v", err),
			})
			continue
		}
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
		client := &http.Client{
			Timeout: 8 * time.Second,
			// This request carries a real provider API key. Don't follow
			// redirects — the default policy can resend the Authorization
			// header to a redirect target, which for a diagnostic probe
			// against a user-configured base_url risks sending the key
			// somewhere unintended.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			res.findings = append(res.findings, finding{
				sev:   sevErr,
				label: "Provider reachability",
				subj:  name,
				msg:   fmt.Sprintf("%s unreachable: %v → check base_url, network, proxy", modelsURL, err),
			})
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			res.findings = append(res.findings, finding{
				sev:   sevErr,
				label: "Provider auth",
				subj:  name,
				msg:   fmt.Sprintf("HTTP %d from %s → check api_key value or env var resolution", resp.StatusCode, modelsURL),
			})
			continue
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			// client.CheckRedirect above stops here instead of following —
			// report it plainly instead of falling through to catalog
			// parsing, which would otherwise fail with a confusing
			// "unexpected end of JSON input" on the redirect body.
			res.findings = append(res.findings, finding{
				sev:   sevErr,
				label: "Provider reachability",
				subj:  name,
				msg: fmt.Sprintf(
					"HTTP %d redirect from %s; redirects disabled to protect API key",
					resp.StatusCode, modelsURL,
				),
			})
			continue
		case resp.StatusCode >= 400:
			res.findings = append(res.findings, finding{
				sev:   sevErr,
				label: "Provider reachability",
				subj:  name,
				msg:   fmt.Sprintf("HTTP %d from %s → %s", resp.StatusCode, modelsURL, snippet(body, 120)),
			})
			continue
		}
		// 2xx — parse catalog.
		var catalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &catalog); err != nil {
			res.findings = append(res.findings, finding{
				sev:   sevWarn,
				label: "Provider catalog",
				subj:  name,
				msg:   fmt.Sprintf("parse: %v → catalog check disabled for this provider", err),
			})
			continue
		}
		ids := make([]string, 0, len(catalog.Data))
		for _, m := range catalog.Data {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		res.catalogs[name] = ids
		res.findings = append(res.findings, finding{
			sev:   sevOK,
			label: "Provider reachability",
			subj:  name,
			msg:   fmt.Sprintf("%s OK, %d models", modelsURL, len(ids)),
		})
	}
	return res
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ============================================================
// Check 3: Model in catalog
// ============================================================

func checkModelsInCatalog(cfg *config.Config, catalogs map[string][]string) []finding {
	var out []finding
	for _, ag := range cfg.Agents {
		model := ag.Model
		if model == "" {
			model = cfg.Settings.Defaults.Model
		}
		provider := ag.Provider
		if provider == "" {
			provider = cfg.Settings.DefaultProvider
		}
		if model == "" {
			out = append(out, finding{
				sev:   sevWarn,
				label: "Agent model",
				subj:  ag.Name,
				msg:   "no model set on agent or in defaults; will rely on provider's compiled default",
			})
			continue
		}
		// Case-insensitive lookup since Viper lowercases keys.
		var matchedProvider string
		for pname := range catalogs {
			if strings.EqualFold(pname, provider) {
				matchedProvider = pname
				break
			}
		}
		if matchedProvider == "" {
			// Either non-OpenAI-compatible (catalog not probed) or unknown
			// provider name. Don't pretend to verify.
			out = append(out, finding{
				sev:   sevInfo,
				label: "Agent model",
				subj:  ag.Name,
				msg:   fmt.Sprintf("model=%q provider=%q — catalog not probed (skipped)", model, provider),
			})
			continue
		}
		ids := catalogs[matchedProvider]
		if containsCaseInsensitive(ids, model) {
			out = append(out, finding{
				sev:   sevOK,
				label: "Agent model",
				subj:  ag.Name,
				msg:   fmt.Sprintf("%q in %s catalog", model, matchedProvider),
			})
		} else {
			// Show up to 5 nearest model names from the catalog for hint.
			hint := ""
			if len(ids) > 0 {
				show := ids
				if len(show) > 5 {
					show = show[:5]
				}
				hint = " → available: " + strings.Join(show, ", ")
				if len(ids) > 5 {
					hint += fmt.Sprintf(" (+%d more)", len(ids)-5)
				}
			}
			out = append(out, finding{
				sev:   sevErr,
				label: "Agent model",
				subj:  ag.Name,
				msg:   fmt.Sprintf("%q NOT in %s catalog%s", model, matchedProvider, hint),
			})
		}
	}
	return out
}

func containsCaseInsensitive(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// ============================================================
// Emit + exit code
// ============================================================

func emit(findings []finding) error {
	if doctorJSON {
		type jsonFinding struct {
			Severity string `json:"severity"`
			Label    string `json:"label"`
			Subject  string `json:"subject,omitempty"`
			Message  string `json:"message"`
		}
		out := make([]jsonFinding, 0, len(findings))
		sevName := map[severity]string{sevOK: "ok", sevInfo: "info", sevWarn: "warn", sevErr: "error"}
		for _, f := range findings {
			out = append(out, jsonFinding{
				Severity: sevName[f.sev],
				Label:    f.label,
				Subject:  f.subj,
				Message:  f.msg,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		for _, f := range findings {
			fmt.Println(f.String())
		}
	}
	exitCode := exitCodeFor(findings)
	if exitCode != 0 {
		// A non-zero exit is wanted even on "warning" cases, but os.Exit here
		// would bypass Cobra/defer cleanup and make doctor impossible to use
		// programmatically (embedded usage, or a test exercising the
		// warning/error paths would kill the test process itself). Report it
		// as a typed error instead — main() unwraps it to pick the exit code.
		return doctorExitError{Code: exitCode}
	}
	return nil
}

// doctorExitError carries the process exit code emit() wants without
// calling os.Exit itself; main() unwraps it to pick the code. Error() stays
// short — emit() already printed the findings in full (JSON or pretty
// text) — but it can't be empty: Cobra still prints "Error: <msg>" for any
// RunE error unless SilenceErrors is set, and that isn't an option here —
// runDoctor also returns plain errors (e.g. a bad config path) on paths
// that never go through emit() and have no other message printed, so
// silencing errors on this command would make those fail silently instead.
type doctorExitError struct{ Code int }

func (e doctorExitError) Error() string {
	return fmt.Sprintf("doctor found issues (exit %d) — see findings above", e.Code)
}

func exitCodeFor(findings []finding) int {
	maxSev := sevOK
	for _, f := range findings {
		if f.sev > maxSev {
			maxSev = f.sev
		}
	}
	switch maxSev {
	case sevErr:
		return 2
	case sevWarn:
		return 1
	default:
		return 0
	}
}

// checkCodex verifies the codex provider end to end: the Codex CLI login
// file exists, the subscription backend answers with a model catalog (which
// also proves the token is valid or refreshable), and the model the
// provider will actually use is in that catalog. Returns the catalog slugs
// so the per-agent model check can use them too.
func checkCodex(ctx context.Context, name string, p config.ProviderDefinition) ([]finding, []string) {
	path := p.CredentialsFile
	if path == "" {
		path = codexProvider.DefaultAuthFile
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if _, err := os.Stat(path); err != nil {
		return []finding{{sev: sevErr, label: "Codex login", subj: name, msg: fmt.Sprintf("%s not found — run \"codex login\" first", path)}}, nil
	}
	out := []finding{{sev: sevOK, label: "Codex login", subj: name, msg: "auth file present at " + path}}

	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := codexProvider.ListModels(probeCtx, &llm.ProviderConfig{CredentialsFile: p.CredentialsFile, BaseURL: p.BaseURL})
	if err != nil {
		out = append(out, finding{sev: sevErr, label: "Codex catalog", subj: name, msg: err.Error()})
		return out, nil
	}
	slugs := codexProvider.ListedSlugs(models)
	out = append(out, finding{sev: sevOK, label: "Codex catalog", subj: name, msg: fmt.Sprintf("%d models available: %s", len(slugs), strings.Join(slugs, ", "))})

	model := p.DefaultModel
	source := "default_model"
	if model == "" {
		model = codexProvider.ConfigTomlModel(path)
		source = "~/.codex/config.toml"
	}
	switch {
	case model == "":
		out = append(out, finding{sev: sevWarn, label: "Codex model", subj: name, msg: "no model: set default_model on the provider or `model` in ~/.codex/config.toml"})
	case containsCaseInsensitive(slugs, model):
		out = append(out, finding{sev: sevOK, label: "Codex model", subj: name, msg: fmt.Sprintf("%q (from %s) is in the catalog", model, source)})
	default:
		out = append(out, finding{sev: sevErr, label: "Codex model", subj: name, msg: fmt.Sprintf("%q (from %s) is not in the catalog", model, source)})
	}
	return out, slugs
}
