// Package cli provides a CLI tool implementation with security sandboxing.
// It executes shell commands with multiple layers of security.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/SK-ENT/rakitsu/internal/config"
)

// System-level whitelist (hard-coded for security)
// These are the ONLY commands allowed to be executed
var systemWhitelist = map[string]bool{
	// File operations (safe)
	"ls":   true,
	"cat":  true,
	"grep": true,
	"find": true,
	"head": true,
	"tail": true,
	"wc":   true,
	"sort": true,
	"uniq": true,
	"diff": true,
	"tree": true,

	// Development tools
	"git":     true,
	"npm":     true,
	"node":    true,
	"go":      true,
	"python3": true,
	"pip3":    true,

	// Kubernetes
	"kubectl": true,
	"helm":    true,

	// Docker (limited)
	"docker": true,

	// Shell wrappers — needed for command templates of the form
	// `sh -c 'git {{args}}'`. The shell payload is linted against
	// blockedCommands at buildCommand time (see lintShellPayload).
	// This lint is best-effort, not a security boundary — rakitsu
	// runs as the local user and ultimately trusts its config.
	"sh":   true,
	"bash": true,
}

// Blocked commands (never allowed, even if in whitelist)
// These are explicitly blocked for safety
var blockedCommands = map[string]bool{
	"rm":       true,
	"rmdir":    true,
	"sudo":     true,
	"su":       true,
	"chmod":    true,
	"chown":    true,
	"chgrp":    true,
	"dd":       true,
	"mkfs":     true,
	"fdisk":    true,
	"shutdown": true,
	"reboot":   true,
	"halt":     true,
	"init":     true,
	"kill":     true,
	"killall":  true,
	"pkill":    true,
	"mv":       true, // Can be destructive
	"cp":       true, // Can overwrite files
}

// sensitiveServerEnvVars are rakitsu-serve control-plane secrets that must
// never reach a spawned tool subprocess. exec.Cmd inherits the parent's full
// environment when Env is left nil, which would otherwise hand these tokens
// to any python3/bash/node/etc. tool call an agent makes — letting a run
// started WITH the correct API token read that same token back out of its
// own environment and exfiltrate it. Names must match internal/server's
// apiTokenEnv (auth.go) and sessionMsgTokenEnv (session_message.go);
// duplicated here rather than imported to keep this package independent of
// internal/server.
var sensitiveServerEnvVars = map[string]bool{
	"RAKITSU_API_TOKEN":         true,
	"RAKITSU_SESSION_MSG_TOKEN": true,
}

// scrubbedEnviron returns os.Environ() with sensitiveServerEnvVars removed.
func scrubbedEnviron() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if sensitiveServerEnvVars[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// DefaultMaxOutputBytes is the default cap on tool output when the config
// does not specify one. Picked to be generous enough for typical `git log`
// and `grep` output but small enough to keep a single tool call from
// blowing out the model's context window. Regression: B15 (2026-04-05),
// where an unbounded `git log` returned 205KB in one call on the weekly
// dogfood run and consumed the entire context.
const DefaultMaxOutputBytes = 8192

// truncateOutput caps s at max bytes. If max <= 0, no truncation is
// applied (caller opted out with -1). When truncation happens, the
// returned string ends with a `... [truncated N bytes, original M]`
// marker so the agent can see data was elided. Truncation prefers a
// clean line boundary near the cap when one exists within the last
// 200 bytes, falling back to a hard byte cut otherwise.
func truncateOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	// Prefer a line boundary within the last 200 bytes of the cap so we
	// don't slice mid-line.
	if lb := strings.LastIndexByte(s[:cut], '\n'); lb >= 0 && cut-lb < 200 {
		cut = lb
	}
	elided := len(s) - cut
	return s[:cut] + fmt.Sprintf("\n... [truncated %d bytes, original %d bytes]", elided, len(s))
}

// Tool implements the Tool interface for CLI commands with security sandboxing
type Tool struct {
	name          string
	description   string
	command       []string // Base command (e.g., ["kubectl", "get"])
	parameters    map[string]config.Parameter
	sandbox       *config.SandboxConfig
	workingDir    string          // Working directory for command execution
	userWhitelist map[string]bool // User-configured allowed commands
}

