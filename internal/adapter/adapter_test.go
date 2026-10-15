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
	withEndpoint := entryFor(registry.KindModel)
	conn, _ := For(&withEndpoint, options())
	health, err := conn.Health(context.Background(), &withEndpoint)
	if err != nil || health != registry.HealthHealthy {
		t.Fatalf("health = %v, %v", health, err)
	}

	noEndpoint := registry.Entry{ID: "x", Kind: registry.KindModel, Name: "local"}
	conn, _ = For(&noEndpoint, options())
	if health, _ := conn.Health(context.Background(), &noEndpoint); health != registry.HealthDegraded {
		t.Fatalf("health without endpoint = %v", health)
	}

	flagged := entryFor(registry.KindModel)
	flagged.Metadata = map[string]string{"mock_health": string(registry.HealthUnhealthy)}
	conn, _ = For(&flagged, options())
	if health, _ := conn.Health(context.Background(), &flagged); health != registry.HealthUnhealthy {
		t.Fatalf("flagged health = %v", health)
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
