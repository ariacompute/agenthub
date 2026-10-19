package handlers

import (
	"net/http"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// The governance half of the control plane: namespaces and the curation queue.
//
// Everything here is about *who may find a record*, never about how a record is
// executed, so it lives beside the resource CRUD without touching the A2A path.

func (d Deps) handleListNamespaces(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"registries": d.Reg.ListNamespaces()})
}

func (d Deps) handleCreateNamespace(w http.ResponseWriter, r *http.Request) {
	var ns registry.Namespace
	if err := httputil.Decode(w, r, &ns, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	stored, err := d.Reg.UpsertNamespace(&ns)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	d.Log.Info("handler: namespace created", "id", stored.ID, "auto_approve", stored.AutoApprove)
	httputil.WriteJSON(w, http.StatusCreated, stored)
}

func (d Deps) handleUpdateNamespace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := d.Reg.GetNamespace(id); !ok {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var ns registry.Namespace
	if err := httputil.Decode(w, r, &ns, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	ns.ID = id
	stored, err := d.Reg.UpsertNamespace(&ns)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, stored)
}

func (d Deps) handleDeleteNamespace(w http.ResponseWriter, r *http.Request) {
	if err := d.Reg.DeleteNamespace(r.PathValue("id")); err != nil {
		writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListApprovals is the curator's queue: every record waiting for a
// decision, newest namespace first so a curator can work through one catalogue
// at a time.
func (d Deps) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	pending := d.Reg.ListFiltered(registry.Filter{Status: string(registry.StatusPending)})
	visible := d.visibleEntries(r, pending)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"records": visible, "total": len(visible)})
}

// handleSetResourceStatus applies one curated transition.
//
// The state machine lives in the registry, so this handler only carries the
// intent: which record, which state, and why. An illegal transition is a bad
// request rather than a silent no-op, because a curator has to know that their
// click did not take effect.
func (d Deps) handleSetResourceStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := d.Reg.Get(id); !ok || !d.canAccess(r, id, true) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var body struct {
		Status string `json:"status"`
		Reason string `json:"reason,omitempty"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	to, err := registry.ParseStatus(body.Status)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	stored, err := d.Reg.SetStatus(id, to)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	// The reason is operator text about a record, not user content: it is
	// logged without the body of the record itself.
	d.Log.Info("handler: record status changed", "id", id, "status", stored.Status, "has_reason", body.Reason != "")
	httputil.WriteJSON(w, http.StatusOK, stored)
}
