package registry

import (
	"strings"
	"testing"
)

// The per-kind configuration is the part of a registration the console builds
// and the backend has to trust, so every branch of the validator is pinned
// here: one bad field must name itself rather than fail the whole registration.

// badKindConfig is one configuration block that must be refused.
func TestValidateKindNamesTheOffendingField(t *testing.T) {
	cases := []struct {
		name  string
		entry *Entry
	}{
		{
			name:  "model: unknown provider mode",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: "anthropic"}}},
		},
		{
			name: "model: openai without an endpoint",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{
				ProviderMode: ProviderModeOpenAICompatible, ModelID: "gpt",
			}}},
		},
		{
			name: "model: endpoint that is not a URL",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{
				ProviderMode: ProviderModeOpenAICompatible, Endpoint: "localhost:4000", ModelID: "gpt",
			}}},
		},
		{
			name: "model: openai without a model id",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{
				ProviderMode: ProviderModeOpenAICompatible, Endpoint: "https://router.test/v1",
			}}},
		},
		{
			name:  "model: key env that is not a variable name",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{APIKeyEnv: "sk-live-key"}}},
		},
		{
			name:  "model: negative timeout",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{TimeoutMS: -1}}},
		},
		{
			name: "model: fallback target without a name",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{
				Targets: []ModelTarget{{Model: "b"}},
			}}},
		},
		{
			name: "model: fallback target with a negative weight",
			entry: &Entry{Kind: KindModel, KindConfig: &KindConfig{Model: &ModelConfig{
				Targets: []ModelTarget{{Name: "b", Weight: -1}},
			}}},
		},
		{
			name:  "agent: card url that is not a URL",
			entry: &Entry{Kind: KindAgent, KindConfig: &KindConfig{Agent: &AgentConfig{CardURL: "/card.json"}}},
		},
		{
			name: "agent: capability that is not a known one",
			entry: &Entry{Kind: KindAgent, KindConfig: &KindConfig{Agent: &AgentConfig{
				Capabilities: []string{"telepathy"},
			}}},
		},
		{
			name:  "mcp: stdio without a command",
			entry: &Entry{Kind: KindMCP, KindConfig: &KindConfig{MCP: &MCPConfig{Transport: "stdio"}}},
		},
		{
			name:  "mcp: http without a url",
			entry: &Entry{Kind: KindMCP, KindConfig: &KindConfig{MCP: &MCPConfig{Transport: "streamable-http"}}},
		},
		{
			name: "mcp: url that is not a URL",
			entry: &Entry{Kind: KindMCP, KindConfig: &KindConfig{MCP: &MCPConfig{
				Transport: "streamable-http", URL: "mcp.test",
			}}},
		},
		{
			name: "mcp: blank tool in the allowlist",
			entry: &Entry{Kind: KindMCP, KindConfig: &KindConfig{MCP: &MCPConfig{
				Transport: "streamable-http", URL: "https://mcp.test", ToolAllowlist: []string{"  "},
			}}},
		},
		{
			name:  "skill: blank trigger",
			entry: &Entry{Kind: KindSkill, KindConfig: &KindConfig{Skill: &SkillConfig{Triggers: []string{" "}}}},
		},
		{
			name:  "skill: blank step",
			entry: &Entry{Kind: KindSkill, KindConfig: &KindConfig{Skill: &SkillConfig{Steps: []string{" "}}}},
		},
		{
			name:  "skill: repository that is not a URL",
			entry: &Entry{Kind: KindSkill, KindConfig: &KindConfig{Skill: &SkillConfig{Repository: "git@github"}}},
		},
		{
			name:  "cli: no command",
			entry: &Entry{Kind: KindCLI, KindConfig: &KindConfig{CLI: &CLIConfig{}}},
		},
		{
			name:  "cli: negative timeout",
			entry: &Entry{Kind: KindCLI, KindConfig: &KindConfig{CLI: &CLIConfig{Command: "echo", TimeoutMS: -1}}},
		},
		{
			name: "cli: negative output cap",
			entry: &Entry{Kind: KindCLI, KindConfig: &KindConfig{CLI: &CLIConfig{
				Command: "echo", MaxOutputBytes: -1,
			}}},
		},
		{
			name: "cli: blank allowlist entry",
			entry: &Entry{Kind: KindCLI, KindConfig: &KindConfig{CLI: &CLIConfig{
				Command: "echo", Allowlist: []string{"ls", ""},
			}}},
		},
		{
			name:  "custom: schema that is not JSON",
			entry: &Entry{Kind: KindCustom, KindConfig: &KindConfig{Custom: &CustomConfig{Schema: "{not json"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateKind(c.entry); code(t, err) != CodeInvalidKindConfig {
				t.Fatalf("ValidateKind = %v, want invalid_kind_config", err)
			}
		})
	}
}

