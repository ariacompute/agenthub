package handlers

import (
	"net/http"

	"agenthub/internal/httputil"
	"agenthub/internal/identity"
	"agenthub/internal/knowledge"
)

// handleListKnowledge is the mock RAG retrieval: it returns stored chunks for an
// agent. A real implementation would embed the query and rank by similarity.
//
// The owner dimension is mandatory: knowing an agent id is not a licence to read
// everybody's retrieval corpus, which is what filtering by agent alone allowed.
// The platform owner and an administrator (identity without an owner scope) keep
// the cross-account view.
func (d Deps) handleListKnowledge(w http.ResponseWriter, r *http.Request) {
	if d.Knowledge == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	chunks, err := d.Knowledge.List(r.Context(), knowledge.Query{
		AgentID: r.URL.Query().Get("agentId"),
		OwnerID: identity.Owner(r.Context()),
	})
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, chunks)
}
