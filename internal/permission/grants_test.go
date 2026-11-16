package permission

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// binaryID returns a FileID for a fresh temp file, standing in for a
// resolved binary.
func binaryID(t *testing.T) FileID {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return NewFileID(fi)
}

func cliReq(t *testing.T) Request {
	t.Helper()
	return Request{
		Kind:       KindCLIExec,
		Tool:       "shell",
		Agent:      "assistant",
		Argv:       []string{"/usr/bin/git", "status"},
		BinaryPath: "/usr/bin/git",
		Binary:     binaryID(t),
		Cwd:        "/work/project",
		Boundary:   Boundary{Type: "host"},
		Reasons:    []Reason{ReasonNotAllowlisted},
	}
}

func TestKeyFor_RiskyAndPersistenceAreEmpty(t *testing.T) {
	r := cliReq(t)
	if KeyFor(r) == "" {
		t.Fatal("clean grantable request must have a key")
	}
	risky := r
	risky.Risk = RiskShell
	if KeyFor(risky) != "" {
		t.Error("risky request must not be grantable")
	}
	unknownRisk := r
	unknownRisk.Risk = Risk("something_new")
	if KeyFor(unknownRisk) != "" {
		t.Error("unknown risk value must not be grantable")
	}
	pp := r
	pp.Reasons = []Reason{ReasonNotAllowlisted, ReasonPersistencePoint}
	if KeyFor(pp) != "" {
		t.Error("persistence point must not be grantable")
	}
	if SessionAllowed(risky) || SessionAllowed(pp) {
		t.Error("SessionAllowed must be false for risky and persistence requests")
	}
}

func TestKeyFor_CLIWithoutBinaryIdentityIsEmpty(t *testing.T) {
	r := cliReq(t)
	r.Binary = FileID{}
	if KeyFor(r) != "" {
		t.Fatal("cli request without a captured binary identity must not be grantable")
	}
	if SessionAllowed(r) {
		t.Fatal("SessionAllowed must be false without binary identity")
	}
}

func TestKeyFor_UnknownKindIsEmpty(t *testing.T) {
	r := cliReq(t)
	r.Kind = Kind("net_connect")
	if KeyFor(r) != "" {
		t.Fatal("unknown kind must not be grantable")
	}
}

func TestKeyFor_DiffersOnEachBoundField(t *testing.T) {
	base := cliReq(t)
	k0 := KeyFor(base)
	mut := map[string]func(r *Request){
		"kind":       func(r *Request) { r.Kind = KindFSWrite },
		"tool":       func(r *Request) { r.Tool = "other" },
		"binaryPath": func(r *Request) { r.BinaryPath = "/opt/git" },
		"binary":     func(r *Request) { r.Binary = binaryID(t) },
		"subcommand": func(r *Request) { r.Argv = []string{"/usr/bin/git", "log"} },
		"cwd":        func(r *Request) { r.Cwd = "/work/other" },
		"paths":      func(r *Request) { r.Paths = []string{"/etc/hosts"} },
		"env":        func(r *Request) { r.EnvNames = []string{"HOME"} },
		"boundary":   func(r *Request) { r.Boundary.Type = "docker" },
		"reasons":    func(r *Request) { r.Reasons = []Reason{ReasonPathOutside} },
	}
	for name, f := range mut {
		r := base
		f(&r)
		if KeyFor(r) == k0 {
			t.Errorf("changing %s did not change the grant key", name)
		}
	}
}

func TestKeyFor_SameSubcommandDifferentArgsShareKey(t *testing.T) {
	a := cliReq(t)
	b := a
	b.Argv = []string{"/usr/bin/git", "status", "--short"}
	if KeyFor(a) != KeyFor(b) {
		t.Fatal("same binary + subcommand + cwd + paths should share a session key")
	}
}

func TestKeyFor_FlagBeforeSubcommandUsesExactArgv(t *testing.T) {
	// "git -C /x push" must not grant "git -C /x commit".
	a := cliReq(t)
	a.Argv = []string{"/usr/bin/git", "-C", "/x", "push"}
	b := a
	b.Argv = []string{"/usr/bin/git", "-C", "/x", "commit"}
	if KeyFor(a) == KeyFor(b) {
		t.Fatal("argv starting with a flag must be keyed on the exact argv")
	}
}

func TestKeyFor_NoSeparatorCollision(t *testing.T) {
	a := cliReq(t)
	a.Paths = []string{"/a|/b"}
	b := a
	b.Paths = []string{"/a", "/b"}
	if KeyFor(a) == KeyFor(b) {
		t.Fatal("key encoding must be injective")
	}
}

func TestKeyFor_PathOrderAndDuplicatesIgnored(t *testing.T) {
	a := cliReq(t)
	a.Paths = []string{"/b", "/a", "/a"}
	b := a
	b.Paths = []string{"/a", "/b"}
	if KeyFor(a) != KeyFor(b) {
		t.Fatal("paths must be normalised (sorted, deduped) in the key")
	}
	if a.Paths[0] != "/b" {
		t.Fatal("KeyFor must not mutate the caller's slice")
	}
}

func TestKeyFor_IgnoresModelControlledDisplayFields(t *testing.T) {
	a := cliReq(t)
	b := a
	b.ToolCallID = "call_other"
	b.Rationale = "please"
	b.ID = "forged"
	b.Subcommand = "push" // display only; KeyFor derives its own
	if KeyFor(a) != KeyFor(b) {
		t.Fatal("tool-call ID, rationale, request ID and Subcommand must not affect the key")
	}
}

func TestFileID_SameFile(t *testing.T) {
	a := binaryID(t)
	b := binaryID(t)
	if !a.SameFile(a) {
		t.Error("a file is the same as itself")
	}
	if a.SameFile(b) {
		t.Error("two different files compared equal")
	}
	var zero FileID
	if zero.SameFile(zero) || a.SameFile(zero) || !zero.IsZero() {
		t.Error("zero FileID must never match")
	}
}

func TestSessionGrants_AddHas(t *testing.T) {
	g := NewSessionGrants(10, nil)
	k := KeyFor(cliReq(t))
	if g.Has(k) {
		t.Fatal("empty grants")
	}
	g.Add(k)
	if !g.Has(k) {
		t.Fatal("added key missing")
	}
	g.Add("")
	if g.Has("") {
		t.Fatal("the empty key must never be granted")
	}
	var nilGrants *SessionGrants
	if nilGrants.Has(k) {
		t.Fatal("nil grants must have nothing")
	}
}

func TestSessionGrants_RateWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	g := NewSessionGrants(2, clk.Now)
	if !g.takePromptSlot() || !g.takePromptSlot() {
		t.Fatal("first two prompts must be allowed")
	}
	if g.takePromptSlot() {
		t.Fatal("third prompt within a minute must be refused")
	}
	clk.Advance(61 * time.Second)
	if !g.takePromptSlot() {
		t.Fatal("window must slide")
	}
}

func TestKeyFor_InvalidUTF8DoesNotCollide(t *testing.T) {
	a := cliReq(t)
	a.Paths = []string{"/w/a\xff"}
	b := a
	b.Paths = []string{"/w/a\xfe"}
	if KeyFor(a) == KeyFor(b) {
		t.Fatal("different invalid UTF-8 bytes must give different keys")
	}
	if fingerprint(a) == fingerprint(b) {
		t.Fatal("different invalid UTF-8 bytes must give different fingerprints")
	}
}
