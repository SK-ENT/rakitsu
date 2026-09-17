package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SK-ENT/rakitsu/internal/scaffold"
	"github.com/spf13/cobra"
)

var scaffoldCmd = &cobra.Command{
	Use:   "scaffold [use-case]",
	Short: "Generate a ready-to-run agent config from a use-case preset",
	Long: `Generate a Rakitsu configuration from a built-in use-case preset.

Outputs a single YAML file by default, or a modular directory with --dir.

Use-cases:
  llm-chat        Simple conversational assistant
  code-review     Code reviewer with file-system tools
  data-analysis   Data analyst with fs + CLI tools
  web-research    Research agent (reasoning only, no tools)
  dev-team        Multi-agent: planner → developer → reviewer
  qa-pipeline     Multi-agent: test generator → validator
  rag-assistant   RAG-style: search knowledge base + synthesise

Examples:
  rakitsu scaffold llm-chat
  rakitsu scaffold code-review --provider anthropic --model claude-sonnet-4-6
  rakitsu scaffold dev-team --dir -o ./my-dev-team
  rakitsu scaffold --list`,
	Args: cobra.MaximumNArgs(1),
	RunE: runScaffold,
}

var (
	scaffoldOutput   string
	scaffoldProvider string
	scaffoldModel    string
	scaffoldDir      bool
	scaffoldList     bool
	scaffoldForce    bool
)

func init() {
	rootCmd.AddCommand(scaffoldCmd)

	scaffoldCmd.Flags().StringVarP(&scaffoldOutput, "output", "o", "", "Output file or directory path (default: <use-case>.yaml or ./<use-case>/)")
	scaffoldCmd.Flags().StringVarP(&scaffoldProvider, "provider", "p", "openai", "LLM provider: openai, anthropic, gemini, ollama, litellm")
	scaffoldCmd.Flags().StringVarP(&scaffoldModel, "model", "m", "", "Model name (default: provider's recommended model)")
	scaffoldCmd.Flags().BoolVar(&scaffoldDir, "dir", false, "Scaffold as modular directory instead of single YAML")
	scaffoldCmd.Flags().BoolVarP(&scaffoldList, "list", "l", false, "List all available use-cases")
	scaffoldCmd.Flags().BoolVarP(&scaffoldForce, "force", "f", false, "Overwrite existing files at the output path")
}

func runScaffold(cmd *cobra.Command, args []string) error {
	if scaffoldList {
		fmt.Print(scaffold.ListTable())
		return nil
	}

	if len(args) == 0 {
		return fmt.Errorf("use-case required; run 'rakitsu scaffold --list' to see available options")
	}

	useCase := args[0]
	preset, ok := scaffold.Presets[useCase]
	if !ok {
		available := make([]string, 0, len(scaffold.PresetsOrdered))
		for _, id := range scaffold.PresetsOrdered {
			available = append(available, id)
		}
		return fmt.Errorf("unknown use-case %q — available: %s", useCase, strings.Join(available, ", "))
	}

	// Resolve model
	model := scaffoldModel
	if model == "" {
		model = scaffold.DefaultModel(scaffoldProvider)
	}

	// Resolve API key / base URL env vars
	apiKeyEnv := scaffold.APIKeyEnvVar(scaffoldProvider)
	baseURLEnv := scaffold.BaseURLEnvVar(scaffoldProvider)

	data := scaffold.TemplateData{
		Provider:   scaffoldProvider,
		Model:      model,
		APIKeyEnv:  apiKeyEnv,
		BaseURLEnv: baseURLEnv,
	}

	// Render templates
	files, err := scaffold.Render(preset, data, scaffoldDir)
	if err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}

	// Resolve output base path
	outputBase := scaffoldOutput
	if outputBase == "" {
		if scaffoldDir {
			outputBase = "./" + useCase
		} else {
			outputBase = useCase + ".yaml"
		}
	}

	// Write output
	if scaffoldDir {
		return writeDir(outputBase, files, scaffoldForce)
	}
	return writeFile(outputBase, files, scaffoldForce)
}

// writeFile writes the single rendered YAML to the given path. Refuses to
// clobber an existing file unless force is set.
func writeFile(path string, files map[string]string, force bool) error {
	// files has exactly one entry; take its content
	var content string
	for _, v := range files {
		content = v
		break
	}

	if !force {
		// Lstat, not Stat: a dangling symlink at path would make Stat report
		// os.ErrNotExist (it follows the link) and let the write through it
		// to the symlink's target — defeating the overwrite guard below.
		// Lstat sees the symlink itself, which always exists here.
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s already exists — pass --force to overwrite", path)
		}
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("✓ Created %s\n", path)
	fmt.Printf("\nNext step:\n  rakitsu run %s \"your query here\"\n", path)
	return nil
}

// writeDir writes the modular directory layout. Refuses to clobber any
// already-existing file unless force is set — checked up front so a
// conflict is reported before any file in the layout is written, not
// after a partial write has already touched the directory.
func writeDir(base string, files map[string]string, force bool) error {
	if !force {
		// Lstat, not Stat — see the matching comment in writeFile.
		for relPath := range files {
			dest := filepath.Join(base, filepath.FromSlash(relPath))
			if _, err := os.Lstat(dest); err == nil {
				return fmt.Errorf("%s already exists — pass --force to overwrite", dest)
			}
		}
	}

	for relPath, content := range files {
		dest := filepath.Join(base, filepath.FromSlash(relPath))
		dir := filepath.Dir(dest)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("  %s\n", dest)
	}
	fmt.Printf("\n✓ Scaffolded %s/\n", base)
	fmt.Printf("\nNext step:\n  rakitsu run %s/config.yaml \"your query here\"\n", base)
	return nil
}
