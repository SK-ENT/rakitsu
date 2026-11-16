package permission

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// HostBoundaryLabel is the honest description of the "host" execution
// boundary. The card always shows this broker-owned text; it never shows a
// label taken from the request.
const HostBoundaryLabel = "Full host access — argument checks only (same-user host mode). Approved commands run as your OS user and can reach anything your user can, including files and network beyond the paths shown. Approval comes from your editor's permission prompt."

// Display limits. Anything the action depends on (argv, binary, cwd, paths,
// names) is never cut: over a limit the card is refused and the Gate denies.
// Only the model's rationale may be cut, and the cut is marked.
const (
	maxArgvElems  = 64
	maxElemBytes  = 1024 // raw bytes of any one displayed value
	maxPaths      = 64
	maxEnvNames   = 128
	maxChain      = 32
	maxReasons    = 16
	maxCardBytes  = 8 << 10 // escaped body, fences included
	maxRationale  = 1024    // escaped bytes of the rationale shown before "[cut]"
	maxTitleBytes = 120
	maxTitleName  = 40
)

// ErrNotDisplayable means the action cannot be shown in full and safely, so
// it must not be approved.
var ErrNotDisplayable = errors.New("permission: action cannot be fully displayed")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrNotDisplayable}, args...)...)
}

// Escape returns s in a visible, unambiguous ASCII form. Printable ASCII is
// kept except backslash (\\), double quote (\"), and backtick (\x60, so it
// cannot close a Markdown fence). \n, \r and \t are shown as such; other
// control bytes, DEL and invalid UTF-8 bytes become \xNN (the exact byte);
// every non-ASCII rune, including bidi controls, zero-width characters and
// look-alike letters, becomes \u{XXXX}. nonASCII reports whether any
// non-ASCII rune or invalid byte was escaped. The mapping is one-to-one, so
// two different inputs never look the same.
func Escape(s string) (out string, nonASCII bool) {
	out, nonASCII, _ = escapeBounded(s, -1)
	return out, nonASCII
}

// escapeBounded escapes s, stopping before the output would exceed max
// bytes (max < 0 means no limit). It never cuts inside an escape sequence.
func escapeBounded(s string, max int) (out string, nonASCII, cut bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		piece, size, na := escapeUnit(s, i)
		if max >= 0 && b.Len()+len(piece) > max {
			return b.String(), nonASCII, true
		}
		b.WriteString(piece)
		nonASCII = nonASCII || na
		i += size
	}
	return b.String(), nonASCII, false
}

// escapeUnit escapes the character starting at s[i] and returns its
// escaped form and the number of input bytes it consumed.
func escapeUnit(s string, i int) (piece string, size int, nonASCII bool) {
	c := s[i]
	if c < utf8.RuneSelf {
		switch {
		case c == '\n':
			return `\n`, 1, false
		case c == '\r':
			return `\r`, 1, false
		case c == '\t':
			return `\t`, 1, false
		case c == '\\':
			return `\\`, 1, false
		case c == '"':
			return `\"`, 1, false
		case c == '`':
			return `\x60`, 1, false
		case c < 0x20 || c == 0x7f:
			return fmt.Sprintf(`\x%02x`, c), 1, false
		}
		return string(c), 1, false
	}
	r, n := utf8.DecodeRuneInString(s[i:])
	if r == utf8.RuneError && n == 1 {
		return fmt.Sprintf(`\x%02x`, c), 1, true
	}
	return fmt.Sprintf(`\u{%04X}`, r), n, true
}

// cardText collects escaped values and remembers whether any was non-ASCII.
type cardText struct{ nonASCII bool }

// q returns the escaped value in double quotes.
func (t *cardText) q(s string) string {
	e, na := Escape(s)
	t.nonASCII = t.nonASCII || na
	return `"` + e + `"`
}

