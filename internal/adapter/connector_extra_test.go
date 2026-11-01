package adapter

import (
	"context"
	"strings"
	"testing"

	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

func TestCommandNeverComesFromThePrompt(t *testing.T) {
	// The regression this pins: the first release read the prompt's first word as
	// the command and the rest as arguments, so an allowlisted `gh` became
	// arbitrary code execution. The prompt must not be able to name a command.
	e := entryFor(registry.KindCLI)
	e.KindConfig = &registry.KindConfig{CLI: &registry.CLIConfig{Command: "git"}}
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{
		ResourceID: e.ID,
		Entry:      e,
		Prompt:     "rm -rf / --no-preserve-root",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if strings.Contains(res.Text, "cli=rm") {
		t.Fatalf("prompt leaked into the command: %q", res.Text)
	}
	if !strings.Contains(res.Text, "cli=git") {
		t.Fatalf("registered command was not used: %q", res.Text)
	}

	// An entry with no registered command is refused outright rather than being
	// filled in from the prompt.
	empty := entryFor(registry.KindCLI)
	conn2, err := For(&empty, options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn2.Invoke(context.Background(), InvokeRequest{Entry: empty, Prompt: "echo hi"}); err == nil {
		t.Fatal("a prompt-only CLI invocation must be refused")
	}
}

func TestCLIArgsFollowThePolicy(t *testing.T) {
	mk := func(policy string, allow []string) registry.Entry {
		e := entryFor(registry.KindCLI)
		e.KindConfig = &registry.KindConfig{
			CLI:     &registry.CLIConfig{Command: "git"},
			Sandbox: &registry.SandboxConfig{ArgsPolicy: policy, ArgsAllowlist: allow},
		}
		return e
	}

	// Default: no arguments from the caller at all.
	def := mk("", nil)
	if sandbox.ArgsAllowed(policyProfile(&def), []string{"status"}) {
		t.Fatal("the default policy must refuse caller supplied arguments")
	}
	// Prefix policy: only the declared prefixes pass.
	prefixed := mk(sandbox.ArgsPrefix, []string{"--short"})
	if !sandbox.ArgsAllowed(policyProfile(&prefixed), []string{"--short"}) {
		t.Fatal("a declared prefix must be allowed")
	}
	if sandbox.ArgsAllowed(policyProfile(&prefixed), []string{"--exec=evil"}) {
		t.Fatal("an undeclared prefix must be refused")
	}
	// Passthrough is the explicit opt-out.
	passthrough := mk(sandbox.ArgsPassthrough, nil)
	if !sandbox.ArgsAllowed(policyProfile(&passthrough), []string{"anything"}) {
		t.Fatal("passthrough must allow arguments")
	}
}

func TestQuoteArgsAndBoolString(t *testing.T) {
	if got := quoteArgs([]string{`a"b`}); got != `["a\"b"]` {
		t.Fatalf("quoteArgs = %q", got)
	}
	if got := quoteArgs(nil); got != "[]" {
		t.Fatalf("quoteArgs(nil) = %q", got)
	}
	if boolString(true) != "true" || boolString(false) != "false" {
		t.Fatal("boolString mismatch")
	}
}

func TestTruncateReportsWhetherItHappened(t *testing.T) {
	out, truncated := truncate("hello", 3)
	if out != "hel" || !truncated {
		t.Fatalf("truncate short = %q %v", out, truncated)
	}
	out, truncated = truncate("hi", 3)
	if out != "hi" || truncated {
		t.Fatalf("truncate within limit = %q %v", out, truncated)
	}
	// A non-positive cap means "no limit".
	out, truncated = truncate("anything", 0)
	if out != "anything" || truncated {
		t.Fatalf("truncate zero cap = %q %v", out, truncated)
	}
}

func TestDigestAndPickAreDeterministic(t *testing.T) {
	if digest("abc") != digest("abc") {
		t.Fatal("digest must be stable")
	}
	if digest("abc") == digest("abd") {
		t.Fatal("digest must differ for different input")
	}
	// pick is bounded by n and deterministic for the same input.
	for i := 0; i < 10; i++ {
		if p := pick("seed", 4); p < 0 || p >= 4 {
			t.Fatalf("pick out of range: %d", p)
		}
		if pick("seed", 4) != pick("seed", 4) {
			t.Fatal("pick must be deterministic")
		}
	}
	if pick("seed", 1) != 0 {
		t.Fatal("pick with n=1 must be 0")
	}
}

func TestTokenCountIsDeterministic(t *testing.T) {
	if tokenCount("one two three") != tokenCount("one two three") {
		t.Fatal("tokenCount must be deterministic")
	}
	if tokenCount("") != tokenCount("") {
		t.Fatal("tokenCount empty must be deterministic")
	}
}

func TestMetaString(t *testing.T) {
	if metaString(map[string]string{"k": "v"}, "k") != "v" {
		t.Fatal("metaString should return the value")
	}
	if metaString(nil, "k") != "" {
		t.Fatal("metaString on nil map should be empty")
	}
	if metaString(map[string]string{}, "missing") != "" {
		t.Fatal("metaString on missing key should be empty")
	}
}

func TestEntryCommand(t *testing.T) {
	e := &registry.Entry{KindConfig: &registry.KindConfig{CLI: &registry.CLIConfig{Command: "git"}}}
	if got := entryCommand(e); got != "git" {
		t.Fatalf("entryCommand kind config = %q, want git", got)
	}
	e = &registry.Entry{Metadata: map[string]string{"command": "ls"}}
	if got := entryCommand(e); got != "ls" {
		t.Fatalf("entryCommand metadata = %q, want ls", got)
	}
	if entryCommand(nil) != "" {
		t.Fatal("entryCommand(nil) must be empty")
	}
	if entryCommand(&registry.Entry{}) != "" {
		t.Fatal("entryCommand empty entry must be empty")
	}
}

func TestCustomConnector(t *testing.T) {
	e := entryFor(registry.KindCustom)
	e.KindConfig = &registry.KindConfig{Custom: &registry.CustomConfig{Schema: `{"type":"object"}`}}
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "describe"})
	if err != nil {
		t.Fatalf("custom invoke: %v", err)
	}
	if !strings.Contains(res.Text, "custom="+e.Name) {
		t.Fatalf("custom text = %q", res.Text)
	}
	if !strings.Contains(res.Text, "digest=") {
		t.Fatalf("custom text missing digest: %q", res.Text)
	}
}

func TestMCPConnector(t *testing.T) {
	e := entryFor(registry.KindMCP)
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "read the manual"})
	if err != nil {
		t.Fatalf("mcp invoke: %v", err)
	}
	if !strings.Contains(res.Text, "mcp="+e.Name) {
		t.Fatalf("mcp text = %q", res.Text)
	}
	if !strings.Contains(res.Text, "transport=") {
		t.Fatalf("mcp text missing transport: %q", res.Text)
	}
	if len(res.Artifacts) < 2 {
		t.Fatalf("mcp should emit tools + call artifacts, got %d", len(res.Artifacts))
	}
}

