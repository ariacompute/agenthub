package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenthub/internal/httputil"
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

func skillData(t *testing.T, res InvokeResult) map[string]any {
	t.Helper()
	for _, part := range res.Parts {
		if part.Data != nil {
			return part.Data
		}
	}
	t.Fatal("invoke result carried no data part")
	return nil
}

func TestSkillInvokeReadsALocalManual(t *testing.T) {
	dir := t.TempDir()
	manual := "# PDF toolkit\n\n1. open the file\n2. extract text\n- [ ] summarise\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}
	e := skillEntry("file://" + dir)
	conn, err := For(e, options())
	if err != nil {
		t.Fatal(err)
	}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "pdf"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	data := skillData(t, res)
	steps, _ := data["steps"].([]string)
	want := []string{"open the file", "extract text", "summarise"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
	if data["provider"] != "skill:manual" || data["manual_title"] != "PDF toolkit" {
		t.Fatalf("data = %+v", data)
	}
	// Three resolved steps plus the manual artifact.
	if len(res.Artifacts) != 4 {
		t.Fatalf("artifacts = %d, want 4", len(res.Artifacts))
	}
	if res.Artifacts[len(res.Artifacts)-1].Name != "skill.manual" {
		t.Fatalf("last artifact = %q", res.Artifacts[len(res.Artifacts)-1].Name)
	}
}

func TestSkillDeclaredStepsWinOverTheManual(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# T\n\n1. from manual\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := skillEntry("file://" + dir)
	e.KindConfig = &registry.KindConfig{Skill: &registry.SkillConfig{Steps: []string{"declared one", "declared two"}}}
	conn, _ := For(e, options())
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	steps, _ := skillData(t, res)["steps"].([]string)
	if len(steps) != 2 || steps[0] != "declared one" {
		t.Fatalf("steps = %v", steps)
	}
}

func TestSkillWithoutAManualKeepsTheOfflinePlan(t *testing.T) {
	e := skillEntry("")
	e.Capabilities = []string{"translate"}
	conn, _ := For(e, options())
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	data := skillData(t, res)
	if data["provider"] != MockProviderName {
		t.Fatalf("provider = %v", data["provider"])
	}
	steps, _ := data["steps"].([]string)
	if len(steps) != 6 || !strings.HasPrefix(steps[5], "apply_capability:") {
		t.Fatalf("steps = %v", steps)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("the offline plan must stay a single artifact, got %d", len(res.Artifacts))
	}
}

func TestSkillManualWithoutStepsFallsBackToThePlan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# No steps here\n\njust prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := skillEntry("file://" + dir)
	conn, _ := For(e, options())
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	data := skillData(t, res)
	if data["provider"] != "skill:manual" {
		t.Fatalf("provider = %v", data["provider"])
	}
	steps, _ := data["steps"].([]string)
	if len(steps) != len(defaultSkillSteps()) {
		t.Fatalf("steps = %v, want the offline plan", steps)
	}
}

func TestSkillInvokeFetchesAnHTTPManual(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# Remote\n\n1. fetch\n2. run\n"))
	}))
	defer srv.Close()

	e := skillEntry(srv.URL)
	conn := &skillConnector{client: srv.Client()}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	data := skillData(t, res)
	if data["provider"] != "skill:manual" {
		t.Fatalf("provider = %v", data["provider"])
	}
	steps, _ := data["steps"].([]string)
	if len(steps) != 2 || steps[0] != "fetch" {
		t.Fatalf("steps = %v", steps)
	}
}

func TestSkillUnreachableHTTPManualFallsBackToThePlan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := skillEntry(srv.URL)
	conn := &skillConnector{client: srv.Client()}
	res, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"})
	if err != nil {
		t.Fatalf("an unreachable manual must not fail the skill: %v", err)
	}
	if skillData(t, res)["provider"] != MockProviderName {
		t.Fatalf("provider = %v", skillData(t, res)["provider"])
	}
}

func TestSkillRejectsAnOversizedManual(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		dir := t.TempDir()
		big := strings.Repeat("x", maxSkillManualBytes+1)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
		e := skillEntry("file://" + dir)
		conn, _ := For(e, options())
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"}); httputil.AsError(err).Code != CodeProviderMalformed {
			t.Fatalf("code = %q, want model_provider_malformed", httputil.AsError(err).Code)
		}
	})

	t.Run("http", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxSkillManualBytes+1)))
		}))
		defer srv.Close()
		e := skillEntry(srv.URL)
		conn := &skillConnector{client: srv.Client()}
		if _, err := conn.Invoke(context.Background(), InvokeRequest{ResourceID: e.ID, Entry: *e, Prompt: "go"}); httputil.AsError(err).Code != CodeProviderMalformed {
			t.Fatalf("code = %q, want model_provider_malformed", httputil.AsError(err).Code)
		}
	})
}

func TestSkillLocalDirHandlesADriveLetter(t *testing.T) {
	got, err := localSkillDir(`file://C:\skills\pdf`)
	if err != nil {
		t.Fatalf("localSkillDir: %v", err)
	}
	if !strings.Contains(got, "skills") {
		t.Fatalf("path = %q", got)
	}
	if _, err := localSkillDir("https://example.test"); err == nil {
		t.Fatal("a non-file endpoint must not resolve to a local directory")
	}
}
