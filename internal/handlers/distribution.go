package handlers

import (
	"net/http"
	"strconv"

	"agenthub/internal/a2a"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
)

// distributionType marks the data part of a distribution task. It is how a
// reader tells "an operator pushed abilities" from "somebody had a
// conversation": the task is otherwise an ordinary A2A message and would be
// indistinguishable in the trace.
const distributionType = "agenthub.distribution"

// distributionOf renders the ability package for one agent, narrowed to the
// records this caller may see.
//
// The visibility filter is the same one the listing and the exports use, so a
// package can never hand an agent a record its operator cannot see: an
// unreachable grant appears in "missing", never as content.
func (d Deps) distributionOf(r *http.Request, agent *registry.Entry) (*registry.Distribution, error) {
	return registry.DistributionOf(d.Reg, agent, func(entries []*registry.Entry) []*registry.Entry {
		return d.visibleEntries(r, entries)
	})
}

// loadAgent resolves the path id to an agent resource the caller may act on.
//
// A record that is not an agent is a bad request rather than a 404: the caller
// named a real record, it just cannot carry bindings, and answering "not found"
// would send an operator hunting for a typo that is not there.
func (d Deps) loadAgent(w http.ResponseWriter, r *http.Request, write bool) (*registry.Entry, bool) {
	id := r.PathValue("id")
	entry, ok := d.Reg.Get(id)
	if !ok || !d.canAccess(r, id, write) {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return nil, false
	}
	if entry.Kind != registry.KindAgent {
		writeDomainErr(w, &httputil.Error{Code: registry.CodeInvalidKind})
		return nil, false
	}
	return entry, true
}

// handleGetResourceDistribution answers "what would this agent receive?".
//
// It is the pull half of the distribution: an agent runtime can fetch the same
// package over HTTP at start-up instead of reading it out of a message.
func (d Deps) handleGetResourceDistribution(w http.ResponseWriter, r *http.Request) {
	agent, ok := d.loadAgent(w, r, false)
	if !ok {
		return
	}
	dist, err := d.distributionOf(r, agent)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, dist)
}

// handlePushResourceDistribution delivers the package to the agent as a task.
//
// The task is the record: it lands in the trace, carries the data part the agent
// read, and can be retried or inspected with the ordinary task endpoints. An
// empty package is allowed on purpose — it is how an operator tells an agent its
// abilities were revoked, which is a state the agent has to learn somehow.
func (d Deps) handlePushResourceDistribution(w http.ResponseWriter, r *http.Request) {
	if d.Hub == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	agent, ok := d.loadAgent(w, r, true)
	if !ok {
		return
	}
	dist, err := d.distributionOf(r, agent)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	// The data part carries the Distribution itself rather than a second
	// hand-written mapping: the pushed copy is then by construction the shape the
	// pull endpoint serves, and a field added to the type cannot quietly go
	// missing from the task.
	msg := a2a.NewMessage(a2a.RoleUser,
		a2a.NewTextPart("agenthub capability distribution: agent="+agent.Name+
			" mcp="+strconv.Itoa(len(dist.MCP))+" skills="+strconv.Itoa(len(dist.Skills))),
		a2a.NewDataPart(map[string]any{"type": distributionType, "distribution": dist}))
	// The selector goes on the message so the hub runs this agent, not whichever
	// agent it would otherwise pick for an unaddressed message.
	msg.Metadata = map[string]any{"resourceId": agent.ID, "distribution": true}
	task, err := d.Hub.Send(r.Context(), a2a.MessageSendParams{Message: msg})
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if d.Log != nil {
		// Counts only: the package itself carries documentation bodies, and a log
		// line is the wrong place for somebody's skill manual.
		d.Log.Info("handler: distribution pushed",
			"agent", agent.ID, "mcp", len(dist.MCP), "skills", len(dist.Skills),
			"unresolved", len(dist.Unresolved), "missing", len(dist.Missing))
	}
	httputil.WriteJSON(w, http.StatusOK, task)
}
