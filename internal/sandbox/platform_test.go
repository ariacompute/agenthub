package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The platform providers (E2B, microsandbox, Daytona, CubeSandbox, OpenHands)
// are all the same HTTP shim, so one fake server proves the whole family: the
// credential stays an environment variable reference, redirects are not
// followed, and an unwired platform still appears in the catalogue.

func platformPreset() PlatformPreset {
	return PlatformPreset{
		Name:       "e2b",
		Layer:      LayerPlatform,
		Isolation:  IsolationPlatform,
		EnvBaseURL: "E2B_SHIM_URL",
		EnvToken:   "E2B_SHIM_TOKEN",
		License:    "Apache-2.0",
		DocsURL:    "https://github.com/e2b-dev/E2B",
	}
}

func platformLookup(base, token string) func(string) string {
	return func(key string) string {
		switch key {
		case "E2B_SHIM_URL":
			return base
		case "E2B_SHIM_TOKEN":
			return token
		}
		return ""
	}
}

func TestAPlatformThatIsNotWiredUpStaysInTheCatalogue(t *testing.T) {
	provider := NewHTTPProvider(platformPreset(), nil)
	ctx := context.Background()

	if provider.Name() != "e2b" {
		t.Fatalf("name = %q", provider.Name())
	}
	desc, err := provider.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if desc.Layer != LayerPlatform || desc.Isolation != IsolationPlatform {
		t.Fatalf("descriptor = %+v", desc)
	}
	// The base URL and the token are environment keys, not binaries: Requires is
	// what a probe looks for, and no probe looks for E2B_SHIM_URL.
	if len(desc.Requires) != 0 {
		t.Fatalf("a platform shim needs no binary, got %v", desc.Requires)
	}
	if len(desc.EnvKeys) != 2 || desc.EnvKeys[0] != "E2B_SHIM_URL" {
		t.Fatalf("the console must be told which variables exist, got %v", desc.EnvKeys)
	}
	// The sandbox runs on the platform; this process cannot constrain its network.
	if desc.Source != SourcePlatform {
		t.Fatalf("source = %q, want %q", desc.Source, SourcePlatform)
	}
	if len(desc.Network) != 1 || desc.Network[0] != NetworkDelegated {
		t.Fatalf("network = %v, want [%s]", desc.Network, NetworkDelegated)
	}

	avail, err := provider.Probe(ctx)
	if err != nil {
		t.Fatalf("probe must not fail: %v", err)
	}
	if avail.Available || !strings.Contains(avail.Reason, "E2B_SHIM_URL") {
		t.Fatalf("the console must be told which variable is missing, got %+v", avail)
	}

	if _, err := provider.Create(ctx, Spec{LeaseID: "l1"}); err == nil {
		t.Fatal("an unwired platform cannot create a sandbox")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if err := provider.Destroy(ctx, Handle{Ref: "sbx-1"}); err != nil {
		t.Fatalf("nothing to release must not be an error: %v", err)
	}
	if state, err := provider.Status(ctx, Handle{Ref: "sbx-1"}); err != nil || state != StateFailed {
		t.Fatalf("status = %q (%v), want failed", state, err)
	}
	if _, err := provider.Attach(ctx, Handle{}, ExecRequest{}); err == nil {
		t.Fatal("attach is not part of the normalized contract")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestThePlatformProviderSpeaksTheShimContract(t *testing.T) {
	var auth, path string
	var forwarded ExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("authorization")
		path = r.URL.Path
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sbx-1"})
		case strings.HasSuffix(r.URL.Path, "/exec"):
			_ = json.NewDecoder(r.Body).Decode(&forwarded)
			_ = json.NewEncoder(w).Encode(ExecResult{ExitCode: 0, Stdout: "hello"})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{"state": StateReady})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer server.Close()

	provider := NewHTTPProvider(platformPreset(), platformLookup(server.URL+"/", " s3cret "))
	ctx := context.Background()

	avail, err := provider.Probe(ctx)
	if err != nil || !avail.Available {
		t.Fatalf("probe = %+v (%v)", avail, err)
	}
	if auth != "Bearer s3cret" {
		t.Fatalf("the token must travel as a bearer header and be trimmed, got %q", auth)
	}

	h, err := provider.Create(ctx, Spec{LeaseID: "lease-1", Profile: Profile{TimeoutMS: 5000}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.Ref != "sbx-1" || h.ID != "lease-1" || h.Provider != "e2b" {
		t.Fatalf("handle = %+v", h)
	}

	res, err := provider.Exec(ctx, h, ExecRequest{Command: "echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Provider != "e2b" || res.Ref != "sbx-1" || res.Stdout != "hello" {
		t.Fatalf("receipt = %+v", res)
	}
	if forwarded.Command != "echo" || len(forwarded.Args) != 1 {
		t.Fatalf("the request must reach the shim unchanged, got %+v", forwarded)
	}
	if path != "/sandboxes/sbx-1/exec" {
		t.Fatalf("exec path = %q", path)
	}

	state, err := provider.Status(ctx, h)
	if err != nil || state != StateReady {
		t.Fatalf("status = %q (%v)", state, err)
	}
	if err := provider.Destroy(ctx, h); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if path != "/sandboxes/sbx-1" {
		t.Fatalf("destroy path = %q", path)
	}
}

func TestPlatformPresetsAreDeclaredButNeverWiredUp(t *testing.T) {
	presets := PlatformPresets()
	if len(presets) == 0 {
		t.Fatal("the shipped platforms must be listed")
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		if seen[preset.Name] {
			t.Fatalf("%s is listed twice", preset.Name)
		}
		seen[preset.Name] = true
		if preset.EnvBaseURL == "" || preset.EnvToken == "" {
			t.Fatalf("%s must name the variables it reads", preset.Name)
		}
		if preset.Layer != LayerPlatform {
			t.Fatalf("%s: layer = %q", preset.Name, preset.Layer)
		}
		// Nothing is wired up until an operator sets the URL, so every preset must
		// appear in the catalogue as unavailable rather than missing.
		provider := NewHTTPProvider(preset, nil)
		if provider.Name() != preset.Name {
			t.Fatalf("name = %q, want %q", provider.Name(), preset.Name)
		}
		if avail, _ := provider.Probe(context.Background()); avail.Available {
			t.Fatalf("%s must not be available without configuration", preset.Name)
		}
	}
}

func TestThePlatformProviderSurfacesTransportFailures(t *testing.T) {
	ctx := context.Background()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	provider := NewHTTPProvider(platformPreset(), platformLookup(failing.URL, ""))
	if _, err := provider.Create(ctx, Spec{LeaseID: "l1"}); err == nil {
		t.Fatal("a shim that refuses to create must not invent a handle")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}

	// A base URL the client cannot build is a code, not a panic.
	malformed := NewHTTPProvider(platformPreset(), platformLookup("://nope", ""))
	if err := malformed.Destroy(ctx, Handle{Ref: "sbx-1"}); err == nil {
		t.Fatal("an unusable base URL must be reported")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
	if _, err := malformed.Exec(ctx, Handle{Ref: "sbx-1"}, ExecRequest{Command: "echo"}); err == nil {
		t.Fatal("exec on an unusable base URL must fail")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestThePlatformProviderNeverFollowsARedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/internal", http.StatusFound)
	}))
	defer server.Close()

	provider := NewHTTPProvider(platformPreset(), platformLookup(server.URL, ""))
	avail, _ := provider.Probe(context.Background())
	if avail.Available {
		t.Fatal("a shim that redirects is a misconfiguration, not an available backend")
	}
	if !strings.Contains(avail.Reason, "302") {
		t.Fatalf("the reason must name what came back, got %q", avail.Reason)
	}
}

func TestThePlatformProviderExplainsItsFailures(t *testing.T) {
	ctx := context.Background()

	broke := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broke.Close()
	provider := NewHTTPProvider(platformPreset(), platformLookup(broke.URL, ""))
	if avail, _ := provider.Probe(ctx); avail.Available || !strings.Contains(avail.Reason, "500") {
		t.Fatalf("a failing shim must report its status, got %+v", avail)
	}

	// A URL the HTTP client cannot even build is still a reason, not an error.
	malformed := NewHTTPProvider(platformPreset(), platformLookup("://nope", ""))
	if avail, _ := malformed.Probe(ctx); avail.Available || !strings.Contains(avail.Reason, "invalid") {
		t.Fatalf("a malformed base URL must be explained, got %+v", avail)
	}

	// Nothing listening: the catalogue says unreachable instead of hanging.
	dead := NewHTTPProvider(platformPreset(), platformLookup("http://127.0.0.1:1", ""))
	if avail, _ := dead.Probe(ctx); avail.Available || !strings.Contains(avail.Reason, "unreachable") {
		t.Fatalf("a dead shim must be reported, got %+v", avail)
	}
	if err := dead.Destroy(ctx, Handle{Ref: "sbx-1"}); err != nil {
		t.Fatalf("releasing on an unreachable shim is best effort: %v", err)
	}
	if _, err := dead.Exec(ctx, Handle{Ref: "sbx-1"}, ExecRequest{Command: "echo"}); err == nil {
		t.Fatal("exec on an unreachable shim must fail")
	} else if code := codeOf(t, err); code != CodeNodeOffline {
		t.Fatalf("expected %s, got %s", CodeNodeOffline, code)
	}
}

func TestThePlatformProviderMapsStatusCodesToCodes(t *testing.T) {
	ctx := context.Background()
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer notFound.Close()
	provider := NewHTTPProvider(platformPreset(), platformLookup(notFound.URL, ""))
	if _, err := provider.Exec(ctx, Handle{Ref: "gone"}, ExecRequest{Command: "echo"}); err == nil {
		t.Fatal("a missing sandbox must be reported")
	} else if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("expected %s, got %s", CodeNotFound, code)
	}
	if state, err := provider.Status(ctx, Handle{Ref: "gone"}); err != nil || state != StateFailed {
		t.Fatalf("an unknown sandbox must read failed, got %q (%v)", state, err)
	}

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer garbage.Close()
	unparsable := NewHTTPProvider(platformPreset(), platformLookup(garbage.URL, ""))
	if _, err := unparsable.Exec(ctx, Handle{Ref: "sbx-1"}, ExecRequest{Command: "echo"}); err == nil {
		t.Fatal("a shim that does not speak the contract must not be trusted")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestThePlatformCreateNeedsAnID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	provider := NewHTTPProvider(platformPreset(), platformLookup(server.URL, ""))
	if _, err := provider.Create(context.Background(), Spec{LeaseID: "l1"}); err == nil {
		t.Fatal("a sandbox without an id cannot be addressed later")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestThePlatformStatusDefaultsToReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	provider := NewHTTPProvider(platformPreset(), platformLookup(server.URL, ""))
	state, err := provider.Status(context.Background(), Handle{Ref: "sbx-1"})
	if err != nil || state != StateReady {
		t.Fatalf("an empty state means the shim has nothing to report: %q (%v)", state, err)
	}
}
