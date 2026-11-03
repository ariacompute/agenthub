package handlers

import (
	"net/http"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// connectorView is the console-facing projection of a kind=mcp resource: its
// endpoint plus the connector policy that gates external invocation.
type connectorView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint"`
	Transport string `json:"transport"`
	Policy    string `json:"policy"`
	Health    string `json:"health"`
}

func policyOf(e *registry.Entry) string {
	if e.KindConfig == nil || e.KindConfig.ConnectorPolicy == "" {
		return registry.ConnectorPolicyOff
	}
	return e.KindConfig.ConnectorPolicy
}

func toConnectorView(e *registry.Entry) connectorView {
	return connectorView{
		ID:        e.ID,
		Name:      e.Name,
		Endpoint:  string(e.Endpoint),
		Transport: string(e.Transport),
		Health:    string(e.Health),
		Policy:    policyOf(e),
	}
}

func (d Deps) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	entries := d.Reg.List(string(registry.KindMCP), "")
	if entries == nil {
		httputil.WriteJSON(w, http.StatusOK, []connectorView{})
		return
	}
	out := make([]connectorView, 0, len(entries))
	for _, e := range entries {
		out = append(out, toConnectorView(e))
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

type connectorPolicyRequest struct {
	Policy string `json:"policy"`
}

func (d Deps) handlePutConnectorPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, ok := d.Reg.Get(id)
	if !ok || e.Kind == "" {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeConnectorNotFound)
		return
	}
	if e.Kind != registry.KindMCP {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeConnectorDenied)
		return
	}
	var req connectorPolicyRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	policy := registry.NormalizeConnectorPolicy(req.Policy)
	updated := *e
	if updated.KindConfig == nil {
		updated.KindConfig = &registry.KindConfig{}
	}
	updated.KindConfig.ConnectorPolicy = policy
	if _, err := d.Reg.Upsert(&updated); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toConnectorView(&updated))
}

type connectorActionRequest struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args"`
}

// defaultConnectorActions is the static action set a connector exposes in the first
// release. A real provider would enumerate these from its capability document.
func defaultConnectorActions() []map[string]any {
	return []map[string]any{
		{"name": "invoke", "description": "Invoke the connector with the given arguments."},
	}
}

func (d Deps) handleListConnectorActions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, ok := d.Reg.Get(id)
	if !ok || e.Kind != registry.KindMCP {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeConnectorNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"policy":  policyOf(e),
		"actions": defaultConnectorActions(),
	})
}

// handleConnectorAction invokes a connector as an external application. The policy
// gates the call: Off refuses, Allowed runs immediately, Ask first parks an
// input-required task that the console approves (HITL) before the run.
func (d Deps) handleConnectorAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	e, ok := d.Reg.Get(id)
	if !ok || e.Kind != registry.KindMCP {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeConnectorNotFound)
		return
	}
	var req connectorActionRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if req.Action == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeChannelPayloadInvalid)
		return
	}
	switch policyOf(e) {
	case registry.ConnectorPolicyOff:
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeConnectorPolicyOff)
		return
	case registry.ConnectorPolicyAskFirst:
		task, err := d.Hub.ParkConnector(r.Context(), id, req.Action, req.Args)
		if err != nil {
			httputil.WriteErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusAccepted, task)
		return
	default: // allowed
		params := a2a.MessageSendParams{
			Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart(req.Action)),
		}
		params.Message.Metadata = map[string]any{
			"resourceId":      id,
			"connectorAction": req.Action,
		}
		if len(req.Args) > 0 {
			params.Message.Metadata["args"] = req.Args
		}
		task, err := d.Hub.Send(r.Context(), params)
		if err != nil {
			httputil.WriteErr(w, err)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, task)
	}
}

// connectorApprovalMarker is the metadata key that turns a parked task into an
// approval request. It is written by hub.ParkConnector and never by a caller.
const connectorApprovalMarker = "connectorApproval"

