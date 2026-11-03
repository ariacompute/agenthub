// Package sandbox is the execution environment domain kernel: it decides which
// provider runs a command, leases an environment for a scope, and records what
// happened.
//
// The split it enforces is the one Anthropic calls the brain and the hands. The
// hub, the router and the adapters never learn whether a command ran in a
// container, under bubblewrap or on somebody's laptop through a node: they ask
// the Manager, the Manager asks a Provider, and the Registry decides which
// providers exist.
//
// Ports are declared here, in the domain package, exactly like internal/router:
// memory.go is what the unit tests run on, internal/store holds the GORM
// implementation, and both must behave the same.
package sandbox

import (
	"strings"
	"time"

	"agenthub/internal/httputil"
)

// Machine readable codes produced by this package. They are declared once in
// httputil so the HTTP status mapping lives next to every other code.
const (
	CodeDisabled        = httputil.CodeSandboxDisabled
	CodeUnsupported     = httputil.CodeSandboxUnsupported
	CodeUnknownProvider = httputil.CodeSandboxUnknownProvider
	CodePolicyDenied    = httputil.CodeSandboxPolicyDenied
	// CodeCapabilityUnavailable refuses a profile that asks for isolation the
	// chosen provider never promised. Silently degrading was the bug: bubblewrap
	// answered "bridge" with the host network namespace.
	CodeCapabilityUnavailable = httputil.CodeSandboxCapabilityUnavailable
	CodeTimeout               = httputil.CodeSandboxTimeout
	CodeLeaseLost             = httputil.CodeSandboxLeaseLost
	CodeLimitReached          = httputil.CodeSandboxLimitReached
	CodeNodeOffline           = httputil.CodeSandboxNodeOffline
	CodeNotFound              = httputil.CodeSandboxNotFound
)

// Layer is the isolation tier a provider belongs to. The values line up with
// docs/sandbox-landscape.md one for one, which is what lets the console print an
// honest strength next to every name instead of a marketing adjective.
type Layer string

const (
	LayerContainer  Layer = "container"
	LayerUserKernel Layer = "usermode-kernel"
	LayerMicroVM    Layer = "microvm"
	LayerProcess    Layer = "process"
	LayerPlatform   Layer = "platform"
	LayerRemote     Layer = "remote"
	LayerNone       Layer = "none"
)

// Layers is the presentation order: the default first, the weakest last.
var Layers = []Layer{LayerContainer, LayerUserKernel, LayerMicroVM, LayerProcess, LayerPlatform, LayerRemote, LayerNone}

// Isolation is the strength of the boundary, kept separate from Layer because
// two entries in the same layer can differ (a docker container versus a rootless
// podman container) and because the console shows this one verbatim.
const (
	IsolationNone       = "none"
	IsolationProcess    = "process"
	IsolationNamespace  = "namespace"
	IsolationContainer  = "container"
	IsolationUserKernel = "usermode-kernel"
	IsolationMicroVM    = "microvm"
	IsolationPlatform   = "platform"
	IsolationRemote     = "remote"
)

// Source records where a provider definition came from: compiled in, read from
// operator configuration, or contributed by a registered node.
const (
	SourceBuiltin     = "builtin"
	SourceDeclarative = "declarative"
	SourceNode        = "node"
	// SourcePlatform is a built-in HTTP client for a hosted sandbox platform
	// (e2b, microsandbox, daytona, …). It used to be reported as "declarative",
	// which said the wrong thing: nothing about it is read from operator
	// configuration.
	SourcePlatform = "platform"
)

// State is the lifecycle of one lease.
const (
	StateStarting   = "starting"
	StateReady      = "ready"
	StateExpired    = "expired"
	StateTerminated = "terminated"
	StateFailed     = "failed"
)

