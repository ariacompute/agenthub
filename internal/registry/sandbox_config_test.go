package registry

import (
	"reflect"
	"testing"

	"agenthub/internal/httputil"
	"agenthub/internal/sandbox"
)

// ---------------------------------------------------------------------------
// kind validity
// ---------------------------------------------------------------------------

func TestKindValidAcceptsOnlyTheSixClasses(t *testing.T) {
	for _, k := range Kinds {
		if !k.Valid() {
			t.Fatalf("%q must be valid", k)
		}
	}
	for _, raw := range []Kind{"", "MODEL", "tool", "agent "} {
		if raw.Valid() {
			t.Fatalf("%q must not be valid", raw)
		}
	}
}

// ---------------------------------------------------------------------------
// fieldError: the machine code travels, the field only annotates
// ---------------------------------------------------------------------------

func TestFieldErrorRendersCodeAloneWhenNoFieldIsNamed(t *testing.T) {
	err := newFieldError(CodeInvalidEntry, "")
	if got := err.Error(); got != string(CodeInvalidEntry) {
		t.Fatalf("Error() = %q, want %q", got, CodeInvalidEntry)
	}
	if field := FieldOf(err); field != "" {
		t.Fatalf("FieldOf = %q, want empty", field)
	}

	named := newFieldError(CodeInvalidKindConfig, "sandbox.provider")
	if got := named.Error(); got != string(CodeInvalidKindConfig)+":sandbox.provider" {
		t.Fatalf("Error() = %q", got)
	}
	if field := FieldOf(named); field != "sandbox.provider" {
		t.Fatalf("FieldOf = %q", field)
	}
}

// ---------------------------------------------------------------------------
// SandboxConfig -> sandbox.Profile
// ---------------------------------------------------------------------------

func TestSandboxConfigProfileIsAPureCopy(t *testing.T) {
	var nilCfg *SandboxConfig
	if got := nilCfg.Profile(); !reflect.DeepEqual(got, sandbox.Profile{}) {
		t.Fatalf("nil config must convert to the zero profile, got %+v", got)
	}

	readonly := false
	c := &SandboxConfig{
		Provider:       " bwrap ",
		Runtime:        " runsc ",
		Image:          " img ",
		WorkDir:        " /work ",
		ReadonlyRoot:   &readonly,
		Network:        sandbox.NetworkNone,
		EgressAllow:    []string{"api.example"},
		CPU:            1.5,
		MemoryMB:       512,
		PidsLimit:      64,
		TimeoutMS:      5000,
		MaxOutputBytes: 4096,
		EnvAllow:       []string{"PATH"},
		ArgsPolicy:     sandbox.ArgsPrefix,
		ArgsAllowlist:  []string{"--safe"},
		ReuseScope:     "thread",
		IdleTTLSeconds: 60,
	}
	got := c.Profile()
	if got.Provider != "bwrap" || got.Runtime != "runsc" || got.Image != "img" || got.WorkDir != "/work" {
		t.Fatalf("names must be trimmed, got %+v", got)
	}
	if got.ReadonlyRoot == nil || *got.ReadonlyRoot != false {
		t.Fatalf("ReadonlyRoot must survive conversion, got %+v", got.ReadonlyRoot)
	}
	if got.CPU != 1.5 || got.MemoryMB != 512 || got.PidsLimit != 64 ||
		got.TimeoutMS != 5000 || got.MaxOutputBytes != 4096 || got.IdleTTLSeconds != 60 {
		t.Fatalf("limits must be copied, got %+v", got)
	}
	if len(got.EgressAllow) != 1 || len(got.EnvAllow) != 1 || len(got.ArgsAllow) != 1 {
		t.Fatalf("allowlists must be copied, got %+v", got)
	}
	if got.ArgsAllow[0] != "--safe" || got.ArgsPolicy != sandbox.ArgsPrefix || got.ReuseScope != "thread" {
		t.Fatalf("argument policy must be copied, got %+v", got)
	}
}

