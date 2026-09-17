package nemoclaw

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/export/shared"
)

// --- Sandbox policy types (NemoClaw openclaw-sandbox.yaml) ---

// SandboxPolicy is the root of the NemoClaw sandbox policy.
type SandboxPolicy struct {
	Version          int                       `yaml:"version"`
	FilesystemPolicy FilesystemPolicy          `yaml:"filesystem_policy"`
	Landlock         LandlockPolicy            `yaml:"landlock"`
	Process          ProcessPolicy             `yaml:"process"`
	NetworkPolicies  map[string]*NetworkPolicy `yaml:"network_policies,omitempty"`
}

type FilesystemPolicy struct {
	IncludeWorkdir bool     `yaml:"include_workdir"`
	ReadOnly       []string `yaml:"read_only"`
	ReadWrite      []string `yaml:"read_write"`
}

type LandlockPolicy struct {
	Compatibility string `yaml:"compatibility"`
}

type ProcessPolicy struct {
	RunAsUser  string `yaml:"run_as_user"`
	RunAsGroup string `yaml:"run_as_group"`
}

type NetworkPolicy struct {
	Name      string            `yaml:"name,omitempty"`
	Endpoints []NetworkEndpoint `yaml:"endpoints"`
	Binaries  []NetworkBinary   `yaml:"binaries,omitempty"`
}

type NetworkBinary struct {
	Path string `yaml:"path"`
}

type NetworkEndpoint struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Protocol    string `yaml:"protocol"`
	Enforcement string `yaml:"enforcement"`
	TLS         string `yaml:"tls"`
	Access      string `yaml:"access"`
}

// BuildPolicy generates a sandbox policy from the Rakitsu config.
func BuildPolicy(cfg *config.Config) (*SandboxPolicy, error) {
	tools := shared.ToolsByName(cfg)

	p := &SandboxPolicy{
		Version: 1,
		FilesystemPolicy: FilesystemPolicy{
			IncludeWorkdir: true,
			ReadOnly: []string{
				"/usr",
				"/lib",
				"/proc",
				"/dev/urandom",
				"/app",
				"/etc",
				"/var/log",
				"/sandbox/.openclaw",
			},
			ReadWrite: []string{
				"/sandbox",
				"/tmp",
				"/dev/null",
				"/sandbox/.openclaw-data",
			},
		},
		Landlock: LandlockPolicy{
			Compatibility: "best_effort",
		},
		Process: ProcessPolicy{
			RunAsUser:  "sandbox",
			RunAsGroup: "sandbox",
		},
	}

	// Add filesystem paths from tool definitions. filepath.Clean normalizes
	// separators and redundant "." / ".." segments but does not confine a
	// path to any root — a config declaring "../../etc" still produces
	// exactly that, cleaned. That's acceptable here: these paths come from
	// the same person's own tool config, the same trust boundary as the rest
	// of the generated policy, not attacker-controlled input.
	for _, t := range cfg.Tools {
		for _, path := range t.AllowedPaths {
			p.FilesystemPolicy.ReadWrite = shared.AppendUnique(p.FilesystemPolicy.ReadWrite, filepath.Clean(path))
		}
		if t.Sandbox != nil {
			for _, path := range t.Sandbox.AllowedPaths {
				p.FilesystemPolicy.ReadWrite = shared.AppendUnique(p.FilesystemPolicy.ReadWrite, filepath.Clean(path))
			}
		}
	}

	// Build network policies based on providers
	policies, err := buildNetworkPolicies(cfg, tools)
	if err != nil {
		return nil, err
	}
	p.NetworkPolicies = policies

	return p, nil
}

