package sandbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The three stdio pipes are the only way a session speaks to its child, so a
// command whose pipes are already taken has to be refused instead of yielding a
// session that cannot deliver a frame.
func TestStartSessionRefusesACommandItCannotWire(t *testing.T) {
	orig := commandContext
	t.Cleanup(func() { commandContext = orig })

	cases := map[string]func(*exec.Cmd){
		"stdin":  func(c *exec.Cmd) { c.Stdin = io.NopCloser(os.Stdin) },
		"stdout": func(c *exec.Cmd) { c.Stdout = io.Discard },
		"stderr": func(c *exec.Cmd) { c.Stderr = io.Discard },
	}
	for name, occupy := range cases {
		t.Run(name, func(t *testing.T) {
			commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, name, args...)
				occupy(cmd)
				return cmd
			}
			_, err := startSession(context.Background(), runSpec{bin: "fake"})
			if code := codeOf(t, err); code != CodeUnsupported {
				t.Fatalf("expected %s, got %s", CodeUnsupported, code)
			}
		})
	}
}

// A child that never answers and never lets its context expire is stopped by the
// session's own backstop, which kills it rather than leaking the call.
func TestSessionFrameBackstop(t *testing.T) {
	orig := sessionFrameTimeout
	t.Cleanup(func() { sessionFrameTimeout = orig })
	sessionFrameTimeout = 20 * time.Millisecond

	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp-hang")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = session.Close() }()

	if _, err := session.RoundTrip(context.Background(), []byte(`{"id":1}`)); codeOf(t, err) != CodeTimeout {
		t.Fatalf("expected %s, got %s", CodeTimeout, codeOf(t, err))
	}
}

// A process killed by a signal has no exit code. The receipt says 1 instead of
// -1, because a negative exit code reads as a broken receipt.
func TestNormalizeExitCode(t *testing.T) {
	cases := map[int]int{-1: 1, -9: 1, 0: 0, 3: 3}
	for in, want := range cases {
		if got := normalizeExitCode(in); got != want {
			t.Fatalf("normalizeExitCode(%d) = %d, want %d", in, got, want)
		}
	}
}

// A context that is already expired never reaches the child: Run reports the
// deadline and the receipt says "timeout" rather than a configuration problem.
func TestRunExternalReportsAnExpiredContext(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := runExternal(ctx, runSpec{bin: fakeBinary(t, "cat"), timeout: time.Minute}); codeOf(t, err) != CodeTimeout {
		t.Fatalf("expected %s, got %s", CodeTimeout, codeOf(t, err))
	}
}

