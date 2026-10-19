package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenthub/internal/registry"
)

// gateway serves /models and, when told to, /chat/completions with the status
// under test. The probe has to reach whichever one answers.
func gateway(t *testing.T, modelsStatus int, serveCompletions bool, completionStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.WriteHeader(modelsStatus)
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/chat/completions") && serveCompletions:
			w.WriteHeader(completionStatus)
			_, _ = w.Write([]byte(`{"choices":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func probeModelEntry(endpoint string, mode string) registry.Entry {
	return registry.Entry{
		ID:      "model-1",
		Kind:    registry.KindModel,
		Name:    "gateway",
		Version: "1.0.0",
		KindConfig: &registry.KindConfig{
			Model: &registry.ModelConfig{
				ProviderMode: mode,
				Endpoint:     endpoint,
				ModelID:      "demo/model",
				APIKeyEnv:    "AGENTHUB_PROBE_TEST_KEY",
			},
		},
	}
}

func TestModelProbeMeasuresTheGateway(t *testing.T) {
	t.Setenv("AGENTHUB_PROBE_TEST_KEY", "not-a-real-key")

	// The listing answers: healthy, and no token is spent.
	srv := gateway(t, http.StatusOK, true, http.StatusOK)
	defer srv.Close()
	e := probeModelEntry(srv.URL+"/v1", registry.ProviderModeOpenAICompatible)
	conn, _ := For(&e, options())
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthHealthy {
		t.Fatalf("health = %v, %v; want healthy", health, err)
	}
	srv.Close()

	// A refused credential: the gateway answered, it just will not serve us.
	srv = gateway(t, http.StatusUnauthorized, true, http.StatusOK)
	e = probeModelEntry(srv.URL+"/v1", registry.ProviderModeOpenAICompatible)
	conn, _ = For(&e, options())
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthDegraded {
		t.Fatalf("401 health = %v, %v; want degraded", health, err)
	}
	srv.Close()

	// No /models route at all: the probe falls back to the endpoint the adapter
	// really calls and takes that answer.
	srv = gateway(t, http.StatusNotFound, true, http.StatusOK)
	e = probeModelEntry(srv.URL+"/v1", registry.ProviderModeOpenAICompatible)
	conn, _ = For(&e, options())
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthHealthy {
		t.Fatalf("fallback health = %v, %v; want healthy", health, err)
	}
	srv.Close()

	// Both routes fail: the gateway is there but broken.
	srv = gateway(t, http.StatusNotFound, true, http.StatusInternalServerError)
	e = probeModelEntry(srv.URL+"/v1", registry.ProviderModeOpenAICompatible)
	conn, _ = For(&e, options())
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthUnhealthy {
		t.Fatalf("broken health = %v, %v; want unhealthy", health, err)
	}
	srv.Close()

	// Nothing is listening: that is an error, so the caller records unhealthy
	// instead of keeping whatever the record said before.
	e = probeModelEntry("http://127.0.0.1:1/v1", registry.ProviderModeOpenAICompatible)
	conn, _ = For(&e, options())
	if _, err := conn.Health(context.Background(), &e); err == nil {
		t.Fatal("unreachable gateway: want an error")
	}
}

func TestModelProbeStaysUnknownWhenNothingIsCalled(t *testing.T) {
	// An offline entry never dials its endpoint: measuring it would describe an
	// address the adapter does not use.
	e := probeModelEntry("http://127.0.0.1:1/v1", registry.ProviderModeMock)
	conn, _ := For(&e, options())
	health, err := conn.Health(context.Background(), &e)
	if err != nil || health != registry.HealthUnknown {
		t.Fatalf("offline health = %v, %v; want unknown", health, err)
	}

	// No endpoint to call at all, even in gateway mode.
	e = probeModelEntry("", registry.ProviderModeOpenAICompatible)
	conn, _ = For(&e, options())
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthUnknown {
		t.Fatalf("endpointless health = %v, %v; want unknown", health, err)
	}
}

func TestMCPProbePostsInitialize(t *testing.T) {
	var seenMethod, seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path
		body := make([]byte, 512)
		n, _ := r.Body.Read(body)
		if !strings.Contains(string(body[:n]), `"initialize"`) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"agenthub-probe","result":{}}`))
	}))
	defer srv.Close()

	e := registry.Entry{
		ID:   "mcp-1",
		Kind: registry.KindMCP,
		Name: "remote-mcp",
		KindConfig: &registry.KindConfig{
			MCP: &registry.MCPConfig{Transport: string(registry.TransportStreamableHTTP), URL: srv.URL + "/mcp"},
		},
	}
	conn, _ := For(&e, options())
	health, err := conn.Health(context.Background(), &e)
	if err != nil || health != registry.HealthHealthy {
		t.Fatalf("health = %v, %v; want healthy", health, err)
	}
	if seenMethod != http.MethodPost || seenPath != "/mcp" {
		t.Fatalf("probe called %s %s; want POST /mcp", seenMethod, seenPath)
	}
}

