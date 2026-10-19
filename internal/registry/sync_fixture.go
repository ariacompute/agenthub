package registry

import (
	"context"
)

// FixtureClient is the offline upstream registry.
//
// It exists so the whole synchronisation pipeline — validation, dedup, curation,
// counters — can be exercised without a network, which is what keeps the test
// suite deterministic and a fresh deployment walkable. The records it returns
// are shaped exactly like the ones the HTTP client produces, so switching a
// source from fixture to live changes nothing downstream.
type FixtureClient struct{}

// Fetch returns a small, stable catalogue derived from the source itself: the
// same source always yields the same records in the same order.
func (FixtureClient) Fetch(ctx context.Context, src RegistrySource) ([]*Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch src.Kind {
	case SourceKindMCP:
		return fixtureMCP(), nil
	case SourceKindSkill:
		return fixtureSkill(), nil
	}
	return nil, nil
}

func fixtureMCP() []*Entry {
	return []*Entry{
		{
			Name:        "filesystem",
			Description: "Read, write and search files through a sandboxed root.",
			Version:     "1.0.0",
			Repository:  "https://github.com/modelcontextprotocol/servers",
			Endpoint:    "https://registry.modelcontextprotocol.test/servers/filesystem",
			Transport:   TransportStreamableHTTP,
			Tags:        []string{"files", "sandbox"},
			Capabilities: []string{
				"list_resources", "call_action", "read_document",
			},
			KindConfig: &KindConfig{MCP: &MCPConfig{
				Transport:     string(TransportStreamableHTTP),
				URL:           "https://registry.modelcontextprotocol.test/servers/filesystem",
				ToolAllowlist: []string{"read_file", "write_file", "list_directory"},
			}},
		},
		{
			Name:        "git",
			Description: "Inspect and manipulate a git repository.",
			Version:     "2.1.0",
			Repository:  "https://github.com/modelcontextprotocol/servers",
			Endpoint:    "https://registry.modelcontextprotocol.test/servers/git",
			Transport:   TransportStdio,
			Tags:        []string{"vcs"},
			Capabilities: []string{
				"list_resources", "call_action",
			},
			KindConfig: &KindConfig{MCP: &MCPConfig{
				Transport:     string(TransportStdio),
				Command:       "npx",
				Args:          []string{"-y", "@modelcontextprotocol/server-git"},
				ToolAllowlist: []string{"git_log", "git_diff", "git_status"},
			}},
		},
		{
			Name:        "postgres",
			Description: "Query a PostgreSQL database through a read-only role.",
			Version:     "0.9.3",
			Endpoint:    "https://registry.modelcontextprotocol.test/servers/postgres",
			Transport:   TransportStreamableHTTP,
			Tags:        []string{"database"},
			Capabilities: []string{
				"list_resources", "read_document",
			},
			KindConfig: &KindConfig{MCP: &MCPConfig{
				Transport:     string(TransportStreamableHTTP),
				URL:           "https://registry.modelcontextprotocol.test/servers/postgres",
				ToolAllowlist: []string{"query"},
			}},
		},
	}
}

func fixtureSkill() []*Entry {
	return []*Entry{
		{
			Name:        "incident-triage",
			Description: "Triage a production incident and produce a handover note.",
			Version:     "1.2.0",
			Repository:  "https://github.com/ariacompute/skills",
			Tags:        []string{"ops", "incident"},
			Capabilities: []string{
				"collect_arguments", "execute_steps", "report_result",
			},
			KindConfig: &KindConfig{Skill: &SkillConfig{
				Version:    "1.2.0",
				Repository: "https://github.com/ariacompute/skills",
				Package:    "ariacompute/skills/incident-triage",
				Triggers:   []string{"incident", "outage", "sev1"},
				Steps:      []string{"match_trigger", "load_manual", "collect_arguments", "execute_steps", "report_result"},
				Tools:      []string{"mcp:postgres", "cli:git"},
				Docs:       "# Incident triage\n\n1. Confirm the blast radius.\n2. Capture the timeline.\n3. Write the handover note.",
			}},
		},
		{
			Name:        "release-notes",
			Description: "Turn a commit range into release notes.",
			Version:     "0.4.1",
			Repository:  "https://github.com/ariacompute/skills",
			Tags:        []string{"release"},
			Capabilities: []string{
				"collect_arguments", "report_result",
			},
			KindConfig: &KindConfig{Skill: &SkillConfig{
				Version:    "0.4.1",
				Repository: "https://github.com/ariacompute/skills",
				Package:    "ariacompute/skills/release-notes",
				Triggers:   []string{"release notes", "changelog"},
				Steps:      []string{"match_trigger", "load_manual", "report_result"},
				Tools:      []string{"cli:git"},
				Docs:       "# Release notes\n\nSummarise the commit range into user facing notes.",
			}},
		},
	}
}
