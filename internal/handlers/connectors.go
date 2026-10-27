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
		task, err := d.Hub.ParkConnector(r.Context(), id, req.Action)
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