// Network modes a profile may ask for.
//
// NetworkAllowlist is kept in the vocabulary so an existing record that names it
// still parses, but it grants nothing: no provider filters hosts yet, so the
// value is read as NetworkNone (see GrantsNetwork). Removing the constant would
// be the honest alternative and is what an egress sidecar would let us avoid —
// a TODO(real) lives on GrantsNetwork for that reason.
const (
	NetworkNone      = "none"
	NetworkAllowlist = "egress_allowlist"
	NetworkBridge    = "bridge"
	NetworkPlatform  = "platform"
	// NetworkDelegated is a declaration, never a request: it says the provider
	// forwards the profile to a layer that enforces it (an execution node, a
	// hosted platform's own runtime) and cannot answer the question itself. It is
	// the honest label for "this process does not constrain the network", which
	// is exactly what the remote and platform providers used to deny while
	// advertising none/bridge/platform as if they enforced them.
	NetworkDelegated = "delegated"
)

// Argument policies for the CLI surface. "none" means: no arguments may come
// from the caller at all, which is the only policy that closes the
// prompt-to-host-command chain by construction.
const (
	ArgsNone        = "none"
	ArgsPrefix      = "prefix"
	ArgsPassthrough = "passthrough"
)

// Descriptor is what a provider says about itself without touching the outside
// world. Probe answers the other half: can it actually run here.
type Descriptor struct {
	Name      string   `json:"name"`
	Layer     Layer    `json:"layer"`
	Isolation string   `json:"isolation"`
	Network   []string `json:"network,omitempty"`
	// Requires names the binaries a probe looks for, so an unavailable entry can
	// say "bwrap is not installed" instead of "unavailable". Only binaries belong
	// here: the platform providers used to put an environment variable name in
	// this list, which made the catalogue claim a binary called E2B_SHIM_URL.
	Requires []string `json:"requires,omitempty"`
	// EnvKeys names the environment variables a provider reads, so the catalogue
	// can say "set E2B_SHIM_URL" without pretending a variable is a binary.
	EnvKeys []string `json:"envKeys,omitempty"`
	Source  string   `json:"source"`
	License string   `json:"license,omitempty"`
	DocsURL string   `json:"docsUrl,omitempty"`
	// Default marks the provider AGENTHUB_SANDBOX_PROVIDER falls back to.
	Default bool `json:"default,omitempty"`
}

// Availability is the probed half of a catalogue entry.
type Availability struct {
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitempty"`
}

// ProviderInfo is one catalogue row: the static descriptor plus a cached probe.
type ProviderInfo struct {
	Descriptor
	Availability
}

