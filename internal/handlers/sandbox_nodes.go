package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/sandbox"
)

// Execution nodes are desktops (agentbuddy, BYOM) that run commands on their own
// machine on agenthub's behalf. The contract is deliberately one-way and
// stateless: the node polls, runs one job inside its own policy, reports back and
// keeps no session. Losing a node therefore loses no state — the next poll simply
// finds nothing, and the lease that was waiting on it rebuilds transparently.

// defaultNodePoll is the long-poll budget when none is configured. It has to stay
// under the common 30s proxy idle timeout or the poll would be cut before the
// server answered.
const defaultNodePoll = 25 * time.Second

// maxNodePoll bounds AGENTHUB_SANDBOX_NODE_POLL_TIMEOUT at the far end: a node
// parked for minutes cannot detect that it was unregistered.
const maxNodePoll = 60 * time.Second

// sandboxNodeRequest is the enrollment body. The id is the machine's stable
// identity (it survives restarts), which is what makes a re-enrollment an update
// rather than a second node.
type sandboxNodeRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// sandboxJobResultRequest is a node's answer for one claimed job.
type sandboxJobResultRequest struct {
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"durationMs"`
	Receipt    string `json:"receipt"`
	Error      string `json:"error"`
}

func (d Deps) handleListSandboxNodes(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	nodes, err := d.Sandboxes.Nodes(r.Context())
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"nodes":    nodes,
		"waiters":  d.Sandboxes.Queue().Waiters(),
		"pollHint": d.nodePollTimeout().String(),
	})
}

// handleRegisterSandboxNode enrolls or refreshes a node. Re-enrolling the same id
// is an update by design: a desktop that restarts must reclaim its identity, not
// accumulate duplicates in the console.
func (d Deps) handleRegisterSandboxNode(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	var req sandboxNodeRequest
	if err := httputil.Decode(w, r, &req, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	id := strings.TrimSpace(req.ID)
	if !validNodeID(id) {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeSandboxPolicyDenied)
		return
	}
	ownerID := PrincipalFrom(r).UserID
	if existing, err := d.Sandboxes.Node(r.Context(), id); err == nil {
		if existing.OwnerID != ownerID && existing.OwnerID != "" && ownerID != "" {
			// Somebody else registered this id first. Reusing it would let a
			// caller hijack another operator's execution node.
			httputil.WriteError(w, http.StatusNotFound, httputil.CodeSandboxNotFound)
			return
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = id
	}
	caps := normalizeCapabilities(req.Capabilities)
	if err := d.Sandboxes.RegisterNode(r.Context(), &sandbox.Node{
		ID:           id,
		OwnerID:      ownerID,
		Name:         name,
		Capabilities: caps,
		Status:       sandbox.NodeOnline,
		LastSeenAt:   time.Now(),
		CreatedAt:    time.Now(),
	}); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	node, err := d.Sandboxes.Node(r.Context(), id)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, node)
}

// handleRemoveSandboxNode unregisters a node. Leases that referenced it are not
// destroyed here: they fail their next call and rebuild, which is the documented
// "the sandbox disappearing must not fail the task" behaviour.
func (d Deps) handleRemoveSandboxNode(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	if _, ok := d.sandboxNode(w, r); !ok {
		return
	}
	if err := d.Sandboxes.RemoveNode(r.Context(), r.PathValue("id")); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNodeHeartbeat records that a node is alive. A stale heartbeat is what the
// console reports as offline, so this is the only liveness signal there is.
func (d Deps) handleNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	node, ok := d.sandboxNode(w, r)
	if !ok {
		return
	}
	if err := d.Sandboxes.TouchNode(r.Context(), node.ID); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	touched, err := d.Sandboxes.Node(r.Context(), node.ID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"node": touched, "pollHint": d.nodePollTimeout().String()})
}

// handleClaimNodeJob is the long poll. An empty 204 is the normal answer: the
// node immediately polls again, which is what keeps an idle fleet cheap (one
// parked request per node, no timer storm).
func (d Deps) handleClaimNodeJob(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	node, ok := d.sandboxNode(w, r)
	if !ok {
		return
	}
	queue := d.Sandboxes.Queue()
	if queue == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	wait := d.nodePollTimeout()
	if raw := r.URL.Query().Get("wait"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil {
			if seconds <= 0 {
				wait = 0
			} else if time.Duration(seconds)*time.Second < wait {
				wait = time.Duration(seconds) * time.Second
			}
		}
	}
	// Polling is a heartbeat: a node parked on a long poll is by definition
	// alive, and making it send a second request just to say so would double the
	// traffic of an idle fleet.
	if err := d.Sandboxes.TouchNode(r.Context(), node.ID); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	queue.EnterWait()
	defer queue.LeaveWait()

	job, found := queue.Claim(node.ID, node.Capabilities, wait)
	if !found {
		if r.Context().Err() != nil {
			// The node hung up; there is nobody to answer.
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, job)
}

// handleReportNodeJob records a node's answer and wakes whoever is waiting on it.
func (d Deps) handleReportNodeJob(w http.ResponseWriter, r *http.Request) {
	if !d.sandboxesReady(w) {
		return
	}
	node, ok := d.sandboxNode(w, r)
	if !ok {
		return
	}
	queue := d.Sandboxes.Queue()
	if queue == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	var req sandboxJobResultRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	jobID := r.PathValue("job")
	job, found := queue.Get(jobID)
	if !found {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeSandboxNotFound)
		return
	}
	if job.NodeID != "" && job.NodeID != node.ID {
		// One node must not complete another node's job.
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeSandboxNotFound)
		return
	}
	res := sandbox.ExecResult{
		ExitCode:   req.ExitCode,
		Stdout:     req.Stdout,
		Stderr:     req.Stderr,
		Truncated:  req.Truncated,
		DurationMS: req.DurationMS,
		Provider:   job.Provider,
		Ref:        job.LeaseID,
		Receipt:    req.Receipt,
	}
	if err := queue.Complete(jobID, res, strings.TrimSpace(req.Error)); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	// The queue holds in-flight work only; the finished job goes to the audit
	// table so the lease drawer can show what ran on which node.
	now := time.Now()
	finished := *job
	finished.FinishedAt = &now
	finished.Result = res
	if msg := strings.TrimSpace(req.Error); msg != "" {
		finished.State = sandbox.JobFailed
		finished.Error = msg
	} else {
		finished.State = sandbox.JobDone
	}
	if err := d.Sandboxes.SaveJob(r.Context(), &finished); err != nil {
		// A missing audit row must not turn a successful execution into a
		// failure the node will retry: log-and-continue is the honest answer.
		if d.Log != nil {
			d.Log.Warn("sandbox: could not persist job audit", "job", finished.ID, "error", err)
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sandboxNode resolves the {id} in the path and enforces ownership, mirroring
// sandboxLease for the node side.
func (d Deps) sandboxNode(w http.ResponseWriter, r *http.Request) (*sandbox.Node, bool) {
	node, err := d.Sandboxes.Node(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return nil, false
	}
	caller := PrincipalFrom(r).UserID
	if caller != "" && node.OwnerID != "" && node.OwnerID != caller {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeSandboxNotFound)
		return nil, false
	}
	return node, true
}

func (d Deps) nodePollTimeout() time.Duration {
	wait := defaultNodePoll
	if d.Cfg != nil && d.Cfg.SandboxNodePollTimeout > 0 {
		wait = d.Cfg.SandboxNodePollTimeout
	}
	if wait > maxNodePoll {
		wait = maxNodePoll
	}
	return wait
}

// normalizeCapabilities keeps the advertised provider list small and ordered. An
// empty list means "remote only": a node that names no provider still accepts
// work routed to the node protocol, it just cannot claim a provider-specific job.
func normalizeCapabilities(in []string) []string {
	if len(in) == 0 {
		return []string{"remote"}
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"remote"}
	}
	return out
}

// validNodeID accepts a machine-chosen identity: letters, digits and the three
// separators a generated id uses. Anything else is refused at enrollment rather
// than becoming a path segment the console has to escape.
func validNodeID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
