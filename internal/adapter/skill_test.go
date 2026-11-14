package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agenthub/internal/registry"
)

func skillEntry(endpoint string) *registry.Entry {
	return &registry.Entry{
		ID:        "skill-1",
		Kind:      registry.KindSkill,
		Name:      "anthropics-pdf",
		Version:   "1.0.0",
		Namespace: "default",
		Endpoint:  endpoint,
	}
}

func skillConnectorFor(t *testing.T) Connector {
	t.Helper()
	conn, err := For(&registry.Entry{Kind: registry.KindSkill}, options())
	if err != nil {
		t.Fatalf("skill connector: %v", err)
	}
	return conn
}

func TestSkillHealthNoEndpointIsUnknown(t *testing.T) {
	conn := skillConnectorFor(t)
	if health, err := conn.Health(context.Background(), skillEntry("")); err != nil || health != registry.HealthUnknown {
		t.Fatalf("health = %v, %v; want unknown for empty endpoint", health, err)
	}
	if health, err := conn.Health(context.Background(), nil); err != nil || health != registry.HealthUnknown {
		t.Fatalf("health = %v, %v; want unknown for nil entry", health, err)
	}
}

func TestSkillHealthHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	conn := skillConnectorFor(t)
	if health, err := conn.Health(context.Background(), skillEntry(srv.URL)); err != nil || health != registry.HealthHealthy {
		t.Fatalf("health = %v, %v; want healthy for http endpoint", health, err)
	}
}

func TestSkillHealthLocalDirectory(t *testing.T) {
	t.Run("present with SKILL.md is healthy", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("name: pdf\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		conn := skillConnectorFor(t)
		health, err := conn.Health(context.Background(), skillEntry("file://"+dir))
		if err != nil || health != registry.HealthHealthy {
			t.Fatalf("health = %v, %v; want healthy", health, err)
		}
	})

	t.Run("directory without SKILL.md is unhealthy", func(t *testing.T) {
		dir := t.TempDir()
		conn := skillConnectorFor(t)
		health, err := conn.Health(context.Background(), skillEntry("file://"+dir))
		if err != nil || health != registry.HealthUnhealthy {
			t.Fatalf("health = %v, %v; want unhealthy", health, err)
		}
	})

	t.Run("missing directory is unhealthy", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		conn := skillConnectorFor(t)
		health, err := conn.Health(context.Background(), skillEntry("file://"+dir))
		if err != nil || health != registry.HealthUnhealthy {
			t.Fatalf("health = %v, %v; want unhealthy", health, err)
		}
	})

	t.Run("a file where the directory should be is unhealthy", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		conn := skillConnectorFor(t)
		health, err := conn.Health(context.Background(), skillEntry("file://"+f))
		if err != nil || health != registry.HealthUnhealthy {
			t.Fatalf("health = %v, %v; want unhealthy", health, err)
		}
	})
}

func TestSkillHealthUnknownSchemeStaysUnknown(t *testing.T) {
	conn := skillConnectorFor(t)
	health, err := conn.Health(context.Background(), skillEntry("command://git"))
	if err != nil || health != registry.HealthUnknown {
		t.Fatalf("health = %v, %v; want unknown for unmeasurable scheme", health, err)
	}
}
