package sandbox

import (
	"context"
	"sort"
	"strings"
	"time"
)

// BwrapProvider runs commands under bubblewrap: unprivileged user namespaces,
// a read-only root, a tmpfs /tmp, and no network unless the profile asks for it.
//
// This is the cheap local option in docs/sandbox-landscape.md. Its honest limit
// is that without a seccomp program it is a namespace boundary, so the
// descriptor says "namespace" and the console prints that word.
type BwrapProvider struct {
	bin string
}

// NewBwrapProvider builds the bubblewrap provider.
func NewBwrapProvider() *BwrapProvider { return &BwrapProvider{bin: "bwrap"} }

// Name is the catalogue key.
func (b *BwrapProvider) Name() string { return "bwrap" }

// Describe reports a namespace boundary, nothing stronger.
func (b *BwrapProvider) Describe(context.Context) (Descriptor, error) {
	return Descriptor{
		Name:      "bwrap",
		Layer:     LayerProcess,
		Isolation: IsolationNamespace,
		// NetworkAllowlist is not advertised: the mode parses, but nothing here
		// implements host filtering, and the descriptor sells only what runs.
		//
		// "bridge" is not advertised either, and used to be. bubblewrap has no NAT
		// interface: --share-net hands over the host's network namespace, which is
		// "platform". Listing "bridge" made a request for a NAT'd interface come
		// back as the host network, and nobody noticed because nothing compared
		// the request with this list.
		Network:  []string{NetworkNone, NetworkPlatform},
		Requires: []string{b.bin},
		Source:   SourceBuiltin,
		License:  "LGPL-2.0-or-later",
		DocsURL:  "https://github.com/containers/bubblewrap",
	}, nil
}

// Probe looks for the binary. On a host without bubblewrap this reports the
// reason rather than letting the first execution fail.
func (b *BwrapProvider) Probe(ctx context.Context) (Availability, error) {
	res, err := runExternal(ctx, runSpec{
		bin:     b.bin,
		args:    []string{"--version"},
		timeout: 2 * time.Second,
		cap:     1 << 10,
	})
	if err != nil || res.exit != 0 {
		return Availability{Available: false, Reason: "bwrap is not installed"}, nil
	}
	return Availability{Available: true, Reason: strings.TrimSpace(res.stdout)}, nil
}

// Create only records the policy: bubblewrap wraps one command at a time.
func (b *BwrapProvider) Create(_ context.Context, spec Spec) (Handle, error) {
	return Handle{ID: spec.LeaseID, Provider: "bwrap", Ref: "bwrap-" + spec.LeaseID, Spec: spec}, nil
}

// Exec wraps and runs the command.
func (b *BwrapProvider) Exec(ctx context.Context, h Handle, req ExecRequest) (ExecResult, error) {
	res, err := runExternal(ctx, runSpec{
		bin:     b.bin,
		args:    bwrapArgs(h.Spec.Profile, req),
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
		Provider:   "bwrap",
		Ref:        h.Ref,
		Receipt:    "exit",
	}
	return out, err
}

// Attach works because bubblewrap execs in place: the wrapped process is the
// session's child.
func (b *BwrapProvider) Attach(_ context.Context, h Handle, req ExecRequest) (Session, error) {
	return startSession(context.Background(), runSpec{
		bin:  b.bin,
		args: bwrapArgs(h.Spec.Profile, req),
	})
}

// Destroy is a no-op: nothing outlives the command.
func (b *BwrapProvider) Destroy(context.Context, Handle) error { return nil }

// Status is always ready.
func (b *BwrapProvider) Status(context.Context, Handle) (string, error) { return StateReady, nil }

// bwrapArgs is pure, and asserted in a unit test: --unshare-all without
// --share-net must appear whenever the profile does not grant network access.
// GrantsNetwork is the one authority for that: egress_allowlist is a "no" here
// exactly as it is in the container provider, because an allowlist without a
// filter process would hand the sandbox the host's whole network.
func bwrapArgs(p Profile, req ExecRequest) []string {
	args := []string{"--unshare-all", "--die-with-parent", "--new-session"}
	// Only "platform" can be honoured: --share-net is the host's network
	// namespace, nothing narrower. Manager refuses every other mode against the
	// descriptor first, so this branch is the last line, not the only one.
	if NormalizeNetwork(p.Network) == NetworkPlatform {
		args = append(args, "--share-net")
	}
	args = append(args,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
	)
	workdir := strings.TrimSpace(p.WorkDir)
	if workdir != "" {
		args = append(args, "--bind", workdir, "/work")
	} else {
		args = append(args, "--tmpfs", "/work")
	}
	args = append(args, "--chdir", "/work", "--clearenv", "--setenv", "HOME", "/tmp")

	allowed := make([]string, 0, len(req.Env))
	for name := range req.Env {
		if envAllowed(p.EnvAllow, name) {
			allowed = append(allowed, name)
		}
	}
	sort.Strings(allowed)
	for _, name := range allowed {
		args = append(args, "--setenv", name, req.Env[name])
	}
	args = append(args, "--", req.Command)
	args = append(args, req.Args...)
	return args
}

func envAllowed(allow []string, name string) bool {
	for _, a := range allow {
		if strings.TrimSpace(a) == name {
			return true
		}
	}
	return false
}
