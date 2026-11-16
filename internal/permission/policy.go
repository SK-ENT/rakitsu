package permission

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Focus string

const (
	FocusCoding Focus = "coding"
	FocusPlan   Focus = "plan"
	FocusDesign Focus = "design"
)

func (f Focus) Valid() bool { return f == FocusCoding || f == FocusPlan || f == FocusDesign }
func (f *Focus) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return fmt.Errorf("focus must be a string")
	}
	*f = Focus(n.Value)
	return nil
}

type Mode string

const (
	ModeLegacy   Mode = "legacy"
	ModeAsk      Mode = "ask"
	ModeSemiAuto Mode = "semi_auto"
	ModeAuto     Mode = "auto"
)

func (m Mode) Valid() bool {
	return m == ModeLegacy || m == ModeAsk || m == ModeSemiAuto || m == ModeAuto
}
func (m Mode) Secure() bool { return m == ModeAsk || m == ModeSemiAuto || m == ModeAuto }
func (m *Mode) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return fmt.Errorf("permission mode must be a string")
	}
	*m = Mode(n.Value)
	return nil
}

type Selection struct {
	Focus Focus
	Mode  Mode
}

// PolicyConfig describes selectors only. It cannot supply action authority.
type PolicyConfig struct {
	DefaultFocus   Focus
	AvailableFocus []Focus
	DefaultMode    Mode
	AvailableModes []Mode
}

func (c PolicyConfig) ResolveSelection(s Selection) (Selection, error) {
	if c.DefaultFocus == "" {
		c.DefaultFocus = FocusCoding
	}
	if c.DefaultMode == "" {
		c.DefaultMode = ModeLegacy
	}
	if !c.DefaultFocus.Valid() || !c.DefaultMode.Valid() {
		return Selection{}, fmt.Errorf("invalid configured focus or permission mode")
	}
	if len(c.AvailableFocus) == 0 {
		c.AvailableFocus = []Focus{c.DefaultFocus}
	}
	if len(c.AvailableModes) == 0 {
		c.AvailableModes = []Mode{c.DefaultMode}
	}
	seenF := map[Focus]bool{}
	for _, f := range c.AvailableFocus {
		if !f.Valid() || seenF[f] {
			return Selection{}, fmt.Errorf("invalid or duplicate available focus %q", f)
		}
		seenF[f] = true
	}
	seenM := map[Mode]bool{}
	for _, m := range c.AvailableModes {
		if !m.Valid() || seenM[m] {
			return Selection{}, fmt.Errorf("invalid or duplicate available permission mode %q", m)
		}
		seenM[m] = true
		if m.Secure() != c.DefaultMode.Secure() {
			return Selection{}, fmt.Errorf("legacy and secure permission modes cannot be mixed")
		}
	}
	if !seenF[c.DefaultFocus] || !seenM[c.DefaultMode] {
		return Selection{}, fmt.Errorf("configured defaults must be available")
	}
	if s.Focus == "" {
		s.Focus = c.DefaultFocus
	}
	if s.Mode == "" {
		s.Mode = c.DefaultMode
	}
	if !s.Focus.Valid() || !seenF[s.Focus] {
		return Selection{}, fmt.Errorf("focus %q is unavailable", s.Focus)
	}
	if !s.Mode.Valid() || !seenM[s.Mode] {
		return Selection{}, fmt.Errorf("permission mode %q is unavailable", s.Mode)
	}
	return s, nil
}

// EffectScope distinguishes measured absence from unknown or ambient capability.
// Adapters supply scopes from execution boundaries, never from model text.
type EffectLevel string

const (
	EffectUnknown EffectLevel = "unknown"
	EffectNone    EffectLevel = "none"
	EffectNamed   EffectLevel = "named"
	EffectAmbient EffectLevel = "ambient"
)

type EffectScope struct {
	Level EffectLevel
	Names []string
}

// CommandAuthority is either exact argv authority or a trusted template ceiling.
// A template ceiling has no Argv and pins Executable, Binary and TemplateID.
// The adapter derives TemplateID from the immutable tool definition and policy
// revision and supplies it only for argv rendered from that template.
type CommandAuthority struct {
	Executable string
	TemplateID string
	Argv       []string
	Binary     FileID
}

func (c CommandAuthority) executable() string {
	if c.Executable != "" {
		return c.Executable
	}
	if len(c.Argv) > 0 {
		return c.Argv[0]
	}
	return ""
}

var policyEnvNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Authority struct {
	Rules []AuthorityRule
}

// AuthorityRule covers one complete action scope. Rules are alternatives, so
// an action cannot borrow a root or capability from another rule. Host command
// roots constrain paths and cwd; they do not promise OS containment.
type AuthorityRule struct {
	Kind        Kind
	Tool        string
	Boundary    Boundary
	Roots       []string
	Command     *CommandAuthority
	EnvNames    []string
	Network     EffectScope
	Credentials EffectScope
	Delegation  bool
}

type OperatorPolicy struct {
	HardLimits       Authority
	Requestable      Authority
	HardDenials      []Kind
	AllowChildGrants bool
}

// ApprovedGrant is supplied only by an authenticated approval adapter after
// validating its owner, exact scope, revocation and consumption. Resolution
// never treats a mode, model text or configuration request as approval.
type ApprovedGrant struct {
	RunID          string
	PolicyRevision string
	ExpiresAt      time.Time
	Authority      Authority
	Exact          *PolicyAction
}

type PolicyInputs struct {
	Operator       OperatorPolicy
	Workflow       Authority
	Preauthorized  []Authority
	Routine        []Authority
	HumanGrants    []ApprovedGrant
	RunID          string
	PolicyRevision string
	Now            time.Time
}

type PolicyAction struct {
	TemplateID  string
	Request     Request
	Network     EffectScope
	Credentials EffectScope
	Delegate    bool
}

type Handling string

const (
	HandlingDeny  Handling = "deny"
	HandlingAsk   Handling = "ask"
	HandlingAllow Handling = "allow"
)

// EffectivePolicy owns copies of all inputs. Inherited ceilings and approval
// requirements are separate; changing the selection changes neither grants nor
// authority. Grant lifecycle and dispatch revalidation remain broker duties.
type EffectivePolicy struct {
	selection                         Selection
	authority                         Authority
	requestable                       Authority
	hard                              Authority
	hardDenials                       []Kind
	constraints                       []Mode
	preauthorized                     []Authority
	routine                           []Authority
	human                             []ApprovedGrant
	runID, revision                   string
	now                               time.Time
	readOnly, child, allowChildGrants bool
}

func (p EffectivePolicy) Selection() Selection { return p.selection }
func (p EffectivePolicy) Authority() Authority { return cloneAuthority(p.authority) }
func (p EffectivePolicy) ReadOnly() bool       { return p.readOnly }

func ResolvePolicy(c PolicyConfig, s Selection, parent *EffectivePolicy, in PolicyInputs) (EffectivePolicy, error) {
	s, err := c.ResolveSelection(s)
	if err != nil {
		return EffectivePolicy{}, err
	}
	authorities := []Authority{in.Operator.HardLimits, in.Operator.Requestable, in.Workflow}
	authorities = append(authorities, in.Preauthorized...)
	authorities = append(authorities, in.Routine...)
	for _, a := range authorities {
		if err := validateAuthority(a); err != nil {
			return EffectivePolicy{}, err
		}
	}
	for _, a := range in.Preauthorized {
		for _, rule := range a.Rules {
			if rule.Command != nil && len(rule.Command.Argv) == 0 {
				return EffectivePolicy{}, fmt.Errorf("CLI preauthorization requires exact argv")
			}
		}
	}
	for _, g := range in.HumanGrants {
		if err := validateAuthority(g.Authority); err != nil {
			return EffectivePolicy{}, err
		}
	}
	for _, k := range in.Operator.HardDenials {
		if !k.known() {
			return EffectivePolicy{}, fmt.Errorf("unknown hard denial %q", k)
		}
	}
	p := EffectivePolicy{selection: s, runID: in.RunID, revision: in.PolicyRevision, now: in.Now, readOnly: s.Focus == FocusPlan, allowChildGrants: in.Operator.AllowChildGrants}
	p.hard = intersectAuthority(in.Operator.HardLimits, in.Operator.Requestable)
	p.hardDenials = append([]Kind(nil), in.Operator.HardDenials...)
	if parent != nil {
		if parent.selection.Mode.Secure() && !s.Mode.Secure() {
			return EffectivePolicy{}, fmt.Errorf("a child cannot select legacy under a secure parent")
		}
		p.child = true
		p.hard = intersectAuthority(p.hard, parent.hard)
		p.hardDenials = append(p.hardDenials, parent.hardDenials...)
		p.constraints = append([]Mode(nil), parent.constraints...)
		p.readOnly = p.readOnly || parent.readOnly
		p.allowChildGrants = p.allowChildGrants && parent.allowChildGrants
	}
	p.requestable = intersectAuthority(p.hard, in.Workflow)
	p.authority = cloneAuthority(p.requestable)
	if parent != nil {
		p.authority = intersectAuthority(p.authority, parent.authority)
	}
	if s.Mode.Secure() {
		p.constraints = append(p.constraints, s.Mode)
	}
	for _, a := range in.Preauthorized {
		p.preauthorized = append(p.preauthorized, cloneAuthority(a))
	}
	for _, a := range in.Routine {
		p.routine = append(p.routine, cloneAuthority(a))
	}
	if parent != nil {
		for _, a := range parent.preauthorized {
			p.preauthorized = append(p.preauthorized, cloneAuthority(a))
		}
		for _, a := range parent.routine {
			p.routine = append(p.routine, cloneAuthority(a))
		}
	}
	for _, g := range in.HumanGrants {
		g.Authority = cloneAuthority(g.Authority)
		if g.Exact != nil {
			exact := *g.Exact
			exact.Request = cloneRequest(exact.Request)
			exact.Network = cloneEffectScope(exact.Network)
			exact.Credentials = cloneEffectScope(exact.Credentials)
			g.Exact = &exact
		}
		p.human = append(p.human, g)
	}
	return p, nil
}

