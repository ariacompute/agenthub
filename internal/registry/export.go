package registry

import (
	"sort"

	"agenthub/internal/a2a"
)

// MCPServerDescription is the MCP Registry flavoured view of a resource. It is
// what agenthub exposes for discovery: the same entries it consumes are also
// publishable, which keeps the hub symmetric instead of a one-way proxy.
type MCPServerDescription struct {
	Name         string    `json:"name"`
	Version      string    `json:"version,omitempty"`
	Description  string    `json:"description,omitempty"`
	Repository   string    `json:"repository,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	Transport    Transport `json:"transport,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	AuthSchemes  []string  `json:"auth_schemes,omitempty"`
	Tags         []string  `json:"tags,omitempty"`
	ResourceKind Kind      `json:"resourceKind"`
	Health       Health    `json:"health"`
}

// MCPServerDescriptions renders the catalogue as MCP server descriptions.
func MCPServerDescriptions(r *Registry) []MCPServerDescription {
	entries := r.Snapshot()
	out := make([]MCPServerDescription, 0, len(entries))
	for _, e := range entries {
		out = append(out, MCPServerDescription{
			Name:         e.Name,
			Version:      e.Version,
			Description:  e.Description,
			Repository:   e.Repository,
			Endpoint:     e.Endpoint,
			Transport:    e.Transport,
			Capabilities: e.Capabilities,
			AuthSchemes:  e.AuthSchemes,
			Tags:         e.Tags,
			ResourceKind: e.Kind,
			Health:       e.Health,
		})
	}
	return out
}

// AgentCard renders agenthub's own discovery document. Every registered
// resource becomes one advertised skill so a peer agent can see what the hub is
// able to reach before sending the first task.
func AgentCard(r *Registry, baseURL, version, protocolVersion string) a2a.AgentCard {
	card := a2a.AgentCard{
		Name:               "agenthub",
		Description:        "A2A interconnection platform for model, agent, mcp, skill and cli resources.",
		URL:                baseURL,
		Version:            version,
		ProtocolVersion:    protocolVersion,
		Capabilities:       a2a.AgentCapabilities{Streaming: true, PushNotifications: true, StateTransitionHistory: true},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text", "data"},
		Skills:             []a2a.AgentSkill{},
		Authentication:     &a2a.Authentication{Schemes: []string{"bearer"}},
		Provider:           &a2a.AgentProvider{Organization: "ariacompute"},
		DocumentationURL:   baseURL + "/healthz",
	}
	if r == nil {
		return card
	}
	entries := r.Snapshot()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind == entries[j].Kind {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Kind < entries[j].Kind
	})
	for _, e := range entries {
		desc := e.Description
		if desc == "" {
			desc = string(e.Kind) + " resource"
		}
		tags := append([]string{}, e.Tags...)
		tags = append(tags, string(e.Kind))
		card.Skills = append(card.Skills, a2a.AgentSkill{
			ID:           e.ID,
			Name:         e.Name,
			Description:  desc,
			Tags:         tags,
			InputModes:   []string{"text"},
			OutputModes:  []string{"text", "data"},
			ResourceKind: string(e.Kind),
		})
	}
	card.Metadata = map[string]any{
		"resourceCounts": r.CountsByKind(),
		"resourceTotal":  r.Size(),
	}
	return card
}
