package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/SK-ENT/rakitsu/internal/config"
	"github.com/SK-ENT/rakitsu/internal/server"
	"github.com/SK-ENT/rakitsu/internal/tools"
)

type mcpReloadingHandler struct {
	mu      sync.Mutex
	load    func() (*config.Config, error)
	build   func(*config.Config) *tools.ToolRegistry
	version string
	current [32]byte
	loaded  bool
	mcp     *server.MCPServer
}

func newMCPReloadingHandler(load func() (*config.Config, error), version string, build func(*config.Config) *tools.ToolRegistry) *mcpReloadingHandler {
	if build == nil {
		build = func(cfg *config.Config) *tools.ToolRegistry {
			return createToolRegistry(context.Background(), cfg)
		}
	}
	return &mcpReloadingHandler{load: load, build: build, version: version}
}

func (h *mcpReloadingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.load()
	if err != nil {
		http.Error(w, "config unavailable", http.StatusServiceUnavailable)
		return
	}
	fingerprintData, err := json.Marshal(struct {
		Tools           []config.ToolDefinition
		AllowedCommands []string
	}{Tools: cfg.Tools, AllowedCommands: cfg.Settings.AllowedCommands})
	if err != nil {
		http.Error(w, "config unavailable", http.StatusServiceUnavailable)
		return
	}
	fingerprint := sha256.Sum256(fingerprintData)

	h.mu.Lock()
	if !h.loaded || h.current != fingerprint {
		registry := h.build(cfg)
		if h.mcp == nil {
			h.mcp = server.NewMCPServer(registry, h.version)
		} else {
			h.mcp.SetRegistry(registry)
		}
		h.current = fingerprint
		h.loaded = true
	}
	mcp := h.mcp
	h.mu.Unlock()

	mcp.ServeHTTP(w, r)
}