// NewTool creates a new CLI tool from configuration.
// allowedCommands is an optional list of user-configured commands to allow
// beyond the built-in system whitelist.
// splitCommand parses a command template string into argv parts, respecting
// single-quoted sections so that patterns like `sh -c 'git {{args}}'` produce
// three parts (sh, -c, `git {{args}}`) instead of four.
//
// This is a minimal shell-aware splitter, not a full shell lexer:
//   - Whitespace separates tokens outside quotes.
//   - Single quotes (') delimit a literal section; its contents (including
//     any double quotes and whitespace) are preserved as one token.
//   - Double quotes are NOT treated specially — they pass through as
//     literal characters. This is deliberate: the common pattern is
//     `sh -c 'git log --since="7 days ago"'`, where the double quotes
//     belong to the payload, not the outer template.
//   - Backslash escapes are not supported. Keep command templates simple.
//
// Regression: B20 (2026-04-05) — the previous implementation used
// strings.Fields which whitespace-split `"sh -c 'git {{args}}'"` into
// `["sh", "-c", "'git", "{{args}}'"]`, breaking every shell-wrapped
// command template in the repo.
func splitCommand(s string) []string {
	var parts []string
	var cur strings.Builder
	inSingle := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inSingle:
			inSingle = true
		case ch == '\'' && inSingle:
			inSingle = false
		case ch == ' ' || ch == '\t':
			if inSingle {
				cur.WriteByte(ch)
			} else if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// reExecCommands re-invoke a later word in their own segment as the actual
// command to run, unlike `git mv`/`git init`, where the later word is git's
// own subcommand vocabulary and never executes as a standalone command. A
// blocked command hiding behind one of these must still be caught even
// though it isn't the segment's first word.
var reExecCommands = map[string]bool{
	"xargs": true,
	"find":  true, // via -exec
	"env":   true,
}

// lintShellPayload does a best-effort scan of a shell -c payload for
// invocations of commands in blockedCommands. It checks only the FIRST word
// of each command segment (so `--grep "arm64"` doesn't trip on the "rm"
// substring, and `git init`/`git mv` don't trip on git's own subcommand
// sharing a name with a blocked command — a later word in a segment is
// normally an argument, not an invocation) — except for reExecCommands,
// where every word in the segment is checked, since those genuinely
// re-invoke a later word as a command.
// Returns a non-nil error describing the first blocked command found.
//
// Security note: this is a footgun fence, NOT a security boundary. A
// determined attacker with config-write access can bypass it many ways
// (env vars, command substitution, unusual quoting). It exists to catch
// obvious mistakes, not to defend against hostile configs. Rakitsu
// ultimately runs as the local user with the user's trust.
func lintShellPayload(payload string) error {
	// Split into command segments wherever a shell command-separator
	// appears (`;`, `&&`, `||`, `|`, a subshell/backtick boundary, or a
	// newline). Redirect operators (`<`, `>`) are deliberately NOT
	// segment separators: their target is a filename argument, never a
	// command, so `echo hi > rm` must not flag "rm".
	segSep := func(r rune) bool {
		switch r {
		case ';', '|', '&', '(', ')', '`', '\n':
			return true
		}
		return false
	}
	for _, segment := range strings.FieldsFunc(payload, segSep) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		// Only the first word of a segment is normally a command
		// invocation; strip a leading "$" artifact from command
		// substitution ($cmd).
		tok := strings.TrimLeft(fields[0], "$")
		if blockedCommands[tok] {
			return fmt.Errorf("shell payload contains blocked command %q", tok)
		}
		if reExecCommands[tok] {
			for _, w := range fields[1:] {
				w = strings.TrimLeft(w, "$")
				if blockedCommands[w] {
					return fmt.Errorf("shell payload contains blocked command %q", w)
				}
			}
		}
	}
	return nil
}

func NewTool(def *config.ToolDefinition, allowedCommands ...[]string) *Tool {
	// Parse command into parts using shell-aware splitting so that
	// templates like `sh -c 'git {{args}}'` parse to 3 tokens, not 4.
	// See splitCommand() comment for the B20 regression details.
	var cmdParts []string
	if def.Command != "" {
		cmdParts = splitCommand(def.Command)
	}

	// Default sandbox config. Copy the caller's struct so normalizing the
	// type here never mutates the loaded config.
	sandbox := &config.SandboxConfig{Type: "local_restricted"}
	if def.Sandbox != nil {
		copied := *def.Sandbox
		sandbox = &copied
		if sandbox.Type == "" {
			sandbox.Type = "local_restricted"
		}
	}

	// Build user whitelist map
	userWL := map[string]bool{}
	if len(allowedCommands) > 0 && allowedCommands[0] != nil {
		for _, cmd := range allowedCommands[0] {
			userWL[cmd] = true
		}
	}

	return &Tool{
		name:          def.Name,
		description:   def.Description,
		command:       cmdParts,
		parameters:    def.Parameters,
		sandbox:       sandbox,
		workingDir:    def.WorkingDir,
		userWhitelist: userWL,
	}
}

// GetName returns the tool's name
func (t *Tool) GetName() string {
	return t.name
}

// GetDescription returns the tool's description
func (t *Tool) GetDescription() string {
	return t.description
}

// GetParametersSchema returns the JSON Schema for parameters
func (t *Tool) GetParametersSchema() map[string]interface{} {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": make(map[string]interface{}),
		"required":   []string{},
	}

	props := schema["properties"].(map[string]interface{})
	required := []string{}

	for name, param := range t.parameters {
		prop := map[string]interface{}{
			"type":        param.Type,
			"description": param.Description,
		}
		if param.Default != nil {
			prop["default"] = param.Default
		}
		if len(param.Enum) > 0 {
			prop["enum"] = param.Enum
		}
		props[name] = prop

		if param.Required {
			required = append(required, name)
		}
	}

	schema["required"] = required
	return schema
}

// Execute runs the CLI command with security sandboxing
func (t *Tool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	// 1. Build full command with arguments
	fullCmd, err := t.buildCommand(args)
	if err != nil {
		return "", err
	}

	// 2. Validate against whitelist
	if !t.isCommandAllowed(fullCmd[0]) {
		return "", &SecurityError{
			Command: fullCmd[0],
			Reason:  "command not in system whitelist",
		}
	}

	// 3. Refuse a sandbox whose settings would silently lose their meaning.
	// Config loading already checks this; tools built directly do not go
	// through it.
	if err := config.ValidateSandbox(t.sandbox); err != nil {
		return "", &SecurityError{Command: fullCmd[0], Reason: err.Error()}
	}

	// 4. Execute based on sandbox type. Fail closed: an unknown type used to
	// fall through to host execution, the least isolated mode.
	switch t.sandbox.Type {
	case "docker":
		return t.executeInDocker(ctx, fullCmd)
	case "local_restricted":
		return t.executeLocalRestricted(ctx, fullCmd)
	default:
		return "", &SecurityError{
			Command: fullCmd[0],
			Reason:  fmt.Sprintf("unknown sandbox type %q (use local_restricted or docker)", t.sandbox.Type),
		}
	}
}

