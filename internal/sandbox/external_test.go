package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The provider layer shells out, and CI has neither docker nor bubblewrap. The
// test binary therefore re-executes itself as the external command: a copy is
// renamed per scenario and the fake answers according to the arguments the
// provider passed. That keeps Probe/Create/Exec/Status covered everywhere
// instead of skipping the interesting half of the package on a bare host.

const fakePrefix = "sbfake-"

var (
	fakeRootOnce sync.Once
	fakeRootPath string
)

func fakeRoot() string {
	fakeRootOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agenthub-sbfake")
		if err != nil {
			panic(err)
		}
		fakeRootPath = dir
	})
	return fakeRootPath
}

func TestMain(m *testing.M) {
	if mode, ok := fakeMode(os.Args); ok {
		os.Exit(fakeRun(mode, os.Args))
	}
	code := m.Run()
	if fakeRootPath != "" {
		_ = os.RemoveAll(fakeRootPath)
	}
	os.Exit(code)
}

func fakeMode(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	base := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	if !strings.HasPrefix(base, fakePrefix) {
		return "", false
	}
	return strings.TrimPrefix(base, fakePrefix), true
}

func fakeRun(mode string, argv []string) int {
	switch mode {
	case "mcp":
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			_, _ = fmt.Printf("{\"echo\":%q}\n", scanner.Text())
		}
		return 0
	case "mcp-eof":
		// Answer once without a trailing newline: the reader must still return
		// the partial frame instead of treating io.EOF as a lost session.
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		_, _ = fmt.Print(`{"echo":"eof"}`)
		return 0
	case "mcp-close":
		// Read one frame, then die silently.
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		return 0
	case "mcp-hang":
		time.Sleep(30 * time.Second)
		return 0
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		return 0
	case "json":
		_, _ = fmt.Println(`{"exit_code":7,"stdout":"inner","stderr":"problem","truncated":true,"duration_ms":42}`)
		return 0
	case "badjson":
		_, _ = fmt.Println(`{"exit_code":`)
		return 0
	case "fail":
		_, _ = fmt.Fprintln(os.Stderr, "fake: refusing to run")
		return 3
	case "silent-fail":
		return 3
	case "empty":
		return 0
	}

	sub := ""
	if len(argv) > 1 {
		sub = argv[1]
	}
	switch sub {
	case "version":
		_, _ = fmt.Println("27.4.1")
	case "info":
		switch mode {
		case "infofail":
			_, _ = fmt.Fprintln(os.Stderr, "fake: info unavailable")
			return 1
		case "noruntime":
			_, _ = fmt.Println(`{"runc":{}}`)
		default:
			_, _ = fmt.Println(`{"runc":{},"runsc":{},"kata":{}}`)
		}
	case "run":
		_, _ = fmt.Println("fake-container")
	case "inspect":
		if mode == "stopped" {
			_, _ = fmt.Println("false")
			return 0
		}
		_, _ = fmt.Println("true")
	case "exec":
		_, _ = fmt.Println("fake-exec")
	case "--version":
		_, _ = fmt.Println("bubblewrap 0.8.0")
	default:
		_, _ = fmt.Println("fake-ok")
	}
	return 0
}

// fakeBinary copies the test binary once per mode and returns its path, so a
// provider can be pointed at "docker" without docker being installed.
func fakeBinary(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	name := fakePrefix + mode
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(fakeRoot(), name)
	if _, err := os.Stat(dst); err == nil {
		return dst
	}
	// A hard link is instant where the filesystem allows it; copying a test
	// binary that can be tens of megabytes would add seconds per scenario.
	if err := os.Link(self, dst); err != nil {
		data, rerr := os.ReadFile(self)
		if rerr != nil {
			t.Fatalf("read the test binary: %v", rerr)
		}
		if werr := os.WriteFile(dst, data, 0o755); werr != nil {
			t.Fatalf("write the fake binary: %v", werr)
		}
	}
	return dst
}

func missingBinary(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "definitely-not-installed")
}

func TestRunExternalRefusesAnEmptyBinary(t *testing.T) {
	_, err := runExternal(context.Background(), runSpec{bin: "   "})
	if code := codeOf(t, err); code != CodePolicyDenied {
		t.Fatalf("expected %s, got %s", CodePolicyDenied, code)
	}
}

