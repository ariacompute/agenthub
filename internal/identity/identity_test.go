package identity

import (
	"context"
	"testing"
)

// The identity decides which rows PostgreSQL will hand back, so every helper
// here is a correctness statement about tenant isolation, not a convenience:
// Owner() returning a value for a system or bypassing identity would make a
// background write land in some user's rows.

func TestWithAndFromRoundTrip(t *testing.T) {
	id := Identity{OwnerID: "u-1", Bypass: true}
	got, ok := From(With(context.Background(), id))
	if !ok || got != id {
		t.Fatalf("From = %+v, %v; want %+v", got, ok, id)
	}
}

func TestFromWithoutAnIdentity(t *testing.T) {
	// Three ways to have no identity: a plain context, a nil one, and a context
	// carrying a value under a different key.
	if _, ok := From(context.Background()); ok {
		t.Fatal("a plain context must not carry an identity")
	}
	if _, ok := From(nil); ok {
		t.Fatal("a nil context must not carry an identity")
	}
	if _, ok := From(context.WithValue(context.Background(), "identity", Identity{OwnerID: "u-1"})); ok {
		t.Fatal("only the package's own key may be read")
	}
}

func TestWithAcceptsANilContext(t *testing.T) {
	// Handlers build the identity from a request context that is never nil, but
	// background sweeps are started from a bare call site.
	got, ok := From(With(nil, Identity{OwnerID: "u-2"}))
	if !ok || got.OwnerID != "u-2" {
		t.Fatalf("From = %+v, %v", got, ok)
	}
}

func TestOwnerOnlyReportsARealOwner(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"user", With(context.Background(), Identity{OwnerID: "u-1"}), "u-1"},
		{"anonymous", With(context.Background(), Anonymous()), ""},
		{"no identity", context.Background(), ""},
		{"system", With(context.Background(), Identity{OwnerID: "u-1", System: true}), ""},
		{"administrator", With(context.Background(), Identity{OwnerID: "u-1", Bypass: true}), ""},
	}
	for _, c := range cases {
		if got := Owner(c.ctx); got != c.want {
			t.Fatalf("%s: Owner = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSystemIsABackgroundIdentity(t *testing.T) {
	ctx := System()
	if !IsSystem(ctx) {
		t.Fatal("System() must report itself as system")
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	// A fresh background context, not the caller's: the state machine keeps
	// writing after the request that started the run is gone.
	if parent.Err() == nil {
		t.Fatal("fixture is wrong: the parent should be cancelled")
	}
	if System().Err() != nil {
		t.Fatal("System() must not inherit a cancellation")
	}
	if owner := Owner(System()); owner != "" {
		t.Fatalf("Owner(system) = %q, want empty", owner)
	}
}

func TestSystemWithKeepsTheExtras(t *testing.T) {
	ctx := SystemWith(Identity{AuditRead: true, Retention: true, OwnerID: "u-9"})
	got, ok := From(ctx)
	if !ok || !got.System || !got.AuditRead || !got.Retention {
		t.Fatalf("From = %+v, %v", got, ok)
	}
	// SystemWith always implies the system scope, even when the caller forgot it.
	if !IsSystem(ctx) {
		t.Fatal("SystemWith must set System")
	}
}

func TestIsSystemRejectsEverythingElse(t *testing.T) {
	if IsSystem(context.Background()) || IsSystem(nil) {
		t.Fatal("a context without an identity is not system")
	}
	if IsSystem(With(context.Background(), Identity{Bypass: true})) {
		t.Fatal("an administrator is not the system: their calls are attributed")
	}
}

func TestAnonymousSeesNothingOnItsOwn(t *testing.T) {
	// The zero identity is the anonymous caller: it opens a scoped transaction
	// (so the policies apply) and matches only ownerless rows.
	id := Anonymous()
	if id.OwnerID != "" || id.Bypass || id.System || id.AuditRead || id.Retention {
		t.Fatalf("anonymous = %+v, want the zero identity", id)
	}
}
