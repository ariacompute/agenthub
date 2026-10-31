package handlers

import (
	"net/http"

	"agenthub/internal/httputil"
)

// handleListKnowledge is the mock RAG retrieval: it returns stored chunks for an
// agent. A real implementation would embed the query and rank by similarity.
func (d Deps) handleListKnowledge(w http.ResponseWriter, r *http.Request) {
	if d.Knowledge == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	agentID := r.URL.Query().Get("agentId")
	chunks, err := d.Knowledge.List(r.Context(), agentID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, chunks)
}