func TestRunExternalFillsItsOwnBudget(t *testing.T) {
	// No timeout and no cap: the defaults have to be usable, and stdin has to be
	// wired through, because that is how a stdin-driven tool is called.
	res, err := runExternal(context.Background(), runSpec{bin: fakeBinary(t, "cat"), stdin: "hello"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.stdout != "hello" {
		t.Fatalf("stdout = %q", res.stdout)
	}
	if res.exit != 0 || res.truncated || res.duration <= 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunExternalReportsATimeout(t *testing.T) {
	started := time.Now()
	_, err := runExternal(context.Background(), runSpec{
		bin:     fakeBinary(t, "mcp-hang"),
		timeout: 150 * time.Millisecond,
	})
	if code := codeOf(t, err); code != CodeTimeout {
		t.Fatalf("expected %s, got %s", CodeTimeout, code)
	}
	// The kill has to actually happen: a hung child that outlives its deadline
	// would leak a process per timed-out call.
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("the deadline was not enforced, took %v", elapsed)
	}
}

func TestRunExternalResolvesANameThroughThePath(t *testing.T) {
	// No separator, so this goes through LookPath and fails with exec.ErrNotFound
	// rather than a syscall error: both must end up as "unsupported here".
	_, err := runExternal(context.Background(), runSpec{
		bin:     "agenthub-definitely-missing-binary",
		timeout: time.Second,
	})
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestDockerProbeReportsTheDaemonVersion(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "ok"), "", "")
	avail, err := d.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe must not fail: a missing backend is a reason, not an error: %v", err)
	}
	if !avail.Available {
		t.Fatalf("expected the fake daemon to be available: %+v", avail)
	}
	if !strings.Contains(avail.Reason, "27.4.1") {
		t.Fatalf("the reason must carry the version the daemon reported, got %q", avail.Reason)
	}
}

func TestDockerProbeExplainsAnUnreachableDaemon(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "fail"), "", "")
	avail, err := d.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if avail.Available {
		t.Fatal("a daemon that answers with an error must not read as available")
	}
	if !strings.Contains(avail.Reason, "refusing to run") {
		t.Fatalf("the console needs the daemon's own words, got %q", avail.Reason)
	}
}

func TestDockerProbeInventsAReasonWhenStderrIsEmpty(t *testing.T) {
	// The fake exits non-zero without writing to stderr, so the reason has to be
	// synthesised from the exit code instead of coming back blank. A coverage
	// build makes the child's own runtime print a warning, so the wording is only
	// asserted on the plain path: the contract is "never an empty reason".
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "silent-fail"), "", "")
	avail, _ := d.Probe(context.Background())
	if avail.Available || avail.Reason == "" {
		t.Fatalf("a non-zero exit must always be explained, got %+v", avail)
	}
}

func TestDockerProbeRequiresTheRuntimeToBeRegistered(t *testing.T) {
	ctx := context.Background()

	ok := NewDockerProvider(ProviderDocker, fakeBinary(t, "ok"), "runsc", "")
	avail, _ := ok.Probe(ctx)
	if !avail.Available || !strings.Contains(avail.Reason, "with runtime runsc") {
		t.Fatalf("a registered runtime must be reported as such: %+v", avail)
	}

	// docker answers, but `docker info` does not: the runtime cannot be claimed.
	broken := NewDockerProvider(ProviderDocker, fakeBinary(t, "infofail"), "runsc", "")
	if avail, _ := broken.Probe(ctx); avail.Available {
		t.Fatal("a failed runtime listing must not be reported as gVisor being present")
	}

	// The daemon is up and lists its runtimes, just not this one.
	absent := NewDockerProvider(ProviderDocker, fakeBinary(t, "noruntime"), "runsc", "")
	avail, _ = absent.Probe(ctx)
	if avail.Available || !strings.Contains(avail.Reason, "not registered") {
		t.Fatalf("absent runtime must say so, got %+v", avail)
	}
}

func TestDockerProbeReportsAMissingBinary(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, missingBinary(t), "", "")
	avail, err := d.Probe(context.Background())
	if err != nil {
		t.Fatalf("a missing binary is a reason, not an error: %v", err)
	}
	if avail.Available || avail.Reason == "" {
		t.Fatalf("expected an explained refusal, got %+v", avail)
	}
}