// A binary this host cannot execute is a host problem, not a result: it is
// neither an exit code nor "not found". A plain text file is the shortest way to
// produce that shape on every platform.
func TestRunExternalReportsAnUnusableBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-program")
	if err := os.WriteFile(path, []byte("this is not an executable image\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runExternal(context.Background(), runSpec{bin: path, timeout: 5 * time.Second})
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

// A hard context deadline on a child that ignores its own budget is a timeout,
// and the kill still happens.
func TestRunExternalKillsAChildThatMissesItsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := runExternal(ctx, runSpec{bin: fakeBinary(t, "mcp-hang"), timeout: time.Minute}); codeOf(t, err) != CodeTimeout {
		t.Fatalf("expected %s, got %s", CodeTimeout, codeOf(t, err))
	}
}

// A declarative template that reaches run time with an unknown placeholder is
// still refused: load-time validation is the first gate, not the only one.
func TestDeclarativeRefusesAnUnvalidatedPlaceholder(t *testing.T) {
	ctx := context.Background()
	spec := Spec{LeaseID: "lease-1"}

	create := &DeclarativeProvider{spec: DeclarativeSpec{Name: "sbx", Binary: "fake"}, create: []string{"create", "{bogus}"}}
	if _, err := create.Create(ctx, spec); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("create: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}

	execPre := &DeclarativeProvider{spec: DeclarativeSpec{Name: "sbx", Binary: "fake"}, execPre: []string{"exec", "{bogus}"}}
	if _, err := execPre.Exec(ctx, Handle{}, ExecRequest{Command: "tool"}); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("exec prefix: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}

	execSuf := &DeclarativeProvider{
		spec:    DeclarativeSpec{Name: "sbx", Binary: "fake"},
		execPre: []string{"exec"},
		execSuf: []string{"{bogus}"},
	}
	if _, err := execSuf.Exec(ctx, Handle{}, ExecRequest{Command: "tool", Args: []string{"a"}}); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("exec suffix: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}

	destroy := &DeclarativeProvider{spec: DeclarativeSpec{Name: "sbx", Binary: "fake"}, destroy: []string{"rm", "{bogus}"}}
	if err := destroy.Destroy(ctx, Handle{}); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("destroy: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}
}

// A create template with an empty argv has nothing to validate, which is a bad
// template rather than a runtime surprise.
func TestNewDeclarativeProviderRejectsAnEmptyCreateTemplate(t *testing.T) {
	spec := declarativeSpec(fakeBinary(t, "ok"))
	spec.CreateArgs = [][]string{{}}
	if _, err := NewDeclarativeProvider(spec); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}
}

// A create command the host cannot even start is a failed create: the lease must
// not be handed back as if the environment existed.
func TestDeclarativeCreateReportsAnUnstartableCreate(t *testing.T) {
	provider := &DeclarativeProvider{
		spec:   DeclarativeSpec{Name: "sbx", Binary: filepath.Join(t.TempDir(), "missing")},
		create: []string{"create"},
	}
	if _, err := provider.Create(context.Background(), Spec{LeaseID: "lease-1"}); codeOf(t, err) != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, codeOf(t, err))
	}
}

// An exec without a suffix passes the request's arguments through unchanged, and
// a vars map that already carries "arg" wins over the positional one.
func TestDeclarativeExecPassesArgumentsThrough(t *testing.T) {
	provider, err := NewDeclarativeProvider(declarativeSpec(fakeBinary(t, "ok")))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	res, err := provider.Exec(context.Background(), Handle{Ref: "sbx"},
		ExecRequest{Command: "tool", Args: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("receipt = %+v", res)
	}

	expanded, err := expandArgv([]string{"cmd", "{arg}"}, map[string]string{"arg": "from-vars"})
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if expanded[1] != "from-vars" {
		t.Fatalf("argv = %v, want the explicit arg to win", expanded)
	}

	// A provider with no suffix template appends the arguments verbatim.
	bare := &DeclarativeProvider{
		spec:    DeclarativeSpec{Name: "sbx", Binary: fakeBinary(t, "ok")},
		execPre: []string{"exec"},
	}
	if _, err := bare.Exec(context.Background(), Handle{}, ExecRequest{Command: "tool", Args: []string{"a", "b"}}); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// A child that cannot be started at all — its working directory is gone, say —
// is neither an exit code nor "the binary is missing".
func TestRunExternalReportsAnUnstartableChild(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	spec := runSpec{bin: fakeBinary(t, "ok"), dir: gone, timeout: 5 * time.Second}
	if _, err := runExternal(context.Background(), spec); codeOf(t, err) != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, codeOf(t, err))
	}
}

// A store that cannot answer the event replay is reported, so the console shows
// a failed catch-up instead of an empty stream.
func TestEventsSinceReportsAStoreFailure(t *testing.T) {
	service, _ := newTestService(t, nil)
	service.manager.opts.Store = &eventsFailer{Store: service.manager.opts.Store}
	if _, _, err := service.EventsSince(context.Background(), "u1", 0, 0); err == nil {
		t.Fatal("a failing store must be reported")
	}
}

// eventsFailer fails only the replay, so the rest of the facade still works.
type eventsFailer struct{ Store }

func (e *eventsFailer) EventsSince(context.Context, string, uint64, int) ([]*Event, error) {
	return nil, errors.New("events unavailable")
}

// A node that never claims the job must not hold the caller forever: the grace
// timer ends the wait and the job is marked as timed out.
func TestRemoteExecGivesUpAfterTheGraceWindow(t *testing.T) {
	orig := remoteJobGrace
	t.Cleanup(func() { remoteJobGrace = orig })
	remoteJobGrace = 10 * time.Millisecond

	provider := NewRemoteProvider(nil)
	_, err := provider.Exec(context.Background(), Handle{ID: "lease-1"}, ExecRequest{Command: "tool", TimeoutMS: 1})
	if code := codeOf(t, err); code != CodeNodeOffline {
		t.Fatalf("expected %s, got %s", CodeNodeOffline, code)
	}
	if provider.Queue().Pending() != 0 {
		t.Fatal("the abandoned job must not stay queued")
	}
}

// A job that vanishes between its answer and the read is a lost lease: the node
// did the work in an environment this instance can no longer describe.
func TestRemoteExecReportsAJobThatVanished(t *testing.T) {
	queue := NewJobQueue()
	provider := NewRemoteProvider(queue)

	go func() {
		job, ok := queue.Claim("node-1", []string{"remote"}, 2*time.Second)
		if !ok {
			return
		}
		// The completion and the removal happen under the queue lock, so the
		// waiting Exec cannot read the entry in between.
		queue.mu.Lock()
		entry := queue.entries[job.ID]
		entry.job.State = JobDone
		close(entry.done)
		delete(queue.entries, job.ID)
		queue.mu.Unlock()
	}()

	if _, err := provider.Exec(context.Background(), Handle{ID: "lease-1"}, ExecRequest{Command: "tool", TimeoutMS: 500}); codeOf(t, err) != CodeLeaseLost {
		t.Fatalf("expected %s, got %s", CodeLeaseLost, codeOf(t, err))
	}
}

// A platform node that answers with an unusable body is not a working node, and
// a call that expects no answer must not try to decode one.
func TestHTTPProviderRejectsUnencodableAndUnreadableAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/garbage":
			_, _ = w.Write([]byte("not json"))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	provider := NewHTTPProvider(PlatformPreset{Name: "node", EnvBaseURL: "NODE_URL"}, func(string) string { return srv.URL })
	ctx := context.Background()

	// A body the encoder refuses never leaves the process.
	if err := provider.do(ctx, http.MethodPost, "/x", map[string]any{"callback": func() {}}, nil); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("unencodable body: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}

	// A caller that expects no payload gets none decoded.
	if err := provider.do(ctx, http.MethodPost, "/ok", nil, nil); err != nil {
		t.Fatalf("nil target: %v", err)
	}

	// A non-JSON answer is a node problem, not a crash.
	var out map[string]any
	if err := provider.do(ctx, http.MethodGet, "/garbage", nil, &out); codeOf(t, err) != CodeUnsupported {
		t.Fatalf("garbage answer: expected %s, got %s", CodeUnsupported, codeOf(t, err))
	}
}

// Acquire refuses a profile the instance will not honour before it creates
// anything, and logs the clamp when it silently lowers one.
func TestAcquireAppliesTheHostPolicyAndTheCeiling(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	// A host directory the shipped denylist refuses.
	if _, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "r1", Profile: Profile{WorkDir: "/etc"}}); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("host policy: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}
	// A hand-built instance whose own defaults cannot be honoured: a prefix
	// argument policy with no prefixes would refuse every argument, which is a
	// misconfiguration rather than a policy.
	broken, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.Defaults.ArgsPolicy = ArgsPrefix
	})
	if _, err := broken.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "r1"}); codeOf(t, err) != CodePolicyDenied {
		t.Fatalf("invalid profile: expected %s, got %s", CodePolicyDenied, codeOf(t, err))
	}

	// Above the ceiling: clamped and logged, not refused.
	clamped, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.Limits = Limits{CPU: 1, MemoryMB: 512, PidsLimit: 64}
		m.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	})
	lease, err := clamped.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "r1",
		Profile: Profile{CPU: 8, MemoryMB: 4096, PidsLimit: 256}})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.Profile.CPU != 1 || lease.Profile.MemoryMB != 512 {
		t.Fatalf("profile = %+v, want the instance ceiling", lease.Profile)
	}
}