func TestEffectiveArgsPolicyPrefersTheSandboxBlock(t *testing.T) {
	cli := &CLIConfig{ArgsPolicy: sandbox.ArgsPassthrough}

	cases := []struct {
		name string
		cfg  *SandboxConfig
		cli  *CLIConfig
		want string
	}{
		{"nil blocks", nil, nil, sandbox.ArgsNone},
		{"cli only", nil, cli, sandbox.ArgsPassthrough},
		{"sandbox block wins", &SandboxConfig{ArgsPolicy: sandbox.ArgsPrefix}, cli, sandbox.ArgsPrefix},
		{"blank sandbox falls through", &SandboxConfig{ArgsPolicy: "  "}, cli, sandbox.ArgsPassthrough},
		{"blank everywhere means none", &SandboxConfig{ArgsPolicy: " "}, &CLIConfig{}, sandbox.ArgsNone},
		{"unknown policy degrades to none", &SandboxConfig{ArgsPolicy: "whatever"}, nil, sandbox.ArgsNone},
	}
	for _, tc := range cases {
		if got := tc.cfg.EffectiveArgsPolicy(tc.cli); got != tc.want {
			t.Fatalf("%s: EffectiveArgsPolicy = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// validateSandbox
// ---------------------------------------------------------------------------

func TestValidateSandboxRefusesWhatItCannotHonour(t *testing.T) {
	if err := validateSandbox(nil); err != nil {
		t.Fatalf("a resource without a sandbox block inherits the instance default: %v", err)
	}

	ok := &SandboxConfig{Provider: "bwrap", Network: sandbox.NetworkNone, ArgsPolicy: sandbox.ArgsNone}
	if err := validateSandbox(ok); err != nil {
		t.Fatalf("a sane policy must pass: %v", err)
	}

	cases := []struct {
		name  string
		cfg   *SandboxConfig
		field string
	}{
		{"provider is a path", &SandboxConfig{Provider: "bin/bwrap"}, "sandbox.provider"},
		{"provider has a space", &SandboxConfig{Provider: "bwrap runsc"}, "sandbox.provider"},
		{"negative cpu", &SandboxConfig{CPU: -1}, "sandbox"},
		{"negative memory", &SandboxConfig{MemoryMB: -1}, "sandbox"},
		{"negative pids", &SandboxConfig{PidsLimit: -1}, "sandbox"},
		{"negative timeout", &SandboxConfig{TimeoutMS: -1}, "sandbox"},
		{"negative output cap", &SandboxConfig{MaxOutputBytes: -1}, "sandbox"},
		{"negative idle ttl", &SandboxConfig{IdleTTLSeconds: -1}, "sandbox"},
		// ValidateProfile already refuses a prefix policy with no prefixes, so the
		// narrower check below it stays as belt and braces.
		{"prefix with no prefixes", &SandboxConfig{ArgsPolicy: sandbox.ArgsPrefix}, "sandbox"},
		// A work directory is a handle on the host, so it is judged when the
		// record is written rather than when the task runs.
		{"the volume root as a workdir", &SandboxConfig{WorkDir: "/"}, "sandbox.work_dir"},
		{"a system tree as a workdir", &SandboxConfig{WorkDir: "/etc/ssl"}, "sandbox.work_dir"},
		{"a relative workdir", &SandboxConfig{WorkDir: "work"}, "sandbox.work_dir"},
		{"a workdir with an option separator", &SandboxConfig{WorkDir: "/srv/work,rw"}, "sandbox.work_dir"},
	}
	for _, tc := range cases {
		err := validateSandbox(tc.cfg)
		if err == nil {
			t.Fatalf("%s must be refused at registration", tc.name)
		}
		if got := FieldOf(err); got != tc.field {
			t.Fatalf("%s: field = %q, want %q", tc.name, got, tc.field)
		}
	}

	// An unknown network name is not an error: it normalises to "none", the
	// fail-closed answer, so a record written against a future vocabulary keeps
	// loading without gaining network it was never granted.
	if err := validateSandbox(&SandboxConfig{Network: "wide-open"}); err != nil {
		t.Fatalf("an unknown network must degrade to none, not fail: %v", err)
	}
}

// TestValidateLegacyWorkDirMatchesTheSandboxBlock keeps the two places a host
// directory may be named from drifting apart: cli.work_dir predates the sandbox
// block but still feeds the resolved profile.
func TestValidateLegacyWorkDirMatchesTheSandboxBlock(t *testing.T) {
	if err := validateLegacyWorkDir(nil); err != nil {
		t.Fatalf("a resource without a cli block has no work directory: %v", err)
	}
	if err := validateLegacyWorkDir(&CLIConfig{}); err != nil {
		t.Fatalf("an empty work directory means an in-sandbox tmpfs: %v", err)
	}
	if err := validateLegacyWorkDir(&CLIConfig{WorkDir: " /srv/work "}); err != nil {
		t.Fatalf("a data path must be accepted: %v", err)
	}
	err := validateLegacyWorkDir(&CLIConfig{WorkDir: "/root"})
	if err == nil {
		t.Fatal("a system tree must be refused")
	}
	if got := FieldOf(err); got != "sandbox.work_dir" {
		t.Fatalf("field = %q, want sandbox.work_dir", got)
	}
}

// ---------------------------------------------------------------------------
// the sandbox checker port
// ---------------------------------------------------------------------------

func sandboxEntry(provider string) *Entry {
	return &Entry{
		Kind:     KindMCP,
		Name:     "mcp-with-sandbox",
		Endpoint: "stdio://mcp",
		KindConfig: &KindConfig{
			Sandbox: &SandboxConfig{Provider: provider},
		},
	}
}

func TestUpsertConsultsTheSandboxCheckerOnlyWhenAProviderIsNamed(t *testing.T) {
	var seen []string
	reg := New().WithSandboxChecker(func(name string) error {
		seen = append(seen, name)
		return nil
	})

	if _, err := reg.Upsert(sandboxEntry("bwrap")); err != nil {
		t.Fatalf("a known provider must be accepted: %v", err)
	}
	if len(seen) != 1 || seen[0] != "bwrap" {
		t.Fatalf("checker calls = %v, want one for bwrap", seen)
	}
}

func TestUpsertSkipsTheCheckerWhenNoProviderIsNamed(t *testing.T) {
	calls := 0
	reg := New().WithSandboxChecker(func(string) error {
		calls++
		return nil
	})
	// Blank means inherit: the lease reports that failure at execution time.
	for i, provider := range []string{"", "   "} {
		e := sandboxEntry(provider)
		// Names are the dedup key, so each save needs its own.
		e.Name = "mcp-blank-" + string(rune('a'+i))
		if _, err := reg.Upsert(e); err != nil {
			t.Fatalf("provider %q must be accepted: %v", provider, err)
		}
	}
	// No sandbox block at all.
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "plain", Endpoint: "stdio://plain"}); err != nil {
		t.Fatalf("an entry without a sandbox block must be accepted: %v", err)
	}
	if calls != 0 {
		t.Fatalf("checker must not run, ran %d times", calls)
	}
}

func TestUpsertRefusesAProviderThisHostCannotRun(t *testing.T) {
	reg := New().WithSandboxChecker(func(name string) error {
		return kindConfigErr("sandbox.provider")
	})
	if _, err := reg.Upsert(sandboxEntry("kata")); err == nil {
		t.Fatal("an unusable provider must be refused at registration")
	} else if got := FieldOf(err); got != "sandbox.provider" {
		t.Fatalf("field = %q, want sandbox.provider", got)
	}
}

func TestUpsertWithoutACheckerAcceptsAnyProviderName(t *testing.T) {
	// No control plane wired in: registration validates syntax only and the
	// lease is where an unusable provider shows up.
	if _, err := New().Upsert(sandboxEntry("anything-goes")); err != nil {
		t.Fatalf("nil checker must not refuse the provider: %v", err)
	}
}

// ---------------------------------------------------------------------------
// the sandbox policy port
// ---------------------------------------------------------------------------

// TestUpsertConsultsTheSandboxPolicyGate is the registration half of "a resource
// may only ask for what the instance allows": the same closure main wires from
// AGENTHUB_SANDBOX_* decides here, while the record is being written, so a
// rejected profile never reaches a lease.
func TestUpsertConsultsTheSandboxPolicyGate(t *testing.T) {
	var seen int
	reg := New().WithSandboxPolicy(func(cfg *SandboxConfig) error {
		seen++
		return sandbox.ValidateHostPolicy(cfg.Profile(), sandbox.HostPolicy{})
	})

	// A writable root is not allowed by that policy, so the save is refused.
	readWrite := false
	refused := sandboxEntry("")
	refused.KindConfig.Sandbox.ReadonlyRoot = &readWrite
	if _, err := reg.Upsert(refused); err == nil {
		t.Fatal("a profile the instance forbids must be refused at registration")
	} else if code := httputil.AsError(err).Code; code != sandbox.CodePolicyDenied {
		t.Fatalf("code = %s, want %s", code, sandbox.CodePolicyDenied)
	}
	if seen != 1 {
		t.Fatalf("gate calls = %d, want one", seen)
	}

	// The same entry with the default (read-only) root passes.
	if _, err := reg.Upsert(sandboxEntry("")); err != nil {
		t.Fatalf("a profile the instance allows must be accepted: %v", err)
	}
	if seen != 2 {
		t.Fatalf("gate calls = %d, want two", seen)
	}

	// An entry without a sandbox block inherits the instance defaults, which are
	// the operator's own values: it is not gated.
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "no-sandbox-block", Endpoint: "stdio://plain"}); err != nil {
		t.Fatalf("an entry without a sandbox block must be accepted: %v", err)
	}
	if seen != 2 {
		t.Fatalf("gate must not run for an entry without a sandbox block, ran %d times", seen)
	}

	// And an instance with no gate wired keeps the historical behaviour.
	if _, err := New().Upsert(refused); err != nil {
		t.Fatalf("nil gate must accept the profile: %v", err)
	}
}
