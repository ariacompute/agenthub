package handlers

import (
	"net/http"
	"strings"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/sandbox"
)

// The sandbox control plane. The lease is the unit an operator looks at: which
// environment exists, who owns it, when it expires and what ran inside it. Every
// endpoint hangs off resources:read / resources:write, the same pair the
// computers surface uses, because a sandbox is where a resource runs.

// sandboxesReady short-circuits the endpoints when the control plane is not
// wired in, for the same reason routerReady exists: an instance without the
// feature is a supported configuration, and 503 says "not here" while 404 would
// suggest "wrong URL".
func (d Deps) sandboxesReady(w http.ResponseWriter) bool {
	if d.Sandboxes == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return false
	}
	return true
}

// sandboxLease resolves the {id} in the path and enforces ownership: a lease
// belongs to the identity that created it, and an anonymous caller (no auth in
// front of the process) sees everything because there is nobody to scope it to.
func (d Deps) sandboxLease(w http.ResponseWriter, r *http.Request) (*sandbox.Lease, bool) {
	lease, err := d.Sandboxes.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return nil, false
	}
	caller := PrincipalFrom(r).UserID
	if caller != "" && lease.OwnerID != "" && lease.OwnerID != caller {
		// Deliberately 404, not 403: whether somebody else's lease exists is not
		// information this caller needs.
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeSandboxNotFound)
		return nil, false
	}
	return lease, true
}

// handleListSandboxes returns the visible leases plus the resolved instance
// defaults, so the console can show "what a new environment will look like"
// without guessing.
func (d Deps) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	leases, err := d.Sandboxes.List(r.Context(), PrincipalFrom(r).UserID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"leases":  leases,
		"default": d.Sandboxes.Manager().Defaults(),
		"live":    d.Sandboxes.Live(),
	})
}

// handleSandboxStats is the water level: five counters and the last sweep.
func (d Deps) handleSandboxStats(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	httputil.WriteJSON(w, http.StatusOK, d.Sandboxes.Stats(r.Context()))
}

// handleGetSandbox returns one lease with its recent execution receipts, which
// is what the audit drawer renders.
func (d Deps) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	lease, ok := d.sandboxLease(w, r)
	if !ok {
		return
	}
	execs, err := d.Sandboxes.Execs(r.Context(), lease.ID, 50)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"lease": lease, "execs": execs})
}

// sandboxExecRequest is the operator console's try-it body.
type sandboxExecRequest struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Stdin     string            `json:"stdin"`
	Dir       string            `json:"dir"`
	Env       map[string]string `json:"env"`
	TimeoutMS int               `json:"timeoutMs"`
}

// handleExecSandbox is the operator console's try-it path. It deliberately does
// not go through the router: opening a ticket for a manual probe would pollute
// the ticket statistics and could park a human approval on a throwaway command.
func (d Deps) handleExecSandbox(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	var req sandboxExecRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	// A whitespace command is not a command: refusing it here keeps the provider
	// from receiving a request that only looks authorised.
	command := strings.TrimSpace(req.Command)
	if command == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeSandboxPolicyDenied)
		return
	}
	lease, ok := d.sandboxLease(w, r)
	if !ok {
		return
	}
	if lease.Expired(time.Now()) || lease.State == sandbox.StateTerminated {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeSandboxLeaseLost)
		return
	}
	res, err := d.Sandboxes.Exec(r.Context(), lease.ID, sandbox.ExecRequest{
		Command:   command,
		Args:      req.Args,
		Stdin:     req.Stdin,
		Dir:       req.Dir,
		Env:       req.Env,
		TimeoutMS: req.TimeoutMS,
		DryRun:    true,
	})
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

// handleRenewSandbox extends a lease. A lease that reached the instance ceiling
// answers 409 rather than being silently kept alive: the caller has to decide
// whether to start a fresh environment.
func (d Deps) handleRenewSandbox(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	if _, ok := d.sandboxLease(w, r); !ok {
		return
	}
	lease, err := d.Sandboxes.Renew(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, lease)
}

// handleTerminateSandbox destroys the environment and closes the lease.
func (d Deps) handleTerminateSandbox(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	if _, ok := d.sandboxLease(w, r); !ok {
		return
	}
	if err := d.Sandboxes.Terminate(r.Context(), r.PathValue("id")); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSandboxEvents streams the live frames, replaying from `since` first so a
// reconnecting console does not lose the interval it was away. Replay and live
// carry the same seq, so the client never sees a gap or a duplicate.
func (d Deps) handleSandboxEvents(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	flusher, ok := httputil.Stream(w)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
		return
	}
	ownerID := PrincipalFrom(r).UserID
	since := sinceSeq(r.URL.Query().Get("since"))

	events, _, err := d.Sandboxes.EventsSince(r.Context(), ownerID, since, 200)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	for _, event := range events {
		if err := httputil.WriteSSE(w, flusher, "sandbox_event", event); err != nil {
			return
		}
		since = event.Seq
	}

	ch, release := d.Sandboxes.Subscribe(ownerID)
	defer release()
	go httputil.KeepAlive(r.Context(), w, flusher, 20*time.Second)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-ch:
			if !open {
				return
			}
			if event.Seq <= since {
				// Already replayed above, or a duplicate frame.
				continue
			}
			if err := httputil.WriteSSE(w, flusher, "sandbox_event", event); err != nil {
				return
			}
			since = event.Seq
		}
	}
}