// A block that is simply absent, or present but empty, must stay valid: that is
// what every record registered before per-kind configuration existed looks like.
func TestValidateKindAcceptsEmptyAndNilBlocks(t *testing.T) {
	ok := []*Entry{
		nil,
		{Kind: KindModel},
		{Kind: KindModel, KindConfig: &KindConfig{}},
		{Kind: KindAgent, KindConfig: &KindConfig{Agent: &AgentConfig{}}},
		// An MCP or CLI block that says nothing still needs to say how the
		// server is reached; the generic endpoint and a command are enough.
		{Kind: KindMCP, Endpoint: "https://mcp.test", KindConfig: &KindConfig{MCP: &MCPConfig{}}},
		{Kind: KindSkill, KindConfig: &KindConfig{Skill: &SkillConfig{}}},
		{Kind: KindCLI, KindConfig: &KindConfig{CLI: &CLIConfig{Command: "echo"}}},
		{Kind: KindCustom, KindConfig: &KindConfig{Custom: &CustomConfig{}}},
	}
	for i, e := range ok {
		if e == nil {
			if err := ValidateKind(nil); code(t, err) != CodeInvalidEntry {
				t.Fatalf("nil entry = %v, want invalid_entry", err)
			}
			continue
		}
		if err := ValidateKind(e); err != nil {
			t.Fatalf("case %d: ValidateKind = %v", i, err)
		}
	}
	// A nil block short circuits before the kind is looked at — the generic
	// validation in Upsert rejects an unknown kind, not this one.
	if err := ValidateKind(&Entry{Kind: Kind("quantum")}); err != nil {
		t.Fatalf("unknown kind without a block = %v, want nil", err)
	}
	// With a block present there is no schema to check it against.
	if err := ValidateKind(&Entry{Kind: Kind("quantum"), KindConfig: &KindConfig{}}); code(t, err) != CodeInvalidKind {
		t.Fatalf("unknown kind with a block = %v, want invalid_kind", err)
	}
}

// The kind config wins over the generic columns, and the generic columns are
// the fallback: an operator who filled only the old fields still gets a usable
// model, transport and endpoint.
func TestKindConfigEffectiveFields(t *testing.T) {
	e := &Entry{Name: "router", Version: "2.1.0", Endpoint: "https://router.test/v1", Transport: TransportSSELegacy}

	var nilModel *ModelConfig
	if got := nilModel.EffectiveProviderMode(); got != ProviderModeMock {
		t.Fatalf("nil model mode = %q, want mock", got)
	}
	if got := nilModel.EffectiveEndpoint(nil); got != "" {
		t.Fatalf("nil model endpoint = %q", got)
	}
	if got := nilModel.EffectiveModelID(nil); got != "" {
		t.Fatalf("nil model id = %q", got)
	}
	if got := (&ModelConfig{}).EffectiveProviderMode(); got != ProviderModeMock {
		t.Fatalf("empty mode = %q, want mock", got)
	}
	if got := (&ModelConfig{ProviderMode: " OpenAI_Compatible "}).EffectiveProviderMode(); got != ProviderModeOpenAICompatible {
		t.Fatalf("mode = %q, want normalised openai_compatible", got)
	}

	cfg := &ModelConfig{ModelID: "gpt-4o-mini"}
	// Endpoint falls back to the entry, model id to the version, then the name.
	if got := cfg.EffectiveEndpoint(e); got != "https://router.test/v1" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := (&ModelConfig{Endpoint: " https://other.test "}).EffectiveEndpoint(e); got != "https://other.test" {
		t.Fatalf("endpoint override = %q", got)
	}
	if got := cfg.EffectiveModelID(e); got != "gpt-4o-mini" {
		t.Fatalf("model id = %q", got)
	}
	if got := (&ModelConfig{}).EffectiveModelID(e); got != "2.1.0" {
		t.Fatalf("model id fallback = %q, want the version", got)
	}
	if got := (&ModelConfig{}).EffectiveModelID(&Entry{Name: "router"}); got != "router" {
		t.Fatalf("model id second fallback = %q, want the name", got)
	}

	var nilMCP *MCPConfig
	if got := nilMCP.EffectiveTransport(nil); got != TransportStreamableHTTP {
		t.Fatalf("nil mcp transport = %q", got)
	}
	if got := (&MCPConfig{Transport: "stdio"}).EffectiveTransport(e); got != TransportStdio {
		t.Fatalf("transport = %q", got)
	}
	// An unparsable transport in the block must not break the record: the entry
	// column is older and still authoritative.
	if got := (&MCPConfig{Transport: "carrier-pigeon"}).EffectiveTransport(e); got != TransportSSELegacy {
		t.Fatalf("transport fallback = %q, want the entry's sse-legacy", got)
	}
	if got := (&MCPConfig{}).EffectiveTransport(nil); got != TransportStreamableHTTP {
		t.Fatalf("transport default = %q", got)
	}
}

