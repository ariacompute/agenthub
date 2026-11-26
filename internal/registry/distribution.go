package registry

import (
	"sort"
	"strings"

	"agenthub/internal/httputil"
)

// A distribution is the package an agent receives: the mcp servers and skill
// manuals bound to it, rendered into something a remote agent can act on.
//
// Binding lives on the agent (kind_config.agent.mcp / .skills) rather than in a
// second table: one PUT still describes the whole record, no migration is
// needed, and the console edits both halves in the same drawer. Resolution is
// deferred to this package because it is also where the rendering belongs: the
// HTTP layer supplies the visibility predicate, so registry keeps its promise of
// never importing authz.
//
// Three consumers share this rendering — the pull endpoint, the A2A call that
// carries the package as message metadata, and the push that posts it as a task
// — so all three describe the same abilities, always.

// MaxAgentBindings caps how many resources one agent may bind per kind. The
// batch read it feeds is already bounded (maxBatchIDs), so the cap keeps one
// record from turning a single call into an unbounded fan-out.
const MaxAgentBindings = 50

// Budgets for skill documentation. Docs are arbitrary markdown, and the package
// travels inside a JSON-RPC message: without a ceiling one skill could push an
// A2A call past the peer's body limit and turn a delivery into a failure.
const (
	maxDistributionDocsBytes      = 8 << 10
	maxDistributionTotalDocBytes  = 32 << 10
	distributionRefBatch          = 100
	distributionDocsElisionMarker = "\n… (truncated)"
)

// MCPBinding is one mcp server, delivered.
//
// It never carries a credential value: EnvKeys lists names, and the auth
// schemes are names too. The process that runs the server is the process that
// holds the secret, and a distribution is by definition handed to someone else.
type MCPBinding struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	DisplayName   string    `json:"display_name,omitempty"`
	Version       string    `json:"version,omitempty"`
	Namespace     string    `json:"namespace,omitempty"`
	Transport     Transport `json:"transport,omitempty"`
	URL           string    `json:"url,omitempty"`
	Command       string    `json:"command,omitempty"`
	Args          []string  `json:"args,omitempty"`
	ToolAllowlist []string  `json:"tool_allowlist,omitempty"`
	AuthSchemes   []string  `json:"auth_schemes,omitempty"`
	EnvKeys       []string  `json:"env_keys,omitempty"`
	Health        Health    `json:"health,omitempty"`
}

// SkillBinding is one skill manual, delivered with its documentation clipped to
// the package budget.
type SkillBinding struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"display_name,omitempty"`
	Version       string   `json:"version,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
	Repository    string   `json:"repository,omitempty"`
	Package       string   `json:"package,omitempty"`
	Triggers      []string `json:"triggers,omitempty"`
	Steps         []string `json:"steps,omitempty"`
	Tools         []string `json:"tools,omitempty"`
	Docs          string   `json:"docs,omitempty"`
	DocsTruncated bool     `json:"docs_truncated,omitempty"`
	Health        Health   `json:"health,omitempty"`
}

// Distribution is the whole package for one agent.
//
// Unresolved and Missing are reported, never fatal: a stale id must not make an
// agent uncallable, and an operator needs to see what no longer resolves.
//
//   - Unresolved: the registry has no such record, or has one of a different
//     kind, so the reference cannot be delivered as written. This is what an
//     agent saved before bindings existed looks like (free-text skill labels).
//   - Missing: the record exists and is of the right kind, but the caller may
//     not see it. It is kept apart from the delivered list so a package never
//     confirms the existence of a record the caller was not granted.
type Distribution struct {
	AgentID          string         `json:"agent_id"`
	AgentName        string         `json:"agent_name"`
	AgentDisplayName string         `json:"agent_display_name,omitempty"`
	MCP              []MCPBinding   `json:"mcp"`
	Skills           []SkillBinding `json:"skills"`
	Unresolved       []string       `json:"unresolved,omitempty"`
	Missing          []string       `json:"missing,omitempty"`
	Truncated        bool           `json:"truncated,omitempty"`
}

// DistributionFilter narrows the resolved records to the ones the caller may
// receive. The HTTP layer passes its visibility filter, so the authorization
// decision stays where identities live; nil means "no filter" (in-process use).
type DistributionFilter func(entries []*Entry) []*Entry