func buildNetworkPolicies(cfg *config.Config, tools map[string]config.ToolDefinition) (map[string]*NetworkPolicy, error) {
	policies := make(map[string]*NetworkPolicy)

	// Provider API endpoints
	var providerEndpoints []NetworkEndpoint
	for name, pDef := range cfg.Settings.Providers {
		pType := pDef.Type
		if pType == "" {
			pType = name
		}
		ep := providerEndpointForType(pType, pDef.BaseURL)
		if ep.Host != "" {
			providerEndpoints = append(providerEndpoints, ep)
		}
	}

	if len(providerEndpoints) > 0 {
		policies["inference"] = &NetworkPolicy{
			Name:      "inference-providers",
			Endpoints: providerEndpoints,
			// Node runs the OpenClaw agent that calls LLM providers.
			// NemoClaw's egress proxy enforces per-binary allowlists.
			Binaries: []NetworkBinary{
				{Path: "/usr/local/bin/node"},
				{Path: "/usr/bin/node"},
			},
		}
	}

	// OpenClaw platform
	policies["openclaw_api"] = &NetworkPolicy{
		Name: "openclaw-platform",
		Endpoints: []NetworkEndpoint{
			{
				Host:        "api.openclaw.ai",
				Port:        443,
				Protocol:    "rest",
				Enforcement: "enforce",
				TLS:         "terminate",
				Access:      "full",
			},
		},
	}

	// Tool-specific network access
	for _, t := range cfg.Tools {
		if t.Sandbox != nil && t.Sandbox.NetworkIsolated {
			continue
		}
		if t.Type == "mcp_server" && t.URL != "" {
			// Expand env vars before extracting host/port — see
			// providerEndpointForType's comment: policy enforcement matches
			// on literal strings, so an unexpanded ${VAR} would never match
			// the sandbox's actual outbound connection.
			resolved := os.ExpandEnv(t.URL)
			host, port := shared.ExtractHostPort(resolved)
			if host == "" {
				continue // skip tools with unparseable URLs
			}
			tls := "terminate"
			if strings.HasPrefix(resolved, "http://") {
				tls = ""
			}
			if port == 0 {
				// Scheme-aware default, mirroring providerEndpointForType —
				// defaulting to 443 regardless of scheme would otherwise
				// disagree with the tls value set above.
				if tls == "" {
					port = 80
				} else {
					port = 443
				}
			}
			key := fmt.Sprintf("tool_%s", shared.SanitizeID(t.Name))
			if existing, exists := policies[key]; exists {
				return nil, fmt.Errorf("tools %q and %q both sanitize to the same network-policy key %q — rename one", existing.Name, t.Name, key)
			}
			policies[key] = &NetworkPolicy{
				Name: t.Name,
				Endpoints: []NetworkEndpoint{
					{
						Host:        host,
						Port:        port,
						Protocol:    "rest",
						Enforcement: "enforce",
						TLS:         tls,
						Access:      "full",
					},
				},
			}
		}
	}

	return policies, nil
}

func providerEndpointForType(pType, baseURL string) NetworkEndpoint {
	ep := NetworkEndpoint{
		Port:        443,
		Protocol:    "rest",
		Enforcement: "enforce",
		TLS:         "terminate",
		Access:      "full",
	}

	if baseURL != "" {
		// Expand env vars so OpenShell sees the real hostname.
		// Policy enforcement matches on literal strings, so ${VAR} would never match.
		resolved := os.ExpandEnv(baseURL)
		host, port := shared.ExtractHostPort(resolved)
		if host == "" {
			return NetworkEndpoint{} // skip endpoint if host extraction failed
		}
		ep.Host = host
		// Checked independently of whether the port was explicit — a
		// previous version only cleared TLS in the no-port branch below, so
		// "http://host:8080" (explicit port) kept the default tls: terminate
		// despite being plaintext.
		if strings.HasPrefix(resolved, "http://") {
			ep.TLS = "" // plaintext HTTP — must not claim tls: terminate
		}
		if port > 0 {
			ep.Port = port
		} else if ep.TLS == "" {
			ep.Port = 80
		}
		return ep
	}

	switch pType {
	case "openai":
		ep.Host = "api.openai.com"
	case "anthropic":
		ep.Host = "api.anthropic.com"
	case "gemini":
		ep.Host = "generativelanguage.googleapis.com"
	case "ollama":
		ep.Host = "127.0.0.1"
		ep.Port = 11434
		ep.TLS = ""
		ep.Enforcement = "permissive"
	default:
		return NetworkEndpoint{}
	}
	return ep
}
