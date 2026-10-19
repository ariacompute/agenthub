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

// AGENTHUB_MODEL_ROUTER_MODE is the instance wide fallback: an entry that omits
// provider_mode inherits it, an entry that names one keeps its own, and a mode
// outside the vocabulary degrades to the offline default rather than turning
// every model entry into a gateway call.
func TestModelProviderModeInstanceDefault(t *testing.T) {
	var nilModel *ModelConfig
	if got := nilModel.EffectiveProviderModeWith(ProviderModeOpenAICompatible); got != ProviderModeMock {
		t.Fatalf("nil model = %q, want mock", got)
	}
	if got := (&ModelConfig{}).EffectiveProviderModeWith(ProviderModeOpenAICompatible); got != ProviderModeOpenAICompatible {
		t.Fatalf("inherited = %q, want openai_compatible", got)
	}
	if got := (&ModelConfig{ProviderMode: "Mock"}).EffectiveProviderModeWith(ProviderModeOpenAICompatible); got != ProviderModeMock {
		t.Fatalf("entry opt-out = %q, want the entry's own mock", got)
	}
	if got := (&ModelConfig{}).EffectiveProviderModeWith(""); got != ProviderModeMock {
		t.Fatalf("unset default = %q, want mock", got)
	}
	if got := (&ModelConfig{}).EffectiveProviderModeWith("anthropic"); got != ProviderModeMock {
		t.Fatalf("unknown default = %q, want mock", got)
	}

	// Validation resolves the same way, so a record is checked the way the
	// instance intends to run it: offline instances tolerate a model block with
	// no endpoint, gateway instances do not.
	withoutEndpoint := func() *Entry {
		return &Entry{Kind: KindModel, Name: "m", Version: "1.0.0",
			KindConfig: &KindConfig{Model: &ModelConfig{ModelID: "gpt-4o-mini"}}}
	}
	if err := ValidateKindWithMode(withoutEndpoint(), ProviderModeMock); err != nil {
		t.Fatalf("offline instance: %v", err)
	}
	if err := ValidateKindWithMode(withoutEndpoint(), ProviderModeOpenAICompatible); err == nil {
		t.Fatal("an openai_compatible instance must require an endpoint")
	}
	// The package entry point keeps its historical behaviour: offline by default.
	if err := ValidateKind(withoutEndpoint()); err != nil {
		t.Fatalf("ValidateKind must stay offline by default: %v", err)
	}
}

// MaxTokens caps a completion: an explicit value is used as is, zero or negative
// means "keep the default", and a negative one is rejected at registration time.
func TestModelMaxTokens(t *testing.T) {
	var nilModel *ModelConfig
	if got := nilModel.EffectiveMaxTokens(); got != DefaultModelMaxTokens {
		t.Fatalf("nil = %d, want %d", got, DefaultModelMaxTokens)
	}
	if got := (&ModelConfig{}).EffectiveMaxTokens(); got != DefaultModelMaxTokens {
		t.Fatalf("unset = %d, want %d", got, DefaultModelMaxTokens)
	}
	if got := (&ModelConfig{MaxTokens: -5}).EffectiveMaxTokens(); got != DefaultModelMaxTokens {
		t.Fatalf("negative = %d, want %d", got, DefaultModelMaxTokens)
	}
	if got := (&ModelConfig{MaxTokens: 128}).EffectiveMaxTokens(); got != 128 {
		t.Fatalf("explicit = %d, want 128", got)
	}

	entry := func(tokens int) *Entry {
		return &Entry{Kind: KindModel, Name: "m",
			KindConfig: &KindConfig{Model: &ModelConfig{
				ProviderMode: ProviderModeOpenAICompatible, Endpoint: "https://router.test",
				ModelID: "m", MaxTokens: tokens}}}
	}
	if err := ValidateKind(entry(128)); err != nil {
		t.Fatalf("positive max_tokens: %v", err)
	}
	if err := ValidateKind(entry(-1)); err == nil {
		t.Fatal("a negative max_tokens must be rejected")
	}
}

