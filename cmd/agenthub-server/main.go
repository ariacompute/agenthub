// Command agenthub-server is the single agenthub binary: it wires the resource
// registry, the mock adapters and the A2A hub behind one HTTP listener that also
// hosts the Vue3 console.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/handlers"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

func main() {
	config.LoadDotEnv()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	setupLogger(cfg)

	reg := registry.New()
	if err := registry.LoadInto(cfg.DataFile, reg); err != nil {
		slog.Error("registry snapshot", "error", err)
		os.Exit(1)
	}
	seedDefaults(reg)

	opts := adapter.Options{
		CLITimeout:     cfg.CLITimeout,
		CLIAllowlist:   cfg.CLIAllowlist,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}
	h := hub.New(cfg, reg, opts, &hub.MockNotifier{})

	deps := handlers.Deps{Cfg: cfg, Hub: h, Reg: reg, Log: slog.Default()}
	srv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           handlers.Register(deps),
		ReadHeaderTimeout: 30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("agenthub listening", "port", cfg.HTTPPort, "env", cfg.Env, "version", config.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			slog.Error("server", "error", err)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "error", err)
	}
	if err := registry.SaveSnapshot(cfg.DataFile, reg); err != nil {
		slog.Error("registry snapshot save", "error", err)
	}
	slog.Info("agenthub stopped")
}

func setupLogger(cfg *config.Config) {
	level := slog.LevelInfo
	if cfg.Env == "dev" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

// seedDefaults registers one deterministic resource per kind so a fresh
// instance is immediately usable and the README smoke commands are reproducible.
func seedDefaults(reg *registry.Registry) {
	if reg.Size() > 0 {
		return
	}
	seeds := []*registry.Entry{
		{
			ID:           "demo-model",
			Kind:         registry.KindModel,
			Name:         "afm-system-one",
			Description:  "AFM foundation model endpoint",
			Endpoint:     "https://api.ariacompute.com/v1",
			Version:      "1.0.0",
			Capabilities: []string{"chat", "stream"},
			AuthSchemes:  []string{"bearer"},
			Tags:         []string{"llm"},
			Health:       registry.HealthHealthy,
		},
		{
			ID:           "demo-agent",
			Kind:         registry.KindAgent,
			Name:         "aria-agent-cloud",
			Description:  "Aria compute cloud agent (OpenAI beta Agents compatible)",
			Endpoint:     "https://agent.ariacompute.com",
			Version:      "1.0.0",
			Capabilities: []string{"sessions", "tools"},
			AuthSchemes:  []string{"bearer"},
			Tags:         []string{"agent"},
			Health:       registry.HealthHealthy,
		},
		{
			ID:           "demo-mcp",
			Kind:         registry.KindMCP,
			Name:         "legacy-hr-mcp",
			Description:  "Thin MCP wrapper over the legacy leave system REST API",
			Endpoint:     "https://mcp.internal.example/mcp",
			Transport:    registry.TransportStreamableHTTP,
			Version:      "1.0.0",
			Capabilities: []string{"tools/list", "tools/call"},
			AuthSchemes:  []string{"oauth2"},
			Tags:         []string{"hr", "legacy"},
			Health:       registry.HealthHealthy,
		},
		{
			ID:           "demo-skill",
			Kind:         registry.KindSkill,
			Name:         "leave-application",
			Description:  "Standard operating procedure for leave requests",
			Version:      "1.0.0",
			Repository:   "https://github.com/ariacompute/agenthub",
			Capabilities: []string{"check_balance", "submit_request"},
			Tags:         []string{"hr", "sop"},
			Health:       registry.HealthHealthy,
		},
		{
			ID:           "demo-cli",
			Kind:         registry.KindCLI,
			Name:         "git-cli",
			Description:  "Structured git wrapper exposed through MCP",
			Capabilities: []string{"status", "log"},
			Tags:         []string{"cli"},
			Metadata:     map[string]string{"command": "git"},
			Health:       registry.HealthHealthy,
		},
	}
	for _, e := range seeds {
		if _, err := reg.Upsert(e); err != nil {
			slog.Error("seed resource", "id", e.ID, "error", err)
		}
	}
	slog.Info("registry: seeded default resources", "count", len(seeds))
}