// Profile is the resolved execution policy: instance defaults merged with what a
// resource asked for. Every provider reads limits from here so a resource cannot
// talk its way past the instance ceiling.
type Profile struct {
	Provider string `json:"provider,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
	Image    string `json:"image,omitempty"`
	WorkDir  string `json:"workDir,omitempty"`
	// ReadonlyRoot is a pointer on purpose: nil means "read-only", which is the
	// only safe default, and an explicit false is the one way to opt out.
	ReadonlyRoot   *bool    `json:"readonlyRoot,omitempty"`
	Network        string   `json:"network,omitempty"`
	EgressAllow    []string `json:"egressAllowlist,omitempty"`
	CPU            float64  `json:"cpu,omitempty"`
	MemoryMB       int      `json:"memoryMB,omitempty"`
	PidsLimit      int      `json:"pidsLimit,omitempty"`
	TimeoutMS      int      `json:"timeoutMs,omitempty"`
	MaxOutputBytes int      `json:"maxOutputBytes,omitempty"`
	EnvAllow       []string `json:"envAllowlist,omitempty"`
	ArgsPolicy     string   `json:"argsPolicy,omitempty"`
	ArgsAllow      []string `json:"argsAllowlist,omitempty"`
	ReuseScope     string   `json:"reuseScope,omitempty"`
	IdleTTLSeconds int      `json:"idleTtlSeconds,omitempty"`
}

// Timeout resolves the execution deadline.
func (p Profile) Timeout() time.Duration {
	if p.TimeoutMS <= 0 {
		return time.Minute
	}
	return time.Duration(p.TimeoutMS) * time.Millisecond
}

// OutputCap resolves the truncation ceiling.
func (p Profile) OutputCap() int {
	if p.MaxOutputBytes <= 0 {
		return 64 << 10
	}
	return p.MaxOutputBytes
}

// Spec is what a provider needs to create one environment.
type Spec struct {
	LeaseID    string  `json:"leaseId"`
	OwnerID    string  `json:"ownerId"`
	ResourceID string  `json:"resourceId"`
	Scope      string  `json:"scope"`
	Profile    Profile `json:"profile"`
}

// Handle is the provider's own reference to a created environment. Nothing above
// the Manager may interpret Ref: for docker it is a container id, for a node it
// is a remote sandbox name, for the mock it is a digest.
type Handle struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Ref      string `json:"ref"`
	// Spec is the lease definition captured at creation. Providers that build
	// their argv per exec (bubblewrap, declarative templates, the node protocol)
	// need the mounts, limits and scope that were agreed when the lease was made,
	// not whatever a request happens to carry.
	Spec Spec `json:"spec,omitempty"`
}

// ExecRequest is one command to run inside a lease.
type ExecRequest struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Stdin     string            `json:"stdin,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int               `json:"timeoutMs,omitempty"`
	// MaxOutputBytes is filled in by the Manager from the lease profile, so a
	// caller cannot ask for an unbounded capture.
	MaxOutputBytes int `json:"maxOutputBytes,omitempty"`
	// DryRun marks the operator console's try-it path, which must not open a
	// router ticket and must be visible as such in the audit trail.
	DryRun bool `json:"dryRun,omitempty"`
}

// OutputCap resolves the capture ceiling for this request.
func (r ExecRequest) OutputCap() int {
	if r.MaxOutputBytes <= 0 {
		return 64 << 10
	}
	return r.MaxOutputBytes
}

