package sandbox

import (
	"context"
	"strings"
	"time"
)

// LocalProvider runs a command as a plain child process. It is the fallback for
// a host with neither a container runtime nor bubblewrap, and it is deliberately
// blunt about what it is: isolation "none".
//
// Two things it still does, because they cost nothing and remove real risk: the
// environment is rebuilt from an allowlist (so the agenthub process's secrets are
// not inherited), and the process gets its own process group so a timeout kills
// the whole tree.
type LocalProvider struct{}

// NewLocalProvider builds the restricted local provider.
func NewLocalProvider() *LocalProvider { return &LocalProvider{} }

// Name is the catalogue key.
func (l *LocalProvider) Name() string { return "local" }

// Describe tells the truth: process isolation only.
//
// The network entry says platform, not none or bridge. A child process has no
// network namespace of its own — it reaches whatever the host reaches, on the
// host's routing table and DNS — so claiming it can be had "without network"
// was a promise this provider cannot keep. Only a container or a bubblewrap
// sandbox can actually withhold the network; those two report the modes they
// enforce.
func (l *LocalProvider) Describe(context.Context) (Descriptor, error) {
	return Descriptor{
		Name:      "local",
		Layer:     LayerProcess,
		Isolation: IsolationNone,
		Network:   []string{NetworkPlatform},
		Source:    SourceBuiltin,
		License:   "n/a",
		DocsURL:   "/docs",
	}, nil
}

// Probe is always available: a Go binary can always fork.
func (l *LocalProvider) Probe(context.Context) (Availability, error) {
	return Availability{Available: true, Reason: "no isolation: runs as the agenthub user"}, nil
}

// Create returns a handle that carries nothing but the lease id.
func (l *LocalProvider) Create(_ context.Context, spec Spec) (Handle, error) {
	return Handle{ID: spec.LeaseID, Provider: "local", Ref: "local-" + spec.LeaseID}, nil
}

// Exec runs one command without a shell.
func (l *LocalProvider) Exec(ctx context.Context, h Handle, req ExecRequest) (ExecResult, error) {
	spec := runSpec{
		bin:     strings.TrimSpace(req.Command),
		args:    req.Args,
		dir:     firstNonEmpty(req.Dir, ""),
		env:     mergeEnv(baseEnv(nil), req.Env, envNames(req.Env)),
		stdin:   req.Stdin,
		timeout: requestTimeout(req),
		cap:     req.OutputCap(),
	}
	res, err := runExternal(ctx, spec)
	out := ExecResult{
		ExitCode:   res.exit,
		Stdout:     res.stdout,
		Stderr:     res.stderr,
		Truncated:  res.truncated,
		DurationMS: res.duration.Milliseconds(),
		Provider:   "local",
		Ref:        h.Ref,
		Receipt:    "exit",
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

// Attach starts a long-lived process for the MCP stdio transport.
func (l *LocalProvider) Attach(_ context.Context, _ Handle, req ExecRequest) (Session, error) {
	return startSession(context.Background(), runSpec{
		bin:  strings.TrimSpace(req.Command),
		args: req.Args,
		dir:  req.Dir,
		env:  mergeEnv(baseEnv(nil), req.Env, envNames(req.Env)),
	})
}

// Destroy is a no-op: the process died with the command.
func (l *LocalProvider) Destroy(context.Context, Handle) error { return nil }

// Status is always ready.
func (l *LocalProvider) Status(context.Context, Handle) (string, error) { return StateReady, nil }

// requestTimeout resolves the deadline for one execution.
func requestTimeout(req ExecRequest) time.Duration {
	if req.TimeoutMS > 0 {
		return time.Duration(req.TimeoutMS) * time.Millisecond
	}
	return time.Minute
}

func envNames(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for name := range env {
		out = append(out, name)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
