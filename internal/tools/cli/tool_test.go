package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

// ============================================================
// Helpers
// ============================================================

func newTool(cmd string, params map[string]config.Parameter, allowedPaths ...[]string) *Tool {
	def := &config.ToolDefinition{
		Name:        "test-tool",
		Description: "test",
		Command:     cmd,
		Parameters:  params,
	}
	if len(allowedPaths) > 0 {
		def.Sandbox = &config.SandboxConfig{
			Type:         "local_restricted",
			AllowedPaths: allowedPaths[0],
		}
	}
	return NewTool(def)
}

func newToolWithUserWhitelist(cmd string, extra []string) *Tool {
	def := &config.ToolDefinition{
		Name:    "test-tool",
		Command: cmd,
	}
	return NewTool(def, extra)
}

// ============================================================
// isCommandAllowed — security whitelist/blocklist
// ============================================================

func TestIsCommandAllowed_BlockedCommand(t *testing.T) {
	tool := newTool("ls", nil)
	for _, cmd := range []string{"rm", "sudo", "kill", "mv", "cp", "chmod"} {
		if tool.isCommandAllowed(cmd) {
			t.Errorf("blocked command %q should not be allowed", cmd)
		}
	}
}

func TestIsCommandAllowed_PathBasedBypass(t *testing.T) {
	// /bin/rm should still be blocked — filepath.Base strips the path
	tool := newTool("ls", nil)
	if tool.isCommandAllowed("/bin/rm") {
		t.Error("/bin/rm should be blocked (base is 'rm')")
	}
	if tool.isCommandAllowed("/usr/bin/sudo") {
		t.Error("/usr/bin/sudo should be blocked")
	}
}

func TestIsCommandAllowed_Whitelisted(t *testing.T) {
	tool := newTool("ls", nil)
	for _, cmd := range []string{"ls", "cat", "grep", "git", "go", "kubectl"} {
		if !tool.isCommandAllowed(cmd) {
			t.Errorf("whitelisted command %q should be allowed", cmd)
		}
	}
}

func TestIsCommandAllowed_UnknownCommand(t *testing.T) {
	tool := newTool("ls", nil)
	if tool.isCommandAllowed("curl") {
		t.Error("unlisted command 'curl' should not be allowed")
	}
	if tool.isCommandAllowed("wget") {
		t.Error("unlisted command 'wget' should not be allowed")
	}
}

func TestIsCommandAllowed_SelfInvocation_BlockedEvenIfUserWhitelisted(t *testing.T) {
	// A cli tool that re-invokes the rakitsu binary itself could point a new
	// process at any config/workdir it likes, escaping this session's sandbox.
	// That must be denied unconditionally, regardless of allowed_commands.
	tool := newToolWithUserWhitelist("ls", []string{"rakitsu"})
	if tool.isCommandAllowed("rakitsu") {
		t.Error("'rakitsu' must never be allowed, even via user whitelist")
	}
	if tool.isCommandAllowed("/usr/local/bin/rakitsu") {
		t.Error("'/usr/local/bin/rakitsu' must be blocked (base is 'rakitsu')")
	}
}

func TestIsCommandAllowed_SelfInvocation_BlocksSymlinkUnderDifferentName(t *testing.T) {
	// Found during review (#76): the name-only check missed a
	// symlink (or hard link) pointed at the running binary under an
	// unrelated name — e.g. allowed_commands: [alias] where "alias" is a
	// symlink to rakitsu. os.Executable() in a test binary resolves to the
	// test binary itself, so that stands in for "the running rakitsu
	// binary" here: a symlink to it must still be blocked even though its
	// name matches neither "rakitsu" nor os.Args[0]'s basename.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	dir := t.TempDir()
	alias := filepath.Join(dir, "totally-unrelated-name")
	if err := os.Symlink(self, alias); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool := newToolWithUserWhitelist("ls", []string{"totally-unrelated-name"})
	if tool.isCommandAllowed("totally-unrelated-name") {
		t.Error("a symlink to the running binary under a different name must still be blocked")
	}
	if tool.isCommandAllowed(alias) {
		t.Error("the same symlink referenced by full path must still be blocked")
	}
}

func TestExecuteLocalRestricted_ReRejectsSelfBinaryAtExecTime(t *testing.T) {
	// Defense in depth, found during review (#76): isCommandAllowed
	// and the actual exec call each did their own, separate PATH lookup for
	// a bare command name, opening a window where the two could resolve to
	// different files. executeLocalRestricted must independently re-reject
	// a self-invocation immediately before exec, not rely solely on
	// isCommandAllowed's earlier (and by now stale) check. Calling
	// executeLocalRestricted directly here — bypassing isCommandAllowed
	// entirely — proves this re-check holds on its own.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	dir := t.TempDir()
	alias := filepath.Join(dir, "some-benign-name")
	if err := os.Symlink(self, alias); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	tool := newTool("ls", nil)
	_, err = tool.executeLocalRestricted(context.Background(), []string{alias})
	if err == nil {
		t.Fatal("expected an error executing a symlink pointed at the running binary")
	}
	var se *SecurityError
	if !errors.As(err, &se) {
		t.Errorf("expected *SecurityError, got %T: %v", err, err)
	}
}

