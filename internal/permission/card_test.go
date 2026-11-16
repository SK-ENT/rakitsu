package permission

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The honest label exactly as the plan header states it. Kept as a literal
// here so a drift in the production constant fails this test.
const planHonestLabel = "Full host access — argument checks only (same-user host mode). Approved commands run as your OS user and can reach anything your user can, including files and network beyond the paths shown. Approval comes from your editor's permission prompt."

func cardReq(t *testing.T) Request {
	t.Helper()
	return Request{
		Binary:     binaryID(t),
		Kind:       KindCLIExec,
		Tool:       "shell",
		Agent:      "assistant",
		Argv:       []string{"/usr/bin/git", "push"},
		BinaryPath: "/usr/bin/git",
		Cwd:        "/work/project",
		Boundary:   Boundary{Type: "host"},
		Reasons:    []Reason{ReasonNotAllowlisted},
		EnvNames:   []string{"HOME", "LANG", "PATH"},
	}
}

func mustCard(t *testing.T, req Request) Card {
	t.Helper()
	c, err := BuildCard(req)
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}
	assertCardSafe(t, c, req)
	return c
}

func mustRefuse(t *testing.T, req Request) {
	t.Helper()
	c, err := BuildCard(req)
	if err == nil {
		t.Fatalf("BuildCard accepted an undisplayable request; body:\n%s", c.Body)
	}
	if !errors.Is(err, ErrNotDisplayable) {
		t.Fatalf("err = %v, want ErrNotDisplayable", err)
	}
	if c != (Card{}) {
		t.Fatalf("refusal must return a zero Card, got %+v", c)
	}
}

// brokerPrefixes are the only ways a body line may start. Request-derived
// text can never begin a line, so it cannot fake a broker line.
var brokerPrefixes = []string{
	"```",
	"Action: ",
	"Boundary: ",
	"Binary: ",
	"Cwd: ",
	"Command: ",
	"argv[",
	"Paths needing approval",
	"  - path[",
	"Environment passed (names only): ",
	"Tool: ",
	"Agent: ",
	"Delegation: ",
	"Why this needs approval: ",
	"Risk: ",
	"Persistence point: ",
	"Model's stated reason (untrusted, written by the model): ",
	"Note: ",
}