func TestIsHTTPURL(t *testing.T) {
	for _, s := range []string{"https://registry.test/servers", "http://127.0.0.1:8080/x"} {
		if !isHTTPURL(s) {
			t.Fatalf("%q must be accepted", s)
		}
	}
	for _, s := range []string{"", "   ", "ftp://registry.test", "https://", "/relative/path", "registry.test"} {
		if isHTTPURL(s) {
			t.Fatalf("%q must be refused", s)
		}
	}
}

// The summary is what a log line may carry, so it counts things and never
// prints a credential — only whether one is referenced.
func TestKindConfigSummaryStaysLogSafe(t *testing.T) {
	cases := []struct {
		cfg  *KindConfig
		want string
	}{
		{nil, ""},
		{&KindConfig{}, ""},
		{&KindConfig{Model: &ModelConfig{APIKeyEnv: "OPENAI_KEY", Targets: []ModelTarget{{Name: "a"}}}}, "model(mock, targets=1, key=true)"},
		{&KindConfig{Model: &ModelConfig{ProviderMode: ProviderModeOpenAICompatible}}, "model(openai_compatible, targets=0, key=false)"},
		{&KindConfig{Agent: &AgentConfig{Capabilities: []string{"streaming"}, Skills: []string{"a", "b"}}}, "agent(caps=1, skills=2)"},
		{&KindConfig{MCP: &MCPConfig{ToolAllowlist: []string{"a", "b", "c"}}}, "mcp(tools=3)"},
		{&KindConfig{Skill: &SkillConfig{Steps: []string{"one"}}}, "skill(steps=1)"},
		{&KindConfig{CLI: &CLIConfig{Allowlist: []string{"ls"}}}, "cli(allowlist=1)"},
		{&KindConfig{Custom: &CustomConfig{Schema: "{}"}}, "custom"},
	}
	for _, c := range cases {
		if got := KindConfigSummary(c.cfg); got != c.want {
			t.Fatalf("summary = %q, want %q", got, c.want)
		}
	}
	// A secret-looking value is referenced but never printed.
	summary := KindConfigSummary(&KindConfig{Model: &ModelConfig{APIKeyEnv: "SECRET_TOKEN"}})
	if strings.Contains(summary, "SECRET_TOKEN") {
		t.Fatalf("summary leaked the credential name: %q", summary)
	}
}

// A model block that names an environment variable is fine; one that looks like
// it contains the secret itself is not.
func TestAPIKeyEnvMustBeAVariableName(t *testing.T) {
	good := []string{"OPENAI_API_KEY", "litellm_key", "_TOKEN1"}
	for _, s := range good {
		if !validEnvName(s) {
			t.Fatalf("%q must be a valid variable name", s)
		}
	}
	for _, s := range []string{"sk-1234", "1abc", "with space", ""} {
		if validEnvName(s) {
			t.Fatalf("%q must be refused", s)
		}
	}
}
