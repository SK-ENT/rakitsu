# Rakitsu Development Guide

Complete setup and development guide for macOS and Windows.

---

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Detailed Setup](#detailed-setup)
  - [macOS](#macos-setup)
  - [Windows](#windows-setup)
- [Building](#building)
- [Development Workflow](#development-workflow)
- [Frontend Development](#frontend-development)
- [Running & Testing](#running--testing)
- [Docker Development](#docker-development)
- [Environment Variables](#environment-variables)
- [Project Structure](#project-structure)
- [Makefile Reference](#makefile-reference)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

| Dependency | Version | Required | Notes |
|-----------|---------|----------|-------|
| Go | 1.25+ | Yes | Backend compilation |
| Node.js | 20.19+ (or 22.12+) | Yes | Frontend build — Vite 7's minimum |
| npm | 9+ | Yes | Bundled with Node.js |
| Git | 2.x+ | Yes | Version injection at build time |
| Make | Any | Recommended | Build automation (macOS built-in; Windows via alternatives) |
| air | Latest | Optional | Go hot-reload for development |
| golangci-lint | Latest | Optional | Enhanced Go linting |
| Docker | 20+ | Optional | Containerized builds and testing |
| Ollama | Latest | Optional | Local model backend used by the Quick Start example below ([ollama.com/download](https://ollama.com/download)); skip if using a cloud provider instead |

---

## Quick Start

```bash
# 1. Clone
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu

# 2. Install Go dependencies
go mod download

# 3. Install frontend dependencies
cd web && npm install && cd ..

# 4. Pick a model backend — local Ollama (keyless, the examples' default)…
ollama pull llama3.1:8b
#    …or set a cloud API key and switch the provider in the example config:
# export OPENAI_API_KEY="sk-..."

# 5. Build (binary with embedded web UI)
make build-embedded

# 6. Run
./bin/rakitsu run examples/single/02-single-agent/config.yaml "List files in the current directory"
./bin/rakitsu serve --port 9100
```

---

## Detailed Setup

### macOS Setup

#### 1. Install Go

**Homebrew (recommended):**
```bash
brew install go
```

**Manual download:**
Download from https://go.dev/dl/ and install the `.pkg` for your architecture (Apple Silicon = arm64, Intel = amd64).

Verify:
```bash
go version
# go version go1.25.0 darwin/arm64
```

#### 2. Install Node.js

**Homebrew:**
```bash
brew install node
```

**nvm (recommended for version management):**
```bash
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.0/install.sh | bash
nvm install 22
nvm use 22
```

Verify:
```bash
node --version   # v22.x.x
npm --version    # 10.x.x
```

#### 3. Install Optional Tools

```bash
# Hot-reload for Go development
go install github.com/air-verse/air@latest

# Enhanced linting
brew install golangci-lint
```

#### 4. Clone and Build

```bash
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu
go mod download
cd web && npm install && cd ..
make build-embedded
```

The binary is at `./bin/rakitsu`.

#### macOS-Specific Notes

- **Xcode Command Line Tools**: Required for Git and Make. Install with `xcode-select --install` if not already present.
- **Extended attributes**: The Makefile runs `xattr -rc` on the embedded frontend to strip macOS quarantine flags. This is handled automatically.
- **Apple Silicon (M1/M2/M3/M4)**: Native arm64 builds. No Rosetta needed.

---

### Windows Setup

#### 1. Install Go

Download the `.msi` installer from https://go.dev/dl/ (Windows amd64).

Or use **winget**:
```powershell
winget install GoLang.Go
```

Or use **Chocolatey**:
```powershell
choco install golang
```

Verify (open a **new** terminal after installation):
```powershell
go version
# go version go1.25.0 windows/amd64
```

#### 2. Install Node.js

Download the LTS `.msi` installer from https://nodejs.org/.

Or use **winget**:
```powershell
winget install OpenJS.NodeJS.LTS
```

Or use **Chocolatey**:
```powershell
choco install nodejs-lts
```

Verify:
```powershell
node --version
npm --version
```

#### 3. Install Git

Download from https://git-scm.com/download/win.

Or use **winget**:
```powershell
winget install Git.Git
```

During installation, select "Use Git from the Windows Command Prompt" to add Git to PATH.

#### 4. Install Make (Optional but Recommended)

The Makefile simplifies builds. Choose one method:

**Option A — Chocolatey:**
```powershell
choco install make
```

**Option B — winget (GnuWin32):**
```powershell
winget install GnuWin32.Make
```

**Option C — Without Make:**
You can run the equivalent Go and npm commands directly (see [Building Without Make](#building-without-make)).

#### 5. Install Optional Tools

```powershell
# Hot-reload
go install github.com/air-verse/air@latest

# Linting
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

#### 6. Clone and Build

**PowerShell:**
```powershell
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu
go mod download
cd web; npm install; cd ..
make build-embedded
```

**Without Make:**
```powershell
cd web; npm install; npm run build; cd ..
Remove-Item -Recurse -Force internal\webui\dist -ErrorAction SilentlyContinue
Copy-Item -Recurse web\dist internal\webui\dist
New-Item -ItemType Directory -Force -Path bin | Out-Null
$commit = git rev-parse --short HEAD
go build -ldflags "-X main.Version=v0.2.0-alpha.3.$commit -X main.BuildCommit=$commit" -o bin\rakitsu.exe .\cmd\rakitsu
```

#### Windows-Specific Notes

- **Path separators in YAML configs**: Always use forward slashes (`/`) in YAML files. They work on all platforms including Windows.
- **Environment variables**: Use `$env:OPENAI_API_KEY = "sk-..."` in PowerShell, or `set OPENAI_API_KEY=sk-...` in Command Prompt.
- **Long paths**: If you encounter path-length issues, enable long paths in Windows:
  ```powershell
  # Run as Administrator
  New-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem" -Name "LongPathsEnabled" -Value 1 -PropertyType DWORD -Force
  ```
- **Binary name**: The output binary is `rakitsu.exe` on Windows.
- **Docker Desktop**: Required for Docker-based workflows. Enable WSL 2 backend for best performance.

---

## Building

### Standard Targets

| Command | Description |
|---------|-------------|
| `make build` | Build Go binary only → `bin/rakitsu` |
| `make build-embedded` | Build frontend + embed in binary (recommended) |
| `make build-all` | Cross-compile: linux (amd64+arm64), darwin (amd64+arm64), windows (amd64) — 5 targets |
| `make frontend` | Build Vue.js frontend only → `web/dist/` |
| `make embed-frontend` | Copy `web/dist/` into Go embed path |
| `make clean` | Remove all build artifacts |

### Version Override

```bash
make build VERSION=v0.2.0-beta.1
# Output: rakitsu version v0.2.0-beta.1+abc1234
```

The build commit hash is injected automatically from `git rev-parse --short HEAD`.

### Cross-Compilation

```bash
make build-all
```

Produces:
```
bin/rakitsu-linux-amd64
bin/rakitsu-linux-arm64
bin/rakitsu-darwin-amd64
bin/rakitsu-darwin-arm64
bin/rakitsu-windows-amd64.exe
```

### Building Without Make

If Make is not available (Windows without Make installed):

```powershell
# Build frontend
cd web
npm install
npm run build
cd ..

# Embed frontend
Remove-Item -Recurse -Force internal\webui\dist -ErrorAction SilentlyContinue
Copy-Item -Recurse web\dist internal\webui\dist

# Build Go binary
New-Item -ItemType Directory -Force -Path bin | Out-Null
$commit = git rev-parse --short HEAD
go build -ldflags "-X main.Version=v0.2.0-alpha.3.$commit -X main.BuildCommit=$commit" -o bin\rakitsu.exe .\cmd\rakitsu
```

---

## Development Workflow

### Hot Reload (Go Backend)

```bash
# Install air (one-time)
go install github.com/air-verse/air@latest

# Start development server with auto-rebuild
make dev
```

Air watches `.go` files and rebuilds automatically on changes.

### Frontend Dev Server (Vite)

```bash
cd web
npm run dev
```

Vite starts a dev server (default: `http://localhost:5173`) with hot module replacement (HMR). The frontend proxies API calls to the Go backend.

### Typical Development Loop

1. **Backend changes**: Use `make dev` (air) or manually `make build && ./bin/rakitsu serve`
2. **Frontend changes**: Use `npm run dev` in `web/` for instant HMR
3. **Full integration test**: `make build-embedded && ./bin/rakitsu serve`
4. **Before committing**: `make check && make test`

### Code Formatting & Linting

```bash
make fmt     # Format Go code (gofmt)
make lint    # go vet + golangci-lint
make check   # go mod tidy + verify + vet
```

---

## Frontend Development

### Setup

```bash
cd web
npm install
```

### Scripts

| Command | Description |
|---------|-------------|
| `npm run dev` | Start Vite dev server with HMR |
| `npm run build` | Type-check (vue-tsc) + production build |
| `npm run preview` | Preview production build locally |

### Key Technologies

- **Vue 3** with `<script setup lang="ts">` and Composition API
- **Vue Flow** for the visual graph builder (nodes, edges, handles)
- **Vite 7** for bundling and dev server
- **TypeScript 5.9** with strict mode enabled

### Directory Layout

```
web/src/
├── components/
│   ├── nodes/          # Vue Flow node types (AgentNode, ToolNode, etc.)
│   ├── builder/        # Visual builder UI (VisualBuilder, NodeEditor, etc.)
│   ├── inspector/      # Run inspector (EventItem, RunInspector)
│   └── debugger/       # Debug view (HierarchyTree, ExecutionGraph, etc.)
├── composables/        # Reusable logic (useEventStream, useYamlExport, etc.)
├── types/              # TypeScript types mirroring Go structs
├── App.vue
└── main.ts
```

---

## Running & Testing

### CLI Commands

```bash
# Run agents with a config and query
./bin/rakitsu run examples/single/02-single-agent/config.yaml "Analyze the project structure"

# Start SSE hub for multi-run monitoring
./bin/rakitsu serve --port 9100

# Run with debug SSE server
./bin/rakitsu run examples/single/02-single-agent/config.yaml "query" --debug-port 9200

# Run with trace output (color-coded on stderr)
./bin/rakitsu run examples/single/02-single-agent/config.yaml "query" --trace

# Show version
./bin/rakitsu version

# Run as an ACP server (stdio JSON-RPC, e.g. for Zed integration)
./bin/rakitsu acp examples/single/02-single-agent/config.yaml

# Serve also exposes MCP (/mcp) and A2A (/a2a) endpoints when given a config
./bin/rakitsu serve --config examples/single/02-single-agent/config.yaml --mcp-port 9200
```

### Running Tests

```bash
# All Go tests
make test

# Specific package
go test -v ./internal/agent/...
go test -v ./internal/tools/...

# With coverage
go test -cover ./...

# Race detection
go test -race ./...
```

### Windows Test Commands

```powershell
# All tests
go test -v ./...

# Specific package
go test -v ./internal/agent/...
```

---

## Docker Development

> The root `Dockerfile` and `docker-compose.yml` referenced below ship as
> part of the Docker packaging release step. If your checkout doesn't have
> them yet, `test/stress/docker-compose.yml` is available today for the
> stress/load test harness.

### Build Image

```bash
docker build -t rakitsu .
```

### Run with Docker Compose

```bash
# Set API key
export OPENAI_API_KEY="sk-..."

# Single agent test
docker-compose up test-single

# Multi-agent test
docker-compose up test-multi

# UI server (accessible at http://localhost:8080)
docker-compose up test-ui

# Gemini provider test
export GEMINI_API_KEY="..."
docker-compose up test-gemini
```

### Available Docker Compose Services

| Service | Description | Required Env |
|---------|-------------|-------------|
| `test-single` | Single agent with reflection | `OPENAI_API_KEY` |
| `test-multi` | Multi-agent orchestration | `OPENAI_API_KEY` |
| `test-gemini` | Gemini API key auth | `GEMINI_API_KEY` |
| `test-gemini-sa` | Gemini service account | `GOOGLE_APPLICATION_CREDENTIALS` |
| `test-ui` | Web UI on port 8080 | `OPENAI_API_KEY` |

---

## Environment Variables

### LLM API Keys

| Variable | Provider | Required |
|----------|----------|----------|
| `OPENAI_API_KEY` | OpenAI (GPT-4o, etc.) | At least one provider key |
| `ANTHROPIC_API_KEY` | Anthropic (Claude) | At least one provider key |
| `GEMINI_API_KEY` | Google Gemini | At least one provider key |
| `GOOGLE_APPLICATION_CREDENTIALS` | Gemini (service account) | Optional |
| `LITELLM_API_KEY` | LiteLLM proxy | Optional |
| `LITELLM_BASE_URL` | LiteLLM base URL | Optional |

### Setting Environment Variables

**macOS / Linux (bash/zsh):**
```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."

# Persistent: add to ~/.zshrc or ~/.bashrc
echo 'export OPENAI_API_KEY="sk-..."' >> ~/.zshrc
```

**Windows PowerShell:**
```powershell
$env:OPENAI_API_KEY = "sk-..."

# Persistent (user-level)
[Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-...", "User")
```

**Windows Command Prompt:**
```cmd
set OPENAI_API_KEY=sk-...

:: Persistent
setx OPENAI_API_KEY "sk-..."
```

### YAML Config References

Environment variables are referenced in YAML configs with `${VAR_NAME}` syntax:

```yaml
settings:
  api_keys:
    openai: "${OPENAI_API_KEY}"
    anthropic: "${ANTHROPIC_API_KEY}"
  base_urls:
    ollama: "http://localhost:11434/v1"
```

---

## Project Structure

```
rakitsu/
├── cmd/rakitsu/                 # CLI entry point
│   ├── root.go               #   Root command + global flags
│   ├── run.go                #   rakitsu run — execute agents
│   ├── serve.go              #   rakitsu serve — SSE hub + web UI + /mcp + /a2a
│   ├── acp.go                #   rakitsu acp — ACP stdio server
│   └── scaffold.go           #   rakitsu scaffold — config templates
├── internal/
│   ├── agent/                # ReAct loop + orchestrator
│   │   ├── agent.go          #   Single agent execution
│   │   └── orchestrator.go   #   Multi-agent coordination
│   ├── acp/                  # ACP (Agent Client Protocol) server
│   │   └── server.go         #   Session/prompt JSON-RPC over stdio
│   ├── config/               # YAML config parsing
│   │   └── config.go         #   Viper-based config structs
│   ├── llm/                  # LLM provider abstraction
│   │   ├── provider.go       #   LLMProvider interface
│   │   ├── openai/           #   OpenAI + Ollama + LiteLLM
│   │   ├── anthropic/        #   Anthropic Claude
│   │   └── gemini/           #   Google Gemini
│   ├── tools/                # Tool interface + implementations
│   │   ├── tool.go           #   Tool interface
│   │   ├── cli/              #   CLI tool (sandboxed)
│   │   ├── fs/                #   File system tool (restricted)
│   │   ├── mcp/               #   MCP client tool (mcp_server type)
│   │   └── a2a/               #   A2A delegation tool (a2a type)
│   ├── telemetry/            # Event bus + typed events
│   │   └── events.go         #   EventType constants + payloads
│   ├── server/               # SSE hub + API + runner + MCP/A2A servers
│   │   ├── sse.go            #   SSE server
│   │   ├── hub.go            #   Hub endpoints
│   │   ├── mcp.go            #   MCP server (/mcp)
│   │   ├── runner.go         #   Agent runner
│   │   └── configs.go        #   Config store
│   ├── debug/                # Debugger subsystem
│   │   ├── controller.go     #   Debug controller
│   │   ├── replay.go         #   Replay execution
│   │   └── export.go         #   Config export
│   ├── store/                # Session persistence (JSONL)
│   │   └── store.go
│   └── webui/                # Embedded frontend
│       ├── embed.go          #   //go:embed directive
│       └── dist/             #   Built frontend (copied from web/dist)
├── web/                      # Vue.js frontend source
│   ├── src/
│   │   ├── components/       #   Vue components
│   │   ├── composables/      #   Composition API hooks
│   │   └── types/            #   TypeScript types
│   ├── package.json
│   ├── vite.config.ts
│   └── tsconfig.json
├── examples/                 # Example YAML configs
│   ├── single/                #   Single-file configs (chat, pipeline, dev-team, ...)
│   ├── modular/               #   Same examples, split across agents/tools/skills dirs
│   ├── providers/             #   Per-provider config samples
│   └── eval/                  #   Eval/benchmark configs
├── test/                     # Test configs + workspace
├── docs/                     # Documentation
├── go.mod                    # Go module + dependencies
├── Makefile                  # Build automation
├── Dockerfile                # Multi-stage Docker build
└── docker-compose.yml        # Test service definitions
```

---

## Makefile Reference

| Target | Description |
|--------|-------------|
| `make build` | Build Go binary → `bin/rakitsu` |
| `make build-embedded` | Frontend + embed + Go binary |
| `make build-all` | Cross-compile (5 targets) |
| `make frontend` | `npm install && npm run build` in `web/` |
| `make embed-frontend` | Copy `web/dist/` → `internal/webui/dist/` |
| `make frontend-init` | Initialize Vue.js project (one-time) |
| `make test` | `go test -v ./...` |
| `make lint` | `go vet` + `golangci-lint` (if installed) |
| `make fmt` | Format Go code with `gofmt` |
| `make check` | `go mod tidy` + `go mod verify` + `go vet` |
| `make install` | Copy binary to `$GOPATH/bin` |
| `make clean` | Remove `bin/`, `web/dist/`, `internal/webui/dist/` |
| `make run` | Build + run (use `ARGS='...'` for CLI args) |
| `make dev` | Hot reload with air |
| `make docs` | Generate Go documentation |
| `make help` | Show all targets |

---

## Troubleshooting

### Common Issues (All Platforms)

**`go mod download` fails with authentication errors:**
```bash
# Check for a proxy/private-module misconfiguration
go env GOPROXY GOPRIVATE GONOSUMCHECK
# Clear module cache if corrupted
go clean -modcache
go mod download
```

**Frontend build fails — `vue-tsc` type errors:**
```bash
cd web
rm -rf node_modules package-lock.json
npm install
npm run build
```

**Binary runs but web UI shows blank page:**
The frontend was not embedded. Rebuild with:
```bash
make build-embedded
```

**LLM API errors (401/403):**
Verify your API key is set and valid:
```bash
echo $OPENAI_API_KEY   # macOS/Linux
echo $env:OPENAI_API_KEY  # Windows PowerShell
```

### macOS-Specific Issues

**`make: command not found`:**
```bash
xcode-select --install
```

**Quarantine warnings on built binary:**
```bash
xattr -rc ./bin/rakitsu
```

**Port already in use:**
```bash
lsof -i :9100
kill -9 <PID>
```

### Windows-Specific Issues

**`make` not recognized:**
Install Make via Chocolatey (`choco install make`) or build manually (see [Building Without Make](#building-without-make)).

**`go build` fails with CGO errors:**
Rakitsu uses `CGO_ENABLED=0` for production builds. If you encounter CGO issues:
```powershell
$env:CGO_ENABLED = "0"
go build -o bin\rakitsu.exe .\cmd\rakitsu
```

**Port conflicts:**
```powershell
netstat -ano | findstr :9100
taskkill /PID <PID> /F
```

**`npm` command not found after Node.js install:**
Close and reopen your terminal. If still not found, add Node.js to PATH manually:
```powershell
$env:Path += ";C:\Program Files\nodejs"
```

**Line ending issues (CRLF vs LF):**
Configure Git to handle line endings:
```powershell
git config --global core.autocrlf true
```

**PowerShell execution policy blocks scripts:**
```powershell
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```
