package handlers

import (
	"net/http"

	"agenthub/internal/computers"
	"agenthub/internal/httputil"
)

// computersReady short-circuits computer endpoints when the store is absent (the
// feature is not wired in), reporting it as unavailable rather than 404.
func (d Deps) computersReady(w http.ResponseWriter) bool {
	if d.Computers == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return false
	}
	return true
}

type computerRequest struct {
	AgentID    string `json:"agentId"`
	Kind       string `json:"kind"`
	SessionRef string `json:"sessionRef"`
	Status     string `json:"status"`
	LastAction string `json:"lastAction"`
}

func (d Deps) handleListComputers(w http.ResponseWriter, r *http.Request) {
	if !d.computersReady(w) {
		return
	}
	list, err := d.Computers.List(r.Context(), PrincipalFrom(r).UserID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, list)
}

func (d Deps) handleGetComputer(w http.ResponseWriter, r *http.Request) {
	if !d.computersReady(w) {
		return
	}
	c, err := d.Computers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if c == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeComputerNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, c)
}

func (d Deps) handleCreateComputer(w http.ResponseWriter, r *http.Request) {
	if !d.computersReady(w) {
		return
	}
	var req computerRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	kind := computers.NormalizeKind(req.Kind)
	if kind == "" {
		kind = computers.KindBrowser
	}
	status := req.Status
	if status == "" {
		status = "idle"
	}
	c := &computers.Computer{
		OwnerID:    PrincipalFrom(r).UserID,
		AgentID:    req.AgentID,
		Kind:       kind,
		SessionRef: req.SessionRef,
		Status:     status,
		LastAction: req.LastAction,
	}
	// TODO(real): attach a real sandboxed driver (browser / shell / screen) here
	// instead of recording a placeholder session. The first release is mock only.
	created, err := d.Computers.Create(r.Context(), c)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, created)
}