func TestExecuteLocalRestricted_ResolvesRelativePathToAbsoluteBeforeExec(t *testing.T) {
	// Found during review (#76): a cli tool's command template can
	// itself be a relative path containing a slash (e.g.
	// `command: "./scripts/tool {{args}}"` — a realistic thing to write in
	// a YAML config). exec.LookPath does not search PATH for such a name
	// (it already contains a separator) and returns it UNCHANGED — still
	// relative — with no error. Unix resolves a relative exec path against
	// the CHILD's cwd, which execCmd.Dir changes to `dir` before the
	// child's own argv[0] is resolved — but the self-invocation check runs
	// in THIS process beforehand, against THIS process's cwd. Without
	// pinning the resolved path to absolute, the check and the actual exec
	// can inspect two different files with the same relative name: a
	// benign one under this process's cwd (passes the check) and the
	// running rakitsu binary under `dir` (what actually executes) — a
	// total bypass of the self-invocation guard.
	if runtime.GOOS == "windows" {
		t.Skip("relies on Unix relative-exec-path resolution semantics")
	}

	parentCwd := t.TempDir()
	childDir := t.TempDir()

	// Benign target at the same relative path, under the process's own
	// cwd — this is what a correct fix must actually execute.
	if err := os.Mkdir(filepath.Join(parentCwd, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	benign := filepath.Join(parentCwd, "bin", "tool")
	if err := os.WriteFile(benign, []byte("#!/bin/sh\necho BENIGN-MARKER\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The SAME relative path, but under childDir, is a symlink to the
	// running binary — what a vulnerable version executes instead, because
	// execCmd.Dir = childDir changes the child's cwd before its relative
	// argv[0] is resolved.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	if err := os.Mkdir(filepath.Join(childDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(childDir, "bin", "tool")); err != nil {
		t.Fatal(err)
	}

	t.Chdir(parentCwd)

	tool := newTool("bin/tool", nil) // cmd[0] already contains a slash: no PATH search, LookPath returns it as-is
	tool.workingDir = childDir

	out, err := tool.executeLocalRestricted(context.Background(), []string{"bin/tool"})
	if err != nil {
		t.Fatalf("expected the benign target to run without error, got: %v", err)
	}
	if !strings.Contains(out, "BENIGN-MARKER") {
		t.Errorf("expected the benign target's own output, got %q — a relative-path bypass may have executed something else instead", out)
	}
}

func TestIsCommandAllowed_SelfInvocation_BlocksRunningBinaryName(t *testing.T) {
	// Also covers a renamed build (os.Args[0] != "rakitsu"), not just the
	// literal name "rakitsu".
	self := filepath.Base(os.Args[0])
	tool := newToolWithUserWhitelist("ls", []string{self})
	if tool.isCommandAllowed(self) {
		t.Errorf("running binary's own name %q must never be allowed", self)
	}
}

// ============================================================
// Execute — security errors via Execute()
// ============================================================

func TestExecute_BlockedCommand_SecurityError(t *testing.T) {
	tool := newTool("rm", nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for blocked command")
	}
	var se *SecurityError
	if !errors.As(err, &se) {
		t.Errorf("expected *SecurityError, got %T: %v", err, err)
	}
	if se.Command == "" {
		t.Error("SecurityError.Command should be set")
	}
}

func TestExecute_UnlistedCommand_SecurityError(t *testing.T) {
	tool := newTool("curl", nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	var se *SecurityError
	if !errors.As(err, &se) {
		t.Errorf("expected *SecurityError for unlisted command, got %T: %v", err, err)
	}
}

func TestExecute_UserWhitelist_Allowed(t *testing.T) {
	// "echo" is not in the system whitelist (and not blocked), so this only
	// succeeds if the user whitelist actually grants it — a regression that
	// broke the user-whitelist override would surface here as an error.
	tool := newToolWithUserWhitelist("echo hello", []string{"echo"})
	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("expected user-whitelisted command to succeed, got error: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in output, got %q", out)
	}
}

// ============================================================
// buildCommand — argument substitution
// ============================================================

func TestBuildCommand_Placeholder(t *testing.T) {
	// Build a tool with a placeholder in the command
	def := &config.ToolDefinition{
		Name:    "cat-file",
		Command: "cat {{file}}",
		Parameters: map[string]config.Parameter{
			"file": {Type: "string", Description: "file to read", Required: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{"file": "/etc/hostname"})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	if len(cmd) < 2 || cmd[1] != "/etc/hostname" {
		t.Errorf("expected cmd[1]='/etc/hostname', got %v", cmd)
	}
}

func TestBuildCommand_WholePartPlaceholder_EmptyValuePreservedAsArgvElement(t *testing.T) {
	// Caught by automated PR review: a command part that is a whole
	// placeholder (no argv_split) substituting to an empty string must still
	// land as one (empty) argv element, matching the pre-existing behavior
	// of always keeping one cmd entry per command part — not silently
	// dropped, which could shift positional arguments for the target binary.
	def := &config.ToolDefinition{
		Name:    "tool",
		Command: "cat {{file}}",
		Parameters: map[string]config.Parameter{
			"file": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{"file": ""})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	want := []string{"cat", ""}
	if len(cmd) != len(want) || cmd[1] != "" {
		t.Errorf("expected empty value preserved as its own argv element %v, got %v", want, cmd)
	}
}

func TestBuildCommand_RequiredParamMissing(t *testing.T) {
	def := &config.ToolDefinition{
		Name:    "tool",
		Command: "ls",
		Parameters: map[string]config.Parameter{
			"dir": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	_, err := tool.buildCommand(map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing required parameter")
	}
}

func TestBuildCommand_OptionalParam_Appended(t *testing.T) {
	def := &config.ToolDefinition{
		Name:    "tool",
		Command: "ls",
		Parameters: map[string]config.Parameter{
			"format": {Type: "string", Required: false},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{"format": "long"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "format=long") {
		t.Errorf("expected --format=long in command, got %q", joined)
	}
}

func TestBuildCommand_ArgvSplit_SplitsIntoArgvTokens(t *testing.T) {
	// Regression: a command template where the whole (only)
	// argument part is a single placeholder — e.g. `gh {{args}}`, no shell
	// wrapper — with that parameter opted in via argv_split must split the
	// substituted value into separate argv tokens. Before the fix, the
	// entire value landed as ONE argv element, so `gh` (or any argv-direct
	// binary) saw its whole multi-word command line as a single
	// unrecognized subcommand.
	def := &config.ToolDefinition{
		Name:    "gh",
		Command: "gh {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true, ArgvSplit: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{
		"args": "repo view --json nameWithOwner,defaultBranchRef",
	})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	want := []string{"gh", "repo", "view", "--json", "nameWithOwner,defaultBranchRef"}
	if len(cmd) != len(want) {
		t.Fatalf("expected %d argv tokens, got %d: %v", len(want), len(cmd), cmd)
	}
	for i, w := range want {
		if cmd[i] != w {
			t.Errorf("cmd[%d]: expected %q, got %q", i, w, cmd[i])
		}
	}
}

func TestBuildCommand_ArgvSplit_UnmatchedSingleQuote_DropsQuoteAndMergesTail(t *testing.T) {
	// Found by review while preparing this fix chain's public port.
	// argv_split reuses splitCommand — originally written only for
	// config-authored, static command TEMPLATES where an author controls
	// quoting deliberately — to now split a runtime, caller/agent-supplied
	// VALUE. A value containing an odd number of single quotes (e.g. an
	// ordinary apostrophe in free text like "don't") trips splitCommand's
	// quote-toggle into a state that never flips back off: the apostrophe
	// itself is silently dropped, and everything after it in the value
	// merges into one argv token instead of splitting on whitespace.
	//
	// This is documented behavior (see Parameter.ArgvSplit's doc comment),
	// not a security issue — no shell parses the result, so nothing
	// escapes containment — but it IS a real, silent, imprecise-splitting
	// gap. This test pins the CURRENT, understood-but-imperfect behavior
	// so it can't silently get worse without a test failing; it is not an
	// endorsement that this is the ideal behavior.
	def := &config.ToolDefinition{
		Name:    "gh",
		Command: "gh {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true, ArgvSplit: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{
		"args": `commit -m "don't forget the --user=x flag" now`,
	})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	want := []string{"gh", "commit", "-m", `"dont forget the --user=x flag" now`}
	if len(cmd) != len(want) {
		t.Fatalf("expected %d argv tokens (pinning current splitCommand behavior), got %d: %v", len(want), len(cmd), cmd)
	}
	for i, w := range want {
		if cmd[i] != w {
			t.Errorf("cmd[%d]: expected %q, got %q", i, w, cmd[i])
		}
	}
}

func TestBuildCommand_WholePartPlaceholder_DefaultStaysSingleToken(t *testing.T) {
	// Without argv_split (the default), a whole-part placeholder must keep
	// the original single-string substitution behavior. This is deliberately the
	// same shape as the ArgvSplit case above (`binary {{param}}`, no
	// surrounding text) to prove the two are told apart by the explicit
	// flag, not by guessing from the template shape — the shape alone is
	// genuinely ambiguous (see e.g. `python3 -c {{code}}` in
	// TestExecute_AllowedPaths_IsArgumentFilterNotContainment, which needs
	// its whole value as ONE argument).
	def := &config.ToolDefinition{
		Name:    "gh",
		Command: "gh {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{
		"args": "repo view --json nameWithOwner,defaultBranchRef",
	})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	want := []string{"gh", "repo view --json nameWithOwner,defaultBranchRef"}
	if len(cmd) != len(want) || cmd[1] != want[1] {
		t.Errorf("expected single-token substitution by default, got %v", cmd)
	}
}

func TestBuildCommand_ShellWrapperPlaceholder_StillSingleToken(t *testing.T) {
	// Confirms the whole-part-placeholder split does NOT kick in when the
	// placeholder is embedded in a larger string (e.g. inside a shell
	// payload passed to `sh -c`) — that case must keep its existing
	// single-string substitution, verified in more detail by
	// TestBuildCommand_ShellWrapperPlaceholder above.
	def := &config.ToolDefinition{
		Name:    "git-shell",
		Command: "sh -c 'git {{args}}'",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{"args": "log --oneline"})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	if len(cmd) != 3 || cmd[2] != "git log --oneline" {
		t.Errorf("expected shell payload to stay one token, got %v", cmd)
	}
}

func TestBuildCommand_ArgvSplit_RejectedOnShellWrapperPayloadSlot(t *testing.T) {
	// Regression: `command: "sh -c {{args}}"` (no internal
	// quoting) has the IDENTICAL post-splitCommand shape as the
	// already-supported `sh -c '{{args}}'` — a whole placeholder at index
	// 2. Setting argv_split: true on that parameter must be rejected, not
	// silently mis-split: sh -c would otherwise receive only the value's
	// first word as its script, turning every later word into a $0/$1/...
	// positional parameter, while the B20 blocklist lint (which only
	// inspects cmd[2]) would validate just that truncated first word.
	def := &config.ToolDefinition{
		Name:    "sh-tool",
		Command: "sh -c {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true, ArgvSplit: true},
		},
	}
	tool := NewTool(def)
	_, err := tool.buildCommand(map[string]interface{}{
		"args": "echo hi; rm -rf /tmp/whatever",
	})
	if err == nil {
		t.Fatal("expected an error rejecting argv_split on a sh -c script argument, got none")
	}
	if !strings.Contains(err.Error(), "argv_split") {
		t.Errorf("error should mention argv_split, got: %v", err)
	}
}

func TestBuildCommand_ArgvSplit_RejectedOnFullPathShellWrapper(t *testing.T) {
	// Round-2 automated-review finding: the first version of this
	// guard only recognized a bare "sh"/"bash" as command[0], so a
	// full-path wrapper (a legitimate, common spelling) slipped through
	// undetected. filepath.Base normalization (matching isCommandAllowed's
	// existing pattern) must catch these too.
	for _, shell := range []string{"/bin/sh", "/usr/bin/bash"} {
		t.Run(shell, func(t *testing.T) {
			def := &config.ToolDefinition{
				Name:    "sh-tool",
				Command: shell + " -c {{args}}",
				Parameters: map[string]config.Parameter{
					"args": {Type: "string", Required: true, ArgvSplit: true},
				},
			}
			tool := NewTool(def)
			_, err := tool.buildCommand(map[string]interface{}{"args": "echo hello"})
			if err == nil {
				t.Fatalf("expected an error rejecting argv_split on %s -c, got none", shell)
			}
			if !strings.Contains(err.Error(), "argv_split") {
				t.Errorf("error should mention argv_split, got: %v", err)
			}
		})
	}
}

func TestBuildCommand_ArgvSplit_RejectedOnTemplatedInterpreter(t *testing.T) {
	// Found by rakitsu-reviewer while rebuilding the public-port
	// representative branch: the guard used to decide whether index 2 is a
	// sh -c/bash -c script slot compared the RAW, unsubstituted template
	// (t.command[0]/t.command[1]) against "sh"/"bash". A command that
	// templates the interpreter itself — `command: "{{shell}} -c
	// {{args}}"` — has t.command[0] == "{{shell}}", which never literally
	// equals "sh"/"bash", so the guard silently never fired even when
	// shell resolved to "sh" at runtime. That let argv_split through on
	// exactly the dangerous slot the guard exists to reject, reproducing
	// the truncated-script/$0-shift bug: `sh -c "echo hi; rm -rf x"` split
	// into argv became `sh -c echo hi; rm -rf x` — sh's script is just
	// "echo" (truncated), with "hi;", "rm", "-rf", "x" as inert positional
	// parameters instead of executed shell text.
	//
	// The fix checks the ALREADY-SUBSTITUTED cmd[0]/cmd[1] instead of the
	// raw template, so this is caught regardless of whether the
	// interpreter name is a literal or itself templated.
	def := &config.ToolDefinition{
		Name:    "templated-shell",
		Command: "{{shell}} -c {{args}}",
		Parameters: map[string]config.Parameter{
			"shell": {Type: "string", Required: true},
			"args":  {Type: "string", Required: true, ArgvSplit: true},
		},
	}
	tool := NewTool(def)
	_, err := tool.buildCommand(map[string]interface{}{
		"shell": "sh",
		"args":  "echo hi; rm -rf /tmp/whatever",
	})
	if err == nil {
		t.Fatal("expected an error rejecting argv_split once {{shell}} resolves to sh, got none")
	}
	if !strings.Contains(err.Error(), "argv_split") {
		t.Errorf("error should mention argv_split, got: %v", err)
	}

	// A shell parameter that resolves to something other than sh/bash must
	// NOT be rejected — the guard is specifically about the sh -c/bash -c
	// shape, not templated interpreters in general.
	cmd, err := tool.buildCommand(map[string]interface{}{
		"shell": "python3",
		"args":  "print(1)",
	})
	if err != nil {
		t.Fatalf("expected a non-sh/bash resolved interpreter to be allowed, got error: %v", err)
	}
	if len(cmd) < 2 || cmd[0] != "python3" || cmd[1] != "-c" {
		t.Errorf("expected python3 -c ... to build normally, got %v", cmd)
	}
}

func TestBuildCommand_ShellPayloadBlocklistLint_RecognizesFullPathWrapper(t *testing.T) {
	// Same full-path normalization gap, but in the pre-existing B20
	// blocklist lint itself (found alongside the argv_split guard's
	// version of this bug): a `/bin/sh -c '...'` payload containing a
	// blocked command must still be caught, not just a bare `sh -c '...'`.
	def := &config.ToolDefinition{
		Name:    "sh-tool",
		Command: "/bin/sh -c '{{args}}'",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	_, err := tool.buildCommand(map[string]interface{}{
		"args": "echo hi; rm -rf /tmp/whatever",
	})
	if err == nil {
		t.Fatal("expected the B20 lint to catch a blocked command behind /bin/sh -c, got none")
	}
	if !strings.Contains(err.Error(), "rm") {
		t.Errorf("error should mention the blocked command, got: %v", err)
	}
}

func TestBuildCommand_ArgvSplit_EmptyValue_YieldsNoExtraArgvElements(t *testing.T) {
	// Documents the intended (asymmetric-with-non-split) behavior of
	// argv_split with an empty value: splitCommand("") returns zero
	// tokens, so nothing is appended for that part — unlike the
	// non-split path (TestBuildCommand_WholePartPlaceholder_
	// EmptyValuePreservedAsArgvElement), which always keeps one empty
	// argv element. This is a deliberate decision (an empty free-text
	// argument string means "no extra args"), not an accident.
	def := &config.ToolDefinition{
		Name:    "gh",
		Command: "gh {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true, ArgvSplit: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{"args": ""})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	if len(cmd) != 1 || cmd[0] != "gh" {
		t.Errorf("expected argv_split with an empty value to add no extra elements, got %v", cmd)
	}
}

func TestBuildCommand_ArgvSplit_ResolvesNestedPlaceholderInValue(t *testing.T) {
	// Regression found by review on SK-ENT/rakitsu#75: the
	// pre-existing per-part loop re-scans a substituted string for further
	// {{...}} patterns, so a value that itself contains {{other_param}}
	// syntax is recursively resolved on the non-split path (see
	// TestBuildCommand_Placeholder-adjacent behavior). The argv_split path
	// broke out immediately after its first substitution and split the raw,
	// still-templated value, so the literal string "{{name}}" reached the
	// executed command instead of being resolved. Verify argv_split now
	// matches the non-split path for the same indirection.
	def := &config.ToolDefinition{
		Name:    "echo-tool",
		Command: "echo {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true, ArgvSplit: true},
			"name": {Type: "string", Required: false},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{
		"args": "{{name}}",
		"name": "value",
	})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	found := false
	for _, c := range cmd {
		if c == "{{name}}" {
			t.Fatalf("unresolved placeholder leaked into argv: %v", cmd)
		}
		if c == "value" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the nested placeholder resolved to \"value\", got %v", cmd)
	}
}

func TestBuildCommand_SelfReferentialValue_TerminatesInsteadOfHanging(t *testing.T) {
	// MAJOR finding, review on SK-ENT/rakitsu#75: a caller passing the
	// literal string "{{name}}" as the VALUE of parameter "name" makes the
	// substitution a no-op (replacing "{{name}}" with "{{name}}"), so the
	// old unbounded re-scan loop never terminated. This is
	// caller/agent-controlled input, not a config-authoring mistake — a
	// real DoS surface, and it hung even the pre-existing non-argv_split
	// path. resolvePlaceholders' iteration bound must stop it. Verified
	// independently (not just trusting the review) with a timeout-guarded
	// repro before writing this fix.
	//
	// Once bounded, this input exhausts maxPlaceholderResolutionPasses with
	// "{{name}}" still present (the self-reference never actually resolves
	// to anything else) — resolvePlaceholders now surfaces that as an error
	// rather than silently returning the untouched literal text, so
	// buildCommand must return an error too, not a "successful" command
	// containing "{{name}}" as if that were an intended argv element.
	def := &config.ToolDefinition{
		Name:    "echo-tool",
		Command: "echo {{name}}",
		Parameters: map[string]config.Parameter{
			"name": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	done := make(chan struct{})
	var cmd []string
	var err error
	go func() {
		cmd, err = tool.buildCommand(map[string]interface{}{"name": "{{name}}"})
		close(done)
	}()
	select {
	case <-done:
		if err == nil {
			t.Fatalf("expected an error once resolution passes are exhausted, got a command instead: %v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HUNG: self-referential placeholder value did not terminate")
	}
}

func TestBuildCommand_WhitespacePaddedPlaceholderValue_TerminatesInsteadOfHanging(t *testing.T) {
	// MAJOR finding, review on SK-ENT/rakitsu#75: resolvePlaceholders
	// used to look up the TRIMMED placeholder name but then reconstruct
	// "{{"+trimmed+"}}" as the search-and-replace target — for a value
	// containing internal whitespace like "{{ name }}", that reconstructed
	// string never actually occurs, so the replacement was a silent no-op
	// and the re-scan loop never terminated. Reproduced only through
	// argv_split's resolvePlaceholders call (a raw runtime value isn't
	// whitespace-tokenized by splitCommand the way a static command
	// template already is). Verified independently before writing the fix.
	def := &config.ToolDefinition{
		Name:    "gh",
		Command: "gh {{args}}",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true, ArgvSplit: true},
			"name": {Type: "string"},
		},
	}
	tool := NewTool(def)
	done := make(chan struct{})
	var cmd []string
	var err error
	go func() {
		cmd, err = tool.buildCommand(map[string]interface{}{"args": "{{ name }}", "name": "value"})
		close(done)
	}()
	select {
	case <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		joined := strings.Join(cmd, " ")
		if !strings.Contains(joined, "value") {
			t.Errorf("expected the whitespace-padded placeholder resolved to \"value\", got %v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HUNG: whitespace-padded placeholder value did not terminate")
	}
}

func TestBuildCommand_ExpandingSelfReference_BoundedNotExhausted(t *testing.T) {
	// Second MAJOR finding on the same review round: the pass bound alone
	// stops an infinite loop but not an EXPANDING one. A value like
	// name: "{{name}}{{name}}" doubles the number of matched placeholders
	// every pass (strings.Replace(-1) replaces every occurrence at once),
	// so within maxPlaceholderResolutionPasses the string balloons past any
	// reasonable size — a caller-controlled memory-exhaustion DoS, not just
	// a hang. Verified independently: this did not finish within 5 seconds
	// before the fix. maxPlaceholderResolutionLength must cap the growth.
	//
	// Once bounded, the size guard trips and stops resolution before it
	// would otherwise finish — resolvePlaceholders now surfaces that as an
	// error, so buildCommand must return an error rather than a "successful"
	// command containing a partially-expanded, memory-bounded-but-still-
	// wrong argv element.
	def := &config.ToolDefinition{
		Name:    "echo-tool",
		Command: "echo {{name}}",
		Parameters: map[string]config.Parameter{
			"name": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	done := make(chan struct{})
	var cmd []string
	var err error
	go func() {
		cmd, err = tool.buildCommand(map[string]interface{}{"name": "{{name}}{{name}}"})
		close(done)
	}()
	select {
	case <-done:
		if err == nil {
			t.Fatalf("expected an error once the size guard bounds the growth, got a command instead: %v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HUNG or memory-exhausting: expanding self-reference not bounded")
	}
}

func TestResolvePlaceholders_ProjectedSizeGuardsAgainstOverflow(t *testing.T) {
	// Third MAJOR finding on the same review round: the projected-size
	// calculation (count * (len(valStr) - len(raw))) could in principle
	// wrap an int for an astronomically large s/valStr — reaching that in
	// practice would need gigabytes already in memory before this code
	// runs, well past any realistic tool-call payload, but the guard is
	// cheap to add regardless of reachability. Verify count and valStr
	// length are bounded independently BEFORE the multiplication, using
	// inputs sized to actually exercise that guard (not gigabytes — this
	// only needs to prove the bounding logic runs, not reproduce a real
	// overflow, which isn't achievable in a test process anyway).
	raw := "{{p}}"
	// A string with more occurrences of raw than maxPlaceholderResolutionLength
	// permits, forcing the "count too large" branch of the guard.
	oversizedS := strings.Repeat(raw, maxPlaceholderResolutionLength+1)
	got, err := resolvePlaceholders(oversizedS, map[string]interface{}{"p": "x"})
	if got != oversizedS {
		t.Errorf("expected an already-oversized s to be left untouched (guard bails before replacing), got a %d-byte result", len(got))
	}
	if err == nil {
		t.Error("expected an error when the size guard bails, got nil")
	}

	// A single replacement value already larger than the cap, forcing the
	// "valStr too large" branch of the guard.
	largeVal := strings.Repeat("x", maxPlaceholderResolutionLength+1)
	got2, err2 := resolvePlaceholders("{{p}}", map[string]interface{}{"p": largeVal})
	if got2 != "{{p}}" {
		t.Errorf("expected an oversized replacement value to be rejected before replacing, got a %d-byte result", len(got2))
	}
	if err2 == nil {
		t.Error("expected an error when the size guard bails, got nil")
	}
}

func TestResolvePlaceholders_ProjectedSizeGuardUsesInt64Arithmetic(t *testing.T) {
	// Fix: the projected-size check now computes
	// count*(len(valStr)-len(raw)) with explicit int64 arithmetic rather
	// than the platform's native int width. A genuine 32-bit overflow of
	// that product can't actually be constructed: count, len(valStr) and
	// len(s) are each independently capped at maxPlaceholderResolutionLength
	// (65536), and count is itself bounded by len(s)/len(raw) — so pushing
	// count and len(valStr) both toward the cap forces len(s) to already
	// exceed the SAME cap, which trips the earlier per-factor guard first.
	// That's why the fix is "cheap to close outright" rather than fixing a
	// reachable bug (see the fix commit's own message).
	//
	// This test instead isolates the projected-size multiplication branch
	// itself: every individual factor (count, len(valStr), len(s)) stays
	// well under the per-factor cap, so none of those guards fire, and only
	// the projected multiplication (len(s) + count*(len(valStr)-len(raw)))
	// exceeding the cap causes the rejection. This proves the multiplication
	// path is reached and evaluated correctly — a lower-severity but honest
	// substitute for an unreachable true overflow scenario.
	raw := "{{p}}"
	s := strings.Repeat(raw, 100) // count = 100, len(s) = 500 — both far under the cap
	val := strings.Repeat("x", 2000) // len(valStr) = 2000 — also far under the cap
	// projected = 500 + 100*(2000-5) = 199,500, well over the 65536 cap,
	// purely from the multiplication — none of the per-factor checks alone
	// would reject this input.
	got, err := resolvePlaceholders(s, map[string]interface{}{"p": val})
	if got != s {
		t.Errorf("expected the projected-size multiplication guard to reject before replacing, got a %d-byte result", len(got))
	}
	if err == nil {
		t.Error("expected an error when the projected-size guard bails, got nil")
	}
}

func TestBuildCommand_AdditionalArgs_DeterministicOrder(t *testing.T) {
	// t.parameters is a map; iterating it directly randomizes argv order
	// across runs. Verify the built command is stable regardless of
	// declaration order.
	def := &config.ToolDefinition{
		Name:    "tool",
		Command: "ls",
		Parameters: map[string]config.Parameter{
			"zeta":  {Type: "string", Required: false},
			"alpha": {Type: "string", Required: false},
			"mid":   {Type: "string", Required: false},
		},
	}
	tool := NewTool(def)
	input := map[string]interface{}{"zeta": "z", "alpha": "a", "mid": "m"}
	want, err := tool.buildCommand(input)
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := tool.buildCommand(input)
		if err != nil {
			t.Fatalf("buildCommand failed: %v", err)
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("nondeterministic argv order: run 0 got %v, run %d got %v", want, i, got)
		}
	}
	joined := strings.Join(want, " ")
	ai, mi, zi := strings.Index(joined, "alpha=a"), strings.Index(joined, "mid=m"), strings.Index(joined, "zeta=z")
	if !(ai < mi && mi < zi) {
		t.Errorf("expected alphabetical flag order, got %q", joined)
	}
}

func TestBuildCommand_OversizedValue_ReturnsErrorInsteadOfLiteralPlaceholder(t *testing.T) {
	// Found by rakitsu-reviewer while rebuilding the public-port
	// representative branch: a legitimate value merely large enough to trip
	// resolvePlaceholders' size guard (maxPlaceholderResolutionLength, 64
	// KiB — a large diff, a JSON payload, file contents) used to be left
	// UNRESOLVED as the literal, unexpanded "{{name}}" text in the built
	// command, with buildCommand returning a nil error. That's silently
	// executing the WRONG command (the literal placeholder string) instead
	// of failing loudly — a materially different, worse failure mode than
	// "config author forgot to pass this parameter", which is the ONLY
	// case this graceful degradation was originally meant to cover.
	// Verified independently before fixing: this returned (cmd, nil) with
	// cmd[1] == "{{msg}}" for a 100KB value.
	def := &config.ToolDefinition{
		Name:    "echo-tool",
		Command: "echo {{msg}}",
		Parameters: map[string]config.Parameter{
			"msg": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	big := strings.Repeat("x", maxPlaceholderResolutionLength+1)
	cmd, err := tool.buildCommand(map[string]interface{}{"msg": big})
	if err == nil {
		t.Fatalf("expected an error for an oversized value, got a command instead: cmd[1] is literal placeholder text: %v", cmd != nil && len(cmd) > 1 && cmd[1] == "{{msg}}")
	}
}

func TestBuildCommand_UnrecognizedPlaceholder_LeftLiteralWithNoError(t *testing.T) {
	// Guards against over-broadening the fix above: an "unrecognized
	// placeholder" (no matching entry in args at all — the pre-existing,
	// genuinely intentional graceful-degradation case, e.g. a stray
	// {{typo}} left over from editing a command template) must still be
	// left as literal text with NO error, same as before this fix. Only a
	// SAFETY BOUND being tripped (the new failure mode) should error — a
	// simply-missing parameter is a config-authoring situation that has
	// always degraded gracefully and must keep doing so.
	def := &config.ToolDefinition{
		Name:    "echo-tool",
		Command: "echo {{typo}}",
		Parameters: map[string]config.Parameter{
			"typo": {Type: "string"},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{})
	if err != nil {
		t.Fatalf("expected no error for an unrecognized/unprovided placeholder, got: %v", err)
	}
	if len(cmd) != 2 || cmd[1] != "{{typo}}" {
		t.Errorf("expected the unrecognized placeholder left as literal text, got %v", cmd)
	}
}

func TestResolvePlaceholders_PassExhaustionEndingInUnrecognizedPlaceholder_NoError(t *testing.T) {
	// Found by automated review, verified independently before
	// fixing: a chain of exactly maxPlaceholderResolutionPasses resolvable
	// placeholders (p0 -> {{p1}} -> {{p2}} -> ... -> {{p32}}), where p32 is
	// NOT in args, needs exactly 32 passes to unwind down to the literal
	// "{{p32}}" — exhausting the pass budget in the SAME iteration that
	// would otherwise gracefully recognize p32 as unresolvable. The
	// post-loop bound check must classify this the same way the in-loop
	// check would have: p32 is genuinely unrecognized, so this is the
	// pre-existing, intentional no-error graceful-degradation case, NOT a
	// bound trip — naively treating any leftover "{{" as a bound trip
	// mis-classified this and made buildCommand fail on input that was
	// never actually hitting a resolution bound.
	args := map[string]interface{}{}
	for i := 0; i < 32; i++ {
		args[fmt.Sprintf("p%d", i)] = fmt.Sprintf("{{p%d}}", i+1)
	}
	got, err := resolvePlaceholders("{{p0}}", args)
	if err != nil {
		t.Fatalf("expected no error (final placeholder genuinely unrecognized), got: %v", err)
	}
	if got != "{{p32}}" {
		t.Errorf("expected {{p32}} left literal, got %q", got)
	}
}

func TestResolvePlaceholders_PassExhaustionEndingInResolvablePlaceholder_StillErrors(t *testing.T) {
	// Same shape as the test above, but the final placeholder in the chain
	// IS present in args — a genuinely resolvable placeholder the pass cap
	// prevented resolution from ever reaching. This must still return an
	// error: unlike the sibling test, there's no legitimate "it was always
	// going to stop here anyway" story — the chain just needed one more
	// pass than the budget allows.
	args := map[string]interface{}{}
	for i := 0; i < 32; i++ {
		args[fmt.Sprintf("p%d", i)] = fmt.Sprintf("{{p%d}}", i+1)
	}
	args["p32"] = "final-value"
	_, err := resolvePlaceholders("{{p0}}", args)
	if err == nil {
		t.Fatal("expected an error (a resolvable placeholder was cut off by the pass cap), got nil")
	}
}

func TestResolvePlaceholders_FinalPassIntroducesUnrecognizedAndResolvableTogether(t *testing.T) {
	// Found by a second automated-review round, verified
	// independently before fixing: the post-loop classification only
	// checked the LEFTMOST remaining {{...}} occurrence. A single
	// substitution on the FINAL pass can introduce more than one new
	// placeholder at once — here, p31's value contains BOTH an
	// unrecognized placeholder ({{unknown}}) AND a genuinely resolvable
	// one ({{p32}}) that simply never got its turn because the pass
	// budget ran out right after producing them. Checking only the first
	// occurrence sees {{unknown}}, concludes "graceful stop", and misses
	// that {{p32}} was cut off. The fix scans every remaining occurrence:
	// only when NONE of them are recognized is this the pre-existing
	// graceful no-error stop; finding even one recognized placeholder
	// means a bound was genuinely exceeded.
	//
	// Constructed so the mixed unknown+resolvable string is produced on
	// EXACTLY the 32nd (last) pass, with no further iteration available to
	// re-inspect it via the in-loop check: 31 links (p0..p30) unwind
	// "{{p0}}" down to "{{p31}}" over 31 passes, and the 32nd pass resolves
	// p31's value into "{{unknown}} {{p32}}" — right as the loop's pass
	// budget is exhausted.
	args := map[string]interface{}{}
	for i := 0; i < 31; i++ {
		args[fmt.Sprintf("p%d", i)] = fmt.Sprintf("{{p%d}}", i+1)
	}
	args["p31"] = "{{unknown}} {{p32}}"
	args["p32"] = "final-value"
	got, err := resolvePlaceholders("{{p0}}", args)
	if err == nil {
		t.Fatalf("expected an error: {{p32}} is recognized but was cut off by the pass cap, even though {{unknown}} appears first; got %q with no error", got)
	}
}

func TestBuildCommand_WhitespacePaddedPlaceholder_NoDuplicateArg(t *testing.T) {
	// Regression: resolvePlaceholders trims a placeholder's
	// name before looking it up (so `{{ name }}` correctly substitutes),
	// but the "was this parameter already consumed by the template"
	// check used to search for the literal substring "{{name}}" and
	// missed the whitespace-padded spelling — appending a redundant
	// --name=value on top of the already-substituted value.
	def := &config.ToolDefinition{
		Name:    "echo-shell",
		Command: "sh -c 'echo {{ name }}'",
		Parameters: map[string]config.Parameter{
			"name": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{"name": "value"})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	if len(cmd) != 3 || cmd[2] != "echo value" {
		t.Fatalf("expected substituted shell payload only, got %v", cmd)
	}
	for _, part := range cmd {
		if strings.Contains(part, "--name=") {
			t.Errorf("name should not be appended again as a flag, got %v", cmd)
		}
	}
}

func TestReferencesPlaceholder_TrimsInternalWhitespace(t *testing.T) {
	cases := []struct {
		part string
		name string
		want bool
	}{
		{"echo {{name}}", "name", true},
		{"echo {{ name }}", "name", true},
		{"echo {{  name  }}", "name", true},
		{"echo {{other}}", "name", false},
		{"echo literal text", "name", false},
		{"echo {{other}} {{ name }}", "name", true},
	}
	for _, c := range cases {
		if got := referencesPlaceholder(c.part, c.name); got != c.want {
			t.Errorf("referencesPlaceholder(%q, %q) = %v, want %v", c.part, c.name, got, c.want)
		}
	}
}

// ============================================================
// GetParametersSchema
// ============================================================

func TestGetParametersSchema_Structure(t *testing.T) {
	def := &config.ToolDefinition{
		Name:    "tool",
		Command: "ls",
		Parameters: map[string]config.Parameter{
			"path":   {Type: "string", Description: "dir path", Required: true},
			"format": {Type: "string", Description: "output format", Required: false},
		},
	}
	tool := NewTool(def)
	schema := tool.GetParametersSchema()

	if schema["type"] != "object" {
		t.Errorf("schema type should be 'object', got %v", schema["type"])
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema.properties should be map[string]interface{}")
	}
	if _, ok := props["path"]; !ok {
		t.Error("schema.properties should contain 'path'")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema.required should be []string")
	}
	found := false
	for _, r := range required {
		if r == "path" {
			found = true
		}
	}
	if !found {
		t.Error("'path' should be in required list")
	}
}

// ============================================================
// Integration — real execution (safe commands only)
// ============================================================

func TestExecute_LS_Success(t *testing.T) {
	tool := newTool("ls", nil)
	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("ls should succeed: %v", err)
	}
	if out == "" {
		t.Error("ls should produce non-empty output")
	}
}

func TestExecute_ContextCancelled(t *testing.T) {
	tool := newTool("ls", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	// Command may still succeed (ls is fast) or fail — just verify no panic
	_, _ = tool.Execute(ctx, map[string]interface{}{})
}

// ============================================================
// B20 regression — shell-wrapper command templates
// ============================================================

func TestSplitCommand_SimpleWhitespace(t *testing.T) {
	got := splitCommand("git log --oneline")
	want := []string{"git", "log", "--oneline"}
	if len(got) != len(want) {
		t.Fatalf("expected %d parts, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("part[%d]: expected %q, got %q", i, w, got[i])
		}
	}
}

func TestSplitCommand_ShellWrapper(t *testing.T) {
	// The exact pattern from examples/single/02-single-agent/config.yaml and
	// examples/dogfood/01-weekly-status/config.yaml
	got := splitCommand("sh -c 'git {{args}}'")
	want := []string{"sh", "-c", "git {{args}}"}
	if len(got) != len(want) {
		t.Fatalf("expected %d parts, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("part[%d]: expected %q, got %q", i, w, got[i])
		}
	}
}

func TestSplitCommand_NestedDoubleQuotesInSingle(t *testing.T) {
	// The payload inside single quotes can contain double quotes untouched —
	// this is what lets `git log --since="7 days ago"` survive templating.
	got := splitCommand(`sh -c 'git log --since="7 days ago" --oneline'`)
	want := []string{"sh", "-c", `git log --since="7 days ago" --oneline`}
	if len(got) != len(want) {
		t.Fatalf("expected %d parts, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("part[%d]: expected %q, got %q", i, w, got[i])
		}
	}
}

func TestBuildCommand_ShellWrapperPlaceholder(t *testing.T) {
	// B20 regression: confirm that {{args}} substitution happens on the
	// shell payload slot (not on the split pieces of the payload).
	def := &config.ToolDefinition{
		Name:    "git-shell",
		Command: "sh -c 'git {{args}}'",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	cmd, err := tool.buildCommand(map[string]interface{}{
		"args": `log --since="7 days ago" --oneline`,
	})
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	if len(cmd) != 3 {
		t.Fatalf("expected 3 parts (sh, -c, payload), got %d: %v", len(cmd), cmd)
	}
	if cmd[0] != "sh" {
		t.Errorf("cmd[0]: expected %q, got %q", "sh", cmd[0])
	}
	if cmd[1] != "-c" {
		t.Errorf("cmd[1]: expected %q, got %q", "-c", cmd[1])
	}
	expected := `git log --since="7 days ago" --oneline`
	if cmd[2] != expected {
		t.Errorf("cmd[2]: expected %q, got %q", expected, cmd[2])
	}
}

func TestIsCommandAllowed_ShAndBashInWhitelist(t *testing.T) {
	// B20: sh and bash must be in the system whitelist so that
	// `sh -c '...'` command templates work as documented.
	tool := newTool("sh", nil)
	if !tool.isCommandAllowed("sh") {
		t.Error("sh should be in system whitelist (needed for shell-wrapper command templates)")
	}
	if !tool.isCommandAllowed("bash") {
		t.Error("bash should be in system whitelist (needed for shell-wrapper command templates)")
	}
}

func TestBuildCommand_ShellPayloadBlocklistLint(t *testing.T) {
	// B20 mitigation: when the shell payload (after {{args}} substitution)
	// contains a blocked command name as a standalone token, reject it.
	def := &config.ToolDefinition{
		Name:    "git-shell",
		Command: "sh -c 'git {{args}}'",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	_, err := tool.buildCommand(map[string]interface{}{
		"args": `log --oneline; rm -rf /tmp/whatever`,
	})
	if err == nil {
		t.Fatal("expected error for shell payload containing blocked command 'rm'")
	}
	if !strings.Contains(err.Error(), "blocked") && !strings.Contains(err.Error(), "rm") {
		t.Errorf("error should mention the blocked command, got: %v", err)
	}
}

func TestBuildCommand_ShellPayloadBlocklistAllowsSubstrings(t *testing.T) {
	// The lint should match standalone command tokens, not substrings inside args.
	// `git log --grep "arm"` contains "rm" as a substring of "arm" but is NOT
	// invoking rm — the lint must not reject this.
	def := &config.ToolDefinition{
		Name:    "git-shell",
		Command: "sh -c 'git {{args}}'",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)
	_, err := tool.buildCommand(map[string]interface{}{
		"args": `log --grep "arm64"`,
	})
	if err != nil {
		t.Errorf("substring 'rm' inside 'arm64' should not trigger blocklist: %v", err)
	}
}

func TestBuildCommand_ShellPayloadBlocklistAllowsSubcommands(t *testing.T) {
	// Regression: git's own subcommands share a name with a blocked
	// standalone command ("init", "mv") -- the lint must only check the
	// first word of each command segment, not every token in the payload.
	// Found live: dogfood scenario 14's bootstrap step ran `git init -q`
	// and got rejected with "shell payload contains blocked command init",
	// even though "init" here is git's subcommand, not the system command.
	def := &config.ToolDefinition{
		Name:    "git-shell",
		Command: "sh -c 'git {{args}}'",
		Parameters: map[string]config.Parameter{
			"args": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)

	cases := []string{
		"init -q",
		"mv old.txt new.txt",
	}
	for _, args := range cases {
		if _, err := tool.buildCommand(map[string]interface{}{"args": args}); err != nil {
			t.Errorf("git subcommand %q should not trigger blocklist: %v", args, err)
		}
	}
}

func TestBuildCommand_ShellPayloadBlocklistCatchesStandaloneAcrossSeparators(t *testing.T) {
	// The fix for the subcommand false positive must not weaken detection
	// of a genuinely standalone blocked command after any shell separator.
	def := &config.ToolDefinition{
		Name:    "sh-tool",
		Command: "sh -c '{{cmd}}'",
		Parameters: map[string]config.Parameter{
			"cmd": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)

	cases := map[string]string{
		"leading semicolon":  "echo hi; rm -rf /tmp/x",
		"leading &&":         "echo hi && rm -rf /tmp/x",
		"leading pipe":       "cat file | rm -rf /tmp/x",
		"sole invocation":    "rm -rf /tmp/x",
		"inside a subshell":  "(cd /tmp && rm -rf x)",
		"backtick expansion": "echo `rm -rf /tmp/x`",
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tool.buildCommand(map[string]interface{}{"cmd": cmd})
			if err == nil {
				t.Fatalf("expected blocked-command error for %q, got none", cmd)
			}
			if !strings.Contains(err.Error(), "rm") {
				t.Errorf("error should mention 'rm', got: %v", err)
			}
		})
	}
}

func TestBuildCommand_ShellPayloadBlocklistCatchesReExecWrappers(t *testing.T) {
	// Regression: restricting the lint to a segment's first word (the fix
	// above) stopped it from catching a blocked command hiding behind a
	// wrapper that re-invokes a later word as the actual command to run —
	// unlike `git mv`, where "mv" is git's own subcommand and never
	// executes as a standalone command.
	def := &config.ToolDefinition{
		Name:    "sh-tool",
		Command: "sh -c '{{cmd}}'",
		Parameters: map[string]config.Parameter{
			"cmd": {Type: "string", Required: true},
		},
	}
	tool := NewTool(def)

	cases := map[string]string{
		"xargs":      "xargs rm -rf /tmp/x",
		"find -exec": "find . -exec rm -rf {} +",
		"env":        "env rm -rf /tmp/x",
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tool.buildCommand(map[string]interface{}{"cmd": cmd})
			if err == nil {
				t.Fatalf("expected blocked-command error for %q, got none", cmd)
			}
			if !strings.Contains(err.Error(), "rm") {
				t.Errorf("error should mention 'rm', got: %v", err)
			}
		})
	}
}

// ============================================================
// Stress — concurrent Execute calls
// ============================================================

func TestStress_ConcurrentExecute(t *testing.T) {
	tool := newTool("ls", nil)
	const N = 30
	var wg sync.WaitGroup
	var successes atomic.Int64

	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := tool.Execute(context.Background(), map[string]interface{}{})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != N {
		t.Errorf("expected %d successes, got %d", N, got)
	}
}

func TestStress_ConcurrentSecurityChecks(t *testing.T) {
	tool := newTool("ls", nil)
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				tool.isCommandAllowed("ls")
			} else {
				tool.isCommandAllowed("rm")
			}
		}(i)
	}
	wg.Wait()
}

// ============================================================
// B15 regression — cli tool output truncation
// ============================================================

func TestTruncateOutput_BelowCap(t *testing.T) {
	in := "short output"
	if got := truncateOutput(in, 100); got != in {
		t.Errorf("output below cap should pass through, got %q", got)
	}
}

func TestTruncateOutput_DisabledWithNegative(t *testing.T) {
	in := strings.Repeat("x", 50000)
	if got := truncateOutput(in, -1); got != in {
		t.Errorf("negative cap should disable truncation")
	}
}

func TestTruncateOutput_HardCut(t *testing.T) {
	in := strings.Repeat("x", 10000) // no newlines in last 200 bytes
	got := truncateOutput(in, 8192)
	if len(got) >= len(in) {
		t.Errorf("truncated output should be shorter than original")
	}
	if !strings.Contains(got, "[truncated") {
		t.Errorf("truncation marker missing: %q", got[len(got)-100:])
	}
	if !strings.Contains(got, "original 10000 bytes") {
		t.Errorf("original size not reported: %q", got[len(got)-100:])
	}
}

func TestTruncateOutput_PrefersLineBoundary(t *testing.T) {
	// Build 8200 bytes with a newline at byte 8100 (within 200 of the 8192 cap)
	head := strings.Repeat("a", 8100)
	tail := "\n" + strings.Repeat("b", 99)
	in := head + tail
	got := truncateOutput(in, 8192)
	// The preserved prefix should end exactly at the newline boundary (8100),
	// not mid-line at 8192.
	prefix := strings.SplitN(got, "\n...", 2)[0]
	if len(prefix) != 8100 {
		t.Errorf("expected line-boundary cut at 8100, got prefix len %d", len(prefix))
	}
}

func TestExecute_OutputTruncatedAtDefault(t *testing.T) {
	// `yes` spews forever — pipe through head via sh -c to produce a deterministic
	// oversized output, then rely on our post-exec truncation to cap it.
	def := &config.ToolDefinition{
		Name:    "spew",
		Command: "sh -c 'yes rakitsu | head -c 50000'",
	}
	tool := NewTool(def)
	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("spew command should succeed: %v", err)
	}
	if len(out) > DefaultMaxOutputBytes+200 {
		t.Errorf("output not truncated: len=%d, cap=%d", len(out), DefaultMaxOutputBytes)
	}
	if !strings.Contains(out, "[truncated") {
		t.Errorf("truncation marker missing from oversized output")
	}
}

func TestExecute_OutputTruncationRespectsConfigOverride(t *testing.T) {
	def := &config.ToolDefinition{
		Name:    "spew",
		Command: "sh -c 'yes rakitsu | head -c 5000'",
		Sandbox: &config.SandboxConfig{
			Type: "local_restricted",
			ResourceLimits: config.ResourceLimits{
				MaxOutputBytes: 1024,
			},
		},
	}
	tool := NewTool(def)
	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("spew command should succeed: %v", err)
	}
	if len(out) > 1024+200 {
		t.Errorf("output should be capped at 1024, got len=%d", len(out))
	}
	if !strings.Contains(out, "[truncated") {
		t.Errorf("truncation marker missing")
	}
}

// ============================================================
// Environment scrubbing — a spawned tool subprocess must never be able to
// read back the rakitsu-serve control-plane secrets out of its own env.
// ============================================================

func TestExecute_ScrubsSensitiveServerEnvVars(t *testing.T) {
	t.Setenv("RAKITSU_API_TOKEN", "supersecret-api-token")
	t.Setenv("RAKITSU_SESSION_MSG_TOKEN", "supersecret-msg-token")
	t.Setenv("HARMLESS_TEST_VAR", "should-still-be-visible")

	def := &config.ToolDefinition{
		Name:    "leak-check",
		Command: `sh -c 'echo "API=$RAKITSU_API_TOKEN MSG=$RAKITSU_SESSION_MSG_TOKEN OK=$HARMLESS_TEST_VAR"'`,
		Sandbox: &config.SandboxConfig{Type: "local_restricted"},
	}
	tool := NewTool(def)
	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("command should succeed: %v", err)
	}
	if strings.Contains(out, "supersecret-api-token") {
		t.Errorf("RAKITSU_API_TOKEN leaked into subprocess env, got: %q", out)
	}
	if strings.Contains(out, "supersecret-msg-token") {
		t.Errorf("RAKITSU_SESSION_MSG_TOKEN leaked into subprocess env, got: %q", out)
	}
	if !strings.Contains(out, "should-still-be-visible") {
		t.Errorf("unrelated env vars should still pass through to the subprocess, got: %q", out)
	}
}

func TestExecute_OutputTruncationDisabledWithNegativeOne(t *testing.T) {
	def := &config.ToolDefinition{
		Name:    "spew",
		Command: "sh -c 'yes rakitsu | head -c 20000'",
		Sandbox: &config.SandboxConfig{
			Type: "local_restricted",
			ResourceLimits: config.ResourceLimits{
				MaxOutputBytes: -1,
			},
		},
	}
	tool := NewTool(def)
	out, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("spew command should succeed: %v", err)
	}
	if len(out) < 20000 {
		t.Errorf("output should not be truncated, got len=%d", len(out))
	}
	if strings.Contains(out, "[truncated") {
		t.Errorf("truncation marker should not be present")
	}
}

func TestBuildDockerArgs_HardensContainer(t *testing.T) {
	sandbox := &config.SandboxConfig{
		Type:            "docker",
		MountWorkdir:    true,
		NetworkIsolated: true,
	}
	args := buildDockerArgs(sandbox, []string{"echo", "hi"}, "")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--read-only") {
		t.Errorf("docker args missing --read-only, got: %v", args)
	}
	if !strings.Contains(joined, "--security-opt no-new-privileges") {
		t.Errorf("docker args missing --security-opt no-new-privileges, got: %v", args)
	}
	if !strings.Contains(joined, "--tmpfs /tmp") {
		t.Errorf("docker args missing a writable /tmp tmpfs (needed since --read-only locks the rest of the root fs), got: %v", args)
	}
}

// TestBuildDockerArgs_DefaultsHardenFurther: a bare docker sandbox config used to run as root, with every Linux
// capability, no process-count limit, an open network, and a writable
// workdir bind mount.
func TestBuildDockerArgs_DefaultsHardenFurther(t *testing.T) {
	sandbox := &config.SandboxConfig{Type: "docker", MountWorkdir: true}
	args := buildDockerArgs(sandbox, []string{"echo", "hi"}, "")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--cap-drop ALL") {
		t.Errorf("docker args missing --cap-drop ALL, got: %v", args)
	}
	if !strings.Contains(joined, "--user "+DefaultDockerUser) {
		t.Errorf("docker args missing default non-root --user, got: %v", args)
	}
	if !strings.Contains(joined, "--pids-limit "+strconv.Itoa(DefaultPidsLimit)) {
		t.Errorf("docker args missing default --pids-limit, got: %v", args)
	}
	if !strings.Contains(joined, "--network none") {
		t.Errorf("docker args should default to no network, got: %v", args)
	}
	if !containsArgPair(args, "-v", func(v string) bool { return strings.HasSuffix(v, ":ro") }) {
		t.Errorf("workdir bind mount should default to read-only, got: %v", args)
	}
}

// TestBuildDockerArgs_OverridesRespected checks every new hardening knob
// can be explicitly opted out of, since some workloads (npm/pip install,
// a build step that writes into the workdir) legitimately need to.
func TestBuildDockerArgs_OverridesRespected(t *testing.T) {
	sandbox := &config.SandboxConfig{
		Type:                 "docker",
		MountWorkdir:         true,
		MountWorkdirWritable: true,
		AllowNetwork:         true,
		User:                 "1000:1000",
		ResourceLimits:       config.ResourceLimits{PidsLimit: -1},
	}
	args := buildDockerArgs(sandbox, []string{"echo", "hi"}, "")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "--network none") {
		t.Errorf("AllowNetwork should skip --network none, got: %v", args)
	}
	if strings.Contains(joined, "--pids-limit") {
		t.Errorf("PidsLimit: -1 should skip --pids-limit, got: %v", args)
	}
	if !strings.Contains(joined, "--user 1000:1000") {
		t.Errorf("User override not applied, got: %v", args)
	}
	if containsArgPair(args, "-v", func(v string) bool { return strings.HasSuffix(v, ":ro") }) {
		t.Errorf("MountWorkdirWritable should mount workdir read-write, got: %v", args)
	}
}

// containsArgPair reports whether args has flag immediately followed by a
// value matching pred.
func containsArgPair(args []string, flag string, pred func(string) bool) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && pred(args[i+1]) {
			return true
		}
	}
	return false
}

// ============================================================
// working-dir existence — SK-ENT/rakitsu#28
// ============================================================

// TestExecute_MissingSandboxWorkdir_ClearError: when the exec cwd falls back
// to sandbox.AllowedPaths[0] and that directory does not exist, the error
// must name the working directory instead of leaking os/exec's misleading
// "fork/exec <binary>: no such file or directory".
func TestExecute_MissingSandboxWorkdir_ClearError(t *testing.T) {
	tool := newTool("ls", nil, []string{"./does-not-exist-28/"})
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error for missing working directory")
	}
	if strings.Contains(err.Error(), "fork/exec") {
		t.Errorf("raw fork/exec error leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "working directory") {
		t.Errorf("error should name the working directory, got: %v", err)
	}
}

// ============================================================
// allowed_paths — argument path filter
// ============================================================

// allowedFixture returns an allowed directory containing inside.txt, whose
// parent holds outside.txt (the file every escape below tries to reach).
func allowedFixture(t *testing.T) (allowed string, outsideFile string) {
	t.Helper()
	parent := t.TempDir()
	allowed = filepath.Join(parent, "allowed")
	if err := os.MkdirAll(filepath.Join(allowed, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile = filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("marker-outside-483"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowed, "inside.txt"), []byte("marker-inside-483"), 0o644); err != nil {
		t.Fatal(err)
	}
	return allowed, outsideFile
}

var pathParam = map[string]config.Parameter{"path": {Type: "string", Required: true}}

func catTool(cmd string, allowed string) *Tool {
	return newTool(cmd, pathParam, []string{allowed})
}

func assertDenied(t *testing.T, tool *Tool, path string) {
	t.Helper()
	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": path})
	var se *SecurityError
	if !errors.As(err, &se) {
		t.Errorf("path %q: want *SecurityError, got err=%v out=%q", path, err, out)
	}
	if strings.Contains(out, "marker-outside-483") {
		t.Errorf("path %q: outside content leaked: %q", path, out)
	}
}

func assertAllowed(t *testing.T, tool *Tool, path, wantMarker string) {
	t.Helper()
	out, err := tool.Execute(context.Background(), map[string]interface{}{"path": path})
	if err != nil {
		t.Fatalf("path %q: unexpected error: %v", path, err)
	}
	if !strings.Contains(out, wantMarker) {
		t.Errorf("path %q: want %q in output, got %q", path, wantMarker, out)
	}
}

func TestExecute_AllowedPaths_ParentTraversalDenied(t *testing.T) {
	allowed, _ := allowedFixture(t)
	assertDenied(t, catTool("cat {{path}}", allowed), "../outside.txt")
}

func TestExecute_AllowedPaths_NestedTraversalDenied(t *testing.T) {
	allowed, _ := allowedFixture(t)
	assertDenied(t, catTool("cat {{path}}", allowed), "sub/../../outside.txt")
}

func TestExecute_AllowedPaths_AbsoluteOutsideDenied(t *testing.T) {
	allowed, outside := allowedFixture(t)
	assertDenied(t, catTool("cat {{path}}", allowed), outside)
}

// Relative arguments resolve against the directory the command actually
// runs in (allowed_paths[0]), not the rakitsu process cwd — the old check
// used filepath.Abs, which wrongly rejected "./inside.txt" whenever the
// process cwd was elsewhere and never saw bare "inside.txt" at all.
func TestExecute_AllowedPaths_RelativeResolvesAgainstSandboxDir(t *testing.T) {
	allowed, _ := allowedFixture(t)
	tool := catTool("cat {{path}}", allowed)
	assertAllowed(t, tool, "inside.txt", "marker-inside-483")
	assertAllowed(t, tool, "./inside.txt", "marker-inside-483")
	assertAllowed(t, tool, "sub/../inside.txt", "marker-inside-483")
}

func TestExecute_AllowedPaths_SymlinkEscapeDenied(t *testing.T) {
	allowed, outside := allowedFixture(t)
	if err := os.Symlink(outside, filepath.Join(allowed, "link.txt")); err != nil {
		t.Skipf("symlink not supported here: %v", err)
	}
	assertDenied(t, catTool("cat {{path}}", allowed), "link.txt")
}

// Shell-wrapped templates carry the whole payload as one argv element; the
// filter tokenizes it so a traversal inside the payload is still caught.
func TestExecute_AllowedPaths_ShellPayloadTraversalDenied(t *testing.T) {
	allowed, _ := allowedFixture(t)
	tool := catTool("sh -c 'cat {{path}}'", allowed)
	assertDenied(t, tool, "../outside.txt")
	assertAllowed(t, tool, "inside.txt", "marker-inside-483")
}

func TestExecute_AllowedPaths_ShellTildeExpansionDenied(t *testing.T) {
	allowed, outside := allowedFixture(t)
	t.Setenv("HOME", filepath.Dir(outside))
	assertDenied(t, catTool("sh -c 'cat {{path}}'", allowed), "~/outside.txt")
}

// The shell expands a glob before the command sees it, so a match that is
// a symlink out of the fence must be caught the same as if it had been
// named directly. Globs over in-fence files still work.
func TestExecute_AllowedPaths_ShellGlobExpandingToSymlinkEscapeDenied(t *testing.T) {
	allowed, outside := allowedFixture(t)
	if err := os.Symlink(outside, filepath.Join(allowed, "link.txt")); err != nil {
		t.Skipf("symlink not supported here: %v", err)
	}
	tool := catTool("sh -c 'cat {{path}}'", allowed)
	assertDenied(t, tool, "*")
	assertDenied(t, tool, "*.txt")
	assertAllowed(t, tool, "in*.txt", "marker-inside-483")
}

// The shell concatenates adjacent quoted fragments: the fragments ".." and
// "/outside.txt" reach cat as ../outside.txt. Stripping only the outer
// quotes would leave a harmless-looking component with embedded quotes.
func TestExecute_AllowedPaths_ShellQuotedFragmentTraversalDenied(t *testing.T) {
	allowed, _ := allowedFixture(t)
	tool := catTool("sh -c 'cat {{path}}'", allowed)
	assertDenied(t, tool, `'..''/outside.txt'`)
	assertDenied(t, tool, `".."'/outside.txt'`)
}

// A not-yet-existing name under a symlinked directory is judged by where
// the symlink points, so `touch linkdir/new` cannot land outside the fence.
func TestExecute_AllowedPaths_SymlinkDirNonexistentChildDenied(t *testing.T) {
	allowed, outside := allowedFixture(t)
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(allowed, "linkdir")); err != nil {
		t.Skipf("symlink not supported here: %v", err)
	}
	assertDenied(t, catTool("cat {{path}}", allowed), "linkdir/does-not-exist-483.txt")
}

// allowed_paths: ["/"] means the whole filesystem; the root must not trim
// to an empty prefix that no path matches.
func TestExecute_AllowedPaths_FilesystemRootAllowsAll(t *testing.T) {
	allowed, _ := allowedFixture(t)
	assertAllowed(t, catTool("cat {{path}}", "/"), filepath.Join(allowed, "inside.txt"), "marker-inside-483")
}

// An explicit working_dir inside allowed_paths is what relative arguments
// resolve against; "../inside.txt" from allowed/sub lands inside the fence.
func TestExecute_AllowedPaths_ExplicitWorkingDir(t *testing.T) {
	allowed, _ := allowedFixture(t)
	def := &config.ToolDefinition{
		Name:       "test-tool",
		Command:    "cat {{path}}",
		Parameters: pathParam,
		WorkingDir: filepath.Join(allowed, "sub"),
		Sandbox:    &config.SandboxConfig{Type: "local_restricted", AllowedPaths: []string{allowed}},
	}
	tool := NewTool(def)
	assertAllowed(t, tool, "../inside.txt", "marker-inside-483")
	assertDenied(t, tool, "../../outside.txt")
}

func TestExecute_NoAllowedPaths_NoPathFilter(t *testing.T) {
	tool := newTool("cat {{path}}", pathParam)
	_, err := tool.Execute(context.Background(), map[string]interface{}{"path": "../definitely-missing-483.txt"})
	var se *SecurityError
	if errors.As(err, &se) {
		t.Errorf("without allowed_paths there is no path filter, got %v", err)
	}
}

// Documents the trust boundary rather than a bug: allowed_paths is an
// ARGUMENT filter. An allowed interpreter opens whatever its code names, so
// the filter cannot contain it — real containment needs sandbox: docker
// (docs/SECURITY.md). If this test ever starts failing because the read is
// blocked, the docs and this comment must change together.
func TestExecute_AllowedPaths_IsArgumentFilterNotContainment(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	allowed, _ := allowedFixture(t)
	def := &config.ToolDefinition{
		Name:       "test-tool",
		Command:    "python3 -c {{code}}",
		Parameters: map[string]config.Parameter{"code": {Type: "string", Required: true}},
		Sandbox:    &config.SandboxConfig{Type: "local_restricted", AllowedPaths: []string{allowed}},
	}
	out, err := NewTool(def).Execute(context.Background(), map[string]interface{}{
		"code": "print(open('../outside.txt').read())",
	})
	if err != nil || !strings.Contains(out, "marker-outside-483") {
		t.Fatalf("expected the interpreter to read past the argument filter (documented limitation); err=%v out=%q", err, out)
	}
}