// maxPlaceholderResolutionPasses bounds how many times resolvePlaceholders
// re-scans a string for further {{...}} patterns. Without a bound, a
// self-referential value — e.g. a caller passing the literal string
// "{{name}}" as the VALUE of parameter "name" — never changes on
// replacement, so the re-scan condition never goes false: an unbounded hang
// reachable via any caller/agent-controlled tool argument, not just a
// config-authoring mistake. Found by review on SK-ENT/rakitsu#75 and
// verified independently (both the pre-existing per-part loop this
// replaces and the new resolvePlaceholders helper hung on this input). 32
// is far beyond any realistic nesting depth for this feature.
const maxPlaceholderResolutionPasses = 32

// maxPlaceholderResolutionLength bounds how large s may grow during
// resolvePlaceholders. The pass bound above stops an infinite loop but not
// an EXPANDING one: a value like {{name}} = "{{name}}{{name}}" doubles the
// number of matched placeholders every pass (strings.Replace(-1) replaces
// every occurrence at once), so within maxPlaceholderResolutionPasses the
// string would balloon past any reasonable size long before the pass bound
// is reached — a caller-controlled memory-exhaustion DoS, not just a hang.
// Checked as a PROJECTED size before each replace, so the oversized string
// is never actually allocated. 64 KiB is far beyond any realistic argv
// value for a cli tool. Found by review on SK-ENT/rakitsu#75, verified
// independently with a timeout-guarded reproduction (didn't finish in 5s).
const maxPlaceholderResolutionLength = 1 << 16

// resolvePlaceholders repeatedly substitutes every {{name}} in s that has a
// matching entry in args, re-scanning the result after each substitution —
// so a value that itself contains {{other_param}} syntax gets resolved too,
// not just the first level (also used for buildCommand's own per-part
// resolution below, replacing what used to be separate, duplicated inline
// logic there). Stops as soon as it hits a {{...}} with no matching entry
// in args, leaving it literal, so a template variable the caller forgot to
// pass doesn't cause a hang. Bounded by maxPlaceholderResolutionPasses and
// maxPlaceholderResolutionLength as independent safeguards against a
// self-referential, expanding, or otherwise non-terminating/unbounded
// value — either bound leaves the rest of s unresolved rather than erroring,
// matching the existing "unrecognized placeholder" graceful-degradation
// behavior.
//
// Replaces the exact matched substring — start..end+2, preserving any
// internal whitespace like "{{ name }}" — rather than a reconstructed
// "{{"+trimmed-name+"}}". Reconstructing it can search for a string that
// never actually occurs when the placeholder has internal whitespace
// (`{{ name }}` written with spaces), making the replacement a silent no-op
// and hanging regardless of the iteration bound's intent — also found by
// the same review, also verified independently.
// referencesPlaceholder reports whether part contains a {{...}} placeholder
// whose TRIMMED name equals name — used by buildCommand to skip appending a
// parameter a second time as --name=val when it was already substituted
// into the command template. Scans for each {{...}} occurrence the same way
// resolvePlaceholders does, rather than searching for the literal substring
// "{{"+name+"}}", so a placeholder written with internal whitespace (e.g.
// {{ name }}) is still recognized as referencing the same parameter. Found
// by review while preparing this fix for its public port: a command like
// `sh -c 'echo {{ name }}'` correctly substitutes "name" via
// resolvePlaceholders' raw-match resolution, but the literal-substring
// check couldn't see it was already consumed, so "name" landed twice —
// once in the substituted script, once again as a redundant --name=val.
func referencesPlaceholder(part, name string) bool {
	for {
		start := strings.Index(part, "{{")
		if start < 0 {
			return false
		}
		relEnd := strings.Index(part[start:], "}}")
		if relEnd < 0 {
			return false
		}
		end := start + relEnd
		if strings.TrimSpace(part[start+2:end]) == name {
			return true
		}
		part = part[end+2:]
	}
}

// errPlaceholderResolutionBoundExceeded is returned by resolvePlaceholders
// when a safety bound (size cap or pass count) stops resolution before it
// would otherwise complete — distinct from the two INTENTIONAL, no-error
// stopping points (no more {{...}} left, or a placeholder with no matching
// entry in args, both of which correctly leave the remaining text literal).
// Found by review while preparing this fix chain's public port: hitting a
// bound silently left the literal, unresolved {{name}} text in the built
// command with no error at all, so a legitimate value merely large enough
// to trip the cap (a diff, a JSON payload, file contents well over 64 KiB)
// silently executed the WRONG command — the literal placeholder text —
// instead of failing loudly. That's a materially different situation from
// "config author forgot to pass this parameter" and must not be treated the
// same way.
var errPlaceholderResolutionBoundExceeded = errors.New("placeholder resolution exceeded a safety bound")