// BuildCard builds the approval card for req. Every line is written by the
// broker; request text appears only escaped, quoted and after a fixed
// label, so it can never start a line or fake a broker field. If the action
// cannot be shown in full (a value or the whole card over its limit, a
// missing or inconsistent field, an unknown boundary), BuildCard returns an
// error wrapping ErrNotDisplayable and a zero Card; it never truncates.
func BuildCard(req Request) (Card, error) {
	if err := checkDisplayable(req); err != nil {
		return Card{}, err
	}
	var t cardText
	risky := req.Risk != RiskNone || req.has(ReasonRisky)
	persist := req.has(ReasonPersistencePoint)
	oneAction := !SessionAllowed(req)

	var lines []string
	add := func(s string) { lines = append(lines, s) }

	action := map[Kind]string{
		KindCLIExec: "run a command",
		KindFSWrite: "write a file",
		KindFSRead:  "read a file",
	}[req.Kind]
	head := "Action: " + action
	if persist {
		head += " [PERSISTENCE POINT]"
	}
	if oneAction {
		head += " [ONE ACTION ONLY]"
	}
	add(head)
	add("Boundary: " + HostBoundaryLabel)

	if req.Kind == KindCLIExec || req.BinaryPath != "" {
		add("Binary: " + t.q(req.BinaryPath))
	}
	if req.Kind == KindCLIExec || req.Cwd != "" {
		add("Cwd: " + t.q(req.Cwd))
	}
	if len(req.Argv) > 0 {
		total := 0
		for _, a := range req.Argv {
			total += len(a)
		}
		add(fmt.Sprintf("Command: %d %s, %d bytes in total", len(req.Argv), plural(len(req.Argv), "element"), total))
		for i, a := range req.Argv {
			add("argv[" + strconv.Itoa(i) + "] = " + t.q(a))
		}
	}
	if len(req.Paths) > 0 {
		add(fmt.Sprintf("Paths needing approval (%d):", len(req.Paths)))
		for i, p := range req.Paths {
			add("  - path[" + strconv.Itoa(i) + "] = " + t.q(p))
		}
	}
	if req.Kind == KindCLIExec || len(req.EnvNames) > 0 {
		if len(req.EnvNames) == 0 {
			add("Environment passed (names only): (not reported)")
		} else {
			names := make([]string, len(req.EnvNames))
			for i, n := range req.EnvNames {
				names[i] = t.q(n)
			}
			add("Environment passed (names only): " + strings.Join(names, ", "))
		}
	}
	if req.Tool != "" {
		add("Tool: " + t.q(req.Tool))
	}
	if req.Agent != "" {
		add("Agent: " + t.q(req.Agent))
	}
	if len(req.ParentChain) > 0 {
		chain := make([]string, 0, len(req.ParentChain)+1)
		for _, a := range req.ParentChain {
			chain = append(chain, t.q(a))
		}
		chain = append(chain, t.q(req.Agent))
		add("Delegation: " + strings.Join(chain, " > "))
	}
	add("Why this needs approval: " + t.reasons(req))
	if risky {
		add("Risk: " + t.risk(req.Risk))
	}
	if persist {
		add("Persistence point: changing this file affects later runs or tools. The protected-path list does not stop commands run by the cli tool.")
	}

	var rationale string
	if req.Rationale != "" {
		e, na, cut := escapeBounded(req.Rationale, maxRationale)
		t.nonASCII = t.nonASCII || na
		rationale = "Model's stated reason (untrusted, written by the model): \"" + e + `"`
		if cut {
			rationale += " [cut]"
		}
	}
	if t.nonASCII {
		add(`Note: some values contain non-ASCII characters, shown as \u{XXXX}, or invalid UTF-8 bytes, shown as \xNN.`)
	}
	if rationale != "" {
		add(rationale) // last, after every broker fact
	}

	body := "```\n" + strings.Join(lines, "\n") + "\n```"
	if len(body) > maxCardBytes {
		return Card{}, refuse("card is %d bytes, limit %d", len(body), maxCardBytes)
	}
	if err := checkBody(body, len(lines)); err != nil {
		return Card{}, err
	}
	title := cardTitle(req, persist, oneAction)
	if len(title) > maxTitleBytes {
		return Card{}, refuse("title is %d bytes, limit %d", len(title), maxTitleBytes)
	}
	return Card{Title: title, Body: body, PersistencePoint: persist, Risky: risky}, nil
}

// checkDisplayable refuses requests that are incomplete, inconsistent or
// over a display limit. It names fields and sizes, never their content.
func checkDisplayable(req Request) error {
	if !req.Kind.known() {
		return refuse("unknown action kind")
	}
	if req.Boundary.Type != "host" {
		return refuse("unknown execution boundary")
	}
	if req.Boundary.Label != "" && req.Boundary.Label != HostBoundaryLabel {
		return refuse("boundary label is not the broker's label")
	}
	if len(req.Argv) > maxArgvElems {
		return refuse("argv has %d elements, limit %d", len(req.Argv), maxArgvElems)
	}
	if len(req.Paths) > maxPaths {
		return refuse("%d paths, limit %d", len(req.Paths), maxPaths)
	}
	if len(req.EnvNames) > maxEnvNames {
		return refuse("%d environment names, limit %d", len(req.EnvNames), maxEnvNames)
	}
	if len(req.ParentChain) > maxChain {
		return refuse("delegation chain has %d entries, limit %d", len(req.ParentChain), maxChain)
	}
	if len(req.Reasons) > maxReasons {
		return refuse("%d reasons, limit %d", len(req.Reasons), maxReasons)
	}
	for i, a := range req.Argv {
		if len(a) > maxElemBytes {
			return refuse("argv[%d] is %d bytes, limit %d", i, len(a), maxElemBytes)
		}
	}
	for i, p := range req.Paths {
		if len(p) > maxElemBytes {
			return refuse("path[%d] is %d bytes, limit %d", i, len(p), maxElemBytes)
		}
		if !filepath.IsAbs(p) {
			return refuse("path[%d] is not absolute", i)
		}
	}
	for i, n := range req.EnvNames {
		if len(n) > maxElemBytes {
			return refuse("environment name %d is %d bytes, limit %d", i, len(n), maxElemBytes)
		}
	}
	for i, a := range req.ParentChain {
		if len(a) > maxElemBytes {
			return refuse("delegation entry %d is %d bytes, limit %d", i, len(a), maxElemBytes)
		}
	}
	for i, r := range req.Reasons {
		if len(r) > maxElemBytes {
			return refuse("reason %d is %d bytes, limit %d", i, len(r), maxElemBytes)
		}
	}
	for name, v := range map[string]string{
		"binary path": req.BinaryPath, "cwd": req.Cwd, "tool": req.Tool,
		"agent": req.Agent, "risk": string(req.Risk),
	} {
		if len(v) > maxElemBytes {
			return refuse("%s is %d bytes, limit %d", name, len(v), maxElemBytes)
		}
	}
	if req.Cwd != "" && !filepath.IsAbs(req.Cwd) {
		return refuse("cwd is not absolute")
	}
	if req.BinaryPath != "" && !filepath.IsAbs(req.BinaryPath) {
		return refuse("binary path is not absolute")
	}
	if req.has(ReasonPathOutside) && len(req.Paths) == 0 {
		return refuse("path outside the allowed paths, but no path given")
	}
	switch req.Kind {
	case KindCLIExec:
		if len(req.Argv) == 0 {
			return refuse("empty argv")
		}
		if req.BinaryPath == "" {
			return refuse("resolved binary path missing")
		}
		if req.Argv[0] != req.BinaryPath {
			return refuse("argv[0] is not the resolved binary path")
		}
		if req.Cwd == "" {
			return refuse("cwd missing")
		}
	case KindFSWrite, KindFSRead:
		if len(req.Paths) == 0 {
			return refuse("no path given")
		}
	}
	return nil
}

