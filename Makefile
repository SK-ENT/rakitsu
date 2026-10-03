.PHONY: all build build-all frontend clean test test-stress install refresh-dogfood refresh-dogfood-dry

BINARY_NAME=rakitsu
BUILD_DIR=bin
FRONTEND_DIR=web
WEBUI_DIR=internal/webui
VERSION_CORE?=v0.3.0-alpha.1-internal
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BRANCH:=$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
COMMIT_COUNT:=$(shell git rev-list --count HEAD 2>/dev/null || echo "0")
# Version 4th component: commit hash when built from main (release), else the
# dev commit-count "try count". `make build-release` always forces the hash.
ifeq ($(BRANCH),main)
BUILD_SUFFIX:=$(COMMIT)
else
BUILD_SUFFIX:=$(COMMIT_COUNT)
endif
VERSION?=$(VERSION_CORE).$(BUILD_SUFFIX)

# Remote dogfood deploy host — any machine you run dogfood scenarios on.
# No default on purpose: this repo ships publicly and a hardcoded private
# host doesn't belong in it. Set locally, e.g. in your shell profile:
#   export DOGFOOD_HOST=user@your-host
DOGFOOD_HOST?=
DOGFOOD_DIR?=~/rakitsu-dogfood

# Go build flags
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

all: build

# Build the CLI binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/rakitsu
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME)"

# Build with embedded frontend
build-embedded: frontend embed-frontend build
	@echo "Built $(BINARY_NAME) with embedded frontend"

# Build with embedded frontend + license gate (matches GitHub release builds).
# -tags rakitsu_pro compiles in the commercial license-key gate (internal-only,
# excluded from the public repo). -s -w strips symbols so the licensed binary
# is not trivially patchable.
build-release: frontend embed-frontend
	@echo "Building $(BINARY_NAME) (release)..."
	@mkdir -p $(BUILD_DIR)
	go build -tags rakitsu_pro -ldflags "-s -w -X main.Version=$(VERSION_CORE).$(COMMIT) -X main.BuildCommit=$(COMMIT) -X main.LicenseRequired=true" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/rakitsu
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME) (license required)"

# Copy frontend dist to webui package for embedding
embed-frontend:
	@echo "Embedding frontend..."
	@if [ -d "$(WEBUI_DIR)/dist" ]; then \
		xattr -cr $(WEBUI_DIR)/dist 2>/dev/null || true; \
		find $(WEBUI_DIR)/dist -type f -exec xattr -c {} + 2>/dev/null || true; \
		rm -rf $(WEBUI_DIR)/dist; \
	fi
	@mkdir -p $(WEBUI_DIR)/dist
	@cp -r $(FRONTEND_DIR)/dist/* $(WEBUI_DIR)/dist/
	@echo "Frontend embedded in $(WEBUI_DIR)/dist/"

# Build for all platforms
build-all: frontend embed-frontend
	@echo "Building for all platforms..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/rakitsu
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/rakitsu
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/rakitsu
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/rakitsu
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/rakitsu
	@echo "Built all binaries in $(BUILD_DIR)/"

# Build frontend (Vue.js)
frontend:
	@echo "Building frontend..."
	@if [ ! -d "$(FRONTEND_DIR)" ]; then \
		echo "Frontend directory not found. Run 'make frontend-init' first."; \
		exit 1; \
	fi
	cd $(FRONTEND_DIR) && npm install && npm run build
	@echo "Frontend built in $(FRONTEND_DIR)/dist/"

# Initialize frontend project
frontend-init:
	@echo "Initializing frontend project..."
	npm create vue@latest $(FRONTEND_DIR) -- --typescript --pinia --vue-router
	cd $(FRONTEND_DIR) && npm install @vue-flow/core @vue-flow/background @vue-flow/controls
	@echo "Frontend initialized. Edit files in $(FRONTEND_DIR)/src/"

# Run tests
test:
	go test -v ./...

# Run the stress/load test harness (test/stress/) -- build-tagged out of the
# default `go test ./...` run since these are longer-running concurrency and
# load tests, not part of the regular suite.
test-stress:
	go test -tags stress -v ./test/stress/...

# Run linter
lint:
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	fi

# Install binary to GOPATH/bin
install: build
	@echo "Installing $(BINARY_NAME) to $(GOPATH)/bin..."
	cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/
	@echo "Installed. Run 'rakitsu --help' to get started."

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -rf $(FRONTEND_DIR)/dist
	rm -rf $(WEBUI_DIR)/dist
	go clean
	@echo "Cleaned."

# Run the CLI in development mode
run: build
	./$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

# Development with hot reload (requires air: go install github.com/cosmtrek/air@latest)
dev:
	@if command -v air >/dev/null 2>&1; then \
		air; \
	else \
		echo "air not found. Install with: go install github.com/cosmtrek/air@latest"; \
		$(MAKE) run; \
	fi

# Generate Go documentation
docs:
	go doc -all > docs/api.md

# Format code
fmt:
	go fmt ./...

# Check for common issues
check:
	go mod tidy
	go mod verify
	go vet ./...

# Refresh remote dogfood deploy content. Mirrors local docs/ and
# examples/ onto $(DOGFOOD_HOST):$(DOGFOOD_DIR). Use refresh-dogfood-dry first to preview.
refresh-dogfood:
	@test -n "$(DOGFOOD_HOST)" || (echo "error: DOGFOOD_HOST not set, e.g. make refresh-dogfood DOGFOOD_HOST=user@host" && exit 1)
	@echo "Refreshing $(DOGFOOD_HOST):$(DOGFOOD_DIR)/{docs,examples}..."
	rsync -av --delete docs/ $(DOGFOOD_HOST):$(DOGFOOD_DIR)/docs/
	rsync -av --delete examples/ $(DOGFOOD_HOST):$(DOGFOOD_DIR)/examples/
	@echo "Refreshed."

# Dry-run preview of refresh-dogfood (no changes made)
refresh-dogfood-dry:
	@test -n "$(DOGFOOD_HOST)" || (echo "error: DOGFOOD_HOST not set, e.g. make refresh-dogfood-dry DOGFOOD_HOST=user@host" && exit 1)
	@echo "DRY-RUN: $(DOGFOOD_HOST):$(DOGFOOD_DIR)/{docs,examples}"
	rsync -avn --delete docs/ $(DOGFOOD_HOST):$(DOGFOOD_DIR)/docs/
	rsync -avn --delete examples/ $(DOGFOOD_HOST):$(DOGFOOD_DIR)/examples/

# Show help
help:
	@echo "Rakitsu Makefile"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build          Build the CLI binary"
	@echo "  build-embedded Build CLI with embedded frontend"
	@echo "  build-all      Build for all platforms"
	@echo "  frontend       Build the Vue.js frontend"
	@echo "  embed-frontend Copy frontend dist to webui package"
	@echo "  frontend-init  Initialize the Vue.js frontend project"
	@echo "  test           Run tests"
	@echo "  test-stress    Run the stress/load test harness (test/stress/)"
	@echo "  lint           Run linter"
	@echo "  install        Install binary to GOPATH/bin"
	@echo "  clean          Clean build artifacts"
	@echo "  run            Run the CLI (use ARGS='...' for args)"
	@echo "  dev            Development with hot reload"
	@echo "  fmt            Format code"
	@echo "  check          Check for issues"
	@echo "  refresh-dogfood      Sync docs/+examples/ to the remote dogfood deploy"
	@echo "  refresh-dogfood-dry  Preview the refresh without writing"
	@echo "  help           Show this help"
