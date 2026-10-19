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
	// Namespace tells a consumer which catalogue the record came from, which
	// matters once an instance hosts more than one.
	Namespace string `json:"namespace,omitempty"`
	// SourceID is set when the record was synchronised from an upstream
	// registry rather than registered by hand.
	SourceID string `json:"source_id,omitempty"`
}

// MCPServerDescriptions renders the catalogue as MCP server descriptions.
func MCPServerDescriptions(r *Registry) []MCPServerDescription {
	return MCPServerDescriptionsOf(r.Snapshot())
}

// MCPServerDescriptionsOf renders an already filtered entry set. The
// authorization layer decides which entries a caller may see, so export is
// always built from what the caller was allowed to list.
//
// Curation is applied here too: a record that is still a draft, waiting for
// review, rejected or deprecated is not part of the discoverable catalogue, so
// it never reaches an export even if a caller forgot to filter upstream.
func MCPServerDescriptionsOf(entries []*Entry) []MCPServerDescription {
	out := make([]MCPServerDescription, 0, len(entries))
	for _, e := range Discoverable(entries) {
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
			Namespace:    e.EffectiveNamespace(),
			SourceID:     e.SourceID,
		})
	}
	return out
}

// AgentCard renders agenthub's own discovery document. Every registered
// resource becomes one advertised skill so a peer agent can see what the hub is
// able to reach before sending the first task.
func AgentCard(r *Registry, baseURL, version, protocolVersion string) a2a.AgentCard {
	if r == nil {
		return AgentCardOf(nil, nil, 0, baseURL, version, protocolVersion)
	}
	// Discovery publishes approved records only, and the totals have to agree
	// with what is published: a peer must not be able to infer the existence of
	// records it can never call from a larger total.
	published := Discoverable(r.Snapshot())
	return AgentCardOf(published, nil, len(published), baseURL, version, protocolVersion)
}

// AgentCardOf renders the discovery document from an already filtered entry
// set, so a peer only ever sees the resources its caller was granted.
//
// Curation is applied here as well: only approved records become advertised
// skills. Doing it inside the renderer means every caller — the well-known
// endpoint, the JSON-RPC card method and the control plane export — shares one
// definition of "discoverable" instead of each remembering to filter.
func AgentCardOf(entries []*Entry, counts map[string]int, total int, baseURL, version, protocolVersion string) a2a.AgentCard {
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
	if entries == nil {
		return card
	}
	published := Discoverable(entries)
	sort.Slice(published, func(i, j int) bool {
		if published[i].Kind == published[j].Kind {
			return published[i].Name < published[j].Name
		}
		return published[i].Kind < published[j].Kind
	})
	for _, e := range published {
		desc := e.Description
		if desc == "" {
			desc = string(e.Kind) + " resource"
		}
		tags := append([]string{}, e.Tags...)
		tags = append(tags, string(e.Kind))
		card.Skills = append(card.Skills, a2a.AgentSkill{
			ID:           e.ID,
			Name:         e.EffectiveDisplayName(),
			Description:  desc,
			Tags:         tags,
			InputModes:   []string{"text"},
			OutputModes:  []string{"text", "data"},
			ResourceKind: string(e.Kind),
		})
	}
	// Counts mirror the filtered set: an agent must not be able to infer the
	// existence of resources it was never granted from a total that is larger
	// than what it can list.
	if counts == nil {
		counts = map[string]int{}
		for _, e := range published {
			counts[string(e.Kind)]++
		}
		total = len(published)
	}
	card.Metadata = map[string]any{
		"resourceCounts": counts,
		"resourceTotal":  total,
	}
	return card
}