func resolvePlaceholders(s string, args map[string]interface{}) (string, error) {
	for i := 0; i < maxPlaceholderResolutionPasses; i++ {
		start := strings.Index(s, "{{")
		if start < 0 {
			return s, nil
		}
		relEnd := strings.Index(s[start:], "}}")
		if relEnd < 0 {
			return s, nil // malformed; bail to avoid infinite loop
		}
		end := start + relEnd
		raw := s[start : end+2]
		placeholder := strings.TrimSpace(s[start+2 : end])
		val, ok := args[placeholder]
		if !ok {
			return s, nil
		}
		valStr := fmt.Sprintf("%v", val)
		count := strings.Count(s, raw)
		// Bound each factor independently before multiplying, so the
		// product itself is capped at maxPlaceholderResolutionLength^2
		// (currently 65536^2 ≈ 4.3e9) — this fits int64 with room to
		// spare on every platform, but on a 32-bit int build (int is
		// 32 bits) it can still exceed math.MaxInt32, so the projected
		// size is computed with explicit int64 arithmetic rather than
		// relying on the platform's native int width. Found by review
		// on SK-ENT/rakitsu#75.
		if count > maxPlaceholderResolutionLength || len(valStr) > maxPlaceholderResolutionLength || len(s) > maxPlaceholderResolutionLength {
			return s, errPlaceholderResolutionBoundExceeded
		}
		if projected := int64(len(s)) + int64(count)*int64(len(valStr)-len(raw)); projected > int64(maxPlaceholderResolutionLength) {
			return s, errPlaceholderResolutionBoundExceeded
		}
		s = strings.Replace(s, raw, valStr, -1)
	}
	// Ran out of passes. Whatever {{...}} remains needs the SAME
	// classification the in-loop checks above already do — malformed, or
	// unrecognized (no matching entry in args) — before concluding this was
	// actually a bound trip. Naively treating any leftover "{{" as a bound
	// trip is wrong: a placeholder chain can need exactly
	// maxPlaceholderResolutionPasses steps to unwind down to a genuinely
	// unrecognized final placeholder (nothing malicious, just a few links
	// too many to also resolve the graceful "unrecognized" check on the
	// same pass) — that's the SAME pre-existing, intentional no-error case
	// as always, only reached one iteration later than the loop allows.
	//
	// Scans EVERY remaining {{...}} occurrence, not just the leftmost one:
	// found by a second review round — a single substitution on the FINAL
	// pass can introduce more than one new placeholder at once (e.g. a
	// value containing both an unrecognized placeholder and a legitimately
	// resolvable one). Checking only the first occurrence would see the
	// unrecognized one, conclude "graceful stop", and miss that a
	// genuinely resolvable placeholder sitting right after it was cut off
	// by the pass cap. Only once NONE of the remaining placeholders are
	// recognized do we call this the same pre-existing graceful stop as
	// always; finding even one recognized, well-formed placeholder means a
	// bound was genuinely exceeded. Both mis-classifications verified
	// independently with reproduction tests before fixing.
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			return s, nil
		}
		relEnd := strings.Index(rest[start:], "}}")
		if relEnd < 0 {
			return s, nil // malformed tail; same graceful stop as the in-loop check
		}
		end := start + relEnd
		placeholder := strings.TrimSpace(rest[start+2 : end])
		if _, ok := args[placeholder]; ok {
			return s, errPlaceholderResolutionBoundExceeded
		}
		rest = rest[end+2:]
	}
}