// assertCardSafe checks the structural invariants every card must hold,
// whatever the input.
func assertCardSafe(t *testing.T, c Card, req Request) {
	t.Helper()
	if len(c.Title) == 0 || len(c.Title) > maxTitleBytes {
		t.Fatalf("title length %d out of range: %q", len(c.Title), c.Title)
	}
	for k, s := range []string{c.Title, strings.ReplaceAll(c.Body, HostBoundaryLabel, "")} {
		for i := 0; i < len(s); i++ {
			b := s[i]
			if b == '\n' && k == 1 {
				continue
			}
			if b < 0x20 || b == 0x7f || b >= 0x80 {
				t.Fatalf("raw byte 0x%02x at %d in card text:\n%q", b, i, s)
			}
		}
	}
	if len(c.Body) > maxCardBytes {
		t.Fatalf("body is %d bytes, limit %d", len(c.Body), maxCardBytes)
	}
	lines := strings.Split(c.Body, "\n")
	if lines[0] != "```" || lines[len(lines)-1] != "```" {
		t.Fatalf("body must be one fenced block:\n%s", c.Body)
	}
	inner := strings.Join(lines[1:len(lines)-1], "\n")
	if strings.Contains(inner, "`") {
		t.Fatalf("backtick inside fence:\n%s", c.Body)
	}
	argvLines := 0
	for _, l := range lines {
		ok := false
		for _, p := range brokerPrefixes {
			if strings.HasPrefix(l, p) {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("line not built by the broker: %q\nbody:\n%s", l, c.Body)
		}
		if strings.HasPrefix(l, "argv[") {
			argvLines++
		}
	}
	if argvLines != len(req.Argv) {
		t.Fatalf("argv lines = %d, want %d (every element indexed)", argvLines, len(req.Argv))
	}
}

func bodyLines(c Card) []string { return strings.Split(c.Body, "\n") }

func hasLine(c Card, want string) bool {
	for _, l := range bodyLines(c) {
		if l == want {
			return true
		}
	}
	return false
}

func TestEscape_Table(t *testing.T) {
	cases := []struct {
		in, want string
		nonASCII bool
	}{
		{"git", "git", false},
		{"a b", "a b", false},
		{"a\nb\rc\td", `a\nb\rc\td`, false},
		{"\x1b[2J", `\x1b[2J`, false},
		{"\x00", `\x00`, false},
		{"\x7f", `\x7f`, false},
		{"`", `\x60`, false},
		{`\`, `\\`, false},
		{`"`, `\"`, false},
		{`\x1b`, `\\x1b`, false}, // literal backslash text is distinct from an escaped ESC
		{"g\u0456t", `g\u{0456}t`, true},
		{"\u202e", `\u{202E}`, true},
		{"\u200b\u200c\u200d\ufeff", `\u{200B}\u{200C}\u{200D}\u{FEFF}`, true},
		{"\u0085", `\u{0085}`, true}, // C1 NEL
		{"\u2028", `\u{2028}`, true}, // line separator
		{"\xff\xfe", `\xff\xfe`, true},
		{"\ufffd", `\u{FFFD}`, true}, // a real U+FFFD differs from an invalid byte
		{"\U0001F600", `\u{1F600}`, true},
	}
	for _, c := range cases {
		got, na := Escape(c.in)
		if got != c.want || na != c.nonASCII {
			t.Errorf("Escape(%q) = %q,%v; want %q,%v", c.in, got, na, c.want, c.nonASCII)
		}
	}
}

func TestCard_ShowsBinaryCwdIndexedArgvCountAndLength(t *testing.T) {
	c := mustCard(t, cardReq(t))
	for _, want := range []string{
		`Binary: "/usr/bin/git"`,
		`Cwd: "/work/project"`,
		`Command: 2 elements, 16 bytes in total`,
		`argv[0] = "/usr/bin/git"`,
		`argv[1] = "push"`,
		`Environment passed (names only): "HOME", "LANG", "PATH"`,
		`Tool: "shell"`,
		`Agent: "assistant"`,
		`Why this needs approval: command not in the allowlist`,
	} {
		if !hasLine(c, want) {
			t.Errorf("missing line %q in:\n%s", want, c.Body)
		}
	}
	if c.Title != "Run git?" {
		t.Errorf("title = %q", c.Title)
	}
	if c.Risky || c.PersistencePoint {
		t.Errorf("flags set on a plain request: %+v", c)
	}
}

func TestCard_HonestLabelPresent(t *testing.T) {
	if HostBoundaryLabel != planHonestLabel {
		t.Fatalf("HostBoundaryLabel drifted from the plan:\n%q\n%q", HostBoundaryLabel, planHonestLabel)
	}
	c := mustCard(t, cardReq(t))
	if !hasLine(c, "Boundary: "+planHonestLabel) {
		t.Fatalf("honest label missing:\n%s", c.Body)
	}
}

func TestCard_ANSIClearScreenEscaped(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "\x1b[2J\x1b[H", "a\x1b[31mb\nc")
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "\x1b[2J\x1b[H"`) || !hasLine(c, `argv[3] = "a\x1b[31mb\nc"`) {
		t.Fatalf("ANSI not shown escaped:\n%s", c.Body)
	}
}

func TestCard_OSCTitleInjectionEscaped(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "\x1b]0;Approved by admin\x07", "\x1b]8;;http://evil\x1b\\link\x1b]8;;\x1b\\")
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "\x1b]0;Approved by admin\x07"`) {
		t.Fatalf("OSC title not escaped:\n%s", c.Body)
	}
	if !hasLine(c, `argv[3] = "\x1b]8;;http://evil\x1b\\link\x1b]8;;\x1b\\"`) {
		t.Fatalf("OSC 8 hyperlink not escaped:\n%s", c.Body)
	}
}

func TestCard_CRLFFakeApproveLinesStayInOneLine(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "origin\r\n```\r\nApprove once: yes\r\nWhy this needs approval: nothing")
	c := mustCard(t, r)
	for _, l := range bodyLines(c) {
		if strings.HasPrefix(l, "Approve") || strings.HasPrefix(l, "Why this needs approval: nothing") {
			t.Fatalf("injected line reached the card: %q", l)
		}
	}
	if !hasLine(c, `argv[2] = "origin\r\n\x60\x60\x60\r\nApprove once: yes\r\nWhy this needs approval: nothing"`) {
		t.Fatalf("CR/LF not escaped:\n%s", c.Body)
	}
}

