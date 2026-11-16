package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/permission"
	"gopkg.in/yaml.v3"
)

// askModeEnabled gates settings.permissions.mode: ask. It stays false until
// the broker is wired end to end; a var (not a const) only so tests can flip it.
var askModeEnabled = false
var semiAutoModeEnabled = false
var autoModeEnabled = false

const (
	defaultAskTimeoutSeconds   = 120
	defaultMaxPromptsPerMinute = 10
)

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PermissionsConfig selects the permission broker. Absent = legacy behaviour.
type PermissionsConfig struct {
	Mode                permission.Mode   `mapstructure:"mode" yaml:"mode,omitempty"`
	AvailableModes      []permission.Mode `mapstructure:"available_modes" yaml:"available_modes,omitempty"`
	AskTimeoutSeconds   int               `mapstructure:"ask_timeout_seconds" yaml:"ask_timeout_seconds,omitempty"`
	MaxPromptsPerMinute int               `mapstructure:"max_prompts_per_minute" yaml:"max_prompts_per_minute,omitempty"`
	EnvPassthrough      []string          `mapstructure:"env_passthrough" yaml:"env_passthrough,omitempty"`
	TrustedPath         []string          `mapstructure:"trusted_path" yaml:"trusted_path,omitempty"`
}

// Ask reports whether the approval broker is selected.
func (p PermissionsConfig) Ask() bool { return p.Mode == "ask" }

// EffectiveAskTimeout is how long a prompt waits for an answer (default 120s).
func (p PermissionsConfig) EffectiveAskTimeout() time.Duration {
	if p.AskTimeoutSeconds <= 0 {
		return defaultAskTimeoutSeconds * time.Second
	}
	return time.Duration(p.AskTimeoutSeconds) * time.Second
}

// EffectiveMaxPromptsPerMinute is the per-session prompt rate cap (default 10).
func (p PermissionsConfig) EffectiveMaxPromptsPerMinute() int {
	if p.MaxPromptsPerMinute <= 0 {
		return defaultMaxPromptsPerMinute
	}
	return p.MaxPromptsPerMinute
}

// loadPermissions fills config.Settings.Permissions from the YAML file only.
// It decodes YAML strictly without environment lookup or scalar coercion.
// Unknown keys and environment policy overrides fail the load.
// It also records config.SourceFiles.
func loadPermissions(configPath string, config *Config) error {
	if err := rejectPolicyEnvironment(); err != nil {
		return err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}
	config.Settings.Permissions = PermissionsConfig{}
	config.Settings.Focus = FocusConfig{}
	if len(root.Content) > 0 {
		if err := validatePolicyKeys(root.Content[0], "", map[*yaml.Node]bool{}); err != nil {
			return err
		}
		var resolved map[string]any
		if err := root.Decode(&resolved); err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		// YAML merges and aliases can introduce collisions across source mappings.
		resolvedData, err := yaml.Marshal(resolved)
		if err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		var resolvedRoot yaml.Node
		if err := yaml.Unmarshal(resolvedData, &resolvedRoot); err != nil {
			return fmt.Errorf("failed to read config file: %w", err)
		}
		if err := validatePolicyKeys(resolvedRoot.Content[0], "", map[*yaml.Node]bool{}); err != nil {
			return err
		}
		for name, value := range resolved {
			if !strings.EqualFold(name, "settings") {
				continue
			}
			settings, ok := value.(map[string]any)
			if !ok {
				continue
			}
			for key, value := range settings {
				field := "settings." + strings.ToLower(key)
				if field != "settings.permissions" && field != "settings.focus" {
					continue
				}
				data, err := yaml.Marshal(value)
				if err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
				var block yaml.Node
				if err := yaml.Unmarshal(data, &block); err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
				node := block.Content[0]
				switch field {
				case "settings.permissions":
					if err := decodePolicyBlock(node, &config.Settings.Permissions, field); err != nil {
						return err
					}
					if config.Settings.Permissions.Mode == "" {
						config.Settings.Permissions.Mode = permission.ModeAsk
					}
				case "settings.focus":
					if err := decodePolicyBlock(node, &config.Settings.Focus, field); err != nil {
						return err
					}
				}
			}
		}
	}

	abs, err := filepath.Abs(configPath)
	if err != nil {
		abs = configPath
	}
	dir := filepath.Dir(abs)
	config.SourceFiles = []string{abs}
	// The loaders do not expose per-file lists, so record the conventional
	// definition directories instead of every discovered file.
	for _, d := range []string{"agents", "tools", "skills", "orchestrators", "prompts"} {
		config.SourceFiles = append(config.SourceFiles, filepath.Join(dir, d))
	}
	return nil
}

