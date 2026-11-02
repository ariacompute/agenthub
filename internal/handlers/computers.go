package handlers

import (
	"net/http"
	"time"

	"agenthub/internal/computers"
	"agenthub/internal/httputil"
	"agenthub/internal/sandbox"
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
	// Provider overrides the instance default for this one computer, so an
	// operator can give a shell computer microVM isolation while the rest of the
	// instance runs plain containers.
	Provider string `json:"provider"`
	// Scope overrides the lease reuse key. Empty means "one environment per
	// computer", which is what the console's cards represent.
	Scope string `json:"scope"`
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
	c, err := d.Computers.Get(r.Context(), r.PathValue("id"), callerID(r))
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

// handleCreateComputer records the card and leases the environment behind it. The
// two happen together on purpose: a computer row with no lease would be a console
// card that cannot run anything, which is exactly the placeholder this change
// removes. If the environment cannot be created the row is rolled back and the
// coded error is returned — no silent degradation to "mock".
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
	ownerID := PrincipalFrom(r).UserID
	c := &computers.Computer{
		OwnerID:    ownerID,
		AgentID:    req.AgentID,
		Kind:       kind,
		SessionRef: req.SessionRef,
		Status:     status,
		LastAction: req.LastAction,
	}
	created, err := d.Computers.Create(r.Context(), c)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if d.Sandboxes == nil {
		// No control plane: the row is the whole feature, as it was before.
		httputil.WriteJSON(w, http.StatusCreated, created)
		return
	}

	scope := req.Scope
	if scope == "" {
		scope = "computer:" + created.ID
	}
	lease, err := d.Sandboxes.Acquire(r.Context(), sandbox.LeaseRequest{
		OwnerID:    ownerID,
		ResourceID: created.AgentID,
		Scope:      scope,
		Provider:   req.Provider,
	})
	if err != nil {
		// Roll back: leaving a card behind would tell the operator an environment
		// exists when the lease was refused (no docker socket, limit reached...).
		if derr := d.Computers.Delete(r.Context(), created.ID, ownerID); derr != nil && d.Log != nil {
			d.Log.Warn("computers: rollback failed", "computer", created.ID, "error", derr)
		}
		httputil.WriteErr(w, err)
		return
	}
	created.Provider = lease.Provider
	created.LeaseID = lease.ID
	created.NodeID = lease.NodeID
	created.Status = lease.State
	if !lease.ExpiresAt.IsZero() {
		created.ExpiresAt = lease.ExpiresAt.UTC().Format(time.RFC3339)
	}
	updated, err := d.Computers.Update(r.Context(), created, ownerID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, updated)
}

// handleDeleteComputer terminates the environment and drops the card. The lease is
// terminated first so the container is not left running behind a row nobody can
// see; a lease that is already gone is not an error, because the sweep may have
// reclaimed it.
func (d Deps) handleDeleteComputer(w http.ResponseWriter, r *http.Request) {
	if !d.computersReady(w) {
		return
	}
	c, err := d.Computers.Get(r.Context(), r.PathValue("id"), callerID(r))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if c == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeComputerNotFound)
		return
	}
	if c.LeaseID != "" && d.Sandboxes != nil {
		if terr := d.Sandboxes.Terminate(r.Context(), c.LeaseID); terr != nil && d.Log != nil {
			d.Log.Warn("computers: lease termination failed", "computer", c.ID, "lease", c.LeaseID, "error", terr)
		}
	}
	if err := d.Computers.Delete(r.Context(), c.ID, callerID(r)); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