// buildCommand builds the full command with arguments
func (t *Tool) buildCommand(args map[string]interface{}) ([]string, error) {
	// If command has placeholders, substitute them.
	// IMPORTANT: a single command part may contain multiple distinct
	// placeholders (e.g. a shell payload like `sh -c 'grep "{{pattern}}" "{{file}}"'`).
	// Iterate until no more recognized placeholders remain, not just once.
	// See New-B-cli-multi-placeholder in STABILITY-gate.md for the original
	// regression (dogfood-01 search tool, 2026-04-05).
	//
	// Special case: when a command part IS the whole placeholder (e.g.
	// `command: "gh {{args}}"`) AND that parameter opts in via
	// `argv_split: true`, the substituted value is shell-aware split into
	// multiple argv tokens instead of inserted as one. This is opt-in
	// (default false) rather than auto-detected from the command's shape,
	// because the same shape (`binary {{param}}`, no surrounding text) is
	// genuinely ambiguous: `gh {{args}}` with a free-text argument string
	// wants multiple tokens, but `python3 -c {{code}}` wants its whole value
	// as ONE argument — there's no way to tell those apart by looking at the
	// template alone. See config.Parameter.ArgvSplit (a gh tool
	// in a dev-agent-system config landed as a single unparseable argv
	// element and failed every call).
	//
	// argv_split must never apply to a shell wrapper's `-c` payload slot
	// (e.g. `command: "sh -c {{args}}"`, index 2 after splitCommand — the
	// identical shape to the already-supported `sh -c '{{args}}'`, since
	// splitCommand's quote-stripping makes the two textually equivalent
	// once there are no spaces inside the placeholder itself). Splitting
	// that slot would hand `sh -c` only the value's first word as its
	// script and turn every subsequent word into a $0/$1/... positional
	// parameter instead of script content — and the B20 blocklist lint
	// below only ever inspects cmd[2], so it would silently validate just
	// that truncated first word while the real (also truncated) script
	// runs unchecked. Reject this combination outright rather than
	// mis-executing it.
	//
	// filepath.Base normalizes a full-path wrapper (`/bin/sh -c {{args}}`)
	// to the same detection as a bare `sh`/`bash` — a review-round-2 gap in
	// the first version of this guard, applied to the pre-existing B20 lint
	// trigger below too. A re-invocation wrapper (`env sh -c {{args}}`, a
	// different index shape entirely) is deliberately NOT covered here: the
	// B20 blocklist lint itself only ever fires when cmd[0] is literally
	// sh/bash (see below), so that shape already has no B20 protection
	// regardless of argv_split — extending only this guard to cover it
	// would be a false sense of safety, not a real fix. That's a
	// pre-existing, broader gap in the lint's own detection scope, not
	// something argv_split introduces or this fix is scoped to solve.
	//
	// hasShellWrapperShape is a template-level structural check only (does
	// this command have at least 3 parts, i.e. could index 2 even be a
	// `-c` payload slot). Deliberately NOT combined with the sh/bash name
	// check here: a templated interpreter (`command: "{{shell}} -c
	// {{args}}"`) has t.command[0] == "{{shell}}" — never literally
	// "sh"/"bash" — so checking the raw template would never catch this
	// shape even when shell resolves to "sh" at runtime, silently letting
	// argv_split through onto what is, after substitution, exactly the
	// dangerous slot this guard exists to reject. The actual sh/bash
	// comparison happens below, inside the loop, against cmd[0]/cmd[1] —
	// the ALREADY-SUBSTITUTED values — once i reaches 2 (by which point
	// both have been resolved and appended in prior loop iterations).
	hasShellWrapperShape := len(t.command) >= 3

	var cmd []string
	for i, part := range t.command {
		wholePartPlaceholder := ""
		if strings.HasPrefix(part, "{{") && strings.HasSuffix(part, "}}") &&
			strings.Count(part, "{{") == 1 {
			name := strings.TrimSpace(part[2 : len(part)-2])
			if t.parameters[name].ArgvSplit {
				isShellWrapperPayloadSlot := hasShellWrapperShape && i == 2 && len(cmd) >= 2 &&
					(filepath.Base(cmd[0]) == "sh" || filepath.Base(cmd[0]) == "bash") && cmd[1] == "-c"
				if isShellWrapperPayloadSlot {
					return nil, fmt.Errorf(
						"parameter %q has argv_split: true but is the sh -c/bash -c script argument (resolved command %q) — "+
							"its value must stay a single token; remove argv_split or restructure the command",
						name, strings.Join(cmd, " "))
				}
				wholePartPlaceholder = name
			}
		}

		// A part eligible for argv_split is, by construction, exactly one
		// placeholder and nothing else — so there's exactly one lookup to
		// do, no scanning loop needed. If the value isn't in args, fall
		// through to the general resolvePlaceholders call below, which
		// leaves an unrecognized placeholder as literal text (same
		// behavior as always).
		if wholePartPlaceholder != "" {
			if val, ok := args[wholePartPlaceholder]; ok {
				resolved, err := resolvePlaceholders(fmt.Sprintf("%v", val), args)
				if err != nil {
					return nil, fmt.Errorf("parameter %q: %w", wholePartPlaceholder, err)
				}
				cmd = append(cmd, splitCommand(resolved)...)
				continue
			}
		}

		// resolvePlaceholders also always appends exactly one cmd entry per
		// command part — including a part that substitutes down to a
		// legitimately empty string (e.g. `{{value}}` with value: "") or
		// one left untouched because it has no recognized placeholder at
		// all — matching the pre-existing behavior this replaces.
		resolved, err := resolvePlaceholders(part, args)
		if err != nil {
			return nil, fmt.Errorf("command part %q: %w", part, err)
		}
		cmd = append(cmd, resolved)
	}

	// Add additional arguments. t.parameters is a map, whose iteration order
	// Go randomizes on every run — iterating it directly would append
	// non-templated `--name=val` flags in a different order each call,
	// making the resulting argv (and any test/log asserting on it)
	// nondeterministic. Sort names for a stable, reproducible argv.
	names := make([]string, 0, len(t.parameters))
	for name := range t.parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		param := t.parameters[name]
		if val, ok := args[name]; ok {
			// Skip if it's a placeholder that was already substituted.
			// Uses referencesPlaceholder (trims each {{...}} name before
			// comparing) rather than a literal "{{"+name+"}}" substring
			// search, so a whitespace-padded placeholder like {{ name }}
			// is still recognized as already consumed and doesn't also
			// get appended as a redundant --name=val.
			isPlaceholder := false
			for _, part := range t.command {
				if referencesPlaceholder(part, name) {
					isPlaceholder = true
					break
				}
			}
			if !isPlaceholder {
				cmd = append(cmd, fmt.Sprintf("--%s=%v", name, val))
			}
		} else if param.Required {
			return nil, fmt.Errorf("required parameter '%s' not provided", name)
		}
	}

	// B20 mitigation: when the assembled command is a shell wrapper
	// (sh -c '<payload>' or bash -c '<payload>'), lint the post-substitution
	// payload for standalone invocations of blocked commands. This catches
	// obvious cases like `log; rm -rf /` without blocking legitimate uses
	// like `log --grep "arm64"` (where "rm" is only a substring).
	//
	// filepath.Base so a full-path wrapper (`/bin/sh -c '...'`,
	// `/usr/bin/bash -c '...'`) is recognized the same as a bare `sh`/`bash`
	// — same normalization isCommandAllowed already applies. Found alongside
	// the argv_split guard (which shares this exact detection).
	if len(cmd) >= 3 && (filepath.Base(cmd[0]) == "sh" || filepath.Base(cmd[0]) == "bash") && cmd[1] == "-c" {
		if err := lintShellPayload(cmd[2]); err != nil {
			return nil, err
		}
	}

	return cmd, nil
}

// isCommandAllowed checks if a command is allowed
func (t *Tool) isCommandAllowed(cmd string) bool {
	// Get base command name
	baseCmd := filepath.Base(cmd)

	// Never allow the running binary to invoke itself, regardless of
	// allowed_commands config. A cli tool that re-launches rakitsu can
	// point the new process at any config/workdir it likes, escaping
	// whatever sandboxing this session was set up with. This check is
	// unconditional — it is not part of blockedCommands so it can't be
	// removed by editing that map, and it runs before any whitelist.
	if isSelfBinary(cmd) {
		return false
	}

	// Check blocked list first (always takes precedence)
	if blockedCommands[baseCmd] {
		return false
	}

	// Check system whitelist
	if systemWhitelist[baseCmd] {
		return true
	}

	// Check user-configured whitelist
	return t.userWhitelist[baseCmd]
}

// isSelfBinary reports whether baseCmd names the currently running
// executable — either literally "rakitsu" or the actual binary name
// os.Args[0] was invoked as (covers renamed builds, e.g. "rakitsu-dev").
func isSelfBinary(cmd string) bool {
	baseCmd := filepath.Base(cmd)
	if baseCmd == "rakitsu" {
		return true
	}
	if len(os.Args) > 0 && baseCmd == filepath.Base(os.Args[0]) {
		return true
	}
	return isSameFileAsSelf(cmd)
}