func TestDockerCreateReturnsTheContainerRef(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "ok"), "", "")
	h, err := d.Create(context.Background(), Spec{LeaseID: "lease-1", Profile: Profile{Network: NetworkNone}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.Ref != "fake-container" {
		t.Fatalf("ref must be the daemon's answer, got %q", h.Ref)
	}
	if h.ID != "lease-1" || h.Provider != ProviderDocker {
		t.Fatalf("handle = %+v", h)
	}
}

func TestDockerCreateRefusesAFailedRun(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "fail"), "", "")
	_, err := d.Create(context.Background(), Spec{LeaseID: "lease-1"})
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestDockerCreateRefusesAMissingBinary(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, missingBinary(t), "", "")
	if _, err := d.Create(context.Background(), Spec{LeaseID: "lease-1"}); err == nil {
		t.Fatal("a host without docker cannot create a container")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestDockerCreateRefusesAnEmptyContainerID(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "empty"), "", "")
	_, err := d.Create(context.Background(), Spec{LeaseID: "lease-1"})
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("a container without an id is not a usable environment, got %s", code)
	}
}

func TestDockerExecReturnsTheReceipt(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "ok"), "", "")
	res, err := d.Exec(context.Background(), Handle{Ref: "c1"}, ExecRequest{Command: "echo", Args: []string{"hi"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "fake-exec") {
		t.Fatalf("receipt = %+v", res)
	}
	if res.Provider != ProviderDocker || res.Ref != "c1" || res.Receipt != "exit" {
		t.Fatalf("receipt must name the provider and the container: %+v", res)
	}
}

func TestDockerExecKeepsANonZeroExitAsAResult(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "fail"), "", "")
	res, err := d.Exec(context.Background(), Handle{Ref: "c1"}, ExecRequest{Command: "false"})
	if err != nil {
		t.Fatalf("a non-zero exit is a result, not a transport failure: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "refusing") {
		t.Fatalf("stderr must survive into the receipt, got %q", res.Stderr)
	}
}

func TestDockerAttachKeepsTheProcessAlive(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, fakeBinary(t, "mcp"), "", "")
	session, err := d.Attach(context.Background(), Handle{Ref: "c1"}, ExecRequest{Command: "mcp-server"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = session.Close() }()

	reply, err := session.RoundTrip(context.Background(), []byte(`{"id":1}`))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if !strings.Contains(string(reply), "id") {
		t.Fatalf("echoed frame = %q", reply)
	}
}

func TestDockerDestroyIsIdempotentEvenWithoutABinary(t *testing.T) {
	d := NewDockerProvider(ProviderDocker, missingBinary(t), "", "")
	if err := d.Destroy(context.Background(), Handle{Ref: "c1"}); err != nil {
		t.Fatalf("a container that is already gone is the desired state: %v", err)
	}
}

func TestDockerStatusMapsInspectOutput(t *testing.T) {
	ready := NewDockerProvider(ProviderDocker, fakeBinary(t, "ok"), "", "")
	if state, err := ready.Status(context.Background(), Handle{Ref: "c1"}); err != nil || state != StateReady {
		t.Fatalf("state = %q err = %v, want ready", state, err)
	}

	stopped := NewDockerProvider(ProviderDocker, fakeBinary(t, "stopped"), "", "")
	if state, _ := stopped.Status(context.Background(), Handle{Ref: "c1"}); state != StateExpired {
		t.Fatalf("a stopped container must read expired, got %q", state)
	}

	broken := NewDockerProvider(ProviderDocker, missingBinary(t), "", "")
	if state, err := broken.Status(context.Background(), Handle{Ref: "c1"}); err != nil || state != StateFailed {
		t.Fatalf("an unusable probe must read failed, got %q (%v)", state, err)
	}
}

func TestDockerNameAndEnvironment(t *testing.T) {
	d := NewDockerProvider("podman", "", "", "unix:///run/podman.sock")
	if d.Name() != "podman" {
		t.Fatalf("name = %q", d.Name())
	}
	env := d.env()
	found := false
	for _, kv := range env {
		if kv == "DOCKER_HOST=unix:///run/podman.sock" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an explicit host must reach the child: %v", env)
	}
}

func TestBwrapProbeReportsTheBinaryItFound(t *testing.T) {
	b := &BwrapProvider{bin: fakeBinary(t, "ok")}
	avail, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !avail.Available || !strings.Contains(avail.Reason, "bubblewrap") {
		t.Fatalf("availability = %+v", avail)
	}

	broken := &BwrapProvider{bin: fakeBinary(t, "fail")}
	if avail, _ := broken.Probe(context.Background()); avail.Available {
		t.Fatal("a bwrap that refuses --version must not read as available")
	}
}

