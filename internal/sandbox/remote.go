package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// JobQueue is the in-process work queue for execution nodes.
//
// It is in memory on purpose: a node's poll *is* the queue, so persisting
// pending work would only create tasks nobody is polling for once the node that
// was going to run them disconnects. Finished jobs go to sandbox_jobs for audit;
// nothing in flight claims to be durable.
type JobQueue struct {
	mu      sync.Mutex
	entries map[string]*jobEntry
	pending []string
	waiters int
	wake    chan struct{}
}

type jobEntry struct {
	job  *Job
	done chan struct{}
}

// NewJobQueue builds an empty queue.
func NewJobQueue() *JobQueue {
	return &JobQueue{entries: map[string]*jobEntry{}, wake: make(chan struct{}, 1)}
}

// Enqueue adds a job and returns a channel closed when the node reports back.
func (q *JobQueue) Enqueue(job *Job) <-chan struct{} {
	// Enqueue means queued: a caller that forgot the state would otherwise hand
	// the queue a job that Claim can never see, and the node would poll forever
	// while the work sat in memory.
	if job.State == "" {
		job.State = JobQueued
	}
	done := make(chan struct{})
	q.mu.Lock()
	q.entries[job.ID] = &jobEntry{job: job, done: done}
	q.pending = append(q.pending, job.ID)
	q.mu.Unlock()
	q.signal()
	return done
}

// Claim hands the oldest queued job matching the node's providers to a node,
// waiting up to wait for work to appear. A zero wait returns immediately.
func (q *JobQueue) Claim(nodeID string, providers []string, wait time.Duration) (*Job, bool) {
	deadline := time.Now().Add(wait)
	for {
		q.mu.Lock()
		for i, id := range q.pending {
			entry := q.entries[id]
			if entry == nil || entry.job.State != JobQueued {
				continue
			}
			if !providerAllowed(entry.job.Provider, providers) {
				continue
			}
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			entry.job.State = JobClaimed
			entry.job.NodeID = nodeID
			claimed := *entry.job
			q.mu.Unlock()
			return &claimed, true
		}
		q.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, false
		}
		if remaining > 250*time.Millisecond {
			remaining = 250 * time.Millisecond
		}
		select {
		case <-q.wake:
		case <-time.After(remaining):
		}
	}
}

// Complete records the node's answer and wakes the waiting Exec.
func (q *JobQueue) Complete(id string, res ExecResult, errMsg string) error {
	q.mu.Lock()
	entry := q.entries[id]
	if entry == nil {
		q.mu.Unlock()
		return Error(CodeNotFound)
	}
	now := time.Now()
	entry.job.FinishedAt = &now
	if errMsg != "" {
		entry.job.State = JobFailed
		entry.job.Error = errMsg
	} else {
		entry.job.State = JobDone
		entry.job.Result = res
	}
	done := entry.done
	q.mu.Unlock()
	select {
	case <-done:
	default:
		close(done)
	}
	return nil
}

// Get returns a copy of one job.
func (q *JobQueue) Get(id string) (*Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.entries[id]
	if entry == nil {
		return nil, false
	}
	cp := *entry.job
	return &cp, true
}

// Pending counts queued jobs, for the console's water level.
func (q *JobQueue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, entry := range q.entries {
		if entry.job.State == JobQueued {
			n++
		}
	}
	return n
}

// Waiters reports how many nodes are parked on a long poll, which is the honest
// answer to "is any node listening".
func (q *JobQueue) Waiters() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waiters
}

// EnterWait marks the start of a long poll.
func (q *JobQueue) EnterWait() {
	q.mu.Lock()
	q.waiters++
	q.mu.Unlock()
}

// LeaveWait marks the end of a long poll.
func (q *JobQueue) LeaveWait() {
	q.mu.Lock()
	if q.waiters > 0 {
		q.waiters--
	}
	q.mu.Unlock()
}