// Viper resolves policy keys and their ancestors via AutomaticEnv. Reject the
// entire policy namespace, including empty values and run-scoped overrides.
func rejectPolicyEnvironment() error {
	names := make(map[string]bool)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		names[name] = true
	}
	envOverridesMu.RLock()
	for name := range envOverrides {
		names[name] = true
	}
	envOverridesMu.RUnlock()
	var policyNames []string
	for name := range names {
		key := strings.ToUpper(strings.ReplaceAll(name, ".", "_"))
		if key == "SETTINGS" || key == "SETTINGS_FOCUS" || strings.HasPrefix(key, "SETTINGS_FOCUS_") || key == "SETTINGS_PERMISSIONS" || strings.HasPrefix(key, "SETTINGS_PERMISSIONS_") {
			policyNames = append(policyNames, name)
		}
	}
	sort.Strings(policyNames)
	if len(policyNames) > 0 {
		return fmt.Errorf("security policy cannot come from the environment (unset %s)", policyNames[0])
	}
	return nil
}

func policyKeyScope(field, key string) (string, bool) {
	key = strings.ToLower(key)
	switch field {
	case "":
		return key, key == "settings"
	case "settings":
		return field + "." + key, key == "permissions" || key == "focus"
	default:
		return field + "." + key, true
	}
}