// checkBody is a last guard on the finished body: it must have exactly the
// lines the broker wrote plus two fences and, outside the broker's own
// label, only printable ASCII, with backticks only in the fence lines.
func checkBody(body string, wantLines int) error {
	if strings.Count(body, "\n") != wantLines+1 {
		return refuse("card line structure changed")
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(body, "```\n"), "\n```")
	inner = strings.Replace(inner, "Boundary: "+HostBoundaryLabel, "Boundary: ", 1)
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c == '`' || (c != '\n' && (c < 0x20 || c > 0x7e)) {
			return refuse("unescaped byte in card")
		}
	}
	return nil
}

var reasonText = map[Reason]string{
	ReasonNotAllowlisted:   "command not in the allowlist",
	ReasonPathOutside:      "path outside the allowed paths",
	ReasonRisky:            "risky command class",
	ReasonPersistencePoint: "writes a persistence point",
}

func (t *cardText) reasons(req Request) string {
	var parts []string
	for _, r := range req.Reasons {
		if s, ok := reasonText[r]; ok {
			parts = append(parts, s)
		} else {
			parts = append(parts, "unknown reason "+t.q(string(r)))
		}
	}
	if len(parts) == 0 {
		if req.Risk != RiskNone {
			return reasonText[ReasonRisky]
		}
		return "(no reason given)"
	}
	return strings.Join(parts, "; ")
}

var riskText = map[Risk]string{
	RiskShell:        "shell. Approving it lets the command run any code as your OS user.",
	RiskInterpreter:  "interpreter. Approving it lets the command run any code as your OS user.",
	RiskReExec:       "re-exec wrapper. It runs another command, which can be anything.",
	RiskInstaller:    "installer. It downloads and runs third-party code.",
	RiskDownloadExec: "download tool. It can fetch code or data from the network.",
	RiskCodeExecFlag: "code-executing option. It can make the command run other programs.",
	RiskDestructive:  "destructive. It can delete or overwrite work that cannot be recovered.",
}

func (t *cardText) risk(r Risk) string {
	if r == RiskNone {
		return "risky command class."
	}
	if s, ok := riskText[r]; ok {
		return s
	}
	return "unknown risky class " + t.q(string(r)) + "; treat it as able to run any code."
}

// cardTitle is fixed broker text. A command name is used only when it is
// short plain ASCII; anything else is described as "shown below".
func cardTitle(req Request, persist, oneAction bool) string {
	var base string
	var tags []string
	switch req.Kind {
	case KindCLIExec:
		if name := filepath.Base(req.BinaryPath); safeTitleName(name) {
			base = "Run " + name
		} else {
			base = "Run a command"
			tags = append(tags, "name shown below")
		}
	case KindFSWrite:
		base = fmt.Sprintf("Write to %d %s", len(req.Paths), plural(len(req.Paths), "path"))
	case KindFSRead:
		base = fmt.Sprintf("Read %d %s", len(req.Paths), plural(len(req.Paths), "path"))
	}
	if persist {
		tags = append(tags, "persistence point")
	}
	if oneAction {
		tags = append(tags, "one action only")
	}
	if len(tags) > 0 {
		base += " (" + strings.Join(tags, ", ") + ")"
	}
	return base + "?"
}

func safeTitleName(s string) bool {
	if s == "" || len(s) > maxTitleName || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