// Handling evaluates an action without executing it or consuming any grant.
// A zero policy denies. Ask/Semi-auto/Auto handling never grants capabilities.
func (p EffectivePolicy) Handling(a PolicyAction) Handling {
	return p.HandlingAt(a, time.Now())
}

// HandlingAt supplies the trusted dispatch time to the pure decision function.
func (p EffectivePolicy) HandlingAt(a PolicyAction, now time.Time) Handling {
	r := a.Request
	if !r.Kind.known() || !p.selection.Mode.Valid() || !p.selection.Focus.Valid() {
		return HandlingDeny
	}
	for _, k := range p.hardDenials {
		if k == r.Kind {
			return HandlingDeny
		}
	}
	if p.readOnly && (r.Kind != KindFSRead || a.Delegate) {
		return HandlingDeny
	}
	if !coversAuthority(p.requestable, a) {
		return HandlingDeny
	}
	human := false
	for _, g := range p.human {
		if p.runID == "" || p.revision == "" || g.RunID != p.runID || g.PolicyRevision != p.revision || p.now.IsZero() || now.IsZero() || !now.Before(g.ExpiresAt) {
			continue
		}
		if g.Exact == nil && (r.Risk != RiskNone || r.has(ReasonPersistencePoint)) {
			continue
		}
		if coversAuthority(g.Authority, a) && ((g.Exact == nil && coversExactAuthority(g.Authority, a)) || (g.Exact != nil && policyActionFingerprint(*g.Exact) == policyActionFingerprint(a) && (r.Kind != KindCLIExec || sameExecutable(g.Exact.Request.Binary, r.Binary)))) {
			human = true
			break
		}
	}
	if !coversAuthority(p.authority, a) && !(p.child && p.allowChildGrants && human) {
		return HandlingDeny
	}
	if human {
		return HandlingAllow
	}
	result := HandlingAllow
	preauthorized := anyExactAuthority(p.preauthorized, a)
	routine := r.Risk == RiskNone && !r.has(ReasonPersistencePoint) && routineEffect(a.Network) && routineEffect(a.Credentials) && anyExactAuthority(p.routine, a)
	for _, m := range p.constraints {
		switch m {
		case ModeAsk:
			result = HandlingAsk
		case ModeSemiAuto:
			if !routine {
				result = HandlingAsk
			}
		case ModeAuto:
			if !preauthorized {
				return HandlingDeny
			}
		default:
			return HandlingDeny
		}
	}
	return result
}

func validateAuthority(a Authority) error {
	for i, rule := range a.Rules {
		if err := validateAuthorityRule(rule); err != nil {
			return fmt.Errorf("authority rule %d: %w", i, err)
		}
	}
	return nil
}