// Empty reports whether the agent has nothing bound at all, which lets a caller
// skip sending an empty envelope on every single call.
func (d *Distribution) Empty() bool {
	if d == nil {
		return true
	}
	return len(d.MCP) == 0 && len(d.Skills) == 0 && len(d.Unresolved) == 0 && len(d.Missing) == 0
}

// Bindings returns the mcp and skill references of the agent, trimmed, without
// blanks and without repeats. Order is preserved: the console renders the
// package in the order the operator selected.
func (c *AgentConfig) Bindings() (mcp []string, skills []string) {
	if c == nil {
		return nil, nil
	}
	return normalizeRefs(c.MCP), normalizeRefs(c.Skills)
}

func normalizeRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// DistributionOf renders the package for one agent resource.
//
// A non-agent entry is refused with invalid_kind: bindings exist on agents only,
// and answering with an empty package would make "this record cannot be an
// agent" look like "this agent has no abilities".
func DistributionOf(reg *Registry, agent *Entry, filter DistributionFilter) (*Distribution, error) {
	if agent == nil {
		return nil, &httputil.Error{Code: CodeInvalidEntry}
	}
	if agent.Kind != KindAgent {
		return nil, &httputil.Error{Code: CodeInvalidKind}
	}
	dist := &Distribution{
		AgentID:          agent.ID,
		AgentName:        agent.Name,
		AgentDisplayName: agent.EffectiveDisplayName(),
		MCP:              []MCPBinding{},
		Skills:           []SkillBinding{},
	}
	var cfg *AgentConfig
	if agent.KindConfig != nil {
		cfg = agent.KindConfig.Agent
	}
	mcpRefs, skillRefs := cfg.Bindings()
	if len(mcpRefs) == 0 && len(skillRefs) == 0 {
		return dist, nil
	}

	resolved, allowed := resolveRefs(reg, append(append([]string{}, mcpRefs...), skillRefs...), filter)
	budget := maxDistributionTotalDocBytes
	for _, ref := range mcpRefs {
		entry, ok := resolved[ref]
		switch {
		case !ok, entry.Kind != KindMCP:
			dist.Unresolved = append(dist.Unresolved, ref)
		case !allowed[ref]:
			dist.Missing = append(dist.Missing, ref)
		default:
			dist.MCP = append(dist.MCP, mcpBindingOf(entry))
		}
	}
	for _, ref := range skillRefs {
		entry, ok := resolved[ref]
		switch {
		case !ok, entry.Kind != KindSkill:
			dist.Unresolved = append(dist.Unresolved, ref)
		case !allowed[ref]:
			dist.Missing = append(dist.Missing, ref)
		default:
			binding := skillBindingOf(entry)
			binding.Docs, binding.DocsTruncated = clipDocs(binding.Docs, &budget)
			dist.Skills = append(dist.Skills, binding)
			if binding.DocsTruncated {
				dist.Truncated = true
			}
		}
	}
	return dist, nil
}

// resolveRefs reads the references in bounded chunks and applies the caller's
// visibility filter once over everything found.
//
// Chunking rather than capping is deliberate: an old record may carry an
// arbitrarily long free-text skills list, and silently dropping the tail would
// turn a display problem into a missing ability. The chunk size is the batch
// read's own ceiling.
func resolveRefs(reg *Registry, refs []string, filter DistributionFilter) (map[string]*Entry, map[string]bool) {
	resolved := make(map[string]*Entry, len(refs))
	if reg == nil {
		return resolved, map[string]bool{}
	}
	// Ask for the union once per chunk; a ref may repeat between the two lists.
	seen := make(map[string]struct{}, len(refs))
	for start := 0; start < len(refs); start += distributionRefBatch {
		end := start + distributionRefBatch
		if end > len(refs) {
			end = len(refs)
		}
		ids := make([]string, 0, end-start)
		for _, ref := range refs[start:end] {
			if _, dup := seen[ref]; dup {
				continue
			}
			seen[ref] = struct{}{}
			ids = append(ids, ref)
		}
		for id, entry := range reg.GetMany(ids) {
			resolved[id] = entry
		}
	}
	allowed := make(map[string]bool, len(resolved))
	entries := make([]*Entry, 0, len(resolved))
	ids := make([]string, 0, len(resolved))
	for id := range resolved {
		ids = append(ids, id)
	}
	// Sorting makes the filter's input deterministic: the authorization layer
	// may batch its query, and a map iteration order would make that query's
	// shape vary between identical calls.
	sort.Strings(ids)
	for _, id := range ids {
		entries = append(entries, resolved[id])
	}
	if filter != nil {
		entries = filter(entries)
	}
	for _, e := range entries {
		if e != nil {
			allowed[e.ID] = true
		}
	}
	return resolved, allowed
}

