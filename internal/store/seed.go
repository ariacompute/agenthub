package store

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agenthub/internal/registry"
)

// Seed inserts the deterministic demo resources: one per kind, so a fresh
// instance is immediately usable and the README smoke commands are
// reproducible. Existing rows win (ON CONFLICT DO NOTHING), which makes seeding
// idempotent across restarts.
func Seed(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("store: seed: db is nil")
	}
	entries := DemoResources()
	for _, e := range entries {
		row, err := resourceFromEntry(e)
		if err != nil {
			return fmt.Errorf("store: seed %s: %w", e.ID, err)
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			return fmt.Errorf("store: seed %s: %w", e.ID, err)
		}
	}
	slog.Info("store: seeded demo resources", "count", len(entries))
	return nil
}

// DemoResources returns the five default entries agenthub ships with. They are
// deterministic mock resources: no network call is made to reach them.
func DemoResources() []*registry.Entry {
	now := time.Now().UTC()
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
			KindConfig: &registry.KindConfig{CLI: &registry.CLIConfig{
				Command:        "git",
				Allowlist:      []string{"status", "log"},
				TimeoutMS:      15000,
				MaxOutputBytes: 65536,
			}},
		},
		{
			ID:          "demo-custom",
			Kind:        registry.KindCustom,
			Name:        "ticket-schema",
			Description: "A caller defined record: an internal ticket schema nothing else fits",
			Endpoint:    "https://internal.example/schemas/ticket.json",
			Version:     "1.0.0",
			Tags:        []string{"schema", "internal"},
			Health:      registry.HealthHealthy,
			KindConfig: &registry.KindConfig{Custom: &registry.CustomConfig{
				Schema: `{"type":"object","properties":{"id":{"type":"string"},"state":{"type":"string"}}}`,
			}},
		},
	}
	for _, e := range seeds {
		e.CreatedAt = now
		e.UpdatedAt = now
	}
	return seeds
}