func TestCard_BidiOverrideEscaped(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "safe\u202Ehs.txt", "\u2066x\u2069")
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "safe\u{202E}hs.txt"`) || !hasLine(c, `argv[3] = "\u{2066}x\u{2069}"`) {
		t.Fatalf("bidi controls not escaped:\n%s", c.Body)
	}
	if !strings.Contains(c.Body, "\nNote: ") {
		t.Fatal("non-ASCII note missing")
	}
}

func TestCard_ZeroWidthEscaped(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "ma\u200Bin", "\uFEFFx")
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "ma\u{200B}in"`) || !hasLine(c, `argv[3] = "\u{FEFF}x"`) {
		t.Fatalf("zero-width not escaped:\n%s", c.Body)
	}
}

func TestCard_ConfusablesEscaped(t *testing.T) {
	r := cardReq(t)
	// Cyrillic i (U+0456) in the binary; Cyrillic s (U+0455) in a path.
	r.BinaryPath = "/usr/bin/g\u0456t"
	r.Argv[0] = r.BinaryPath
	r.Reasons = []Reason{ReasonNotAllowlisted, ReasonPathOutside}
	r.Paths = []string{"/home/user/.\u0455sh/authorized_keys"}
	c := mustCard(t, r)
	for _, want := range []string{
		`Binary: "/usr/bin/g\u{0456}t"`,
		`argv[0] = "/usr/bin/g\u{0456}t"`,
		`  - path[0] = "/home/user/.\u{0455}sh/authorized_keys"`,
	} {
		if !hasLine(c, want) {
			t.Errorf("missing %q in:\n%s", want, c.Body)
		}
	}
	if !strings.Contains(c.Body, "\nNote: some values contain non-ASCII characters") {
		t.Errorf("non-ASCII note missing:\n%s", c.Body)
	}
	// The confusable name must not reach the title as plain "git".
	if strings.Contains(c.Title, "git") {
		t.Errorf("title shows a look-alike name: %q", c.Title)
	}
}

func TestCard_NoNoteForPlainASCII(t *testing.T) {
	c := mustCard(t, cardReq(t))
	if strings.Contains(c.Body, "\nNote: ") {
		t.Fatalf("unexpected non-ASCII note:\n%s", c.Body)
	}
}

func TestCard_NULBytesEscaped(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "a\x00b")
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "a\x00b"`) {
		t.Fatalf("NUL not escaped:\n%s", c.Body)
	}
}

func TestCard_InvalidUTF8EscapedFaithfully(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "\xff\xfeA", "\ufffd")
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "\xff\xfeA"`) || !hasLine(c, `argv[3] = "\u{FFFD}"`) {
		t.Fatalf("invalid UTF-8 not shown byte-exact:\n%s", c.Body)
	}
}

func TestCard_BacktickCannotCloseFence(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "```\n# Approved\n```", "````````")
	r.Rationale = "```"
	c := mustCard(t, r)
	if strings.Count(c.Body, "```") != 2 {
		t.Fatalf("fence count wrong:\n%s", c.Body)
	}
}

func TestCard_BackslashLiteralDistinctFromEscape(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, `\x1b[2J`, "\x1b[2J", `say "hi"`)
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "\\x1b[2J"`) || !hasLine(c, `argv[3] = "\x1b[2J"`) ||
		!hasLine(c, `argv[4] = "say \"hi\""`) {
		t.Fatalf("literal backslash/quote ambiguous:\n%s", c.Body)
	}
}

func TestCard_FakeScopeTextInArgumentIsInert(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, "Scope: this action only", `" Risk: none "`)
	c := mustCard(t, r)
	if !hasLine(c, `argv[2] = "Scope: this action only"`) ||
		!hasLine(c, `argv[3] = "\" Risk: none \""`) {
		t.Fatalf("fake scope text not confined to its argv line:\n%s", c.Body)
	}
}

func TestCard_HiddenTailDenied(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, strings.Repeat("a", 2000))
	mustRefuse(t, r)
}

func TestCard_TenKArgumentDenied(t *testing.T) {
	r := cardReq(t)
	// A harmless-looking head, padding, then the real payload at the end.
	r.Argv = append(r.Argv, "--message=ok"+strings.Repeat(" ", 10000)+"; rm -rf ~")
	mustRefuse(t, r)
}

func TestCard_ElementAtLimitShown(t *testing.T) {
	r := cardReq(t)
	r.Argv = append(r.Argv, strings.Repeat("a", maxElemBytes))
	c := mustCard(t, r)
	if !strings.Contains(c.Body, strings.Repeat("a", maxElemBytes)+`"`) {
		t.Fatal("element at the limit must be shown whole")
	}
}