// Inspect source nodes before map decoding loses duplicate keys. Inspect the
// resolved mapping again so mixed-case keys from aliases/merges also fail.
func validatePolicyKeys(node *yaml.Node, field string, active map[*yaml.Node]bool) error {
	if node == nil || active[node] {
		return fmt.Errorf("security policy contains a recursive YAML alias")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yaml.AliasNode {
		return validatePolicyKeys(node.Alias, field, active)
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			if value.Kind == yaml.SequenceNode {
				for _, merged := range value.Content {
					if err := validatePolicyKeys(merged, field, active); err != nil {
						return err
					}
				}
			} else if err := validatePolicyKeys(value, field, active); err != nil {
				return err
			}
			continue
		}
		scope, relevant := policyKeyScope(field, key.Value)
		if !relevant {
			continue
		}
		if seen[scope] {
			return fmt.Errorf("%s: duplicate policy key (case-insensitive)", scope)
		}
		seen[scope] = true
		if field == "" || field == "settings" {
			if err := validatePolicyKeys(value, scope, active); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePermissions checks settings.permissions and, under mode: ask,
// refuses features the broker does not mediate yet.
func (c *Config) validatePermissions(add func(field, msg string)) {
	p := c.Settings.Permissions
	switch p.Mode {
	case "", permission.ModeLegacy:
	case permission.ModeAsk:
		if !askModeEnabled {
			add("settings.permissions.mode", `"ask" is not enabled in this build`)
		}
	case permission.ModeSemiAuto:
		if !semiAutoModeEnabled {
			add("settings.permissions.mode", `"semi_auto" is not enabled in this build`)
		}
	case permission.ModeAuto:
		if !autoModeEnabled {
			add("settings.permissions.mode", `"auto" is not enabled in this build`)
		}
	default:
		add("settings.permissions.mode", fmt.Sprintf("unknown value %q: must be legacy, ask, semi_auto or auto", p.Mode))
	}
	for _, mode := range p.AvailableModes {
		if mode == permission.ModeAsk && !askModeEnabled || mode == permission.ModeSemiAuto && !semiAutoModeEnabled || mode == permission.ModeAuto && !autoModeEnabled {
			add("settings.permissions.available_modes", fmt.Sprintf("%q is not enabled in this build", mode))
		}
	}
	if c.Settings.Focus.Mode != "" && !c.Settings.Focus.Mode.Valid() {
		add("settings.focus.mode", fmt.Sprintf("unknown value %q: must be coding, plan or design", c.Settings.Focus.Mode))
	}
	if err := c.ValidatePlan(); err != nil {
		add("settings.focus.mode", err.Error())
	}
	if _, err := c.Settings.PolicyConfig().ResolveSelection(permission.Selection{}); err != nil {
		add("settings.permissions", err.Error())
	}
	if p.AskTimeoutSeconds != 0 && (p.AskTimeoutSeconds < 10 || p.AskTimeoutSeconds > 600) {
		add("settings.permissions.ask_timeout_seconds", "must be between 10 and 600")
	}
	if p.MaxPromptsPerMinute != 0 && (p.MaxPromptsPerMinute < 1 || p.MaxPromptsPerMinute > 60) {
		add("settings.permissions.max_prompts_per_minute", "must be between 1 and 60")
	}
	for i, n := range p.EnvPassthrough {
		f := fmt.Sprintf("settings.permissions.env_passthrough[%d]", i)
		switch {
		case !envNameRe.MatchString(n):
			add(f, fmt.Sprintf("%q is not a valid environment variable name", n))
		case strings.HasPrefix(strings.ToUpper(n), "RAKITSU_"):
			add(f, fmt.Sprintf("%q is refused: RAKITSU_* variables carry control tokens", n))
		case strings.EqualFold(n, "PATH"):
			add(f, "PATH is refused: it is pinned separately (see trusted_path)")
		}
	}
	for i, d := range p.TrustedPath {
		if !filepath.IsAbs(d) {
			add(fmt.Sprintf("settings.permissions.trusted_path[%d]", i), fmt.Sprintf("%q must be an absolute directory", d))
		} else if filepath.Clean(d) != d {
			add(fmt.Sprintf("settings.permissions.trusted_path[%d]", i), fmt.Sprintf("%q must be a cleaned path (want %q)", d, filepath.Clean(d)))
		}
	}

	if !p.Mode.Secure() {
		return
	}
	const hint = "; remove it, or drop settings.permissions.mode to use it"
	check := func(t ToolDefinition, field string) {
		switch t.Type {
		case "mcp_server", "a2a", "jev":
			add(field, fmt.Sprintf("tool %q (type %s) is not available under settings.permissions.mode: %s yet: starting it or calling it needs approvals this version does not implement%s", t.Name, t.Type, p.Mode, hint))
		}
	}
	for i, t := range c.Tools {
		check(t, fmt.Sprintf("tools[%d]", i))
	}
	for ai, a := range c.Agents {
		for ti, t := range a.ToolsInline {
			check(t, fmt.Sprintf("agents[%d].tools_inline[%d]", ai, ti))
		}
	}
	blocked := func(on bool, field string) {
		if on {
			add(field, fmt.Sprintf("is not available under settings.permissions.mode: %s yet: it is a persistence or remote-control surface the broker does not mediate%s", p.Mode, hint))
		}
	}
	blocked(c.Settings.Memory.Enabled, "settings.memory.enabled")
	blocked(c.Settings.SessionMsg.Enabled, "settings.session_msg.enabled")
	blocked(c.Settings.Wake.Enabled, "settings.wake.enabled")
	blocked(len(c.Monitors) > 0, "monitors")
}

func decodePolicyBlock(node *yaml.Node, dst any, field string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: must be a mapping", field)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag != "!!str" {
			return fmt.Errorf("%s: keys must be strings", field)
		}
		switch key.Value {
		case "mode":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return fmt.Errorf("%s.mode: must be a non-empty string", field)
			}
		case "ask_timeout_seconds", "max_prompts_per_minute":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return fmt.Errorf("%s.%s: must be an integer", field, key.Value)
			}
		case "available_modes", "env_passthrough", "trusted_path":
			if value.Kind != yaml.SequenceNode || key.Value == "available_modes" && len(value.Content) == 0 {
				return fmt.Errorf("%s.%s: must be a sequence", field, key.Value)
			}
			for _, v := range value.Content {
				if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
					return fmt.Errorf("%s.%s: values must be strings", field, key.Value)
				}
			}
		}
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		msg := err.Error()
		if i := strings.Index(msg, "field "); i >= 0 {
			tail := msg[i+len("field "):]
			if j := strings.Index(tail, " not found"); j >= 0 {
				msg = fmt.Sprintf("unknown key %q", tail[:j])
			}
		}
		return fmt.Errorf("%s: %s", field, msg)
	}
	return nil
}