// A lease created before the ceiling was lowered is reclaimed and rebuilt inside
// it, rather than kept alive on a budget the instance no longer allows.
func TestAcquireReclaimsALeaseAboveTheCeiling(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()

	first, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "r1", Profile: Profile{CPU: 4}})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// The operator lowers the ceiling underneath the live lease.
	service.manager.opts.Limits = Limits{CPU: 1}
	service.manager.opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	second, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "r1", Profile: Profile{CPU: 1}})
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("a lease above the ceiling must be replaced, not renewed")
	}
	if second.Profile.CPU != 1 {
		t.Fatalf("profile = %+v, want the ceiling", second.Profile)
	}
}

// The instance total is a ceiling of its own, independent of the per-owner one.
func TestAcquireRefusesPastTheInstanceTotal(t *testing.T) {
	service, _ := newTestService(t, func(m *ManagerOptions, _ *ServiceOptions) {
		m.MaxPerOwner = 4
		m.MaxTotal = 2
	})
	ctx := context.Background()
	for i, owner := range []string{"u1", "u2"} {
		if _, err := service.Acquire(ctx, LeaseRequest{OwnerID: owner, ResourceID: "r"}); err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
	}
	if _, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u3", ResourceID: "r"}); codeOf(t, err) != CodeLimitReached {
		t.Fatalf("expected %s, got %s", CodeLimitReached, codeOf(t, err))
	}
}

