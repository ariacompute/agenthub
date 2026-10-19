package handlers

import (
	"net/http"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// The upstream registry half of the control plane.
//
// A synchronization is a control plane action with a data plane consequence — it
// writes records into the catalogue — so it is a permission of its own rather
// than a side effect of registering a resource by hand.

// sources returns the synchronization service, or nil when the instance runs
// without one. Every handler reports that as "not configured" instead of
// failing: an agenthub with no upstream registries is a supported deployment.
func (d Deps) sources() *registry.SourceService { return d.Sources }

func (d Deps) handleListSources(w http.ResponseWriter, r *http.Request) {
	if d.sources() == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"sources": d.sources().ListSources(r.URL.Query().Get("namespace"))})
}

func (d Deps) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	if d.sources() == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var src registry.RegistrySource
	if err := httputil.Decode(w, r, &src, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	stored, err := d.sources().UpsertSource(&src)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	d.Log.Info("handler: registry source created", "id", stored.ID, "kind", stored.Kind, "fixture", stored.Fixture)
	httputil.WriteJSON(w, http.StatusCreated, stored)
}

func (d Deps) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	if d.sources() == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	id := r.PathValue("id")
	var src registry.RegistrySource
	if err := httputil.Decode(w, r, &src, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	src.ID = id
	stored, err := d.sources().UpsertSource(&src)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, stored)
}

func (d Deps) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if d.sources() == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	if err := d.sources().DeleteSource(r.PathValue("id")); err != nil {
		writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncSource runs one source now.
//
// A failing upstream is reported as an error with the machine readable code, not
// as an empty success: a curator has to be able to tell "the registry has
// nothing new" apart from "the registry could not be reached".
func (d Deps) handleSyncSource(w http.ResponseWriter, r *http.Request) {
	if d.sources() == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	id := r.PathValue("id")
	res, err := d.sources().Sync(r.Context(), id)
	// A failed run is a gateway error with the upstream code on the wire, not a
	// 500: the failure belongs to the registry agenthub called, not to agenthub.
	if res.Status == registry.SyncStatusFailed {
		code := httputil.Code(res.Error)
		if code == "" {
			code = httputil.CodeInternal
		}
		httputil.WriteError(w, http.StatusBadGateway, code)
		return
	}
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}