// mcpBindingOf renders one mcp resource for delivery.
func mcpBindingOf(e *Entry) MCPBinding {
	var cfg *MCPConfig
	if e.KindConfig != nil {
		cfg = e.KindConfig.MCP
	}
	transport := cfg.EffectiveTransport(e)
	binding := MCPBinding{
		ID:            e.ID,
		Name:          e.Name,
		DisplayName:   e.EffectiveDisplayName(),
		Version:       e.Version,
		Namespace:     e.EffectiveNamespace(),
		Transport:     transport,
		URL:           strings.TrimSpace(e.Endpoint),
		Health:        e.Health,
		AuthSchemes:   append([]string{}, e.AuthSchemes...),
		ToolAllowlist: nil,
		EnvKeys:       nil,
	}
	if cfg != nil {
		if url := strings.TrimSpace(cfg.URL); url != "" {
			binding.URL = url
		}
		if transport == TransportStdio {
			binding.Command = strings.TrimSpace(cfg.Command)
			binding.Args = append([]string{}, cfg.Args...)
		}
		binding.ToolAllowlist = append([]string{}, cfg.ToolAllowlist...)
		if len(cfg.AuthSchemes) > 0 {
			binding.AuthSchemes = append([]string{}, cfg.AuthSchemes...)
		}
		binding.EnvKeys = envKeys(e.Env, cfg.Env)
	}
	if len(binding.AuthSchemes) == 0 {
		binding.AuthSchemes = nil
	}
	return binding
}

// envKeys merges the declared variable names of both env maps. Values are
// dropped here, once, so no renderer downstream has to remember to.
func envKeys(envs ...map[string]string) []string {
	set := map[string]struct{}{}
	for _, env := range envs {
		for key := range env {
			if k := strings.TrimSpace(key); k != "" {
				set[k] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// skillBindingOf renders one skill resource for delivery.
func skillBindingOf(e *Entry) SkillBinding {
	var cfg *SkillConfig
	if e.KindConfig != nil {
		cfg = e.KindConfig.Skill
	}
	binding := SkillBinding{
		ID:          e.ID,
		Name:        e.Name,
		DisplayName: e.EffectiveDisplayName(),
		Version:     e.Version,
		Namespace:   e.EffectiveNamespace(),
		Health:      e.Health,
	}
	if cfg != nil {
		if v := strings.TrimSpace(cfg.Version); v != "" {
			binding.Version = v
		}
		binding.Repository = strings.TrimSpace(cfg.Repository)
		binding.Package = strings.TrimSpace(cfg.Package)
		binding.Triggers = append([]string{}, cfg.Triggers...)
		binding.Steps = append([]string{}, cfg.Steps...)
		binding.Tools = append([]string{}, cfg.Tools...)
		binding.Docs = cfg.Docs
	}
	return binding
}

// clipDocs trims one document to the remaining package budget. The marker is
// appended so a reader can tell a short manual from a clipped one instead of
// assuming the manual ended there.
func clipDocs(docs string, budget *int) (string, bool) {
	if docs == "" {
		return "", false
	}
	limit := *budget
	if limit > maxDistributionDocsBytes {
		limit = maxDistributionDocsBytes
	}
	if limit <= 0 {
		*budget = 0
		return "", true
	}
	if len(docs) <= limit {
		*budget -= len(docs)
		return docs, false
	}
	clipped := clipToRuneBoundary(docs, limit) + distributionDocsElisionMarker
	*budget = 0
	return clipped, true
}

// clipToRuneBoundary cuts at or below n bytes without splitting a rune, so a
// clipped document stays valid UTF-8 rather than ending in a replacement char.
// Callers only reach it with n < len(s); the loop is what makes a multi-byte
// rune land on a boundary instead of a fragment.
func clipToRuneBoundary(s string, n int) string {
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
