package sandbox

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DockerProvider runs commands inside a container. It covers docker and podman
// (same argv surface) and, through the runtime argument, gVisor and Kata: those
// are `--runtime` values on the same backend, and giving them separate Go
// implementations would let the three drift apart.
type DockerProvider struct {
	name    string
	bin     string
	runtime string
	host    string
}

// NewDockerProvider builds a container provider.
//
// runtime is "" for a plain container, "runsc" for gVisor and "kata" for Kata
// Containers; the descriptor reports the resulting layer honestly, so the console
// never presents a plain container as a microVM.
func NewDockerProvider(name, bin, runtime, host string) *DockerProvider {
	if strings.TrimSpace(bin) == "" {
		bin = name
	}
	return &DockerProvider{name: name, bin: bin, runtime: strings.TrimSpace(runtime), host: strings.TrimSpace(host)}
}

// Name is the catalogue key.
func (d *DockerProvider) Name() string { return d.name }

// Describe reports the layer implied by the runtime.
func (d *DockerProvider) Describe(context.Context) (Descriptor, error) {
	layer, isolation := LayerContainer, IsolationContainer
	switch d.runtime {
	case "runsc", "gvisor":
		layer, isolation = LayerUserKernel, IsolationUserKernel
	case "kata", "kata-runtime":
		layer, isolation = LayerMicroVM, IsolationMicroVM
	}
	return Descriptor{
		Name:      d.name,
		Layer:     layer,
		Isolation: isolation,
		// NetworkAllowlist is not advertised: see bwrap.go — the mode parses but
		// needs an egress sidecar to mean anything, and a descriptor must not
		// claim a filter that does not exist. All three named modes are real here:
		// createArgs maps them onto --network none / bridge / the daemon default.
		Network: []string{NetworkNone, NetworkBridge, NetworkPlatform},
		// All three limits are real here: createArgs passes --cpus, --memory with
		// a matching --memory-swap, and --pids-limit to the daemon.
		Limits:   []string{LimitCPU, LimitMemory, LimitPids},
		Requires: []string{d.bin},
		Source:   SourceBuiltin,
		License:  "Apache-2.0",
		DocsURL:  "https://docs.docker.com/engine/security/",
	}, nil
}

// Probe asks the daemon for its version: a present CLI with an unreachable
// daemon is the failure that actually happens in containerised deployments, and
// `docker ps` would be a heavier way to learn the same thing.
func (d *DockerProvider) Probe(ctx context.Context) (Availability, error) {
	spec := runSpec{
		bin:     d.bin,
		args:    []string{"version", "--format", "{{.Server.Version}}"},
		env:     d.env(),
		timeout: 3 * time.Second,
		cap:     4 << 10,
	}
	res, err := runExternal(ctx, spec)
	if err != nil {
		return Availability{Available: false, Reason: d.bin + " is not usable on this host (binary missing or daemon unreachable)"}, nil
	}
	if res.exit != 0 {
		reason := strings.TrimSpace(res.stderr)
		if reason == "" {
			reason = d.bin + " reported exit " + itoa(res.exit) + "; is the socket reachable?"
		}
		return Availability{Available: false, Reason: reason}, nil
	}
	if d.runtime != "" {
		// `docker version` succeeds on a host that never installed runsc, so a
		// runtime-backed entry has to ask the daemon which runtimes it actually
		// has. Reporting gVisor as available because docker is would be exactly
		// the green badge this catalogue exists to prevent.
		runtimes, rerr := runExternal(ctx, runSpec{
			bin:     d.bin,
			args:    []string{"info", "--format", "{{json .Runtimes}}"},
			env:     d.env(),
			timeout: 3 * time.Second,
			cap:     8 << 10,
		})
		if rerr != nil || runtimes.exit != 0 {
			return Availability{Available: false, Reason: d.bin + ": could not list the registered runtimes"}, nil
		}
		if !strings.Contains(runtimes.stdout, `"`+d.runtime+`"`) {
			return Availability{Available: false, Reason: "runtime " + d.runtime + " is not registered with " + d.bin}, nil
		}
		return Availability{Available: true, Reason: d.bin + " " + strings.TrimSpace(res.stdout) + " with runtime " + d.runtime}, nil
	}
	return Availability{Available: true, Reason: d.bin + " " + strings.TrimSpace(res.stdout)}, nil
}