func validateAuthorityRule(rule AuthorityRule) error {
	if !rule.Kind.known() {
		return fmt.Errorf("unknown action kind %q", rule.Kind)
	}
	if strings.TrimSpace(rule.Tool) == "" {
		return fmt.Errorf("tool cannot be empty")
	}
	if strings.TrimSpace(rule.Boundary.Type) == "" || strings.TrimSpace(rule.Boundary.Label) == "" {
		return fmt.Errorf("boundary must have a type and label")
	}
	if len(rule.Roots) == 0 {
		return fmt.Errorf("path and cwd scope requires at least one root")
	}
	for _, root := range rule.Roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return fmt.Errorf("root must be a canonical absolute path")
		}
	}
	if rule.Kind == KindCLIExec {
		cmd := rule.Command
		if cmd == nil || !filepath.IsAbs(cmd.executable()) || filepath.Clean(cmd.executable()) != cmd.executable() || cmd.Binary.IsZero() || (len(cmd.Argv) == 0 && strings.TrimSpace(cmd.TemplateID) == "") || (len(cmd.Argv) > 0 && cmd.Argv[0] != cmd.executable()) {
			return fmt.Errorf("CLI command must have an absolute canonical executable and file identity")
		}
	} else if rule.Command != nil {
		return fmt.Errorf("filesystem rule cannot carry command authority")
	}
	for _, name := range rule.EnvNames {
		if !policyEnvNameRE.MatchString(name) {
			return fmt.Errorf("environment name is invalid")
		}
	}
	for _, scope := range []EffectScope{rule.Network, rule.Credentials} {
		if err := validateEffectScope(scope); err != nil {
			return err
		}
	}
	return nil
}
func cloneAuthority(a Authority) Authority {
	out := Authority{Rules: make([]AuthorityRule, len(a.Rules))}
	for i, rule := range a.Rules {
		out.Rules[i] = cloneAuthorityRule(rule)
	}
	return out
}