// A volume root has no directory to mount: the predicate refuses it before any
// provider is asked to, on every platform — the Unix absolute-path check would
// otherwise refuse "C:" first and leave the case unpinned.
func TestNamesNoDirectory(t *testing.T) {
	for _, cleaned := range []string{"/", ".", "..", "C:", "D:"} {
		if !namesNoDirectory(cleaned) {
			t.Fatalf("%q must not name a directory", cleaned)
		}
	}
	for _, cleaned := range []string{"/srv/work", "C:/work", "/tmp/x"} {
		if namesNoDirectory(cleaned) {
			t.Fatalf("%q does name a directory", cleaned)
		}
	}

	// And the same answer through the public path: a traversal back to the root
	// is refused, an ordinary directory is not.
	if _, err := CleanWorkDir("/srv/work/../.."); err == nil {
		t.Fatal("a traversal to the root must be refused")
	}
	if _, err := CleanWorkDir("/srv/work"); err != nil {
		t.Fatalf("an ordinary directory must pass: %v", err)
	}
}

// The daemon's own words are the reason when it has any; a silent failure still
// has to say something.
func TestDockerRefusalReason(t *testing.T) {
	if got := dockerRefusalReason("docker", 3, "  cannot connect to the daemon\n"); got != "cannot connect to the daemon" {
		t.Fatalf("reason = %q, want the daemon's words", got)
	}
	got := dockerRefusalReason("docker", 3, "   ")
	if got == "" || !strings.Contains(got, "reported exit 3") {
		t.Fatalf("reason = %q, want the synthesised explanation", got)
	}
}

// A provider that declares it delegates the network covers any concrete mode; a
// provider that names one mode covers only that mode.
func TestSupportsNetworkHonoursDelegation(t *testing.T) {
	delegated := Descriptor{Network: []string{NetworkDelegated}}
	if !SupportsNetwork(delegated, Profile{Network: NetworkBridge}) {
		t.Fatal("a delegated provider covers any mode")
	}
	none := Descriptor{Network: []string{NetworkNone}}
	if SupportsNetwork(none, Profile{Network: NetworkBridge}) {
		t.Fatal("a provider that named one mode must not cover another")
	}
	if !SupportsNetwork(Descriptor{Network: []string{"  NONE "}}, Profile{Network: NetworkNone}) {
		t.Fatal("the declaration is compared case-insensitively and trimmed")
	}
}