// Create starts a long-lived container that Exec can attach to.
func (d *DockerProvider) Create(ctx context.Context, spec Spec) (Handle, error) {
	res, err := runExternal(ctx, runSpec{
		bin:     d.bin,
		args:    d.createArgs(spec),
		env:     d.env(),
		timeout: 2 * time.Minute,
		cap:     4 << 10,
	})
	if err != nil {
		return Handle{}, err
	}
	if res.exit != 0 {
		return Handle{}, Error(CodeUnsupported)
	}
	ref := strings.TrimSpace(res.stdout)
	if ref == "" {
		return Handle{}, Error(CodeUnsupported)
	}
	return Handle{ID: spec.LeaseID, Provider: d.name, Ref: ref}, nil
}

// Exec runs one command inside the container.
func (d *DockerProvider) Exec(ctx context.Context, h Handle, req ExecRequest) (ExecResult, error) {
	res, err := runExternal(ctx, runSpec{
		bin:     d.bin,
		args:    d.execArgs(h.Ref, req),
		env:     d.env(),
		stdin:   req.Stdin,
		timeout: requestTimeout(req),
		cap:     req.OutputCap(),
	})
	out := ExecResult{
		ExitCode:   res.exit,
		Stdout:     res.stdout,
		Stderr:     res.stderr,
		Truncated:  res.truncated,
		DurationMS: res.duration.Milliseconds(),
		Provider:   d.name,
		Ref:        h.Ref,
		Receipt:    "exit",
	}
	return out, err
}

// Attach keeps a process inside the container for the MCP stdio transport.
func (d *DockerProvider) Attach(_ context.Context, h Handle, req ExecRequest) (Session, error) {
	return startSession(context.Background(), runSpec{
		bin:  d.bin,
		args: d.execArgs(h.Ref, req),
		env:  d.env(),
	})
}

// Destroy removes the container. It is idempotent: a container the daemon
// already reaped (--rm after a crash) is not an error.
func (d *DockerProvider) Destroy(ctx context.Context, h Handle) error {
	// A missing container is the desired end state, so a failure here is not
	// reported: the lease bookkeeping is the authority on whether it existed.
	_, _ = runExternal(ctx, runSpec{
		bin:     d.bin,
		args:    []string{"rm", "-f", h.Ref},
		env:     d.env(),
		timeout: 3 * time.Second,
		cap:     4 << 10,
	})
	return nil
}

// Status reports whether the container is still running.
func (d *DockerProvider) Status(ctx context.Context, h Handle) (string, error) {
	res, err := runExternal(ctx, runSpec{
		bin:     d.bin,
		args:    []string{"inspect", "-f", "{{.State.Running}}", h.Ref},
		env:     d.env(),
		timeout: 3 * time.Second,
		cap:     1 << 10,
	})
	if err != nil || res.exit != 0 {
		return StateFailed, nil
	}
	if strings.TrimSpace(res.stdout) == "true" {
		return StateReady, nil
	}
	return StateExpired, nil
}

// Anonymous writable areas are tmpfs, which lives in memory: without a ceiling a
// single runaway process would consume the host's RAM through the page cache. The
// numbers are deliberately generous for build/CLI work and small enough that a
// mistake is a failed command rather than a dead host.
const (
	tmpfsSizeTmp  = "64m"
	tmpfsSizeWork = "512m"
)

