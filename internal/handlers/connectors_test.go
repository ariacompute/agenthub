package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

func TestListConnectors(t *testing.T) {
	d, _ := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	code, raw := doReq(t, srv, "GET", "/api/connectors", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var list []connectorView
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	// A freshly registered mcp resource defaults to the off policy.
	if list[0].Policy != registry.ConnectorPolicyOff {
		t.Fatalf("policy = %q, want off", list[0].Policy)
	}
}

func TestConnectorPolicyUpdateAndActionGate(t *testing.T) {
	d, reg := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()
	mcp := reg.List(string(registry.KindMCP), "")[0]

	// Default policy is off, so an action is denied with 403.
	code, _ := doReq(t, srv, "POST", "/api/connectors/"+mcp.ID+"/actions", `{"action":"invoke"}`)
	if code != http.StatusForbidden {
		t.Fatalf("off-policy action = %d, want 403", code)
	}

	// Switch to ask-first, which parks a task instead of running it.
	code, _ = doReq(t, srv, "PUT", "/api/connectors/"+mcp.ID+"/policy", `{"policy":"ask_first"}`)
	if code != http.StatusOK {
		t.Fatalf("put policy = %d", code)
	}
	code, raw := doReq(t, srv, "POST", "/api/connectors/"+mcp.ID+"/actions", `{"action":"invoke"}`)
	if code != http.StatusAccepted {
		t.Fatalf("ask-first action = %d, want 202", code)
	}
	var task struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task.Status.State != "input-required" {
		t.Fatalf("state = %q, want input-required", task.Status.State)
	}

	// The actions view reflects the updated policy.
	code, raw = doReq(t, srv, "GET", "/api/connectors/"+mcp.ID+"/actions", "")
	if code != http.StatusOK {
		t.Fatalf("actions = %d", code)
	}
	var actions struct {
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal(raw, &actions); err != nil {
		t.Fatal(err)
	}
	if actions.Policy != registry.ConnectorPolicyAskFirst {
		t.Fatalf("policy = %q, want ask_first", actions.Policy)
	}
}

func TestConnectorActionUnknownResource(t *testing.T) {
	d, _ := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	code, _ := doReq(t, srv, "PUT", "/api/connectors/missing/policy", `{"policy":"allowed"}`)
	if code != http.StatusNotFound {
		t.Fatalf("put unknown = %d, want 404", code)
	}
	code, _ = doReq(t, srv, "POST", "/api/connectors/missing/actions", `{"action":"invoke"}`)
	if code != http.StatusNotFound {
		t.Fatalf("action unknown = %d, want 404", code)
	}
}

func TestConnectorPolicyRejectedForNonMCP(t *testing.T) {
	d, reg := newDeps(t)
	other, err := reg.Upsert(&registry.Entry{
		Kind:     registry.KindAgent,
		Name:     "agent1",
		Endpoint: "http://agent1",
		Health:   registry.HealthHealthy,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	code, raw := doReq(t, srv, "PUT", "/api/connectors/"+other.ID+"/policy", `{"policy":"allowed"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("non-mcp policy = %d, want 400", code)
	}
	if httputil.AsError(decodeErr(raw)).Code != httputil.CodeConnectorDenied {
		t.Fatalf("code = %q", httputil.AsError(decodeErr(raw)).Code)
	}
}
