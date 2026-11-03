package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// parkApproval switches the connector to ask_first and parks one action, which
// is the only way a task can enter the queue: the marker is written by the hub.
func parkApproval(t *testing.T, srv *httptest.Server, mcpID string) string {
	t.Helper()
	if code, _ := doReq(t, srv, http.MethodPut, "/api/connectors/"+mcpID+"/policy", `{"policy":"ask_first"}`); code != http.StatusOK {
		t.Fatalf("put policy = %d", code)
	}
	code, raw := doReq(t, srv, http.MethodPost, "/api/connectors/"+mcpID+"/actions", `{"action":"invoke","args":{"path":"/tmp/a.txt"}}`)
	if code != http.StatusAccepted {
		t.Fatalf("park = %d (%s), want 202", code, raw)
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("decode parked task: %v", err)
	}
	if task.ID == "" {
		t.Fatal("the parked task has no id")
	}
	return task.ID
}

func approvalCode(t *testing.T, raw []byte) httputil.Code {
	t.Helper()
	var body struct {
		Code httputil.Code `json:"code"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body %q: %v", raw, err)
	}
	return body.Code
}

func TestConnectorApprovalQueueShowsWhatWasAskedFor(t *testing.T) {
	d, reg := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()
	mcp := reg.List(string(registry.KindMCP), "")[0]

	// An empty queue is an empty array, not a missing field: the console renders
	// "nothing pending" from length.
	code, raw := doReq(t, srv, http.MethodGet, "/api/connectors/approvals", "")
	if code != http.StatusOK {
		t.Fatalf("empty queue = %d", code)
	}
	var empty []connectorApprovalView
	if err := json.Unmarshal(raw, &empty); err != nil {
		t.Fatalf("decode empty queue: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("queue = %+v, want empty", empty)
	}

	parked := parkApproval(t, srv, mcp.ID)

	code, raw = doReq(t, srv, http.MethodGet, "/api/connectors/approvals", "")
	if code != http.StatusOK {
		t.Fatalf("queue = %d", code)
	}
	var queue []connectorApprovalView
	if err := json.Unmarshal(raw, &queue); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("queue = %+v, want one row", queue)
	}
	row := queue[0]
	if row.TaskID != parked {
		t.Fatalf("taskId = %q, want %q", row.TaskID, parked)
	}
	if row.ConnectorID != mcp.ID || row.ConnectorName != mcp.Name {
		t.Fatalf("connector = %q/%q, want %q/%q", row.ConnectorID, row.ConnectorName, mcp.ID, mcp.Name)
	}
	if row.Action != "invoke" {
		t.Fatalf("action = %q, want invoke", row.Action)
	}
	// The approver has to read the arguments before agreeing to them.
	if row.Args["path"] != "/tmp/a.txt" {
		t.Fatalf("args = %+v, want the parked arguments", row.Args)
	}
	if row.Asked == "" {
		t.Fatal("the queue row carries no prompt")
	}
	if row.CreatedAt == "" {
		t.Fatal("the queue row carries no timestamp")
	}
}

func TestApprovingAParkedActionRunsItOnce(t *testing.T) {
	d, reg := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()
	mcp := reg.List(string(registry.KindMCP), "")[0]
	parked := parkApproval(t, srv, mcp.ID)

	code, raw := doReq(t, srv, http.MethodPost, "/api/connectors/approvals/"+parked+"/approve", "")
	if code != http.StatusOK {
		t.Fatalf("approve = %d (%s)", code, raw)
	}
	var task struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
		History []struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"history"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("decode resumed task: %v", err)
	}
	if task.Status.State != "completed" {
		t.Fatalf("state = %q, want completed", task.Status.State)
	}
	// The answer carries the action back into the run, so the resumed flow is the
	// same request the approver read.
	found := false
	for _, msg := range task.History {
		if msg.Metadata == nil {
			continue
		}
		if action, ok := msg.Metadata["connectorAction"].(string); ok && action == "invoke" {
			found = true
		}
	}
	if !found {
		t.Fatalf("history = %+v, want the action echoed back on resume", task.History)
	}

	// The park is consumed: the queue is empty and a second approve is a conflict,
	// not a second run.
	_, raw = doReq(t, srv, http.MethodGet, "/api/connectors/approvals", "")
	var queue []connectorApprovalView
	if err := json.Unmarshal(raw, &queue); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue = %+v, want empty after approval", queue)
	}
	code, raw = doReq(t, srv, http.MethodPost, "/api/connectors/approvals/"+parked+"/approve", "")
	if code != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409", code)
	}
	if got := approvalCode(t, raw); got != httputil.CodeConnectorApprovalClosed {
		t.Fatalf("code = %s, want connector_approval_closed", got)
	}
}

func TestRejectingAParkedActionRunsNothing(t *testing.T) {
	d, reg := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()
	mcp := reg.List(string(registry.KindMCP), "")[0]
	parked := parkApproval(t, srv, mcp.ID)

	code, raw := doReq(t, srv, http.MethodPost, "/api/connectors/approvals/"+parked+"/reject", "")
	if code != http.StatusOK {
		t.Fatalf("reject = %d (%s)", code, raw)
	}
	var task struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("decode canceled task: %v", err)
	}
	if task.Status.State != "canceled" {
		t.Fatalf("state = %q, want canceled", task.Status.State)
	}

	// A closed approval cannot be approved afterwards.
	code, raw = doReq(t, srv, http.MethodPost, "/api/connectors/approvals/"+parked+"/approve", "")
	if code != http.StatusConflict {
		t.Fatalf("approve after reject = %d, want 409", code)
	}
	if got := approvalCode(t, raw); got != httputil.CodeConnectorApprovalClosed {
		t.Fatalf("code = %s, want connector_approval_closed", got)
	}

	_, raw = doReq(t, srv, http.MethodGet, "/api/connectors/approvals", "")
	var queue []connectorApprovalView
	if err := json.Unmarshal(raw, &queue); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue = %+v, want empty after rejection", queue)
	}
}

func TestApprovalDecisionsRefuseUnknownAndUnrelatedTasks(t *testing.T) {
	d, reg := newDeps(t)
	srv := httptest.NewServer(Register(d))
	defer srv.Close()
	mcp := reg.List(string(registry.KindMCP), "")[0]

	for _, suffix := range []string{"/approve", "/reject"} {
		path := "/api/connectors/approvals/missing-task" + suffix
		code, raw := doReq(t, srv, http.MethodPost, path, "")
		if code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, code)
		}
		if got := approvalCode(t, raw); got != httputil.CodeConnectorApprovalNotFound {
			t.Fatalf("code = %s, want connector_approval_not_found", got)
		}
	}

	// A completed connector run is not an approval request: reporting it as
	// absent keeps the endpoint from confirming which task ids exist.
	if code, _ := doReq(t, srv, http.MethodPut, "/api/connectors/"+mcp.ID+"/policy", `{"policy":"allowed"}`); code != http.StatusOK {
		t.Fatalf("put policy = %d", code)
	}
	code, raw := doReq(t, srv, http.MethodPost, "/api/connectors/"+mcp.ID+"/actions", `{"action":"invoke"}`)
	if code != http.StatusOK {
		t.Fatalf("allowed action = %d", code)
	}
	var ran struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &ran); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	code, raw = doReq(t, srv, http.MethodPost, "/api/connectors/approvals/"+ran.ID+"/approve", "")
	if code != http.StatusNotFound {
		t.Fatalf("approve of a non-approval task = %d, want 404", code)
	}
	if got := approvalCode(t, raw); got != httputil.CodeConnectorApprovalNotFound {
		t.Fatalf("code = %s, want connector_approval_not_found", got)
	}
}

func TestApprovalQueueNeedsAHub(t *testing.T) {
	d, _ := newDeps(t)
	d.Hub = nil
	h := Register(d)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/connectors/approvals"},
		{http.MethodPost, "/api/connectors/approvals/task-1/approve"},
		{http.MethodPost, "/api/connectors/approvals/task-1/reject"},
	} {
		w := do(t, h, c.method, c.path, "", nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503", c.method, c.path, w.Code)
		}
	}
}
