package handlers

import (
	"net/http"
	"sort"

	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// handleWorkspace is agentbuddy's aggregated landing view: the agents the caller
// may see, plus recent task activity across the hub. It introduces no new query
// surface of its own — it aggregates data already served by /api/resources and
// the hub's in-memory task store, so it stays cheap and consistent.
func (d Deps) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, httputil.CodeMethodNotAllowed)
		return
	}
	agentKind, _ := registry.ParseKind("agent")
	seen := d.visibleEntries(r, d.Reg.List(string(agentKind), ""))
	agents := make([]*registry.Entry, 0, len(seen))
	for _, e := range seen {
		if e.Kind == agentKind {
			agents = append(agents, e)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })

	// TaskStore.List already returns newest first; cap to the eight most recent.
	tasks := d.Hub.Store().List()
	if len(tasks) > 8 {
		tasks = tasks[:8]
	}
	activity := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		activity = append(activity, map[string]any{
			"id":         t.ID,
			"resourceId": t.ResourceID,
			"state":      t.Status.State,
			"updatedAt":  t.Status.Timestamp,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"agents":   agents,
		"activity": activity,
	})
}
