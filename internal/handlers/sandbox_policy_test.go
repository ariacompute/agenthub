package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/sandbox"
)

// The execution-policy gate is assembled in main from AGENTHUB_SANDBOX_*, which
// is exactly why it has to be observed through the write path: the closure is
// the operator's, but *whether a save goes through it* is the handler's. These
// cases pin both halves — an allowed profile is stored as written, a forbidden
// one is refused while the record is being written (not when a task first runs)
// and leaves nothing behind.

// policyDeps is the shared harness with the same gate main.go wires: host-path
// rules first, then the quota ceiling. No ceiling is set here, so the
// writable-root rule is the one that decides.
func policyDeps(t *testing.T) Deps {
	t.Helper()
	d := testDeps(t, openConfig(""))
	d.Reg = registry.New().WithSandboxPolicy(func(cfg *registry.SandboxConfig) error {
		profile := cfg.Profile()
		if err := sandbox.ValidateHostPolicy(profile, sandbox.HostPolicy{}); err != nil {
			return err
		}
		return sandbox.Limits{}.Exceeds(profile)
	})
	return d
}

func policyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(Register(policyDeps(t)))
	t.Cleanup(srv.Close)
	return srv
}

// policyCLI builds a create body with a sandbox block. `extra` is appended
// inside the block so the two cases differ by exactly one field.
func policyCLI(extra string) string {
	return fmt.Sprintf(`{"kind":"cli","name":"gate-probe","kind_config":{"cli":{"command":"echo"},"sandbox":{"timeout_ms":5000%s}}}`, extra)
}

func TestCreateResourceStoresAnAllowedSandboxProfile(t *testing.T) {
	srv := policyServer(t)
	res, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources", policyCLI(""))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %v", res.StatusCode, body)
	}
	// The block the caller wrote is the block that was stored: a gate that
	// silently rewrites a profile would be worse than one that refuses it.
	cfg, _ := body["kind_config"].(map[string]any)
	box, _ := cfg["sandbox"].(map[string]any)
	if got := box["timeout_ms"]; got != float64(5000) {
		t.Fatalf("stored sandbox = %v, want the profile as written", cfg)
	}
}

func TestCreateResourceRefusesAProfileTheInstanceForbids(t *testing.T) {
	srv := policyServer(t)
	// A writable root with no AGENTHUB_SANDBOX_ALLOW_WRITABLE_ROOT: the same
	// profile sandbox.ValidateHostPolicy refuses at lease time.
	res, body := doJSON(t, http.MethodPost, srv.URL+"/api/resources", policyCLI(`,"readonly_root":false`))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, body = %v, want 403 policy_denied", res.StatusCode, body)
	}
	if code, _ := body["code"].(string); code != string(httputil.CodeSandboxPolicyDenied) {
		t.Fatalf("code = %v, want %s", body["code"], httputil.CodeSandboxPolicyDenied)
	}

	// And nothing was written: a refused save must not leave a record that the
	// next lease would have to reject again.
	_, list := doJSON(t, http.MethodGet, srv.URL+"/api/resources?kind=cli", "")
	if entries, _ := list["resources"].([]any); len(entries) != 0 {
		t.Fatalf("resources = %v, want none after a refused create", entries)
	}
}