// connectorApprovalView is one row of the queue: what was asked for, by whom, and
// which task holds the park.
type connectorApprovalView struct {
	TaskID        string         `json:"taskId"`
	ConnectorID   string         `json:"connectorId"`
	ConnectorName string         `json:"connectorName,omitempty"`
	Action        string         `json:"action"`
	Args          map[string]any `json:"args,omitempty"`
	RequestedBy   string         `json:"requestedBy,omitempty"`
	// Asked is the prompt the task is parked on, so the queue shows the same
	// sentence the HITL card shows.
	Asked     string `json:"asked,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
}

// connectorApprovalOf reads the request off a parked task.
//
// The marker lives on the task's first message rather than on a table of its
// own: the approval is the parked A2A task, and a second table would be a second
// truth to keep in step with the state machine.
func connectorApprovalOf(task *a2a.Task) (map[string]any, bool) {
	for i := range task.History {
		meta := task.History[i].Metadata
		if meta == nil {
			continue
		}
		if marked, _ := meta[connectorApprovalMarker].(bool); marked {
			return meta, true
		}
	}
	return nil, false
}

func (d Deps) approvalView(task *a2a.Task, meta map[string]any) connectorApprovalView {
	view := connectorApprovalView{TaskID: task.ID, CreatedAt: string(task.Status.Timestamp)}
	if id, ok := meta["resourceId"].(string); ok {
		view.ConnectorID = id
		if entry, found := d.Reg.Get(id); found {
			view.ConnectorName = entry.Name
		}
	}
	if action, ok := meta["connectorAction"].(string); ok {
		view.Action = action
	}
	if args, ok := meta["args"].(map[string]any); ok {
		view.Args = args
	}
	if who, ok := meta["requestedBy"].(string); ok {
		view.RequestedBy = who
	}
	if task.Status.Message != nil {
		view.Asked = task.Status.Message.Text()
	}
	return view
}

// handleListConnectorApprovals is the queue the console's approval panel reads.
//
// It derives from the parked tasks rather than from a table of its own: "pending
// approval" already has exactly one representation — a task in input-required
// whose request carries the connector marker — and a second one would drift.
func (d Deps) handleListConnectorApprovals(w http.ResponseWriter, r *http.Request) {
	if d.Hub == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	tasks := d.Hub.ListTasksInState(r.Context(), a2a.TaskStateInputRequired)
	out := make([]connectorApprovalView, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		meta, ok := connectorApprovalOf(task)
		if !ok {
			continue
		}
		out = append(out, d.approvalView(task, meta))
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// handleApproveConnectorAction resumes the parked task, which runs the action
// once. The answer repeats the action and its arguments so the resumed flow —
// and the audit trail — carry the same request the approver read.
func (d Deps) handleApproveConnectorAction(w http.ResponseWriter, r *http.Request) {
	d.decideConnectorApproval(w, r, true)
}

// handleRejectConnectorAction cancels the parked task. Nothing is executed: a
// refusal must not leave a half-run side effect behind.
func (d Deps) handleRejectConnectorAction(w http.ResponseWriter, r *http.Request) {
	d.decideConnectorApproval(w, r, false)
}

func (d Deps) decideConnectorApproval(w http.ResponseWriter, r *http.Request, approve bool) {
	if d.Hub == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	id := r.PathValue("id")
	// Nil history length: the marker lives on the first message, so the whole
	// history is needed to read the request back.
	task, err := d.Hub.Get(r.Context(), id, nil)
	if err != nil || task == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeConnectorApprovalNotFound)
		return
	}
	meta, ok := connectorApprovalOf(task)
	if !ok {
		// A task that is not an approval request is reported as absent: saying
		// "that task exists but is not an approval" would be an id oracle.
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeConnectorApprovalNotFound)
		return
	}
	if task.Status.State != a2a.TaskStateInputRequired {
		// Somebody already approved, rejected or the task expired.
		httputil.WriteError(w, http.StatusConflict, httputil.CodeConnectorApprovalClosed)
		return
	}
	if !approve {
		updated, cerr := d.Hub.Cancel(r.Context(), id)
		if cerr != nil {
			httputil.WriteErr(w, cerr)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, updated)
		return
	}
	answer := a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("approved"))
	answer.Metadata = map[string]any{
		"approved":        true,
		"connectorAction": meta["connectorAction"],
	}
	if args, has := meta["args"]; has {
		answer.Metadata["args"] = args
	}
	updated, rerr := d.Hub.Resume(r.Context(), a2a.TaskResumeParams{ID: id, Message: answer})
	if rerr != nil {
		httputil.WriteErr(w, rerr)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, updated)
}