func TestSkillConnector(t *testing.T) {
	e := entryFor(registry.KindSkill)
	e.Capabilities = []string{"translate"}
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("skill invoke: %v", err)
	}
	if !strings.Contains(res.Text, "skill="+e.Name) {
		t.Fatalf("skill text = %q", res.Text)
	}
	if !strings.Contains(res.Text, "steps=") {
		t.Fatalf("skill text missing steps: %q", res.Text)
	}
}

func TestModelConnectorMockProvider(t *testing.T) {
	e := entryFor(registry.KindModel)
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"})
	if err != nil {
		t.Fatalf("model invoke: %v", err)
	}
	if !strings.Contains(res.Text, "provider=mock") {
		t.Fatalf("model text should use the mock provider: %q", res.Text)
	}
}

func TestCompletionsURL(t *testing.T) {
	if got := completionsURL("https://api.example.test/v1"); got != "https://api.example.test/v1/chat/completions" {
		t.Fatalf("completionsURL base = %q", got)
	}
	if got := completionsURL("https://api.example.test/chat/completions"); got != "https://api.example.test/chat/completions" {
		t.Fatalf("completionsURL already-suffixed = %q", got)
	}
	if got := completionsURL(""); got != "" {
		t.Fatalf("completionsURL empty = %q", got)
	}
}

func TestNewModelRouterFor(t *testing.T) {
	mkEntry := func() *registry.Entry {
		return &registry.Entry{
			Kind: registry.KindModel,
			KindConfig: &registry.KindConfig{
				Model: &registry.ModelConfig{ModelID: "gpt-4o-mini"},
			},
		}
	}

	// Default (mock) mode stays entirely offline.
	r := NewModelRouterFor(mkEntry(), "")
	if out, provider, err := r.GenerateWith(context.Background(), "hi"); err != nil || provider != MockProviderName {
		t.Fatalf("mock router: out=%q provider=%q err=%v", out, provider, err)
	}

	// openai_compatible without an endpoint degrades to the offline mock rather
	// than fabricating a gateway call to nowhere.
	openai := mkEntry()
	openai.KindConfig.Model.ProviderMode = registry.ProviderModeOpenAICompatible
	if r := NewModelRouterFor(openai, ""); r.providers[0].Name() != MockProviderName {
		t.Fatalf("openai without endpoint should fall back to mock, got %q", r.providers[0].Name())
	}

	// openai_compatible with an endpoint puts a real provider first in the chain.
	openai.KindConfig.Model.Endpoint = "https://api.example.test/v1"
	r3 := NewModelRouterFor(openai, "")
	if len(r3.providers) == 0 || !strings.HasPrefix(r3.providers[0].Name(), "openai:") {
		t.Fatalf("openai with endpoint should lead with a real provider, got %v", r3.providers)
	}
}