// isSameFileAsSelf resolves cmd to a real file (via a PATH lookup if it's
// a bare name — exec.LookPath handles both cases) and compares its device
// and inode against the currently running rakitsu binary. This is what
// actually catches a symlink or hard link pointed at the same binary under
// an unrelated name — the name check above alone misses it, since the
// allowed_commands entry and the argv[0] name can both be anything the
// config author picked. It cannot catch a byte-for-byte copy of the
// binary under a different name: that has its own inode and is
// indistinguishable from any other unknown executable short of hashing
// file contents on every cli call, which this guard deliberately doesn't
// do (see docs/SECURITY.md's residual-limitation notes).
func isSameFileAsSelf(cmd string) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return false
	}
	candidate, err := exec.LookPath(cmd)
	if err != nil {
		return false
	}
	candidateInfo, err := os.Stat(candidate)
	if err != nil {
		return false
	}
	return os.SameFile(selfInfo, candidateInfo)
}

// executeLocalRestricted executes the command locally with restrictions
func (t *Tool) executeLocalRestricted(ctx context.Context, cmd []string) (string, error) {
	// Working directory: explicit workingDir first, then sandbox allowed path.
	dir := t.workingDir
	if dir == "" && t.sandbox != nil && len(t.sandbox.AllowedPaths) > 0 {
		dir = t.sandbox.AllowedPaths[0]
	}
	// A nonexistent Dir surfaces from os/exec as "fork/exec <binary>: no such
	// file or directory", which reads as the binary being missing — check up
	// front so the error names the real problem (SK-ENT/rakitsu#28).
	if dir != "" {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", fmt.Errorf("working directory %q for command execution is not usable: %w", dir, statErr)
		}
	}

	if t.sandbox != nil && len(t.sandbox.AllowedPaths) > 0 {
		if err := checkArgPaths(cmd, dir, t.sandbox.AllowedPaths); err != nil {
			return "", err
		}
	}

	// Set timeout
	timeout := 30 * time.Second
	if t.sandbox != nil && t.sandbox.ResourceLimits.TimeoutSec > 0 {
		timeout = time.Duration(t.sandbox.ResourceLimits.TimeoutSec) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Resolve the executable ourselves and exec that exact absolute path,
	// rather than passing a bare name and letting exec.CommandContext
	// perform its own, separate PATH lookup. Two independent lookups (one
	// here for the self-invocation re-check, one inside exec.Command) open
	// a window where each could resolve a bare name to a different file if
	// something on PATH changes in between (found by automated review
	// right after the isSelfBinary fix above landed). An absolute
	// path always skips exec.Command's internal lookup, so this collapses
	// the two lookups into one. It does not eliminate the smaller, harder
	// to close, check-then-exec gap between this os.Stat and the actual
	// exec syscall — that would need fd-based exec, out of scope for this
	// fix — see docs/SECURITY.md's residual-limitation notes.
	resolved, err := exec.LookPath(cmd[0])
	if err != nil {
		return "", fmt.Errorf("cannot resolve command %q: %w", cmd[0], err)
	}
	// exec.LookPath does not guarantee an absolute result: if PATH contains
	// a relative directory entry, it can return a path like "bin/tool",
	// resolved against THIS process's current working directory. Below,
	// execCmd.Dir changes the CHILD's working directory before its argv[0]
	// is resolved — so a relative resolved path would be checked here
	// against one file (relative to our cwd) and executed as a different
	// file (the same relative path, but under dir), a bypass found by
	// automated review. filepath.Abs pins it to one exact file for
	// both the check and the exec, independent of any later chdir.
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot resolve command %q: %w", cmd[0], err)
	}
	if isSelfBinary(resolved) {
		return "", &SecurityError{Command: cmd[0], Reason: "command resolves to the running rakitsu binary"}
	}
	cmd[0] = resolved

	execCmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	execCmd.Env = scrubbedEnviron()
	execCmd.Dir = dir

	// On Linux, harden further: re-verify self-identity via a file
	// descriptor opened with O_PATH (no read permission required, unlike
	// a normal open — matching what exec itself needs) and exec through
	// that exact fd via /proc/self/fd, eliminating the remaining
	// check-then-exec race between the isSelfBinary check above and the
	// actual exec syscall. No equivalent exists on macOS/BSD (no /proc,
	// no portable fexecve) — see docs/SECURITY.md.
	execFile, err := execViaFD(execCmd, resolved)
	if err != nil {
		return "", err
	}
	if execFile != nil {
		defer execFile.Close()
	}

	// Run in its own process group so a timeout kills the whole tree, not
	// just the direct child — matters for whitelisted shell wrappers
	// (sh -c/bash -c) that spawn further children of their own.
	setNewProcessGroup(execCmd)
	execCmd.Cancel = func() error { return killProcessGroup(execCmd) }

	// Capture output
	output, err := execCmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("command timed out after %v", timeout)
		}
		return truncateOutput(string(output), t.maxOutputBytes()), fmt.Errorf("command failed: %w", err)
	}

	return truncateOutput(string(output), t.maxOutputBytes()), nil
}