// createArgs is pure so the security-relevant flags can be asserted in a unit
// test without a daemon: --network=none, read-only root, dropped capabilities,
// no-new-privileges, a numeric non-root user and the resource limits.
func (d *DockerProvider) createArgs(spec Spec) []string {
	p := spec.Profile
	args := []string{"run", "-d", "--rm", "--name", "agenthub-" + shortID(spec.LeaseID)}
	if d.runtime != "" {
		args = append(args, "--runtime", d.runtime)
	}
	// "platform" means the daemon's default network, so the honest translation is
	// to pass no --network at all. This used to emit a literal "bridge", which
	// made the descriptor's "platform" a lie whenever the daemon default was
	// something else. "bridge" and "none" are named explicitly.
	switch NormalizeNetwork(p.Network) {
	case NetworkPlatform:
		// Daemon default.
	case NetworkBridge:
		args = append(args, "--network", "bridge")
	default:
		args = append(args, "--network", "none")
	}
	if p.Readonly() {
		args = append(args, "--read-only")
	}
	// /tmp is the only writable place a read-only root leaves behind, so it is
	// narrowed to exactly what a temp directory needs: no execution, no device
	// nodes, no setuid and a size ceiling so a runaway process cannot fill the
	// host's memory through the page cache.
	args = append(args, "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size="+tmpfsSizeTmp)
	// Many CLIs write under $HOME before they read anything; without these they
	// fail with a permission error that reads like a bug in the resource. They
	// point at the tmpfs, never at the (read-only) image.
	args = append(args, "--env", "HOME=/tmp", "--env", "TMPDIR=/tmp")
	if p.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(p.PidsLimit))
	}
	if p.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(p.MemoryMB)+"m")
		// --memory alone leaves swap unlimited on a host that has it, which makes
		// the memory ceiling advisory. Setting both to the same value is the
		// documented way to forbid swap for one container.
		args = append(args, "--memory-swap", strconv.Itoa(p.MemoryMB)+"m")
	}
	if p.CPU > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(p.CPU, 'f', -1, 64))
	}
	args = append(args, "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", "65534:65534")

	if p.WorkDir != "" {
		// --mount rather than -v: -v silently creates a missing host directory,
		// which turns a typo into an empty (and, when the root is writable,
		// host-owned) share instead of a failure. It also parses "," as an option
		// separator, which is why ValidateHostPolicy refuses commas in a path.
		mount := "type=bind,src=" + p.WorkDir + ",dst=/work"
		if p.Readonly() {
			mount += ",readonly"
		}
		args = append(args, "--mount", mount)
	} else {
		// No host directory was named, so the working area is a writable tmpfs
		// inside the container: it disappears with the container instead of
		// leaving state on the host, and it is capped so it cannot become an
		// unbounded write target.
		args = append(args, "--tmpfs", "/work:rw,nosuid,nodev,mode=1777,size="+tmpfsSizeWork)
	}
	image := p.Image
	if image == "" {
		image = DefaultImage
	}
	args = append(args, "--pull", "missing", "-w", "/work", image, "sleep", "infinity")
	return args
}

// execArgs is pure for the same reason: it is where an argument could smuggle a
// shell in, so it is asserted directly.
func (d *DockerProvider) execArgs(ref string, req ExecRequest) []string {
	args := []string{"exec", "-i"}
	if req.Dir != "" {
		args = append(args, "-w", req.Dir)
	}
	names := make([]string, 0, len(req.Env))
	for name := range req.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		args = append(args, "-e", name+"="+req.Env[name])
	}
	args = append(args, ref, req.Command)
	args = append(args, req.Args...)
	return args
}

func (d *DockerProvider) env() []string {
	env := baseEnv(nil)
	if d.host != "" {
		env = append(env, "DOCKER_HOST="+d.host)
	}
	return env
}

// shortID keeps container names legal and stable: docker refuses names outside
// [a-zA-Z0-9_.-].
func shortID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= 24 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "lease"
	}
	return out
}
