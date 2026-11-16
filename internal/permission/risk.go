package permission

import "strings"

// Risky classes are one-action only: they never get a session option.
// Matching is on the lower-cased base name of argv[0] with a trailing
// Windows launcher suffix (".exe", ".cmd", ".bat", ".com") removed, so "/bin/BASH" (case-insensitive file systems) and
// "bash.exe" count as bash. Additions are cheap: add an entry and a row in
// risk_test.go.

// riskByName classifies a command by argv[0] alone.
var riskByName = map[string]Risk{
	// Shells.
	"sh": RiskShell, "bash": RiskShell, "zsh": RiskShell,
	"dash": RiskShell, "ksh": RiskShell, "fish": RiskShell,

	// Interpreters ("python3.N" is handled in nameRisk).
	"python": RiskInterpreter, "python3": RiskInterpreter,
	"node": RiskInterpreter, "ruby": RiskInterpreter, "perl": RiskInterpreter,
	"deno": RiskInterpreter, "bun": RiskInterpreter, "php": RiskInterpreter,
	"lua": RiskInterpreter, "osascript": RiskInterpreter,

	// Re-exec wrappers.
	"env": RiskReExec, "xargs": RiskReExec, "nohup": RiskReExec,
	"timeout": RiskReExec, "nice": RiskReExec,

	// Installers that are risky whatever the arguments.
	"npx": RiskInstaller, "brew": RiskInstaller,

	// Download-and-execute.
	"curl": RiskDownloadExec, "wget": RiskDownloadExec,

	// Destructive.
	"truncate": RiskDestructive, "shred": RiskDestructive,
}

// installerVerbs lists, per package manager, the subcommands that install
// or execute downloaded code.
var installerVerbs = map[string]map[string]bool{
	"npm":   {"install": true, "i": true, "ci": true, "exec": true, "x": true},
	"pnpm":  {"dlx": true},
	"yarn":  {"dlx": true},
	"pip":   {"install": true},
	"pip3":  {"install": true},
	"cargo": {"install": true},
	"go":    {"install": true, "run": true},
	"gem":   {"install": true},
}

// findExecFlags are find primaries that run commands or delete files.
var findExecFlags = map[string]bool{
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true, "-delete": true,
}

// commandName returns the normalized name used for matching.
func commandName(argv0 string) string {
	// Accept both separators: a Windows-style path must not hide the name.
	name := argv0
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com"} {
		if n, ok := strings.CutSuffix(name, ext); ok {
			return n
		}
	}
	return name
}

// isPythonVersioned matches "python3.<digits>[.<digits>]".
func isPythonVersioned(name string) bool {
	rest, ok := strings.CutPrefix(name, "python3.")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// isPipVersioned matches "pip3.<digits>" (the versioned pip3 launcher).
func isPipVersioned(name string) bool {
	rest, ok := strings.CutPrefix(name, "pip3.")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ClassifyRisk returns the risky class of a final argv, or RiskNone. It is
// pure (no I/O): it cannot see through symlinks or renamed binaries.
func ClassifyRisk(argv []string) Risk {
	if len(argv) == 0 {
		return RiskNone
	}
	name := commandName(argv[0])
	args := argv[1:]

	if isPythonVersioned(name) {
		return RiskInterpreter
	}
	if isPipVersioned(name) {
		name = "pip3"
	}
	if r, ok := riskByName[name]; ok {
		return r
	}
	switch name {
	case "find":
		for _, a := range args {
			if findExecFlags[a] {
				return RiskReExec
			}
		}
	case "git":
		return gitRisk(args)
	}
	if verbs, ok := installerVerbs[name]; ok {
		// Global flags may take values ("npm --prefix dir install"), so any
		// non-flag argument naming an installer verb counts. This can
		// over-match (e.g. "npm run i"), which only costs an extra prompt.
		for _, a := range args {
			if !strings.HasPrefix(a, "-") && verbs[a] {
				return RiskInstaller
			}
		}
	}
	return RiskNone
}

// gitGlobalWithValue are git global options whose value is the next argv
// element.
var gitGlobalWithValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--super-prefix": true, "--config-env": true,
}

// gitRisk classifies git arguments (argv without argv[0]).
func gitRisk(args []string) Risk {
	// Code-executing flags anywhere on the line, including after "--".
	// Matching the whole line (not just the global position) over-matches
	// harmlessly and catches "git fetch --upload-pack=x".
	for _, a := range args {
		switch {
		case a == "-c",
			a == "--config-env", strings.HasPrefix(a, "--config-env="),
			a == "--upload-pack", strings.HasPrefix(a, "--upload-pack="),
			a == "--receive-pack", strings.HasPrefix(a, "--receive-pack="),
			a == "--exec-path", strings.HasPrefix(a, "--exec-path="):
			return RiskCodeExecFlag
		}
	}

	// Find the subcommand: skip global options and their values.
	sub, i := "", 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			sub = a
			break
		}
		if gitGlobalWithValue[a] {
			i++ // skip the value
		}
		i++
	}
	rest := []string{}
	if sub != "" {
		rest = args[i+1:]
	}

	switch sub {
	case "clean":
		return RiskDestructive
	case "reset":
		for _, a := range rest {
			if a == "--hard" {
				return RiskDestructive
			}
		}
	case "push":
		for _, a := range rest {
			if a == "--force" || isShortCluster(a, 'f') {
				return RiskDestructive
			}
		}
	case "checkout":
		// "git checkout -- <paths>" discards local changes. The plan names
		// "git checkout -- ."; any pathspec after "--" is the same
		// operation, so all of them count.
		for j, a := range rest {
			if a == "--" && j+1 < len(rest) {
				return RiskDestructive
			}
		}
	}
	return RiskNone
}

// isShortCluster reports whether a is a single-dash short-flag cluster
// ("-f", "-uf") that contains c.
func isShortCluster(a string, c byte) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	return strings.IndexByte(a[1:], c) >= 0
}