func cloneAuthorityRule(rule AuthorityRule) AuthorityRule {
	rule.Roots = append([]string(nil), rule.Roots...)
	rule.EnvNames = append([]string(nil), rule.EnvNames...)
	rule.Network = cloneEffectScope(rule.Network)
	rule.Credentials = cloneEffectScope(rule.Credentials)
	if rule.Command != nil {
		cmd := *rule.Command
		cmd.Argv = append([]string(nil), cmd.Argv...)
		rule.Command = &cmd
	}
	return rule
}
func cloneRequest(r Request) Request {
	r.Argv = append([]string(nil), r.Argv...)
	r.Paths = append([]string(nil), r.Paths...)
	r.EnvNames = append([]string(nil), r.EnvNames...)
	r.ParentChain = append([]string(nil), r.ParentChain...)
	r.Reasons = append([]Reason(nil), r.Reasons...)
	return r
}
func hasValue[T comparable](xs []T, v T) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
func common[T comparable](a, b []T) []T {
	var out []T
	for _, v := range a {
		if hasValue(b, v) && !hasValue(out, v) {
			out = append(out, v)
		}
	}
	return out
}
func within(root, path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func equalCommand(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func intersectAuthority(a, b Authority) Authority {
	var out Authority
	for _, ar := range a.Rules {
		for _, br := range b.Rules {
			if ar.Kind != br.Kind || ar.Tool != br.Tool || ar.Boundary != br.Boundary {
				continue
			}
			if (ar.Command == nil) != (br.Command == nil) {
				continue
			}
			var command *CommandAuthority
			if ar.Command != nil {
				command = intersectCommand(ar.Command, br.Command)
				if command == nil {
					continue
				}
			}
			rule := cloneAuthorityRule(ar)
			rule.Command = command
			rule.Roots = nil
			for _, aroot := range ar.Roots {
				for _, broot := range br.Roots {
					switch {
					case within(aroot, broot):
						rule.Roots = append(rule.Roots, broot)
					case within(broot, aroot):
						rule.Roots = append(rule.Roots, aroot)
					}
				}
			}
			if len(rule.Roots) == 0 {
				continue
			}
			rule.Roots = sortedUnique(rule.Roots)
			rule.EnvNames = common(ar.EnvNames, br.EnvNames)
			rule.Network = intersectEffectScope(ar.Network, br.Network)
			rule.Credentials = intersectEffectScope(ar.Credentials, br.Credentials)
			rule.Delegation = ar.Delegation && br.Delegation
			out.Rules = append(out.Rules, rule)
		}
	}
	return out
}

func coversAuthority(bound Authority, a PolicyAction) bool {
	for _, rule := range bound.Rules {
		if coversAuthorityRule(rule, a) {
			return true
		}
	}
	return false
}

func coversAuthorityRule(rule AuthorityRule, a PolicyAction) bool {
	r := a.Request
	if rule.Boundary != r.Boundary || rule.Kind != r.Kind || r.Tool == "" || rule.Tool != r.Tool || (a.Delegate && !rule.Delegation) {
		return false
	}
	for _, name := range r.EnvNames {
		if !policyEnvNameRE.MatchString(name) || !hasValue(rule.EnvNames, name) {
			return false
		}
	}
	if !coversEffectScope(rule.Network, a.Network) || !coversEffectScope(rule.Credentials, a.Credentials) {
		return false
	}
	paths := r.Paths
	if r.Kind == KindCLIExec {
		if rule.Command == nil || len(r.Argv) == 0 || r.Argv[0] != r.BinaryPath || rule.Command.executable() != r.BinaryPath || rule.Command.TemplateID != a.TemplateID || (len(rule.Command.Argv) > 0 && !equalCommand(rule.Command.Argv, r.Argv)) || !sameExecutable(rule.Command.Binary, r.Binary) {
			return false
		}
		paths = append(append([]string(nil), paths...), r.Cwd)
	}
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		found := false
		for _, root := range rule.Roots {
			if within(root, path) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Match inode identity and the captured metadata used by broker grant keys.
func sameExecutable(a, b FileID) bool {
	return a.SameFile(b) && a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime())
}

func anyExactAuthority(grants []Authority, a PolicyAction) bool {
	for _, g := range grants {
		if coversExactAuthority(g, a) {
			return true
		}
	}
	return false
}

func coversExactAuthority(bound Authority, a PolicyAction) bool {
	for _, rule := range bound.Rules {
		if a.Request.Kind == KindCLIExec && (rule.Command == nil || len(rule.Command.Argv) == 0) {
			continue
		}
		if coversAuthorityRule(rule, a) {
			return true
		}
	}
	return false
}

func validateEffectScope(s EffectScope) error {
	switch s.Level {
	case "", EffectUnknown, EffectNone, EffectAmbient:
		if len(s.Names) != 0 {
			return fmt.Errorf("only named effect scopes can contain names")
		}
	case EffectNamed:
		if len(s.Names) == 0 {
			return fmt.Errorf("named effect scope requires names")
		}
		for _, name := range s.Names {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("effect scope names cannot be empty")
			}
		}
	default:
		return fmt.Errorf("unknown effect scope level %q", s.Level)
	}
	return nil
}

func cloneEffectScope(s EffectScope) EffectScope {
	s.Names = append([]string(nil), s.Names...)
	return s
}

func knownEffect(s EffectScope) bool {
	return s.Level != "" && s.Level != EffectUnknown && validateEffectScope(s) == nil
}

func routineEffect(s EffectScope) bool { return knownEffect(s) && s.Level != EffectAmbient }

func coversEffectScope(ceiling, action EffectScope) bool {
	if !knownEffect(ceiling) || !knownEffect(action) {
		return false
	}
	if ceiling.Level == EffectAmbient || action.Level == EffectNone {
		return true
	}
	if ceiling.Level != EffectNamed || action.Level != EffectNamed {
		return false
	}
	for _, name := range action.Names {
		if !hasValue(ceiling.Names, name) {
			return false
		}
	}
	return true
}

func intersectEffectScope(a, b EffectScope) EffectScope {
	if !knownEffect(a) || !knownEffect(b) {
		return EffectScope{Level: EffectUnknown}
	}
	if a.Level == EffectAmbient {
		return cloneEffectScope(b)
	}
	if b.Level == EffectAmbient {
		return cloneEffectScope(a)
	}
	names := common(a.Names, b.Names)
	if a.Level == EffectNone || b.Level == EffectNone || len(names) == 0 {
		return EffectScope{Level: EffectNone}
	}
	return EffectScope{Level: EffectNamed, Names: names}
}

func intersectCommand(a, b *CommandAuthority) *CommandAuthority {
	if a.TemplateID != b.TemplateID || a.executable() != b.executable() || !sameExecutable(a.Binary, b.Binary) {
		return nil
	}
	if len(a.Argv) > 0 && len(b.Argv) > 0 && !equalCommand(a.Argv, b.Argv) {
		return nil
	}
	chosen := a
	if len(b.Argv) > 0 {
		chosen = b
	}
	cmd := *chosen
	cmd.Argv = append([]string(nil), chosen.Argv...)
	return &cmd
}
