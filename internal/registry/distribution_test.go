package registry

import (
	"strconv"
	"strings"
	"testing"

	"agenthub/internal/httputil"
)

// bindingAgent registers an agent that binds the given mcp and skill ids, which
// is the shape the console writes: ids on the agent, nothing on the targets.
func bindingAgent(t *testing.T, reg *Registry, mcp, skills []string) *Entry {
	t.Helper()
	agent, err := reg.Upsert(&Entry{
		Kind: KindAgent,
		Name: "researcher",
		KindConfig: &KindConfig{
			Agent: &AgentConfig{MCP: mcp, Skills: skills},
		},
	})
	if err != nil {
		t.Fatalf("register agent: %v", err)
	}
	return agent
}

func TestDistributionOfRendersBothKinds(t *testing.T) {
	reg := New()
	mcp, err := reg.Upsert(&Entry{
		Kind:     KindMCP,
		Name:     "hr-mcp",
		Endpoint: "https://mcp.test/rpc",
		Health:   HealthHealthy,
		KindConfig: &KindConfig{MCP: &MCPConfig{
			Transport:     string(TransportStreamableHTTP),
			URL:           "https://mcp.test/rpc",
			ToolAllowlist: []string{"lookup", "write"},
			AuthSchemes:   []string{"bearer"},
			// The value must never leave the process; only the name is delivered.
			Env: map[string]string{"HR_TOKEN": "s3cr3t", "HR_REGION": "eu"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := reg.Upsert(&Entry{
		Kind:    KindSkill,
		Name:    "onboarding",
		Version: "1.2.0",
		KindConfig: &KindConfig{Skill: &SkillConfig{
			Version:  "2.0.0",
			Triggers: []string{"new hire"},
			Steps:    []string{"create account", "assign buddy"},
			Tools:    []string{"hr-mcp"},
			Docs:     "# Onboarding",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := bindingAgent(t, reg, []string{mcp.ID}, []string{skill.ID})

	dist, err := DistributionOf(reg, agent, nil)
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if dist.AgentID != agent.ID || dist.AgentName != "researcher" || dist.AgentDisplayName != "researcher" {
		t.Fatalf("agent block = %+v", dist)
	}
	if len(dist.MCP) != 1 || len(dist.Skills) != 1 || len(dist.Unresolved) != 0 || len(dist.Missing) != 0 {
		t.Fatalf("dist = %+v", dist)
	}

	got := dist.MCP[0]
	if got.ID != mcp.ID || got.URL != "https://mcp.test/rpc" || got.Transport != TransportStreamableHTTP {
		t.Fatalf("mcp binding = %+v", got)
	}
	if strings.Join(got.ToolAllowlist, ",") != "lookup,write" || strings.Join(got.AuthSchemes, ",") != "bearer" {
		t.Fatalf("mcp allowlists = %+v", got)
	}
	// EnvKeys exists so an operator can see which variables the server expects;
	// the values themselves must be absent from the package in every shape.
	if strings.Join(got.EnvKeys, ",") != "HR_REGION,HR_TOKEN" {
		t.Fatalf("env keys = %v", got.EnvKeys)
	}
	if strings.Contains(strings.Join(got.EnvKeys, ","), "s3cr3t") {
		t.Fatal("an env value leaked into the distribution")
	}

	s := dist.Skills[0]
	if s.ID != skill.ID || s.Version != "2.0.0" || s.Docs != "# Onboarding" {
		t.Fatalf("skill binding = %+v", s)
	}
	if strings.Join(s.Steps, "|") != "create account|assign buddy" || strings.Join(s.Tools, ",") != "hr-mcp" {
		t.Fatalf("skill steps = %+v", s)
	}
}

func TestDistributionOfSeparatesUnresolvedFromMissing(t *testing.T) {
	reg := New()
	visible, _ := reg.Upsert(&Entry{Kind: KindSkill, Name: "visible"})
	hidden, _ := reg.Upsert(&Entry{Kind: KindSkill, Name: "hidden"})
	wrongKind, _ := reg.Upsert(&Entry{Kind: KindCLI, Name: "git"})

	// The filter stands in for visibleEntries: exactly one record is granted.
	filter := func(entries []*Entry) []*Entry {
		out := make([]*Entry, 0, len(entries))
		for _, e := range entries {
			if e.ID != hidden.ID {
				out = append(out, e)
			}
		}
		return out
	}
	agent := bindingAgent(t, reg, []string{"mcp-does-not-exist"}, []string{
		visible.ID, hidden.ID, wrongKind.ID, "hand written skill label",
	})

	dist, err := DistributionOf(reg, agent, filter)
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if len(dist.Skills) != 1 || dist.Skills[0].ID != visible.ID {
		t.Fatalf("skills = %+v", dist.Skills)
	}
	// Unknown id, wrong kind and a legacy free-text label are all "cannot be
	// delivered as written"; a record the caller may not see is kept apart so the
	// package never confirms that somebody else's skill exists.
	if strings.Join(dist.Unresolved, ",") != "mcp-does-not-exist,"+wrongKind.ID+",hand written skill label" {
		t.Fatalf("unresolved = %v", dist.Unresolved)
	}
	if strings.Join(dist.Missing, ",") != hidden.ID {
		t.Fatalf("missing = %v", dist.Missing)
	}
}

func TestDistributionOfEmptyAndInvalidInputs(t *testing.T) {
	reg := New()

	if _, err := DistributionOf(reg, nil, nil); httputil.AsError(err).Code != CodeInvalidEntry {
		t.Fatalf("nil agent = %v", err)
	}
	mcp, _ := reg.Upsert(&Entry{Kind: KindMCP, Name: "not-an-agent"})
	if _, err := DistributionOf(reg, mcp, nil); httputil.AsError(err).Code != CodeInvalidKind {
		t.Fatalf("non-agent = %v", err)
	}

	agent := bindingAgent(t, reg, nil, nil)
	dist, err := DistributionOf(reg, agent, nil)
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if dist.MCP == nil || dist.Skills == nil {
		t.Fatalf("an empty package must still carry empty slices: %+v", dist)
	}
	if !dist.Empty() {
		t.Fatalf("no bindings must read as empty: %+v", dist)
	}
	if (&Distribution{MCP: []MCPBinding{{ID: "x"}}}).Empty() {
		t.Fatal("a package with one binding is not empty")
	}
	// Nil-safe so a caller that skips the read (a degraded instance) can still
	// ask "is there anything to send?" without a guard of its own.
	var none *Distribution
	if !none.Empty() {
		t.Fatal("a nil package must read as empty")
	}
}

func TestDistributionOfToleratesMissingRegistryAndBlanks(t *testing.T) {
	// A nil registry (in-process tests, a degraded instance) renders references
	// as unresolved instead of panicking: the package is a read, not a write.
	agent := &Entry{
		Kind: KindAgent,
		Name: "researcher",
		KindConfig: &KindConfig{Agent: &AgentConfig{
			MCP:    []string{" mcp-a ", "mcp-a", ""},
			Skills: []string{"skill-a", " "},
		}},
	}
	dist, err := DistributionOf(nil, agent, nil)
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if len(dist.MCP) != 0 || len(dist.Skills) != 0 {
		t.Fatalf("dist = %+v", dist)
	}
	// Blanks and repeats are dropped before resolution, so the report is honest
	// about how many distinct references the record actually holds.
	if strings.Join(dist.Unresolved, ",") != "mcp-a,skill-a" {
		t.Fatalf("unresolved = %v", dist.Unresolved)
	}
}

func TestDistributionOfClipsDocumentation(t *testing.T) {
	reg := New()
	long := strings.Repeat("界", maxDistributionDocsBytes) // 3 bytes per rune
	first, _ := reg.Upsert(&Entry{Kind: KindSkill, Name: "first", KindConfig: &KindConfig{Skill: &SkillConfig{Docs: long}}})
	second, _ := reg.Upsert(&Entry{Kind: KindSkill, Name: "second", KindConfig: &KindConfig{Skill: &SkillConfig{Docs: long}}})
	third, _ := reg.Upsert(&Entry{Kind: KindSkill, Name: "third", KindConfig: &KindConfig{Skill: &SkillConfig{Docs: "short"}}})
	agent := bindingAgent(t, reg, nil, []string{first.ID, second.ID, third.ID})

	dist, err := DistributionOf(reg, agent, nil)
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if !dist.Truncated {
		t.Fatal("the package must report that it clipped documentation")
	}
	if !dist.Skills[0].DocsTruncated || !dist.Skills[1].DocsTruncated {
		t.Fatalf("per skill flags = %+v", dist.Skills)
	}
	if len(dist.Skills[0].Docs) > maxDistributionDocsBytes+len(distributionDocsElisionMarker) {
		t.Fatalf("first doc = %d bytes", len(dist.Skills[0].Docs))
	}
	if !strings.HasSuffix(dist.Skills[0].Docs, distributionDocsElisionMarker) {
		t.Fatal("a clipped document must say so")
	}
	// The whole budget is spent by the first two documents, so the small third
	// one cannot be delivered intact — it is marked rather than silently dropped.
	if dist.Skills[2].Docs != "" || !dist.Skills[2].DocsTruncated {
		t.Fatalf("third = %+v", dist.Skills[2])
	}
	// Clipping is rune aware: invalid UTF-8 would make the JSON body unusable.
	if !utf8Valid(dist.Skills[0].Docs) {
		t.Fatal("clipped documentation is not valid UTF-8")
	}
}

func TestDistributionOfResolvesLongListsInChunks(t *testing.T) {
	reg := New()
	refs := make([]string, 0, distributionRefBatch+5)
	for i := 0; i < distributionRefBatch+5; i++ {
		entry, err := reg.Upsert(&Entry{Kind: KindSkill, Name: "skill-" + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, entry.ID)
	}
	agent := bindingAgent(t, reg, nil, refs)

	// More references than one batch read allows: the package must still describe
	// all of them, which is why resolution chunks instead of truncating.
	dist, err := DistributionOf(reg, agent, nil)
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if len(dist.Skills) != len(refs) {
		t.Fatalf("skills = %d, want %d", len(dist.Skills), len(refs))
	}
	if len(dist.Unresolved) != 0 {
		t.Fatalf("unresolved = %v", dist.Unresolved)
	}
}

func TestDistributionOfToleratesANilFromTheFilter(t *testing.T) {
	reg := New()
	skill, _ := reg.Upsert(&Entry{Kind: KindSkill, Name: "s"})
	agent := bindingAgent(t, reg, nil, []string{skill.ID})

	// The filter is caller-supplied; a nil element is a bug in that caller, not a
	// grant. It must not panic and must not be counted as visible.
	dist, err := DistributionOf(reg, agent, func(entries []*Entry) []*Entry {
		return append(entries, nil)
	})
	if err != nil {
		t.Fatalf("DistributionOf: %v", err)
	}
	if len(dist.Skills) != 1 || dist.Skills[0].ID != skill.ID {
		t.Fatalf("skills = %+v", dist.Skills)
	}
}

func TestMCPBindingOfKeepsCommandOnlyForStdio(t *testing.T) {
	stdio := &Entry{
		ID:   "mcp-1",
		Kind: KindMCP,
		Name: "local",
		Env:  map[string]string{"PATH": "/usr/bin"},
		KindConfig: &KindConfig{MCP: &MCPConfig{
			Transport: string(TransportStdio),
			Command:   "npx",
			Args:      []string{"-y", "server"},
			URL:       "https://ignored.test",
		}},
	}
	got := mcpBindingOf(stdio)
	if got.Transport != TransportStdio || got.Command != "npx" || strings.Join(got.Args, " ") != "-y server" {
		t.Fatalf("stdio binding = %+v", got)
	}
	if got.URL != "https://ignored.test" {
		// The URL is echoed as recorded; only the command/args pair is
		// transport specific, and dropping the URL would hide a misconfiguration.
		t.Fatalf("url = %q", got.URL)
	}
	if strings.Join(got.EnvKeys, ",") != "PATH" {
		t.Fatalf("env keys = %v", got.EnvKeys)
	}

	// A blank key is dropped rather than delivered as an empty name.
	blank := mcpBindingOf(&Entry{
		ID: "mcp-3", Kind: KindMCP, Name: "blank",
		Env: map[string]string{"": "value"},
	})
	if len(blank.EnvKeys) != 0 {
		t.Fatalf("blank env key = %v", blank.EnvKeys)
	}

	httpEntry := &Entry{
		ID:       "mcp-2",
		Kind:     KindMCP,
		Name:     "remote",
		Endpoint: "https://remote.test/rpc",
		Health:   HealthDegraded,
	}
	httpBinding := mcpBindingOf(httpEntry)
	if httpBinding.Command != "" || len(httpBinding.Args) != 0 {
		t.Fatalf("an http mcp must not carry a command: %+v", httpBinding)
	}
	if httpBinding.Transport != TransportStreamableHTTP || httpBinding.URL != "https://remote.test/rpc" {
		t.Fatalf("http binding = %+v", httpBinding)
	}
	if httpBinding.Health != HealthDegraded {
		t.Fatalf("health = %q", httpBinding.Health)
	}
}

func TestAgentBindingsNormalizeAndValidate(t *testing.T) {
	cfg := &AgentConfig{MCP: []string{" a ", "a", ""}, Skills: []string{"s1", " s1 ", ""}}
	mcp, skills := cfg.Bindings()
	if strings.Join(mcp, ",") != "a" || strings.Join(skills, ",") != "s1" {
		t.Fatalf("bindings = %v %v", mcp, skills)
	}
	if a, b := (&AgentConfig{}).Bindings(); a != nil || b != nil {
		t.Fatalf("empty bindings = %v %v", a, b)
	}
	var nilCfg *AgentConfig
	if a, b := nilCfg.Bindings(); a != nil || b != nil {
		t.Fatalf("nil config = %v %v", a, b)
	}

	tooMany := make([]string, MaxAgentBindings+1)
	for i := range tooMany {
		tooMany[i] = NewID("mcp")
	}
	err := ValidateKind(&Entry{Kind: KindAgent, Name: "a", KindConfig: &KindConfig{Agent: &AgentConfig{MCP: tooMany}}})
	if httputil.AsError(err).Code != CodeInvalidKindConfig {
		t.Fatalf("too many bindings = %v", err)
	}
	err = ValidateKind(&Entry{Kind: KindAgent, Name: "a", KindConfig: &KindConfig{Agent: &AgentConfig{MCP: []string{"  "}}}})
	if httputil.AsError(err).Code != CodeInvalidKindConfig {
		t.Fatalf("blank binding = %v", err)
	}
	// skills stays lenient: free-text labels written before bindings existed must
	// keep validating, including blanks inside an old CSV split.
	if err := ValidateKind(&Entry{Kind: KindAgent, Name: "a", KindConfig: &KindConfig{Agent: &AgentConfig{Skills: []string{"", "hand written label"}}}}); err != nil {
		t.Fatalf("legacy skills = %v", err)
	}
	if summary := KindConfigSummary(&KindConfig{Agent: &AgentConfig{MCP: []string{"a"}, Skills: []string{"b", "c"}}}); summary != "agent(caps=0, mcp=1, skills=2)" {
		t.Fatalf("summary = %q", summary)
	}
}

// utf8Valid keeps the rune-boundary assertion readable at the call site.
func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "\uFFFD") == s }