func (q *JobQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func providerAllowed(provider string, allowed []string) bool {
	if len(allowed) == 0 {
		return provider == "remote"
	}
	for _, a := range allowed {
		if a == provider || a == "remote" {
			return true
		}
	}
	return false
}

// RemoteProvider hands execution to a registered node. Everything above it
// believes it is talking to a local provider: that is the point of the seam.
type RemoteProvider struct {
	queue *JobQueue
}

// NewRemoteProvider builds the node-backed provider.
func NewRemoteProvider(queue *JobQueue) *RemoteProvider {
	if queue == nil {
		queue = NewJobQueue()
	}
	return &RemoteProvider{queue: queue}
}

// Queue exposes the queue for the node handlers.
func (r *RemoteProvider) Queue() *JobQueue { return r.queue }

// Name is the catalogue key.
func (r *RemoteProvider) Name() string { return "remote" }

// Describe reports a remote boundary.
func (r *RemoteProvider) Describe(context.Context) (Descriptor, error) {
	return Descriptor{
		Name:      "remote",
		Layer:     LayerRemote,
		Isolation: IsolationRemote,
		// The node decides, and this process cannot check. It used to advertise
		// none/bridge/platform as if it enforced them, which turned "the sandbox
		// has no network" into an unverified claim about somebody else's machine.
		// "delegated" says what actually happens: the profile travels in the Job
		// and the node's own policy is the enforcement point, recorded in the
		// audit trail on that node.
		Network: []string{NetworkDelegated},
		Source:  SourceBuiltin,
		License: "n/a",
		DocsURL: "/docs",
	}, nil
}

// Probe reports availability that is decided per execution: the queue exists,
// whether a node is listening is answered by the exec itself.
func (r *RemoteProvider) Probe(context.Context) (Availability, error) {
	return Availability{Available: true, Reason: "delivered to a registered execution node"}, nil
}

// Create records the lease; the node creates its environment on first use.
func (r *RemoteProvider) Create(_ context.Context, spec Spec) (Handle, error) {
	return Handle{ID: spec.LeaseID, Provider: "remote", Ref: spec.LeaseID, Spec: spec}, nil
}

// Exec dispatches one job and waits for the node's answer.
func (r *RemoteProvider) Exec(ctx context.Context, h Handle, req ExecRequest) (ExecResult, error) {
	job := &Job{
		ID:        newJobID(),
		Provider:  "remote",
		LeaseID:   h.ID,
		Op:        JobExec,
		Spec:      h.Spec,
		Request:   req,
		State:     JobQueued,
		CreatedAt: time.Now(),
	}
	done := r.queue.Enqueue(job)
	timeout := time.NewTimer(requestTimeout(req) + 30*time.Second)
	defer timeout.Stop()
	select {
	case <-done:
		stored, ok := r.queue.Get(job.ID)
		if !ok {
			return ExecResult{}, Error(CodeLeaseLost)
		}
		if stored.State == JobFailed {
			return ExecResult{}, Error(CodeNodeOffline)
		}
		res := stored.Result
		res.Provider = "remote"
		res.Ref = h.Ref
		return res, nil
	case <-ctx.Done():
		_ = r.queue.Complete(job.ID, ExecResult{}, "canceled")
		return ExecResult{}, Error(CodeTimeout)
	case <-timeout.C:
		_ = r.queue.Complete(job.ID, ExecResult{}, "timeout")
		return ExecResult{}, Error(CodeNodeOffline)
	}
}

// Attach is not available over the job protocol: framing a live session needs a
// stream, and the node contract is one request and one response per job. MCP
// stdio therefore requires a provider that can start a process locally.
func (r *RemoteProvider) Attach(context.Context, Handle, ExecRequest) (Session, error) {
	return nil, Error(CodeUnsupported)
}

// Destroy asks the node to release its environment, best effort.
func (r *RemoteProvider) Destroy(ctx context.Context, h Handle) error {
	job := &Job{
		ID:        newJobID(),
		Provider:  "remote",
		LeaseID:   h.ID,
		Op:        JobDestroy,
		Spec:      h.Spec,
		State:     JobQueued,
		CreatedAt: time.Now(),
	}
	done := r.queue.Enqueue(job)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
	return nil
}

// Status reports ready: the queue has no memory of a node's environment.
func (r *RemoteProvider) Status(context.Context, Handle) (string, error) { return StateReady, nil }

func newJobID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "job_" + hex.EncodeToString(raw[:])
}