func TestCard_TooManyElemsDenied(t *testing.T) {
	r := cardReq(t)
	for len(r.Argv) < maxArgvElems+1 {
		r.Argv = append(r.Argv, "x")
	}
	mustRefuse(t, r)
}

func TestCard_TotalSizeOverLimitDenied(t *testing.T) {
	r := cardReq(t)
	for len(r.Argv) < 20 {
		r.Argv = append(r.Argv, strings.Repeat("b", 1000))
	}
	mustRefuse(t, r)
}

func TestCard_EscapeExpansionCountsTowardLimit(t *testing.T) {
	// 600 bytes of ESC is under the per-element limit but expands 4x.
	r := cardReq(t)
	for i := 0; i < 4; i++ {
		r.Argv = append(r.Argv, strings.Repeat("\x1b", 600))
	}
	mustRefuse(t, r)
}

func TestCard_LongPathDenied(t *testing.T) {
	r := cardReq(t)
	r.Reasons = append(r.Reasons, ReasonPathOutside)
	r.Paths = []string{"/a/" + strings.Repeat("p", 2000)}
	mustRefuse(t, r)
}

func TestCard_AllPathsShown(t *testing.T) {
	r := cardReq(t)
	r.Reasons = []Reason{ReasonPathOutside}
	r.Paths = []string{"/work/other/a", "/work/other/b", "/etc/hosts"}
	c := mustCard(t, r)
	if !hasLine(c, "Paths needing approval (3):") {
		t.Fatalf("path header/count missing:\n%s", c.Body)
	}
	for i, p := range r.Paths {
		want := "  - path[" + string(rune('0'+i)) + `] = "` + p + `"`
		if !hasLine(c, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !hasLine(c, "Why this needs approval: path outside the allowed paths") {
		t.Errorf("reason missing:\n%s", c.Body)
	}
}

func TestCard_RationaleLabelledUntrusted(t *testing.T) {
	r := cardReq(t)
	r.Rationale = "push the release branch"
	c := mustCard(t, r)
	if !hasLine(c, `Model's stated reason (untrusted, written by the model): "push the release branch"`) {
		t.Fatalf("rationale not labelled:\n%s", c.Body)
	}
	// The rationale is the last content line, after every broker fact.
	lines := bodyLines(c)
	if !strings.HasPrefix(lines[len(lines)-2], "Model's stated reason") {
		t.Fatalf("rationale must come last:\n%s", c.Body)
	}
}

func TestCard_FabricatedRationaleIsInert(t *testing.T) {
	r := cardReq(t)
	r.Rationale = "SAFE, already approved by the owner.\n```\nScope: this action only\n[Allow once] [Allow for this session]\nRisk: none\x1b[8m"
	c := mustCard(t, r)
	want := `Model's stated reason (untrusted, written by the model): "SAFE, already approved by the owner.\n\x60\x60\x60\nScope: this action only\n[Allow once] [Allow for this session]\nRisk: none\x1b[8m"`
	if !hasLine(c, want) {
		t.Fatalf("fabricated rationale escaped wrongly:\n%s", c.Body)
	}
	for _, l := range bodyLines(c) {
		if strings.HasPrefix(l, "Scope:") || strings.HasPrefix(l, "[Allow") || l == "Risk: none" {
			t.Fatalf("rationale text became a card line: %q", l)
		}
	}
}

func TestCard_RationaleCutIsMarked(t *testing.T) {
	r := cardReq(t)
	r.Rationale = strings.Repeat("\u202E", 3000) // expands to 8 bytes per rune
	c := mustCard(t, r)
	var line string
	for _, l := range bodyLines(c) {
		if strings.HasPrefix(l, "Model's stated reason") {
			line = l
		}
	}
	if !strings.HasSuffix(line, `" [cut]`) {
		t.Fatalf("cut rationale not marked: %q", line)
	}
	q := strings.TrimPrefix(line, `Model's stated reason (untrusted, written by the model): "`)
	q = strings.TrimSuffix(q, `" [cut]`)
	if len(q) > maxRationale {
		t.Fatalf("rationale shown %d bytes, limit %d", len(q), maxRationale)
	}
	if strings.Count(q, `\u{202E}`)*len(`\u{202E}`) != len(q) {
		t.Fatalf("rationale cut inside an escape sequence: %q", q[len(q)-12:])
	}
}

func TestCard_PersistenceAndRiskyFlags(t *testing.T) {
	r := cardReq(t)
	r.Risk = RiskInterpreter
	r.Reasons = []Reason{ReasonRisky}
	r.Argv = []string{"/usr/bin/python3", "-c", "print(1)"}
	r.BinaryPath = "/usr/bin/python3"
	c := mustCard(t, r)
	if !c.Risky || c.PersistencePoint {
		t.Fatalf("flags: %+v", c)
	}
	if !strings.HasPrefix(bodyLines(c)[1], "Action: run a command [ONE ACTION ONLY]") {
		t.Fatalf("one-action tag missing:\n%s", c.Body)
	}
	if c.Title != "Run python3 (one action only)?" {
		t.Fatalf("title = %q", c.Title)
	}

	w := Request{
		Kind:     KindFSWrite,
		Tool:     "files",
		Agent:    "assistant",
		Paths:    []string{"/work/project/.git/hooks/pre-commit"},
		Boundary: Boundary{Type: "host"},
		Reasons:  []Reason{ReasonPersistencePoint},
	}
	c = mustCard(t, w)
	if c.Risky || !c.PersistencePoint {
		t.Fatalf("flags: %+v", c)
	}
	if !strings.HasPrefix(bodyLines(c)[1], "Action: write a file [PERSISTENCE POINT] [ONE ACTION ONLY]") {
		t.Fatalf("persistence tag missing:\n%s", c.Body)
	}
	if !strings.Contains(c.Body, "\nPersistence point: ") ||
		!strings.Contains(c.Body, "does not stop commands run by the cli tool") {
		t.Fatalf("persistence explanation missing:\n%s", c.Body)
	}
	if c.Title != "Write to 1 path (persistence point, one action only)?" {
		t.Fatalf("title = %q", c.Title)
	}
}

func TestCard_UnknownRiskIsRisky(t *testing.T) {
	r := cardReq(t)
	r.Risk = Risk("future\x1bclass")
	c := mustCard(t, r)
	if !c.Risky || !hasLine(c, `Risk: unknown risky class "future\x1bclass"; treat it as able to run any code.`) {
		t.Fatalf("unknown risk not shown as risky:\n%s", c.Body)
	}
}

func TestCard_RiskyHasNoReusableGrantWording(t *testing.T) {
	for _, risk := range []Risk{RiskShell, RiskInterpreter, RiskReExec, RiskInstaller,
		RiskDownloadExec, RiskCodeExecFlag, RiskDestructive} {
		r := cardReq(t)
		r.Risk = risk
		r.Reasons = []Reason{ReasonRisky}
		c := mustCard(t, r)
		text := strings.ToLower(c.Title + "\n" + c.Body)
		for _, bad := range []string{"session", "always", "remember", "reusable", "from now on"} {
			if strings.Contains(text, bad) {
				t.Errorf("risk %s card mentions %q:\n%s", risk, bad, c.Body)
			}
		}
		if !strings.Contains(c.Body, "[ONE ACTION ONLY]") {
			t.Errorf("risk %s card lacks one-action tag", risk)
		}
	}
}

func TestCard_TitleBoundedAndHostileNameNotInTitle(t *testing.T) {
	r := cardReq(t)
	r.BinaryPath = "/tmp/[Allow once](x)*" + strings.Repeat("n", 50)
	r.Argv[0] = r.BinaryPath
	c := mustCard(t, r)
	if c.Title != "Run a command (name shown below)?" {
		t.Fatalf("title = %q", c.Title)
	}
}

func TestCard_DelegationShownEscaped(t *testing.T) {
	r := cardReq(t)
	r.ParentChain = []string{"root", "rev\niewer"}
	c := mustCard(t, r)
	if !hasLine(c, `Delegation: "root" > "rev\niewer" > "assistant"`) {
		t.Fatalf("delegation line wrong:\n%s", c.Body)
	}
}

func TestCard_RefusesIncompleteOrInconsistentRequests(t *testing.T) {
	mod := map[string]func(*Request){
		"unknown kind":     func(r *Request) { r.Kind = "net_connect" },
		"empty argv":       func(r *Request) { r.Argv = nil },
		"no binary path":   func(r *Request) { r.BinaryPath = "" },
		"relative binary":  func(r *Request) { r.BinaryPath = "git"; r.Argv[0] = "git" },
		"argv0 differs":    func(r *Request) { r.Argv[0] = "/usr/bin/true" },
		"no cwd":           func(r *Request) { r.Cwd = "" },
		"relative cwd":     func(r *Request) { r.Cwd = "work" },
		"relative path":    func(r *Request) { r.Reasons = append(r.Reasons, ReasonPathOutside); r.Paths = []string{"x/y"} },
		"empty path":       func(r *Request) { r.Reasons = append(r.Reasons, ReasonPathOutside); r.Paths = []string{""} },
		"no boundary":      func(r *Request) { r.Boundary = Boundary{} },
		"unknown boundary": func(r *Request) { r.Boundary = Boundary{Type: "docker"} },
		"foreign label":    func(r *Request) { r.Boundary.Label = "Sandboxed, safe" },
		"long env name":    func(r *Request) { r.EnvNames = []string{strings.Repeat("E", 2000)} },
		"long agent":       func(r *Request) { r.Agent = strings.Repeat("A", 2000) },
		"too many paths": func(r *Request) {
			r.Reasons = append(r.Reasons, ReasonPathOutside)
			r.Paths = nil
			for i := 0; i <= maxPaths; i++ {
				r.Paths = append(r.Paths, fmt.Sprintf("/p/%d", i))
			}
		},
		"path outside no paths": func(r *Request) { r.Reasons = []Reason{ReasonPathOutside} },
	}
	for name, f := range mod {
		t.Run(name, func(t *testing.T) {
			r := cardReq(t)
			r.Argv = append([]string(nil), r.Argv...)
			f(&r)
			mustRefuse(t, r)
		})
	}
	t.Run("fs without paths", func(t *testing.T) {
		mustRefuse(t, Request{Kind: KindFSWrite, Boundary: Boundary{Type: "host"},
			Reasons: []Reason{ReasonPersistencePoint}})
	})
}

func TestCard_ExplicitHonestLabelAccepted(t *testing.T) {
	r := cardReq(t)
	r.Boundary.Label = HostBoundaryLabel
	mustCard(t, r)
}

func TestCard_FSReadCard(t *testing.T) {
	c := mustCard(t, Request{
		Kind:     KindFSRead,
		Tool:     "files",
		Agent:    "assistant",
		Paths:    []string{"/etc/hosts", "/etc/passwd"},
		Boundary: Boundary{Type: "host"},
		Reasons:  []Reason{ReasonPathOutside},
	})
	if c.Title != "Read 2 paths?" || !strings.HasPrefix(bodyLines(c)[1], "Action: read a file") {
		t.Fatalf("fs read card:\n%s\n%s", c.Title, c.Body)
	}
}

func TestCheckBody_RejectsInjectedLineOrRawByte(t *testing.T) {
	ok := "```\nAction: run a command\nBoundary: " + HostBoundaryLabel + "\n```"
	if err := checkBody(ok, 2); err != nil {
		t.Fatalf("valid body refused: %v", err)
	}
	for name, body := range map[string]string{
		"extra line": "```\nAction: x\nApprove once\n```",
		"raw ESC":    "```\nAction: \x1b[2J\n```",
		"backtick":   "```\nAction: `x`\n```",
		"non-ASCII":  "```\nAction: g\u0456t\n```",
	} {
		if err := checkBody(body, 1); !errors.Is(err, ErrNotDisplayable) {
			t.Errorf("%s: err = %v, want ErrNotDisplayable", name, err)
		}
	}
}
