package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"agenthub/internal/a2a"
	"agenthub/internal/registry"
)

// packagePeer serves a card and records every message body it is sent.
func packagePeer(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/a2a" {
			_, _ = w.Write([]byte(`{"name":"peer","version":"1.0.0","protocolVersion":"1.0.0","url":"http://` +
				r.Host + `/a2a","capabilities":{"streaming":true}}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"id":"t1","status":{"state":"completed"},` +
			`"artifacts":[{"artifactId":"a1","name":"answer","parts":[{"kind":"text","text":"ok"}]}]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any{}, bodies...)
	}
}

// messageOf digs the message out of a recorded JSON-RPC body.
func messageOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	params, _ := body["params"].(map[string]any)
	message, ok := params["message"].(map[string]any)
	if !ok {
		t.Fatalf("body = %+v", body)
	}
	return message
}

func TestAgentCarriesThePackageOnBothTransports(t *testing.T) {
	srv, bodies := packagePeer(t)
	e := agentEntry(&registry.AgentConfig{CardURL: srv.URL + "/.well-known/agent-card.json"})
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	dist := &registry.Distribution{
		AgentID: e.ID,
		MCP:     []registry.MCPBinding{{ID: "mcp-1", Name: "hr-mcp", Transport: registry.TransportStreamableHTTP, URL: "https://mcp.test/rpc"}},
		Skills:  []registry.SkillBinding{{ID: "skill-1", Name: "onboarding", Steps: []string{"create account"}}},
	}
	call := InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello", Distribution: dist}

	if _, err := conn.Invoke(context.Background(), call); err != nil {
		t.Fatalf("message/send: %v", err)
	}
	// The streaming path is the other half of the delivery: an agent must not
	// lose its abilities because the caller asked for frames.
	streaming := call
	streaming.Stream = func(a2a.Artifact) error { return nil }
	if _, err := conn.Invoke(context.Background(), streaming); err != nil {
		t.Fatalf("message/stream: %v", err)
	}

	seen := bodies()
	if len(seen) != 2 {
		t.Fatalf("bodies = %+v", seen)
	}
	for i, body := range seen {
		metadata, _ := messageOf(t, body)["metadata"].(map[string]any)
		pkg, ok := metadata[DistributionKey].(map[string]any)
		if !ok {
			t.Fatalf("body %d metadata = %+v", i, metadata)
		}
		if pkg["agent_id"] != e.ID {
			t.Fatalf("body %d package = %+v", i, pkg)
		}
		mcp, _ := pkg["mcp"].([]any)
		if len(mcp) != 1 {
			t.Fatalf("body %d mcp = %+v", i, pkg["mcp"])
		}
	}
}

func TestAgentOmitsMetadataWhenNothingIsBound(t *testing.T) {
	srv, bodies := packagePeer(t)
	e := agentEntry(&registry.AgentConfig{CardURL: srv.URL + "/.well-known/agent-card.json"})
	conn, err := For(&e, options())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: e, Prompt: "hello"}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	seen := bodies()
	if len(seen) != 1 {
		t.Fatalf("bodies = %+v", seen)
	}
	// The field is omitempty and only set from a non-empty package: the request
	// stays byte identical to the pre-distribution shape.
	if _, has := messageOf(t, seen[0])["metadata"]; has {
		t.Fatalf("a call with no bindings must carry no metadata: %+v", seen[0])
	}
}

func TestDistributionMetadataSkipsEmptyPackages(t *testing.T) {
	if got := distributionMetadata(nil); got != nil {
		t.Fatalf("nil package = %+v", got)
	}
	if got := distributionMetadata(&registry.Distribution{}); got != nil {
		t.Fatalf("empty package = %+v", got)
	}
	dist := &registry.Distribution{AgentID: "a1", Skills: []registry.SkillBinding{{ID: "s1"}}}
	got := distributionMetadata(dist)
	if got[DistributionKey] != dist {
		t.Fatalf("metadata = %+v", got)
	}
}