// checkArgPaths is the allowed_paths ARGUMENT filter for local_restricted
// execution. It rejects any argument token naming a location outside every
// allowed path once resolved the way the command will see it: relative to
// dir (the directory the command runs in), `..` components cleaned, and
// symlinks resolved. Shell-wrapped templates (sh -c '…') and
// placeholder-substituted arguments can carry several tokens in one argv
// element, so each element is split on whitespace; quote characters are
// dropped the way the shell drops them (the adjacent fragments
// "..", "/x" concatenate to ../x); `--opt=path` and `VAR=path` forms are
// checked on their value; a `~`-prefixed token is expanded the way the
// shell would; a token with glob metacharacters is expanded against the
// working directory and every match is checked, since that is what the
// shell hands the command. Every token is resolved, not just
// those that look like paths: a token such as a flag or a git ref lands
// inside the working directory and passes, while a bare name that is a
// symlink out of the fence is caught.
//
// Trust model (docs/SECURITY.md): this is an argument filter, not
// containment. It only sees paths that appear verbatim as tokens. An
// allowed interpreter (python3, node, bash, …) opens whatever its code
// names, a shell payload can build a path from variables or substitution,
// and `find -exec`/`xargs` re-invoke commands the filter never sees.
// Real containment is sandbox: docker.
func checkArgPaths(cmd []string, dir string, allowed []string) error {
	sep := string(filepath.Separator)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	roots := make([]string, 0, len(allowed))
	for _, a := range allowed {
		// When dir (working_dir / --workdir) is absolute, a relative fence is
		// anchored on it — the same base the argument tokens below are
		// resolved against. Otherwise allowed_paths: ["."] with a --workdir
		// elsewhere denied every token, even `sh -c`. A relative dir is itself
		// cwd-relative, so relative fences keep resolving against the cwd.
		if !filepath.IsAbs(a) && filepath.IsAbs(dir) {
			a = filepath.Join(absDir, a)
		}
		absAllowed, err := filepath.Abs(a)
		if err != nil {
			continue
		}
		roots = append(roots, strings.TrimRight(resolveExistingPath(absAllowed), sep))
	}
	deny := func(tok, why string) error {
		return &SecurityError{
			Command: strings.Join(cmd, " "),
			Reason:  fmt.Sprintf("path '%s' %s", tok, why),
		}
	}
	dequote := strings.NewReplacer(`"`, "", `'`, "")
	for _, arg := range cmd[1:] {
		for _, tok := range strings.Fields(arg) {
			tok = dequote.Replace(tok)
			if i := strings.IndexByte(tok, '='); i > 0 && !strings.Contains(tok[:i], sep) {
				tok = tok[i+1:] // --opt=path / VAR=path
			}
			if tok == "" {
				continue
			}
			p := tok
			if strings.HasPrefix(p, "~") {
				if p != "~" && !strings.HasPrefix(p, "~/") {
					return deny(tok, "uses another user's home directory") // ~user/…: cannot resolve safely
				}
				home, err := os.UserHomeDir()
				if err != nil {
					return deny(tok, "cannot be resolved")
				}
				p = home + p[1:]
			}
			if !filepath.IsAbs(p) {
				p = filepath.Join(absDir, p)
			}
			p = filepath.Clean(p)
			candidates := []string{p}
			if strings.ContainsAny(p, "*?[") {
				// The shell expands the glob before the command sees it; a
				// match that is a symlink out of the fence must be caught the
				// same as if it had been named directly. No match: the shell
				// passes the literal token, checked as-is above.
				if matches, err := filepath.Glob(p); err == nil && len(matches) > 0 {
					candidates = matches
				}
			}
			for _, c := range candidates {
				if insideRoots(resolveExistingPath(c), roots, sep) {
					continue
				}
				if c != p {
					return deny(tok, fmt.Sprintf("expands to '%s', which is not in allowed paths", c))
				}
				return deny(tok, "is not in allowed paths")
			}
		}
	}
	return nil
}

// insideRoots reports whether real (an absolute, symlink-resolved path) is
// one of roots or below one. The filesystem root trims to "", so its prefix
// is sep alone and every absolute path is inside it.
func insideRoots(real string, roots []string, sep string) bool {
	for _, r := range roots {
		if real == r || strings.HasPrefix(real, r+sep) {
			return true
		}
	}
	return false
}

// resolveExistingPath resolves symlinks for the deepest existing prefix of
// absPath and re-appends the remaining components, so a not-yet-existing
// target is still judged by where it would land. Mirrors
// internal/tools/fs.resolvePathWithSymlinks; duplicated to keep this package
// independent of the fs tool.
func resolveExistingPath(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved
	}
	dir := absPath
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
	}
	return absPath
}

// maxOutputBytes returns the configured cap for this tool invocation, or
// DefaultMaxOutputBytes when the config leaves it unset (0). A negative
// value disables truncation.
func (t *Tool) maxOutputBytes() int {
	if t.sandbox == nil || t.sandbox.ResourceLimits.MaxOutputBytes == 0 {
		return DefaultMaxOutputBytes
	}
	return t.sandbox.ResourceLimits.MaxOutputBytes
}

// DefaultPidsLimit caps the number of processes/threads a docker-sandboxed
// container can create when the config leaves
// sandbox.resource_limits.pids_limit unset (0) — a floor against a fork
// bomb or runaway subprocess spawn. Set pids_limit to -1 to disable it.
const DefaultPidsLimit = 128

// DefaultDockerUser is the docker "--user" applied when sandbox.user is
// unset: nobody:nogroup, so a container escape or compromised tool
// doesn't get root inside the container.
const DefaultDockerUser = "65534:65534"

