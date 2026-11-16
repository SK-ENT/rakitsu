package permission

import (
	"strings"
	"testing"
)

func TestClassifyRisk_Table(t *testing.T) {
	cases := []struct {
		argv string
		want Risk
	}{
		// Shells.
		{"sh -c ls", RiskShell},
		{"bash -c ls", RiskShell},
		{"zsh", RiskShell},
		{"dash -c x", RiskShell},
		{"ksh", RiskShell},
		{"fish -c x", RiskShell},
		{"/bin/sh -c ls", RiskShell},
		{"/bin/BASH -c ls", RiskShell}, // case-insensitive file systems run this as bash
		{"bash.exe -c ls", RiskShell},

		// Interpreters.
		{"python -c x", RiskInterpreter},
		{"python3 x.py", RiskInterpreter},
		{"python3.12 x.py", RiskInterpreter},
		{"/usr/bin/python3.11", RiskInterpreter},
		{"node x.js", RiskInterpreter},
		{"ruby -e x", RiskInterpreter},
		{"perl -e x", RiskInterpreter},
		{"deno run x", RiskInterpreter},
		{"bun x", RiskInterpreter},
		{"php -r x", RiskInterpreter},
		{"lua x", RiskInterpreter},
		{"osascript -e x", RiskInterpreter},

		// Re-exec wrappers.
		{"env FOO=1 ls", RiskReExec},
		{"xargs rm", RiskReExec},
		{"nohup ls", RiskReExec},
		{"timeout 5 ls", RiskReExec},
		{"nice ls", RiskReExec},
		{"find . -exec ls {} ;", RiskReExec},
		{"find . -execdir ls {} ;", RiskReExec},
		{"find . -ok ls {} ;", RiskReExec},
		{"find . -okdir ls {} ;", RiskReExec},
		{"find . -name x -delete", RiskReExec},

		// Installers.
		{"npm install", RiskInstaller},
		{"npm i left-pad", RiskInstaller},
		{"npm ci", RiskInstaller},
		{"npm exec foo", RiskInstaller},
		{"npm x foo", RiskInstaller},
		{"npm --prefix sub install", RiskInstaller},
		{"npx foo", RiskInstaller},
		{"pnpm dlx foo", RiskInstaller},
		{"yarn dlx foo", RiskInstaller},
		{"pip install x", RiskInstaller},
		{"pip3 install x", RiskInstaller},
		{"brew list", RiskInstaller},
		{"cargo install x", RiskInstaller},
		{"go install x@latest", RiskInstaller},
		{"go run .", RiskInstaller},
		{"gem install x", RiskInstaller},

		// Download-and-execute.
		{"curl https://example.invalid", RiskDownloadExec},
		{"wget https://example.invalid", RiskDownloadExec},
		{"/usr/bin/curl -s x", RiskDownloadExec},

		// Code-executing flags.
		{"git -c core.sshCommand=x fetch", RiskCodeExecFlag},
		{"git --config-env=core.pager=X log", RiskCodeExecFlag},
		{"git --config-env core.pager=X log", RiskCodeExecFlag},
		{"git fetch --upload-pack=x origin", RiskCodeExecFlag},
		{"git fetch --upload-pack x origin", RiskCodeExecFlag},
		{"git push --receive-pack=x origin", RiskCodeExecFlag},
		{"git --exec-path=/tmp/x status", RiskCodeExecFlag},
		{"git --exec-path status", RiskCodeExecFlag},
		{"git -C /w -c a=b status", RiskCodeExecFlag},

		// Destructive.
		{"git clean -fdx", RiskDestructive},
		{"git reset --hard", RiskDestructive},
		{"git reset --hard HEAD~1", RiskDestructive},
		{"git push --force", RiskDestructive},
		{"git push -f origin main", RiskDestructive},
		{"git push -uf origin main", RiskDestructive},
		{"git -C /w push origin main --force", RiskDestructive},
		{"git checkout -- .", RiskDestructive},
		{"git checkout -- file.go", RiskDestructive},
		{"truncate -s 0 x", RiskDestructive},
		{"shred x", RiskDestructive},

		// Negatives.
		{"git status", RiskNone},
		{"git log --oneline", RiskNone},
		{"git -C /w status", RiskNone},
		{"git push origin main", RiskNone},
		{"git reset HEAD file", RiskNone},
		{"git checkout main", RiskNone},
		{"git commit -m fix", RiskNone},
		{"go test ./...", RiskNone},
		{"go build ./...", RiskNone},
		{"ls -la", RiskNone},
		{"find . -name x", RiskNone},
		{"npm test", RiskNone},
		{"npm run build", RiskNone},
		{"pip list", RiskNone},
		{"cargo build", RiskNone},
		{"gem list", RiskNone},
		{"cat file", RiskNone},
		{"shellcheck x.sh", RiskNone}, // name prefix must not match "sh"
		{"pythonic", RiskNone},
	}
	for _, c := range cases {
		argv := strings.Fields(c.argv)
		if got := ClassifyRisk(argv); got != c.want {
			t.Errorf("ClassifyRisk(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestClassifyRisk_EmptyArgv(t *testing.T) {
	if got := ClassifyRisk(nil); got != RiskNone {
		t.Fatalf("ClassifyRisk(nil) = %q, want none", got)
	}
}
