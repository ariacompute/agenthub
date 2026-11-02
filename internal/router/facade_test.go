package router

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// the facade's accessors
// ---------------------------------------------------------------------------

func TestServiceExposesTheEngineItOwns(t *testing.T) {
	svc, store, _, _ := newTestService(t, ServiceOptions{})
	if svc.Engine() == nil {
		t.Fatal("Engine() must expose the single writer")
	}
	if svc.Engine().Store() != store {
		t.Fatal("the engine must write through the store it was given")
	}
	// The bus is created when the caller does not pass one, and both views have
	// to be the same fan-out or a console would subscribe to a stream nobody
	// publishes to.
	if svc.Bus() == nil || svc.Bus() != svc.Engine().Bus() {
		t.Fatal("Service.Bus and Engine.Bus must be the same fan-out")
	}
	if got := svc.Engine().Resource(); got != "res-agent" {
		t.Fatalf("Resource() = %q, want res-agent", got)
	}
	if svc.Engine().Clock() == nil {
		t.Fatal("Clock() must never be nil: the defaults fill it in")
	}
}

func TestServiceKickIsSafeBeforeTheBeatRuns(t *testing.T) {
	svc, _, _, _ := newTestService(t, ServiceOptions{})
	if svc.Live() {
		t.Fatal("a service that was never run must not report a live beat")
	}
	// A console kicks after an injection; on an idle service that must be a
	// no-op rather than a panic on a nil channel.
	svc.Kick()
	svc.Kick()
	if svc.Live() {
		t.Fatal("kicking must not start the beat")
	}
}

func TestServiceGetTicketReturnsOneDetailAndRefusesAMissingOne(t *testing.T) {
	svc, _, c, _ := newTestService(t, ServiceOptions{})
	ctx := context.Background()
	ticket, _ := openOneTicket(t, svc, c, "please look at the build")

	detail, err := svc.GetTicket(ctx, ticket.ID)
	if err != nil {
		t.Fatalf("GetTicket: %v", err)
	}
	if detail == nil || detail.Ticket == nil || detail.Ticket.ID != ticket.ID {
		t.Fatalf("detail = %+v", detail)
	}
	if _, err := svc.GetTicket(ctx, "tkt-does-not-exist"); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("missing ticket: err = %v, want ErrTicketNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// vocabulary helpers
// ---------------------------------------------------------------------------

func TestDispositionSettled(t *testing.T) {
	want := map[Disposition]bool{
		DispositionRouted:      true,
		DispositionIgnored:     true,
		DispositionFiltered:    true,
		DispositionPending:     false,
		DispositionStopped:     false,
		DispositionUnavailable: false,
	}
	for _, d := range Dispositions {
		if got := d.Settled(); got != want[d] {
			t.Fatalf("%q.Settled() = %v, want %v", d, got, want[d])
		}
	}
	if Disposition("nonsense").Settled() {
		t.Fatal("an unknown disposition must not count as settled")
	}
}

func TestDecisionKindMapsToADisposition(t *testing.T) {
	want := map[DecisionKind]Disposition{
		DecisionAccept:      DispositionRouted,
		DecisionAttach:      DispositionRouted,
		DecisionStop:        DispositionStopped,
		DecisionIgnore:      DispositionIgnored,
		DecisionUnavailable: DispositionUnavailable,
		DecisionKind(""):    DispositionUnavailable,
	}
	for kind, expected := range want {
		if got := kind.Disposition(); got != expected {
			t.Fatalf("%q.Disposition() = %q, want %q", kind, got, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// the memory store's policy seed
// ---------------------------------------------------------------------------

func TestMemoryStoreSeedPolicyCopiesThePolicyIn(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	seed := &Policy{
		Source:   "seed-src",
		Mode:     "candidate",
		Allow:    []string{"alice"},
		Keywords: []string{"deploy"},
		Resource: "res-a",
	}
	store.SeedPolicy(seed)

	got, err := store.GetPolicy(ctx, "seed-src")
	if err != nil || got == nil {
		t.Fatalf("GetPolicy: %v %v", got, err)
	}
	if got.Mode != "candidate" || got.Resource != "res-a" || len(got.Allow) != 1 || len(got.Keywords) != 1 {
		t.Fatalf("seeded policy = %+v", got)
	}

	// The store keeps its own copy: a caller that keeps mutating the struct it
	// seeded must not change what the engine evaluates against.
	seed.Mode = "off"
	seed.Allow[0] = "mallory"
	again, _ := store.GetPolicy(ctx, "seed-src")
	if again == nil || again.Mode != "candidate" || again.Allow[0] != "alice" {
		t.Fatalf("stored policy followed the caller's edits: %+v", again)
	}
}

func TestMemoryStoreSeedPolicyReplacesAnExistingOne(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	store.SeedPolicy(&Policy{Source: "replace-me", Mode: "off"})
	store.SeedPolicy(&Policy{Source: "replace-me", Mode: "candidate", QuietStart: -1, QuietEnd: -1})

	got, err := store.GetPolicy(ctx, "replace-me")
	if err != nil || got == nil {
		t.Fatalf("GetPolicy: %v %v", got, err)
	}
	if got.Mode != "candidate" {
		t.Fatalf("the later seed must win, got %+v", got)
	}
	if _, err := store.GetPolicy(ctx, "never-seeded"); err != nil {
		t.Fatalf("an unknown source must read as no policy, not an error: %v", err)
	}
}

// A seeded policy has to survive the validation the engine applies on the way
// out, otherwise the test helper above could hide a contract break.
func TestSeededPolicyPassesValidation(t *testing.T) {
	p := &Policy{Source: string(SourceCron), Mode: PolicyCandidate, QuietStart: -1, QuietEnd: -1, UpdatedAt: time.Now()}
	if err := p.Validate(); err != nil {
		t.Fatalf("seeded policy must validate: %v", err)
	}
}