func TestMCPProbeSkipsStdio(t *testing.T) {
	// Reaching a stdio server means launching it, which the offline connector
	// never does on its own: no subprocess, so no measurement.
	e := registry.Entry{
		ID:        "mcp-2",
		Kind:      registry.KindMCP,
		Name:      "stdio-mcp",
		Transport: registry.TransportStdio,
		KindConfig: &registry.KindConfig{
			MCP: &registry.MCPConfig{Transport: string(registry.TransportStdio), Command: "npx"},
		},
	}
	conn, _ := For(&e, options())
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthUnknown {
		t.Fatalf("stdio health = %v, %v; want unknown", health, err)
	}
}

func TestCLIProbeLooksUpTheCommand(t *testing.T) {
	// A real executable on disk: the probe only has to find it, never run it.
	dir := t.TempDir()
	bin := filepath.Join(dir, "aria-probe-tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	e := registry.Entry{
		ID:   "cli-1",
		Kind: registry.KindCLI,
		Name: "probe-tool",
		KindConfig: &registry.KindConfig{
			CLI: &registry.CLIConfig{Command: bin},
		},
	}

	// Found and allowlisted: usable.
	conn, _ := For(&e, Options{CLIAllowlist: []string{bin}})
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthHealthy {
		t.Fatalf("allowlisted health = %v, %v; want healthy", health, err)
	}

	// Found but refused by the allowlist: the binary is there, it is just not
	// usable, which is degraded rather than missing.
	conn, _ = For(&e, Options{CLIAllowlist: []string{"something-else"}})
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthDegraded {
		t.Fatalf("refused health = %v, %v; want degraded", health, err)
	}

	// Not installed at all.
	e.KindConfig.CLI.Command = filepath.Join(dir, "not-installed")
	conn, _ = For(&e, Options{CLIAllowlist: []string{"not-installed"}})
	if health, err := conn.Health(context.Background(), &e); err != nil || health != registry.HealthUnhealthy {
		t.Fatalf("missing health = %v, %v; want unhealthy", health, err)
	}
}

func TestSkillAndCustomProbeMeasureTheEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manual.md" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("# manual"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	skill := registry.Entry{ID: "skill-1", Kind: registry.KindSkill, Name: "manual", Endpoint: srv.URL + "/manual.md"}
	conn, _ := For(&skill, options())
	if health, err := conn.Health(context.Background(), &skill); err != nil || health != registry.HealthHealthy {
		t.Fatalf("skill health = %v, %v; want healthy", health, err)
	}

	custom := registry.Entry{ID: "custom-1", Kind: registry.KindCustom, Name: "record", Endpoint: srv.URL + "/missing"}
	conn, _ = For(&custom, options())
	if health, err := conn.Health(context.Background(), &custom); err != nil || health != registry.HealthUnhealthy {
		t.Fatalf("custom health = %v, %v; want unhealthy", health, err)
	}
}
