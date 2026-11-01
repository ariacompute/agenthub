package sandbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The provider layer is the security boundary, and none of it needs a daemon:
// docker, bubblewrap and the declarative templates all reduce to an argv, so the
// cases below assert the argv instead of trusting a comment. They run on a
// machine with no docker, no bwrap and no node installed, which is what makes
// them useful in CI.

func hasElement(argv []string, want string) bool {
	for _, element := range argv {
		if element == want {
			return true
		}
	}
	return false
}

func hasPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func hasPrefixElement(argv []string, prefix string) bool {
	for _, element := range argv {
		if strings.HasPrefix(element, prefix) {
			return true
		}
	}
	return false
}

// Elements after the first n are the command line; a provider that ever put a
// shell there would let an argument become code.
func tail(argv []string, from int) []string {
	if from >= len(argv) {
		return nil
	}
	return argv[from:]
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDockerCreateArgsCarryTheSecurityFlags(t *testing.T) {
	provider := NewDockerProvider(ProviderDocker, "", "", "")
	argv := provider.createArgs(Spec{
		LeaseID: "lease-abc/../x",
		Profile: Profile{
			Network:   NetworkNone,
			PidsLimit: 64,
			MemoryMB:  512,
			CPU:       1.5,
		},
	})

	for _, want := range []string{
		"run", "-d", "--rm", "--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", "65534:65534",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev",
		"--tmpfs", "/work:rw,nosuid,nodev,mode=1777",
	} {
		if !hasElement(argv, want) {
			t.Fatalf("create argv is missing %q: %v", want, argv)
		}
	}
	// The pairs are what actually configure the daemon, so they are asserted as
	// pairs: a lone "--network" would accept whatever follows it as the value.
	for _, pair := range [][2]string{
		{"--network", NetworkNone},
		{"--pids-limit", "64"},
		{"--memory", "512m"},
		{"--cpus", "1.5"},
		{"--pull", "missing"},
		{"-w", "/work"},
	} {
		if !hasPair(argv, pair[0], pair[1]) {
			t.Fatalf("create argv is missing %s %s: %v", pair[0], pair[1], argv)
		}
	}
	// No host directory was named, so there must be no bind mount at all.
	if hasElement(argv, "-v") {
		t.Fatalf("an unnamed workdir must not mount the host: %v", argv)
	}
	tailArgv := tail(argv, len(argv)-3)
	if !equalStrings(tailArgv, []string{DefaultImage, "sleep", "infinity"}) {
		t.Fatalf("the container must idle on the default image, got %v", tailArgv)
	}
	for _, element := range argv {
		if strings.ContainsAny(element, "/.") && strings.HasPrefix(element, "agenthub-") {
			t.Fatalf("a container name must stay legal for docker: %q", element)
		}
	}
}

func TestDockerCreateArgsMountTheWorkdirAsRequested(t *testing.T) {
	provider := NewDockerProvider(ProviderDocker, "", "", "")
	writable := false

	argv := provider.createArgs(Spec{
		LeaseID: "l1",
		Profile: Profile{
			WorkDir:      "/host/work",
			ReadonlyRoot: &writable,
			Network:      NetworkBridge,
			Image:        "alpine:3",
		},
	})

	if hasElement(argv, "--read-only") {
		t.Fatalf("an explicit writable root must not be overridden: %v", argv)
	}
	if !hasPair(argv, "--network", NetworkBridge) {
		t.Fatalf("a bridge profile must reach the daemon: %v", argv)
	}
	if !hasPair(argv, "-v", "/host/work:/work") {
		t.Fatalf("a writable workdir must not be marked read-only: %v", argv)
	}
	if hasPrefixElement(argv, "/work:rw,nosuid") {
		t.Fatalf("a named workdir replaces the anonymous tmpfs: %v", argv)
	}
	if !hasElement(argv, "alpine:3") {
		t.Fatalf("the image the resource asked for must be used: %v", argv)
	}
}

func TestDockerCreateArgsMarkAReadonlyWorkdirAsReadonly(t *testing.T) {
	provider := NewDockerProvider(ProviderDocker, "", "", "")
	argv := provider.createArgs(Spec{LeaseID: "l1", Profile: Profile{WorkDir: "/host/work"}})
	if !hasPair(argv, "-v", "/host/work:/work:ro") {
		t.Fatalf("read-only is the default, so the mount must say so: %v", argv)
	}
}

func TestDockerDescribeReportsTheRuntimeNotTheBinary(t *testing.T) {
	cases := []struct {
		runtime   string
		layer     Layer
		isolation string
	}{
		{"", LayerContainer, IsolationContainer},
		{"runsc", LayerUserKernel, IsolationUserKernel},
		{"gvisor", LayerUserKernel, IsolationUserKernel},
		{"kata", LayerMicroVM, IsolationMicroVM},
	}
	for _, tc := range cases {
		provider := NewDockerProvider(ProviderDocker, "", tc.runtime, "")
		described, err := provider.Describe(context.Background())
		if err != nil {
			t.Fatalf("describe %q: %v", tc.runtime, err)
		}
		if described.Layer != tc.layer || described.Isolation != tc.isolation {
			t.Fatalf("runtime %q must report %s/%s, got %s/%s",
				tc.runtime, tc.layer, tc.isolation, described.Layer, described.Isolation)
		}
		argv := provider.createArgs(Spec{LeaseID: "l1"})
		if tc.runtime == "" {
			if hasElement(argv, "--runtime") {
				t.Fatalf("a plain container must not pass --runtime: %v", argv)
			}
			continue
		}
		if !hasPair(argv, "--runtime", tc.runtime) {
			t.Fatalf("runtime %q must reach the daemon: %v", tc.runtime, argv)
		}
	}
}

func TestDockerExecArgsNeverBuildAShellLine(t *testing.T) {
	provider := NewDockerProvider(ProviderDocker, "", "", "")
	argv := provider.execArgs("ctr-1", ExecRequest{
		Command: "printf",
		Args:    []string{"%s", "a b", "; rm -rf /", "$(id)"},
		Dir:     "/work",
		Env:     map[string]string{"ZED": "1", "LANG": "C"},
	})

	if !hasPair(argv, "-w", "/work") {
		t.Fatalf("the requested directory must be passed as a value: %v", argv)
	}
	// Environment is sorted so the argv is stable across map iteration.
	if !(hasPair(argv, "-e", "LANG=C") && hasPair(argv, "-e", "ZED=1")) {
		t.Fatalf("allow-listed environment must be passed element by element: %v", argv)
	}
	if strings.Index(strings.Join(argv, "\x00"), "LANG=C") > strings.Index(strings.Join(argv, "\x00"), "ZED=1") {
		t.Fatalf("environment order must be deterministic: %v", argv)
	}
	command := tail(argv, len(argv)-6)
	want := []string{"ctr-1", "printf", "%s", "a b", "; rm -rf /", "$(id)"}
	if !equalStrings(command, want) {
		t.Fatalf("arguments must stay one element each, got %v", command)
	}
	for _, element := range argv {
		if element == "sh" || element == "-c" || element == "bash" {
			t.Fatalf("no shell may appear in an exec argv: %v", argv)
		}
	}
}

func TestDockerEnvNamesTheSocketWithoutLeakingIt(t *testing.T) {
	plain := NewDockerProvider(ProviderDocker, "", "", "")
	if hasPrefixElement(plain.env(), "DOCKER_HOST=") {
		t.Fatalf("an unset host must not invent DOCKER_HOST: %v", plain.env())
	}

	proxy := NewDockerProvider("podman", "", "", "tcp://socket-proxy:2375")
	if !hasElement(proxy.env(), "DOCKER_HOST=tcp://socket-proxy:2375") {
		t.Fatalf("a configured host must reach the CLI: %v", proxy.env())
	}
	described, err := proxy.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	// The binary defaults to the name, so a bare podman entry still probes podman.
	if !equalStrings(described.Requires, []string{"podman"}) {
		t.Fatalf("the descriptor must name the binary it needs, got %v", described.Requires)
	}
}

func TestDockerProbeReportsAMissingBinaryInsteadOfFailing(t *testing.T) {
	provider := NewDockerProvider("agenthub-no-such-runtime", "", "", "")
	availability, err := provider.Probe(context.Background())
	if err != nil {
		t.Fatalf("a probe must answer, not fail: %v", err)
	}
	if availability.Available {
		t.Fatal("a binary that is not there cannot be available")
	}
	// The reason is what the console prints, so it must say something.
	if strings.TrimSpace(availability.Reason) == "" {
		t.Fatal("an unavailable backend must carry a reason")
	}
}

func TestShortIDKeepsContainerNamesLegal(t *testing.T) {
	cases := map[string]string{
		"lease-abc":                    "lease-abc",
		"lease abc/../def":             "leaseabcdef",
		"":                             "lease",
		"../../":                       "lease",
		strings.Repeat("a", 40):        strings.Repeat("a", 24),
		"LEASE_1.with.dots":            "LEASE_1withdots",
		"名字ü":                         "lease",
		"lease-0000-1111-2222-3333-444": "lease-0000-1111-2222-333",
	}
	for in, want := range cases {
		if got := shortID(in); got != want {
			t.Fatalf("shortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBwrapUnsharesTheNetworkUnlessItIsGranted(t *testing.T) {
	closed := bwrapArgs(Profile{Network: NetworkNone}, ExecRequest{Command: "true"})
	if !hasElement(closed, "--unshare-all") || !hasElement(closed, "--die-with-parent") {
		t.Fatalf("the sandbox must unshare and die with its parent: %v", closed)
	}
	if hasElement(closed, "--share-net") {
		t.Fatalf("no network grant must mean no loopback-free netns: %v", closed)
	}
	if !hasElement(closed, "--clearenv") || !hasPair(closed, "--chdir", "/work") {
		t.Fatalf("the sandbox must start in a cleared, known directory: %v", closed)
	}
	if !hasPair(closed, "--tmpfs", "/work") {
		t.Fatalf("an unnamed workdir must be an in-sandbox tmpfs: %v", closed)
	}

	for _, network := range []string{NetworkBridge, NetworkAllowlist, NetworkPlatform} {
		open := bwrapArgs(Profile{Network: network}, ExecRequest{Command: "true"})
		if !hasElement(open, "--share-net") {
			t.Fatalf("network %q must share the host netns: %v", network, open)
		}
	}
}

func TestBwrapBindsTheWorkdirAndOnlyForwardsAllowedEnvironment(t *testing.T) {
	argv := bwrapArgs(
		Profile{WorkDir: "/host/work", EnvAllow: []string{"LANG"}},
		ExecRequest{
			Command: "printf",
			Args:    []string{"a b"},
			Env:     map[string]string{"LANG": "C", "AGENTHUB_TOKEN": "secret"},
		},
	)

	if !hasPair(argv, "--bind", "/host/work") || !hasElement(argv, "/work") {
		t.Fatalf("a named workdir must be bound into the sandbox: %v", argv)
	}
	if !hasPair(argv, "--setenv", "LANG") || !hasPair(argv, "LANG", "C") {
		t.Fatalf("an allow-listed variable must be forwarded: %v", argv)
	}
	if hasElement(argv, "secret") || hasPrefixElement(argv, "AGENTHUB_TOKEN=") {
		t.Fatalf("a variable outside the allow list must never be forwarded: %v", argv)
	}
	// The command and its arguments stay separate elements after the terminator.
	command := tail(argv, len(argv)-2)
	if !equalStrings(command, []string{"printf", "a b"}) {
		t.Fatalf("the command line must not be joined, got %v", command)
	}
}

func declarativeSpec(bin string) DeclarativeSpec {
	return DeclarativeSpec{
		Name:      "helper",
		// DeclarativeSpec carries the layer as text: the operator writes it in JSON.
		Layer:     string(LayerProcess),
		Isolation: IsolationNamespace,
		Binary:    bin,
		ExecArgs:  [][]string{{bin, "{command}"}, {"{arg}"}},
	}
}

func TestADeclarativeSpecIsRefusedBeforeItCanRun(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tool")

	cases := []struct {
		name   string
		mutate func(*DeclarativeSpec)
	}{
		{"no name", func(s *DeclarativeSpec) { s.Name = "" }},
		{"reserved off", func(s *DeclarativeSpec) { s.Name = ProviderOff }},
		{"reserved mock", func(s *DeclarativeSpec) { s.Name = ProviderMock }},
		{"relative binary", func(s *DeclarativeSpec) { s.Binary = "tool" }},
		{"empty binary", func(s *DeclarativeSpec) { s.Binary = " " }},
		{"no exec argv", func(s *DeclarativeSpec) { s.ExecArgs = nil }},
		{
			"three exec segments",
			func(s *DeclarativeSpec) { s.ExecArgs = [][]string{{bin}, {bin}, {bin}} },
		},
		{"unknown placeholder", func(s *DeclarativeSpec) { s.ExecArgs = [][]string{{bin, "{nope}"}} }},
		{"empty env placeholder", func(s *DeclarativeSpec) { s.ExecArgs = [][]string{{bin, "{env:}"}} }},
		{"bad probe placeholder", func(s *DeclarativeSpec) { s.ProbeArgs = [][]string{{bin, "{wat}"}} }},
		{"empty destroy argv", func(s *DeclarativeSpec) { s.DestroyArgs = [][]string{{}} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := declarativeSpec(bin)
			tc.mutate(&spec)
			if _, err := NewDeclarativeProvider(spec); err == nil {
				t.Fatalf("%s must be refused at load time", tc.name)
			} else if code := codeOf(t, err); code != CodePolicyDenied {
				t.Fatalf("%s must be a policy denial, got %s", tc.name, code)
			}
		})
	}
}

func TestADeclarativeProviderDescribesWhatTheOperatorDeclared(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tool")
	spec := declarativeSpec(bin)
	provider, err := NewDeclarativeProvider(spec)
	if err != nil {
		t.Fatalf("spec must load: %v", err)
	}
	if provider.Name() != "helper" {
		t.Fatalf("the catalogue key is the declared name, got %q", provider.Name())
	}
	if provider.Spec().Binary != bin {
		t.Fatal("the definition must round-trip for the console")
	}
	described, err := provider.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Source != SourceDeclarative {
		t.Fatalf("declarative providers must be marked as such, got %q", described.Source)
	}
	if !equalStrings(described.Requires, []string{bin}) {
		t.Fatalf("the descriptor must name the binary it needs, got %v", described.Requires)
	}

	// Missing optional fields fall back rather than describing a zero value.
	sparse, err := NewDeclarativeProvider(DeclarativeSpec{
		Name:     "sparse",
		Binary:   bin,
		ExecArgs: [][]string{{bin}},
	})
	if err != nil {
		t.Fatalf("a minimal spec must load: %v", err)
	}
	defaults, _ := sparse.Describe(context.Background())
	if defaults.Layer != LayerProcess || defaults.Isolation != IsolationNamespace || defaults.License != "unknown" {
		t.Fatalf("a sparse spec must describe process/namespace/unknown, got %s/%s/%s",
			defaults.Layer, defaults.Isolation, defaults.License)
	}
}

func TestDeclarativePlaceholdersExpandPerElement(t *testing.T) {
	vars := varsFor(
		Spec{
			LeaseID:    "lease-1",
			OwnerID:    "u1",
			ResourceID: "agent-1",
			Scope:      "ticket-7",
			Profile: Profile{
				WorkDir:   "/host/work",
				CPU:       2,
				MemoryMB:  256,
				PidsLimit: 8,
				Network:   NetworkBridge,
				TimeoutMS: 1500,
			},
		},
		"ls",
		map[string]string{"FOO": "bar"},
	)

	argv, err := expandArgv(
		[]string{"{command}", "{arg}", "{env:FOO}", "{workdir}", "{network}", "{timeout_s}", "{lease_id}", "{scope}", "literal"},
		vars,
		"-l",
	)
	if err != nil {
		t.Fatalf("expansion: %v", err)
	}
	want := []string{"ls", "-l", "bar", "/host/work", NetworkBridge, "1", "lease-1", "ticket-7", "literal"}
	if !equalStrings(argv, want) {
		t.Fatalf("expansion produced %v, want %v", argv, want)
	}

	// An unresolved placeholder is a bug brought forward, not a literal to pass on.
	if _, err := expandArgv([]string{"{missing}"}, vars); err == nil {
		t.Fatal("an unknown placeholder must fail instead of passing as literal text")
	} else if code := codeOf(t, err); code != CodePolicyDenied {
		t.Fatalf("an unknown placeholder is a policy denial, got %s", code)
	}

	// A multi-line expansion stays one element: that is the whole point of never
	// joining the argv into a shell string.
	multi, err := expandArgv([]string{"{arg}"}, vars, "a b;c\nd")
	if err != nil || len(multi) != 1 || multi[0] != "a b;c\nd" {
		t.Fatalf("an argument must stay one element, got %v (%v)", multi, err)
	}
}

func TestAStructuredReceiptUpgradesTheExitCode(t *testing.T) {
	cases := []struct {
		name    string
		stdout  string
		want    ExecResult
		receipt string
	}{
		{
			name:   "plain output stays an exit-code result",
			stdout: "hello",
			want:   ExecResult{ExitCode: 0, Stdout: "hello", Receipt: "exit"},
		},
		{
			name:    "json becomes the receipt",
			stdout:  `{"exit_code":2,"stdout":"out","stderr":"err","truncated":true,"duration_ms":42}`,
			want:    ExecResult{ExitCode: 2, Stdout: "out", Stderr: "err", Truncated: true, DurationMS: 42},
			receipt: "json",
		},
		{
			name:    "a json fragment is marked unparsed",
			stdout:  "{not json",
			want:    ExecResult{ExitCode: 0, Stdout: "{not json", Receipt: "unparsed"},
			receipt: "unparsed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withReceipt(ExecResult{Stdout: tc.stdout, Receipt: "exit"})
			tc.want.Provider = ""
			if tc.receipt != "" {
				tc.want.Receipt = tc.receipt
			}
			if got.ExitCode != tc.want.ExitCode || got.Stdout != tc.want.Stdout || got.Receipt != tc.want.Receipt {
				t.Fatalf("receipt = %+v, want %+v", got, tc.want)
			}
			if got.Truncated != tc.want.Truncated || got.DurationMS != tc.want.DurationMS || got.Stderr != tc.want.Stderr {
				t.Fatalf("receipt did not carry the structured fields: %+v", got)
			}
		})
	}
}

func TestDeclarativeCreateAndDestroyHandleTheEmptyCase(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tool")
	spec := declarativeSpec(bin)
	spec.CreateArgs = nil
	spec.DestroyArgs = nil
	provider, err := NewDeclarativeProvider(spec)
	if err != nil {
		t.Fatalf("spec must load: %v", err)
	}

	// A process sandbox wraps one command at a time, so "create" is a lease id.
	handle, err := provider.Create(context.Background(), Spec{LeaseID: "lease-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if handle.Ref != "lease-1" || handle.Provider != "helper" {
		t.Fatalf("a process sandbox must record the lease id, got %+v", handle)
	}
	if err := provider.Destroy(context.Background(), handle); err != nil {
		t.Fatalf("destroy without a template must be a no-op: %v", err)
	}
	if state, err := provider.Status(context.Background(), handle); err != nil || state != StateReady {
		t.Fatalf("status must be ready, got %q (%v)", state, err)
	}
	if _, err := provider.Attach(context.Background(), handle, ExecRequest{}); err == nil {
		t.Fatal("attach must be refused: one command at a time is the contract")
	} else if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("attach refusal must be unsupported, got %s", code)
	}

	availability, err := provider.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !availability.Available {
		t.Fatal("a provider with no probe argv trusts the operator and reports available")
	}
}

func TestDeclarativeExecRefusesAMissingBinaryWithACode(t *testing.T) {
	// A configured tool that is not on the host is a host problem, so it must
	// surface as unsupported rather than as a zero-exit success.
	bin := filepath.Join(t.TempDir(), "not-here")
	provider, err := NewDeclarativeProvider(declarativeSpec(bin))
	if err != nil {
		t.Fatalf("spec must load: %v", err)
	}
	handle := Handle{ID: "lease-1", Provider: "helper", Ref: "ref-1", Spec: Spec{LeaseID: "lease-1"}}

	result, err := provider.Exec(context.Background(), handle, ExecRequest{Command: "true"})
	if err == nil {
		t.Fatalf("a missing tool must not report success: %+v", result)
	}
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("a missing tool is unsupported, got %s", code)
	}
}

func TestProfileAndRequestResolveTheirCeilings(t *testing.T) {
	if got := (Profile{}).Timeout(); got != time.Minute {
		t.Fatalf("an unset timeout must fall back to a minute, got %s", got)
	}
	if got := (Profile{TimeoutMS: 1500}).Timeout(); got != 1500*time.Millisecond {
		t.Fatalf("a set timeout must be honoured, got %s", got)
	}
	if got := (Profile{}).OutputCap(); got != 64<<10 {
		t.Fatalf("an unset cap must be 64KiB, got %d", got)
	}
	if got := (Profile{MaxOutputBytes: 1024}).OutputCap(); got != 1024 {
		t.Fatalf("a set cap must be honoured, got %d", got)
	}
	if got := (ExecRequest{}).OutputCap(); got != 64<<10 {
		t.Fatalf("a request without a cap must not be unbounded, got %d", got)
	}
	if got := (ExecRequest{MaxOutputBytes: 2048}).OutputCap(); got != 2048 {
		t.Fatalf("a request cap must be honoured, got %d", got)
	}

	// The router's ticket wins over the context, which is what makes a multi-step
	// run share one environment.
	if got := ScopeFor(map[string]any{"ticket": "ticket-9"}, "ctx-1"); got != "ticket-9" {
		t.Fatalf("the ticket must win, got %q", got)
	}
	if got := ScopeFor(map[string]any{"ticket": "  "}, "ctx-1"); got != "ctx-1" {
		t.Fatalf("a blank ticket must fall through to the context, got %q", got)
	}
	if got := ScopeFor(nil, ""); got == "" {
		t.Fatal("the default scope must be non-empty so leases stay separable")
	}
}

func TestTheSweepBeatCountsPassesAndKicks(t *testing.T) {
	passed := make(chan struct{}, 8)
	sweeper := NewSweeper(SweeperOptions{
		Interval: time.Hour,
		Sweep: func(context.Context) (int, error) {
			passed <- struct{}{}
			return 2, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(ctx) }()

	expectPass := func() {
		t.Helper()
		select {
		case <-passed:
		case <-time.After(2 * time.Second):
			t.Fatal("the beat never ran a pass")
		}
	}

	sweeper.Kick()
	expectPass()
	sweeper.Kick()
	expectPass()

	sweeper.Stop()
	sweeper.Wait(2 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("run must return cleanly on stop: %v", err)
	}

	passes, reclaimed, lastAt, lastErr := sweeper.Counters()
	if passes < 2 || reclaimed < 4 {
		t.Fatalf("the counters must add up, got passes=%d reclaimed=%d", passes, reclaimed)
	}
	if lastAt.IsZero() {
		t.Fatal("the console needs to know when the last sweep happened")
	}
	if lastErr != "" {
		t.Fatalf("a clean pass must clear the error, got %q", lastErr)
	}
}

func TestASweepFailureIsRecordedAndClearedByTheNextPass(t *testing.T) {
	calls := 0
	passed := make(chan struct{}, 4)
	sweeper := NewSweeper(SweeperOptions{
		Interval: time.Hour,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sweep: func(context.Context) (int, error) {
			calls++
			passed <- struct{}{}
			if calls == 1 {
				return 0, errors.New("daemon gone")
			}
			return 1, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sweeper.Run(ctx) }()

	wait := func() {
		t.Helper()
		select {
		case <-passed:
		case <-time.After(2 * time.Second):
			t.Fatal("the beat never ran")
		}
	}

	sweeper.Kick()
	wait()
	if _, _, _, lastErr := sweeper.Counters(); lastErr != "daemon gone" {
		t.Fatalf("a failed sweep must be visible to the console, got %q", lastErr)
	}

	sweeper.Kick()
	wait()
	if _, reclaimed, _, lastErr := sweeper.Counters(); lastErr != "" || reclaimed != 1 {
		t.Fatalf("the next pass must clear the error and count the reclaim, got %q/%d", lastErr, reclaimed)
	}
	sweeper.Stop()
	sweeper.Wait(time.Second)
}

func TestStopIsSafeTwiceAndBeforeRun(t *testing.T) {
	sweeper := NewSweeper(SweeperOptions{})
	sweeper.Stop()
	sweeper.Stop()
	// Wait returns immediately rather than blocking forever when nothing ran.
	sweeper.Wait(50 * time.Millisecond)
	if passes, reclaimed, _, _ := sweeper.Counters(); passes != 0 || reclaimed != 0 {
		t.Fatalf("a beat that never ran must count nothing, got %d/%d", passes, reclaimed)
	}
}

func TestNodesAreOnlineOnlyWhileTheirHeartbeatIsFresh(t *testing.T) {
	service, tick := newTestService(t, func(_ *ManagerOptions, opts *ServiceOptions) {
		opts.NodeOnline = time.Minute
	})
	ctx := context.Background()

	node := &Node{
		ID:           "node-1",
		OwnerID:      "u1",
		Name:         "desk",
		Capabilities: []string{ProviderMock},
		LastSeenAt:   tick.now(),
	}
	if err := service.RegisterNode(ctx, node); err != nil {
		t.Fatalf("register: %v", err)
	}

	nodes, err := service.Nodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Status != NodeOnline {
		t.Fatalf("a fresh node must read online, got %+v (%v)", nodes, err)
	}

	tick.advance(2 * time.Minute)
	nodes, err = service.Nodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Status != NodeOffline {
		t.Fatalf("a stale node must read offline, got %+v (%v)", nodes, err)
	}
	if stats := service.Stats(ctx); stats.OnlineNodes != 0 {
		t.Fatalf("the water level must not count a stale node, got %d", stats.OnlineNodes)
	}

	if err := service.TouchNode(ctx, "node-1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if stats := service.Stats(ctx); stats.OnlineNodes != 1 {
		t.Fatalf("a heartbeat must bring the node back, got %d", stats.OnlineNodes)
	}

	if _, err := service.Node(ctx, "node-1"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := service.Node(ctx, "node-missing"); err == nil {
		t.Fatal("an unknown node must be a not-found, not a nil")
	} else if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("an unknown node must be not found, got %s", code)
	}
	if err := service.TouchNode(ctx, "node-missing"); err == nil {
		t.Fatal("a heartbeat for an unknown node must fail")
	} else if code := codeOf(t, err); code != CodeNotFound {
		t.Fatalf("a heartbeat for an unknown node is not found, got %s", code)
	}

	if err := service.RemoveNode(ctx, "node-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := service.Node(ctx, "node-1"); err == nil {
		t.Fatal("a removed node must be gone")
	}
}

func TestASweepReclaimsAnExpiredLease(t *testing.T) {
	service, tick := newTestService(t, nil)
	ctx := context.Background()

	first, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// The default idle window in this harness is ten minutes.
	tick.advance(11 * time.Minute)

	reclaimed, err := service.sweepOnce(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("the expired lease must be reclaimed, got %d", reclaimed)
	}

	// The row stays for the audit trail, but the environment is gone: the next
	// call builds a new one instead of handing back a destroyed sandbox.
	next, err := service.Acquire(ctx, LeaseRequest{OwnerID: "u1", ResourceID: "agent-1"})
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if next.ID == first.ID {
		t.Fatalf("a reclaimed lease must not be reused, got %s twice", first.ID)
	}
	live, err := service.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if live.State != StateExpired {
		t.Fatalf("the reclaimed lease must read as expired, got %q", live.State)
	}
}

func TestTheFacadeReplaysItsOwnSubjectsOnly(t *testing.T) {
	service, _ := newTestService(t, nil)
	ctx := context.Background()
	store := service.manager.opts.Store

	if err := store.AppendEvent(ctx, &Event{Kind: EventLeaseCreated, Subject: subjectFor("u1")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.AppendEvent(ctx, &Event{Kind: EventLeaseCreated, Subject: subjectFor("u2")}); err != nil {
		t.Fatalf("append: %v", err)
	}

	events, maxSeq, err := service.EventsSince(ctx, "u1", 0, 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(events) != 1 || events[0].Subject != subjectFor("u1") {
		t.Fatalf("an owner must only replay their own frames, got %+v", events)
	}
	if maxSeq == 0 {
		t.Fatal("a console needs the current sequence to know it has caught up")
	}

	// A live console receives what the bus fans out for its subject.
	ch, release := service.Subscribe("u1")
	defer release()
	service.bus.Publish(Event{Kind: EventLeaseRenewed, Subject: subjectFor("u1")})
	select {
	case frame := <-ch:
		if frame.Kind != EventLeaseRenewed {
			t.Fatalf("unexpected frame %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a subscriber never received its frame")
	}
}

func TestSubscribeWithoutABusIsClosedRatherThanSilent(t *testing.T) {
	service, _ := newTestService(t, func(_ *ManagerOptions, opts *ServiceOptions) {
		opts.Bus = nil
	})
	ch, release := service.Subscribe("u1")
	if _, open := <-ch; open {
		t.Fatal("a service without a bus must hand out a closed stream, not one that never speaks")
	}
	release() // must be safe to call on the closed path
}

func TestRefreshingTheCatalogueReprobesEveryBackend(t *testing.T) {
	service, _ := newTestService(t, nil)
	rows := service.RefreshCatalog(context.Background())
	if len(rows) != 1 {
		t.Fatalf("the mock backend is the only one registered here, got %d rows", len(rows))
	}
	if rows[0].Name != ProviderMock || !rows[0].Available {
		t.Fatalf("a refresh must report availability, got %+v", rows[0])
	}
	if len(service.Catalog(context.Background())) != 1 {
		t.Fatal("the catalogue must keep the backend after a probe")
	}
}

func TestNopStoreKeepsTheControlPlaneRunningWithoutAnAuditTrail(t *testing.T) {
	ctx := context.Background()
	store := NopStore()

	// Every write succeeds and every read is empty: an instance without a database
	// still leases correctly, it just has nothing to replay afterwards.
	if err := store.SaveLease(ctx, &Lease{ID: "l1"}); err != nil {
		t.Fatalf("save lease: %v", err)
	}
	if lease, err := store.GetLease(ctx, "l1"); lease != nil || err != nil {
		t.Fatalf("a nop store must not remember leases, got %+v (%v)", lease, err)
	}
	if lease, err := store.FindLease(ctx, "u1", "agent-1", "", ProviderMock); lease != nil || err != nil {
		t.Fatalf("a nop store must not find leases, got %+v (%v)", lease, err)
	}
	if leases, err := store.ListLeases(ctx, "u1"); leases != nil || err != nil {
		t.Fatalf("a nop store must list nothing, got %+v (%v)", leases, err)
	}
	if expired, err := store.ExpiredLeases(ctx, time.Now(), 10); expired != nil || err != nil {
		t.Fatalf("a nop store has nothing to expire, got %+v (%v)", expired, err)
	}
	if count, err := store.CountLeases(ctx, ""); count != 0 || err != nil {
		t.Fatalf("a nop store counts nothing, got %d (%v)", count, err)
	}
	if err := store.AppendExec(ctx, &ExecRecord{ID: "e1"}); err != nil {
		t.Fatalf("append exec: %v", err)
	}
	if execs, err := store.ListExecs(ctx, "l1", 10); execs != nil || err != nil {
		t.Fatalf("a nop store has no audit rows, got %+v (%v)", execs, err)
	}
	if count, err := store.CountExecsSince(ctx, time.Now()); count != 0 || err != nil {
		t.Fatalf("a nop store counts nothing, got %d (%v)", count, err)
	}
	if err := store.SaveNode(ctx, &Node{ID: "n1"}); err != nil {
		t.Fatalf("save node: %v", err)
	}
	if node, err := store.GetNode(ctx, "n1"); node != nil || err != nil {
		t.Fatalf("a nop store must not remember nodes, got %+v (%v)", node, err)
	}
	if nodes, err := store.ListNodes(ctx); nodes != nil || err != nil {
		t.Fatalf("a nop store lists no nodes, got %+v (%v)", nodes, err)
	}
	if err := store.DeleteNode(ctx, "n1"); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if err := store.SaveJob(ctx, &Job{ID: "j1"}); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := store.AppendEvent(ctx, &Event{Kind: EventLeaseCreated}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if events, err := store.EventsSince(ctx, "owner:u1", 0, 10); events != nil || err != nil {
		t.Fatalf("a nop store cannot replay, got %+v (%v)", events, err)
	}
	if seq, err := store.MaxSeq(ctx); seq != 0 || err != nil {
		t.Fatalf("a nop store has no sequence, got %d (%v)", seq, err)
	}
}