// buildDockerArgs constructs the "docker run" argument list for cmd under
// sandbox. workingDir is the tool's explicit working_dir ("" = process cwd).
// Split out from executeInDocker so the hardening flags below are
// unit-testable without actually invoking Docker.
func buildDockerArgs(sandbox *config.SandboxConfig, cmd []string, workingDir string) []string {
	// A unique name lets executeInDocker remove the container even when the
	// docker client itself was killed by a timeout or cancellation — killing
	// the client does not reliably stop the container it started.
	dockerCmd := []string{"run", "--rm", "--name", "rakitsu-" + uuid.NewString()}

	// Root filesystem is read-only by default; only the explicit mounts below
	// are writable. --tmpfs gives commands that need scratch space (compilers,
	// package managers, etc.) somewhere to write without punching a hole in
	// the read-only root. --security-opt=no-new-privileges blocks setuid/
	// setgid privilege escalation inside the container. --cap-drop=ALL drops
	// every Linux capability (CAP_NET_RAW, CAP_SYS_ADMIN, etc.) the
	// container's root would otherwise retain even without host root.
	dockerCmd = append(dockerCmd, "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m")
	dockerCmd = append(dockerCmd, "--security-opt", "no-new-privileges")
	dockerCmd = append(dockerCmd, "--cap-drop", "ALL")

	// Run as an unprivileged user inside the container by default, so a
	// container escape or a compromised tool doesn't come out as root.
	user := sandbox.User
	if user == "" {
		user = DefaultDockerUser
	}
	dockerCmd = append(dockerCmd, "--user", user)

	// Cap process/thread count as a floor against a fork bomb or runaway
	// subprocess spawn. -1 opts out.
	pidsLimit := sandbox.ResourceLimits.PidsLimit
	if pidsLimit == 0 {
		pidsLimit = DefaultPidsLimit
	}
	if pidsLimit > 0 {
		dockerCmd = append(dockerCmd, "--pids-limit", strconv.Itoa(pidsLimit))
	}

	// Add working directory mount. Read-only by default: a command that
	// only needs to read the workdir (most linters, test runners) can't
	// also modify or delete files there just because MountWorkdir was set.
	if sandbox.MountWorkdir {
		cwd := workingDir
		if cwd == "" {
			cwd, _ = os.Getwd()
		} else if abs, err := filepath.Abs(cwd); err == nil {
			// Docker requires an absolute host path for a bind-mount source;
			// a relative one is either rejected or read as a named volume
			// instead of the intended directory.
			cwd = abs
		}
		mount := cwd + ":/workspace"
		if !sandbox.MountWorkdirWritable {
			mount += ":ro"
		}
		dockerCmd = append(dockerCmd, "-v", mount)
		dockerCmd = append(dockerCmd, "-w", "/workspace")
	}

	// Add resource limits
	if sandbox.ResourceLimits.CPULimit != "" {
		dockerCmd = append(dockerCmd, "--cpus", sandbox.ResourceLimits.CPULimit)
	}
	if sandbox.ResourceLimits.MemoryLimit != "" {
		dockerCmd = append(dockerCmd, "--memory", sandbox.ResourceLimits.MemoryLimit)
	}

	// No network by default — most cli tools (linters, formatters,
	// interpreters running trusted config-authored commands) don't need
	// it, and a container that can't reach the network can't exfiltrate
	// or phone home even if the command running inside it turns out
	// hostile. AllowNetwork opts back in explicitly; the legacy
	// NetworkIsolated=true is equivalent to the default (kept so existing
	// configs setting it keep working unchanged).
	if !sandbox.AllowNetwork {
		dockerCmd = append(dockerCmd, "--network", "none")
	}

	// Add image
	image := sandbox.Image
	if image == "" {
		image = "alpine:latest"
	}
	dockerCmd = append(dockerCmd, image)

	// Add the actual command
	dockerCmd = append(dockerCmd, cmd...)
	return dockerCmd
}

// dockerContainerName returns the value buildDockerArgs passed to --name, so
// cleanup does not depend on the flag's position in the argument list.
func dockerContainerName(dockerArgs []string) string {
	for i := 0; i+1 < len(dockerArgs); i++ {
		if dockerArgs[i] == "--name" {
			return dockerArgs[i+1]
		}
	}
	return ""
}

// executeInDocker executes the command in a Docker container
// This provides stronger isolation but requires Docker to be installed
func (t *Tool) executeInDocker(ctx context.Context, cmd []string) (string, error) {
	// Resolve the docker client up front so a missing Docker reads as exactly
	// that, and so nothing else is ever attempted in its place.
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("docker sandbox unavailable: docker is not installed or not on PATH (host execution was not attempted): %w", err)
	}

	// Same timeout rule as local execution; before this the docker path had
	// no deadline at all.
	timeout := 30 * time.Second
	if t.sandbox.ResourceLimits.TimeoutSec > 0 {
		timeout = time.Duration(t.sandbox.ResourceLimits.TimeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dockerArgs := buildDockerArgs(t.sandbox, cmd, t.workingDir)
	containerName := dockerContainerName(dockerArgs)
	defer func() {
		// Best-effort removal of this invocation's container. On the happy
		// path --rm already did it and this is a no-op; after a timeout or
		// cancellation only the client was killed and the container may
		// still be running.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		cleanup := exec.CommandContext(cleanupCtx, dockerPath, "rm", "-f", containerName)
		cleanup.Env = scrubbedEnviron()
		_ = cleanup.Run()
	}()

	// Execute. Scrub sensitive env from the local `docker` CLI process itself;
	// the container's own environment is separate and unaffected (docker run
	// does not forward host env unless -e/--env-file is passed, which this
	// command builder does not do).
	execCmd := exec.CommandContext(ctx, dockerPath, dockerArgs...)
	execCmd.Env = scrubbedEnviron()
	output, err := execCmd.CombinedOutput()
	if err != nil {
		return truncateOutput(string(output), t.maxOutputBytes()), fmt.Errorf("docker execution failed: %w", err)
	}

	return truncateOutput(string(output), t.maxOutputBytes()), nil
}

// SecurityError is returned when a command is blocked by security rules
type SecurityError struct {
	Command string
	Reason  string
}

func (e *SecurityError) Error() string {
	return fmt.Sprintf("security error: command '%s' blocked: %s", e.Command, e.Reason)
}
