package adapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

func options() Options {
	return Options{CLITimeout: time.Second, CLIAllowlist: []string{"git", "ls", "echo"}, MaxOutputBytes: 4096}
}

func entryFor(kind registry.Kind) registry.Entry {
	return registry.Entry{ID: string(kind) + "-1", Kind: kind, Name: "demo", Endpoint: "https://example.test", Metadata: map[string]string{}}
}

func TestForResolvesEveryKind(t *testing.T) {
	for _, kind := range registry.Kinds {
		e := entryFor(kind)
		conn, err := For(&e, options())
		if err != nil {
			t.Fatalf("For(%s): %v", kind, err)
		}
		if conn.Kind() != kind {
			t.Fatalf("Kind() = %v, want %v", conn.Kind(), kind)
		}
	}
	if _, err := For(nil, options()); httputil.AsError(err).Code != CodeUnknownKind {
		t.Fatal("nil entry must be rejected")
	}
	bad := registry.Entry{Kind: "bogus"}
	if _, err := For(&bad, options()); httputil.AsError(err).Code != CodeUnknownKind {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestMockAdaptersAreDeterministic(t *testing.T) {
	for _, kind := range registry.Kinds {
		e := entryFor(kind)
		conn, _ := For(&e, options())
		req := InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "leave request", Metadata: map[string]string{"command": "git"}}
		first, err := conn.Invoke(context.Background(), req)
		if err != nil {
			t.Fatalf("%s invoke: %v", kind, err)
		}
		second, err := conn.Invoke(context.Background(), req)
		if err != nil {
			t.Fatalf("%s invoke again: %v", kind, err)
		}
		if first.Text != second.Text {
			t.Fatalf("%s is not deterministic: %q vs %q", kind, first.Text, second.Text)
		}
		if first.Text == "" || len(first.Artifacts) == 0 {
			t.Fatalf("%s produced no output: %+v", kind, first)
		}
	}
}

func TestAgentParksForApproval(t *testing.T) {
	e := entryFor(registry.KindAgent)
	conn, _ := For(&e, options())
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "please approve the change"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !res.RequiresInput {
		t.Fatal("agent must park an approval request")
	}
	if !strings.HasPrefix(res.Prompt, "approval_required:") {
		t.Fatalf("prompt = %q", res.Prompt)
	}

	res, err = conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "approved"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.RequiresInput {
		t.Fatal("an answer must resume instead of re-parking")
	}
}

func TestCLIAllowlistAndTruncation(t *testing.T) {
	e := entryFor(registry.KindCLI)
	conn, _ := For(&e, options())

	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "rm -rf /"}); httputil.AsError(err).Code != CodeCommandNotAllowed {
		t.Fatalf("blocked command = %v", err)
	}
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: ""}); httputil.AsError(err).Code != CodeCommandNotAllowed {
		t.Fatalf("empty command = %v", err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "git status --short"})
	if err != nil {
		t.Fatalf("allowed command: %v", err)
	}
	if !strings.Contains(res.Text, "cli=git") {
		t.Fatalf("text = %q", res.Text)
	}

	tight := Options{CLITimeout: time.Second, CLIAllowlist: []string{"git"}, MaxOutputBytes: 8}
	tightConn, _ := For(&e, tight)
	res, err = tightConn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "git log"})
	if err != nil {
		t.Fatalf("truncated invoke: %v", err)
	}
	if !strings.Contains(res.Text, "truncated=true") {
		t.Fatalf("truncation must be reported: %q", res.Text)
	}
}

func TestHealthSignals(t *testing.T) {
	// A model nobody calls is not measured: offline mode never touches the
	// endpoint, so "unknown" is the only true answer — claiming healthy because
	// a URL happens to be filled in is what a probe stopped doing.
	offline := entryFor(registry.KindModel)
	conn, _ := For(&offline, options())
	if health, err := conn.Health(context.Background(), &offline); err != nil || health != registry.HealthUnknown {
		t.Fatalf("offline model health = %v, %v; want unknown", health, err)
	}

	// A stdio MCP server, a skill with no endpoint and a CLI with no command
	// give the probe nothing to reach, so they answer the same way.
	stdio := registry.Entry{ID: "mcp-1", Kind: registry.KindMCP, Name: "stdio", Transport: registry.TransportStdio}
	conn, _ = For(&stdio, options())
	if health, _ := conn.Health(context.Background(), &stdio); health != registry.HealthUnknown {
		t.Fatalf("stdio mcp health = %v; want unknown", health)
	}

	skill := registry.Entry{ID: "skill-1", Kind: registry.KindSkill, Name: "local"}
	conn, _ = For(&skill, options())
	if health, _ := conn.Health(context.Background(), &skill); health != registry.HealthUnknown {
		t.Fatalf("skill without endpoint health = %v; want unknown", health)
	}

	bare := registry.Entry{ID: "cli-1", Kind: registry.KindCLI, Name: "bare"}
	conn, _ = For(&bare, options())
	if health, _ := conn.Health(context.Background(), &bare); health != registry.HealthUnknown {
		t.Fatalf("cli without command health = %v; want unknown", health)
	}
}

func TestModelRouterFallsBack(t *testing.T) {
	if _, err := (&ModelRouter{}).Generate(context.Background(), "hi"); err == nil {
		t.Fatal("a router without providers must fail")
	}
	out, err := NewModelRouter().Generate(context.Background(), "hi")
	if err != nil || !strings.Contains(out, "provider=mock") {
		t.Fatalf("generate = %q, %v", out, err)
	}
}

func TestContextCancellationPropagates(t *testing.T) {
	e := entryFor(registry.KindModel)
	conn, _ := For(&e, options())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conn.Invoke(ctx, InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hi"}); err == nil {
		t.Fatal("cancelled context must surface an error")
	}
}
