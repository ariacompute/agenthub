package sandbox

import (
	"context"
	"strings"
	"testing"
)

// The plumbing behind an execution: the capped writer that keeps a runaway
// command from exhausting memory, the environment rebuild that keeps the
// agenthub process's secrets out of a sandbox, the restricted local provider,
// and the bus that releases subscribers. None of it needs a database.

func TestCappedBufferKeepsTheHeadAndFlagsTheDrop(t *testing.T) {
	buffer := &cappedBuffer{limit: 5}

	if n, err := buffer.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("a write under the cap must be accepted whole, got %d (%v)", n, err)
	}
	// A partial write still reports the full length: a short write would make the
	// child's own write blow up with EPIPE instead of being truncated by us.
	if n, err := buffer.Write([]byte("defgh")); n != 5 || err != nil {
		t.Fatalf("a write over the cap must report success, got %d (%v)", n, err)
	}
	if got := buffer.String(); got != "abcde" {
		t.Fatalf("the buffer must keep the head of the output, got %q", got)
	}
	if !buffer.truncated {
		t.Fatal("dropping output must be remembered so the receipt can say truncated")
	}

	// Everything after the cap is accepted and discarded, not refused.
	if n, err := buffer.Write([]byte("zz")); n != 2 || err != nil {
		t.Fatalf("a write past the cap must be swallowed, got %d (%v)", n, err)
	}
	if got := buffer.String(); got != "abcde" {
		t.Fatalf("a write past the cap must not grow the buffer, got %q", got)
	}
}

func TestCappedBufferDoesNotCryTruncationAtTheExactCap(t *testing.T) {
	buffer := &cappedBuffer{limit: 3}
	if _, err := buffer.Write([]byte("abc")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buffer.truncated {
		t.Fatal("output that fits exactly must not be reported as truncated")
	}
}

func TestMergeEnvOnlyAddsWhatTheCallerAllowed(t *testing.T) {
	base := []string{"PATH=/usr/bin"}

	out := mergeEnv(base, map[string]string{"LANG": "C", "AGENTHUB_TOKEN": "secret"}, []string{" LANG "})
	joined := strings.Join(out, "\x00")
	if !strings.Contains(joined, "LANG=C") {
		t.Fatalf("an allowed variable must be added: %v", out)
	}
	if strings.Contains(joined, "secret") {
		t.Fatalf("a variable outside the list must never be added: %v", out)
	}
	if strings.Contains(joined, "PATH=/usr/bin") != true {
		t.Fatalf("the base environment must be preserved: %v", out)
	}

	// Nothing requested means the base is returned untouched, without copying.
	if got := mergeEnv(base, nil, nil); len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("an empty request must not rebuild the environment, got %v", got)
	}
}

func TestBaseEnvDropsTheServersOwnSecrets(t *testing.T) {
	t.Setenv("AGENTHUB_SANDBOX_LEAK", "should-not-appear")

	base := baseEnv(nil)
	if strings.Contains(strings.Join(base, "\x00"), "should-not-appear") {
		t.Fatalf("a sandbox must not inherit the server's environment: %v", base)
	}

	// The same variable is inherited once the profile names it, which is how an
	// operator opts a resource into one specific secret.
	named := baseEnv([]string{"AGENTHUB_SANDBOX_LEAK"})
	if !strings.Contains(strings.Join(named, "\x00"), "AGENTHUB_SANDBOX_LEAK=should-not-appear") {
		t.Fatalf("an explicitly allowed variable must be inherited: %v", named)
	}
}

func TestTheLocalProviderIsHonestAboutItsIsolation(t *testing.T) {
	provider := NewLocalProvider()
	ctx := context.Background()

	if provider.Name() != "local" {
		t.Fatalf("the catalogue key must be local, got %q", provider.Name())
	}
	described, err := provider.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Layer != LayerProcess || described.Isolation != IsolationNone {
		t.Fatalf("a plain child process must claim no isolation, got %s/%s", described.Layer, described.Isolation)
	}

	availability, err := provider.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !availability.Available || !strings.Contains(availability.Reason, "no isolation") {
		// "Available" without the caveat would be a green badge over the weakest
		// backend in the catalogue.
		t.Fatalf("the reason must carry the caveat, got %+v", availability)
	}

	handle, err := provider.Create(ctx, Spec{LeaseID: "lease-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if handle.Ref != "local-lease-1" || handle.Provider != "local" {
		t.Fatalf("the handle must record the lease, got %+v", handle)
	}
	if state, err := provider.Status(ctx, handle); err != nil || state != StateReady {
		t.Fatalf("status must be ready, got %q (%v)", state, err)
	}
	if err := provider.Destroy(ctx, handle); err != nil {
		t.Fatalf("destroy must be a no-op: %v", err)
	}
}

func TestLocalExecRefusesAMissingBinaryWithoutPanicking(t *testing.T) {
	provider := NewLocalProvider()
	handle := Handle{ID: "lease-1", Provider: "local", Ref: "local-lease-1"}

	result, err := provider.Exec(context.Background(), handle, ExecRequest{Command: "agenthub-no-such-command-xyz"})
	if err == nil {
		t.Fatalf("a missing binary must not look like a successful run: %+v", result)
	}
	if code := codeOf(t, err); code != CodeUnsupported {
		t.Fatalf("a missing binary is a host problem, got %s", code)
	}
	if result.Provider != "local" || result.Ref != "local-lease-1" {
		t.Fatalf("the receipt must identify the backend even on failure: %+v", result)
	}
}

func TestLocalAttachRefusesAnEmptyCommand(t *testing.T) {
	provider := NewLocalProvider()
	if _, err := provider.Attach(context.Background(), Handle{}, ExecRequest{}); err == nil {
		t.Fatal("starting a session with no command must fail")
	} else if code := codeOf(t, err); code != CodePolicyDenied {
		t.Fatalf("an unnamed command is a policy denial, got %s", code)
	}
}

func TestTheBusReleasesSubscribersAndRefusesNewOnesAfterClose(t *testing.T) {
	// A nil store and a nil clock are the "single process, no database" defaults.
	bus := NewBus(nil, nil)

	ch, release := bus.Subscribe("", 0)
	release()
	if _, open := <-ch; open {
		t.Fatal("releasing a subscription must close its channel")
	}
	release() // sync.Once: a second release must not double-close

	bus.Publish(Event{Kind: EventLeaseCreated})
	bus.Close()
	bus.Close() // safe twice

	late, cancel := bus.Subscribe("owner:u1", 4)
	defer cancel()
	if _, open := <-late; open {
		t.Fatal("a closed bus must hand out a closed stream, not one that never speaks")
	}

	// Publishing after close is a no-op rather than a panic: a sweep in flight can
	// race shutdown.
	bus.Publish(Event{Kind: EventLeaseExpired})
}