// ExecResult is the structured receipt of one execution. Stdout and stderr are
// returned to the caller but never persisted: the audit table keeps digests.
type ExecResult struct {
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"durationMs"`
	Provider   string `json:"provider"`
	Ref        string `json:"ref,omitempty"`
	// Receipt says how the result was understood: "exit" for a plain exit code,
	// "json" for a declarative provider that answered with a structured receipt,
	// "unparsed" when it answered with something that looked like JSON and was not.
	Receipt string `json:"receipt,omitempty"`
}

// Lease is one execution environment held for a scope. The lifecycle is
// deliberately not tied to a task: a cancelled task does not destroy the lease,
// and a lost lease does not fail the task.
type Lease struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"ownerId"`
	ResourceID  string    `json:"resourceId"`
	Scope       string    `json:"scope"`
	Provider    string    `json:"provider"`
	Runtime     string    `json:"runtime,omitempty"`
	NodeID      string    `json:"nodeId,omitempty"`
	State       string    `json:"state"`
	Ref         string    `json:"ref,omitempty"`
	Profile     Profile   `json:"profile"`
	CreatedAt   time.Time `json:"createdAt"`
	RenewedAt   time.Time `json:"renewedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Execs       int       `json:"execs"`
	LastExit    int       `json:"lastExitCode"`
	LastCommand string    `json:"lastCommand,omitempty"`
}

// Expired reports whether the lease is past its deadline at now.
func (l *Lease) Expired(now time.Time) bool {
	return l != nil && !l.ExpiresAt.IsZero() && now.After(l.ExpiresAt)
}

// LeaseRequest asks the Manager for an environment.
type LeaseRequest struct {
	OwnerID    string  `json:"ownerId"`
	ResourceID string  `json:"resourceId"`
	Scope      string  `json:"scope"`
	Profile    Profile `json:"profile"`
	// Provider overrides the profile's provider; empty means "use the profile".
	Provider string `json:"provider,omitempty"`
}

// ExecRecord is one row of the audit trail. It carries digests rather than
// output: the console only needs to show that something ran and how it ended.
type ExecRecord struct {
	ID           string    `json:"id"`
	LeaseID      string    `json:"leaseId"`
	OwnerID      string    `json:"ownerId"`
	Provider     string    `json:"provider"`
	Command      string    `json:"command"`
	ExitCode     int       `json:"exitCode"`
	DurationMS   int64     `json:"durationMs"`
	Truncated    bool      `json:"truncated"`
	StdoutSHA256 string    `json:"stdoutSha256,omitempty"`
	StderrSHA256 string    `json:"stderrSha256,omitempty"`
	DryRun       bool      `json:"dryRun"`
	At           time.Time `json:"at"`
}

// Node is a registered execution node (an agentbuddy desktop, in the reference
// deployment). Online is derived from LastSeenAt, never stored, so a crashed
// node cannot leave a stale "online" row behind.
type Node struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"ownerId"`
	Name         string    `json:"name"`
	APIKeyID     string    `json:"apiKeyId,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Status       string    `json:"status"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Node status values.
const (
	NodeOnline  = "online"
	NodeOffline = "offline"
)

// Job operations a node may be asked to perform.
const (
	JobCreate  = "create"
	JobExec    = "exec"
	JobAttach  = "attach"
	JobDestroy = "destroy"
	JobStatus  = "status"
)

// Job is one unit of work handed to a node. The live queue is in process: a
// node's connection is the queue, so persisting pending work would only create
// ghosts that nobody is polling for. Finished jobs are written to
// sandbox_jobs for audit.
type Job struct {
	ID         string      `json:"id"`
	NodeID     string      `json:"nodeId"`
	LeaseID    string      `json:"leaseId"`
	Provider   string      `json:"provider"`
	Op         string      `json:"op"`
	Spec       Spec        `json:"spec,omitempty"`
	Request    ExecRequest `json:"request,omitempty"`
	State      string      `json:"state"`
	Result     ExecResult  `json:"result,omitempty"`
	Error      string      `json:"error,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
}

// Job states.
const (
	JobQueued  = "queued"
	JobClaimed = "claimed"
	JobDone    = "done"
	JobFailed  = "failed"
)

// Event is one frame of the live stream: what the console's timeline renders.
type Event struct {
	Seq     uint64    `json:"seq"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	LeaseID string    `json:"leaseId,omitempty"`
	At      time.Time `json:"at"`
	Payload any       `json:"payload,omitempty"`
}

// Event kinds.
const (
	EventLeaseCreated   = "lease.created"
	EventLeaseRenewed   = "lease.renewed"
	EventLeaseExpired   = "lease.expired"
	EventLeaseDestroyed = "lease.destroyed"
	EventExec           = "exec"
	EventNodeOnline     = "node.online"
	EventNodeOffline    = "node.offline"
)

// Stats is the water level the console shows: five counters, no prose.
type Stats struct {
	ActiveLeases int       `json:"activeLeases"`
	PendingJobs  int       `json:"pendingJobs"`
	OnlineNodes  int       `json:"onlineNodes"`
	RecentExecs  int       `json:"recentExecs"`
	LastSweepAt  time.Time `json:"lastSweepAt"`
	Sweeps       int       `json:"sweeps"`
	Reclaimed    int       `json:"reclaimed"`
	LastError    string    `json:"lastError,omitempty"`
}

// NormalizeScope keeps the reuse key stable: empty becomes "default" so a lookup
// never depends on whether the caller passed a blank or omitted field.
func NormalizeScope(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	return s
}

// ScopeFor resolves the reuse scope out of the metadata a message carries. The
// router stamps every task it launches with its ticket id, so a ticket's steps
// share one environment; without that, each step would open its own container.
func ScopeFor(metadata map[string]any, contextID string) string {
	if metadata != nil {
		if v, ok := metadata["ticket"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return NormalizeScope(contextID)
}

// Error builds a coded error, so callers never invent ad-hoc strings.
func Error(code httputil.Code) error { return &httputil.Error{Code: code} }