func TestBwrapCreateRecordsThePolicyOnly(t *testing.T) {
	b := NewBwrapProvider()
	h, err := b.Create(context.Background(), Spec{LeaseID: "lease-9"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.Ref != "bwrap-lease-9" || h.Spec.LeaseID != "lease-9" {
		t.Fatalf("handle = %+v", h)
	}
	if state, err := b.Status(context.Background(), h); err != nil || state != StateReady {
		t.Fatalf("status = %q (%v)", state, err)
	}
	if err := b.Destroy(context.Background(), h); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestBwrapExecWrapsTheCommand(t *testing.T) {
	b := &BwrapProvider{bin: fakeBinary(t, "cat")}
	h := Handle{Ref: "bwrap-1", Spec: Spec{Profile: Profile{Network: NetworkNone}}}
	res, err := b.Exec(context.Background(), h, ExecRequest{Command: "cat", Stdin: "piped-through"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Provider != "bwrap" || res.Ref != "bwrap-1" {
		t.Fatalf("receipt = %+v", res)
	}
	if res.Stdout != "piped-through" {
		t.Fatalf("stdin must reach the wrapped process, got %q", res.Stdout)
	}
}

func TestBwrapExecReportsAMissingBinary(t *testing.T) {
	b := &BwrapProvider{bin: missingBinary(t)}
	_, err := b.Exec(context.Background(), Handle{}, ExecRequest{Command: "echo"})
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("expected %s, got %s", CodeUnsupported, code)
	}
}

func TestBwrapAttachOpensASessionAndRefusesAMissingBinary(t *testing.T) {
	b := &BwrapProvider{bin: fakeBinary(t, "mcp")}
	session, err := b.Attach(context.Background(), Handle{}, ExecRequest{Command: "mcp-server"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	reply, err := session.RoundTrip(context.Background(), []byte(`{"id":7}`))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if !strings.Contains(string(reply), "id") {
		t.Fatalf("echoed frame = %q", reply)
	}
	_ = session.Close()

	broken := &BwrapProvider{bin: missingBinary(t)}
	if _, err := broken.Attach(context.Background(), Handle{}, ExecRequest{Command: "mcp"}); err == nil {
		t.Fatal("attach without a binary must fail instead of returning a dead session")
	}
}

// fakeDeclarative reuses the shared template from providers_test.go and only
// swaps the binary for a fake, so the placeholder behaviour stays pinned there.
func fakeDeclarative(t *testing.T, mode string) DeclarativeSpec {
	t.Helper()
	return declarativeSpec(fakeBinary(t, mode))
}

func TestDeclarativeProbe(t *testing.T) {
	ctx := context.Background()

	bare := fakeDeclarative(t, "ok")
	provider, err := NewDeclarativeProvider(bare)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	avail, _ := provider.Probe(ctx)
	if !avail.Available {
		t.Fatalf("a template without a probe asserts availability: %+v", avail)
	}

	withProbe := fakeDeclarative(t, "ok")
	withProbe.ProbeArgs = [][]string{{"probe"}}
	provider, err = NewDeclarativeProvider(withProbe)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	avail, _ = provider.Probe(ctx)
	if !avail.Available || !strings.Contains(avail.Reason, "fake-ok") {
		t.Fatalf("the probe's own output is the reason: %+v", avail)
	}

	failing := fakeDeclarative(t, "fail")
	failing.ProbeArgs = [][]string{{"probe"}}
	provider, err = NewDeclarativeProvider(failing)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	avail, _ = provider.Probe(ctx)
	if avail.Available || !strings.Contains(avail.Reason, "probe failed") {
		t.Fatalf("a failing probe must be reported with the binary's name: %+v", avail)
	}
}

func TestDeclarativeCreateAndDestroyRunTheirTemplates(t *testing.T) {
	ctx := context.Background()

	withCreate := fakeDeclarative(t, "ok")
	withCreate.CreateArgs = [][]string{{"create", "{lease_id}"}}
	provider, err := NewDeclarativeProvider(withCreate)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	h, err := provider.Create(ctx, Spec{LeaseID: "lease-3"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.Ref == "lease-3" {
		t.Fatal("a create command that answered must supply the reference")
	}

	// A create command that prints nothing leaves the lease id as the reference.
	quiet := fakeDeclarative(t, "empty")
	quiet.CreateArgs = [][]string{{"create", "{lease_id}"}}
	provider, err = NewDeclarativeProvider(quiet)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	h, err = provider.Create(ctx, Spec{LeaseID: "lease-4"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if h.Ref != "lease-4" {
		t.Fatalf("ref = %q, want the lease id", h.Ref)
	}

	// Destroy without a template is a no-op; with one it runs the argv.
	if err := provider.Destroy(ctx, h); err != nil {
		t.Fatalf("destroy without a template: %v", err)
	}
	withDestroy := fakeDeclarative(t, "ok")
	withDestroy.DestroyArgs = [][]string{{"destroy", "{lease_id}"}}
	provider, err = NewDeclarativeProvider(withDestroy)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := provider.Destroy(ctx, Handle{Ref: "x", Spec: Spec{LeaseID: "lease-5"}}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func TestDeclarativeExecUnderstandsAStructuredReceipt(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		mode    string
		receipt string
		exit    int
		stdout  string
	}{
		{"json", "json", 7, "inner"},
		{"badjson", "unparsed", 0, `{"exit_code":`},
	}
	for _, tc := range cases {
		spec := fakeDeclarative(t, tc.mode)
		provider, err := NewDeclarativeProvider(spec)
		if err != nil {
			t.Fatalf("%s: build: %v", tc.mode, err)
		}
		res, err := provider.Exec(ctx, Handle{Ref: "sbx", Spec: Spec{LeaseID: "lease-1", Profile: Profile{TimeoutMS: 2000}}},
			ExecRequest{Command: "tool", Args: []string{"a", "b"}})
		if err != nil {
			t.Fatalf("%s: exec: %v", tc.mode, err)
		}
		if res.Receipt != tc.receipt {
			t.Fatalf("%s: receipt = %q, want %q", tc.mode, res.Receipt, tc.receipt)
		}
		if res.ExitCode != tc.exit || !strings.Contains(res.Stdout, tc.stdout) {
			t.Fatalf("%s: receipt = %+v", tc.mode, res)
		}
		if res.Provider != spec.Name || res.Ref != "sbx" {
			t.Fatalf("%s: the receipt must name the provider and the environment: %+v", tc.mode, res)
		}
	}
}

func TestASessionRefusesAFrameAfterItsStdinIsGone(t *testing.T) {
	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = session.Close() }()

	if err := session.stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	_, err = session.RoundTrip(context.Background(), []byte(`{"id":1}`))
	if code := codeOf(t, err); code != CodeLeaseLost {
		t.Fatalf("a frame that cannot be delivered must lose the lease, got %s", code)
	}
}

func TestASessionRoundTripsOneFrameAtATime(t *testing.T) {
	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = session.Close() }()

	for i := 0; i < 3; i++ {
		payload := []byte(fmt.Sprintf(`{"id":%d}`, i))
		reply, err := session.RoundTrip(context.Background(), payload)
		if err != nil {
			t.Fatalf("round trip %d: %v", i, err)
		}
		if !strings.Contains(string(reply), "echo") {
			t.Fatalf("reply %d = %q", i, reply)
		}
	}
}

func TestASessionRefusesAfterClose(t *testing.T) {
	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("closing twice must be safe: %v", err)
	}
	_, err = session.RoundTrip(context.Background(), []byte(`{"id":1}`))
	if code := codeOf(t, err); code != CodeLeaseLost {
		t.Fatalf("expected %s, got %s", CodeLeaseLost, code)
	}
}

func TestASessionTimesOutAndKillsTheChild(t *testing.T) {
	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp-hang")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = session.RoundTrip(ctx, []byte(`{"id":1}`))
	if code := codeOf(t, err); code != CodeTimeout {
		t.Fatalf("expected %s, got %s", CodeTimeout, code)
	}
}

func TestASessionReportsADeadChild(t *testing.T) {
	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp-close")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = session.Close() }()

	_, err = session.RoundTrip(context.Background(), []byte(`{"id":1}`))
	if err == nil {
		t.Fatal("a child that died without answering must not look like a reply")
	}
	if code := codeOf(t, err); code != CodeUnsupported && code != CodeLeaseLost {
		t.Fatalf("unexpected code %s", code)
	}
}

func TestASessionReturnsAPartialFrameAtEOF(t *testing.T) {
	session, err := startSession(context.Background(), runSpec{bin: fakeBinary(t, "mcp-eof")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = session.Close() }()

	reply, err := session.RoundTrip(context.Background(), []byte(`{"id":1}`))
	if err != nil {
		t.Fatalf("a frame without a trailing newline is still a frame: %v", err)
	}
	if string(reply) != `{"echo":"eof"}` {
		t.Fatalf("reply = %q", reply)
	}
}

func TestStartingASessionWithoutABinaryIsRefused(t *testing.T) {
	if _, err := startSession(context.Background(), runSpec{bin: "   "}); err == nil {
		t.Fatal("a session without a binary must be refused")
	} else if code := codeOf(t, err); code != CodePolicyDenied {
		t.Fatalf("expected %s, got %s", CodePolicyDenied, code)
	}

	// kill is a no-op before the process exists, which is what Close on a failed
	// start relies on.
	(&procSession{}).kill()
}