// The endpoint is validated the way the adapter resolves it: kind_config.model
// first, then the entry's own endpoint. An entry that only fills the generic
// column must therefore register, because that is the address it will be called
// on — and an entry with no address anywhere must still be refused.
func TestModelEndpointFallsBackToTheEntry(t *testing.T) {
	model := func(cfg *ModelConfig) *KindConfig { return &KindConfig{Model: cfg} }

	// Only the generic column carries the address.
	inherited := &Entry{Kind: KindModel, Name: "m", Endpoint: "https://router.test/v1",
		KindConfig: model(&ModelConfig{ProviderMode: ProviderModeOpenAICompatible, ModelID: "gpt-4o-mini"})}
	if err := ValidateKind(inherited); err != nil {
		t.Fatalf("entry endpoint must satisfy validation: %v", err)
	}

	// The block wins when both are present, and it is the one checked.
	blockWins := &Entry{Kind: KindModel, Name: "m", Endpoint: "https://router.test/v1",
		KindConfig: model(&ModelConfig{ProviderMode: ProviderModeOpenAICompatible, ModelID: "gpt-4o-mini",
			Endpoint: "stdio://not-http"})}
	if err := ValidateKind(blockWins); err == nil {
		t.Fatal("the block's endpoint must be the one validated")
	}

	// A non-HTTP address inherited from the entry is still refused.
	nonHTTP := &Entry{Kind: KindModel, Name: "m", Endpoint: "stdio://pi",
		KindConfig: model(&ModelConfig{ProviderMode: ProviderModeOpenAICompatible, ModelID: "gpt-4o-mini"})}
	if err := ValidateKind(nonHTTP); err == nil {
		t.Fatal("a non-HTTP entry endpoint must be refused")
	}

	// No address anywhere.
	missing := &Entry{Kind: KindModel, Name: "m",
		KindConfig: model(&ModelConfig{ProviderMode: ProviderModeOpenAICompatible, ModelID: "gpt-4o-mini"})}
	if err := ValidateKind(missing); err == nil {
		t.Fatal("an entry without any endpoint must be refused")
	}

	// Offline entries never needed an address and still do not.
	offline := &Entry{Kind: KindModel, Name: "m",
		KindConfig: model(&ModelConfig{ProviderMode: ProviderModeMock})}
	if err := ValidateKind(offline); err != nil {
		t.Fatalf("mock mode: %v", err)
	}
}

// Same rule for the model id: the adapter falls back to the entry version and
// then its name, so validation accepts what the call will actually carry. An
// entry with neither is refused.
func TestModelIDFallsBackToTheEntry(t *testing.T) {
	entry := func(version, name string) *Entry {
		return &Entry{Kind: KindModel, Name: name, Version: version, Endpoint: "https://router.test/v1",
			KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: ProviderModeOpenAICompatible}}}
	}
	if err := ValidateKind(entry("gpt-4o-mini", "m")); err != nil {
		t.Fatalf("version fallback: %v", err)
	}
	if err := ValidateKind(entry("", "m")); err != nil {
		t.Fatalf("name fallback: %v", err)
	}
	if err := ValidateKind(entry("", "")); err == nil {
		t.Fatal("an entry with no model id anywhere must be refused")
	}

	// A whitespace-only model_id behaves like an unset one: the fallback still
	// applies, both here and at call time, so the version is what gets sent.
	blank := &Entry{Kind: KindModel, Name: "m", Version: "gpt-4o-mini", Endpoint: "https://router.test/v1",
		KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: ProviderModeOpenAICompatible, ModelID: "  "}}}
	if err := ValidateKind(blank); err != nil {
		t.Fatalf("a blank model_id falls back like an unset one: %v", err)
	}

	// The block wins when it names one — even a name the fallback would not have
	// produced, which is the whole point of letting it override.
	explicit := &Entry{Kind: KindModel, Name: "m", Version: "1.0.0", Endpoint: "https://router.test/v1",
		KindConfig: &KindConfig{Model: &ModelConfig{ProviderMode: ProviderModeOpenAICompatible, ModelID: "gpt-4o-mini"}}}
	if err := ValidateKind(explicit); err != nil {
		t.Fatalf("explicit model_id: %v", err)
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
