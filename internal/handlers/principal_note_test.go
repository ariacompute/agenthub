package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/audit"
	"agenthub/internal/authz"
	"agenthub/internal/identity"
)

// notePrincipal is where the audit row learns who called. It runs inside the
// identity middleware because that is the only place the session has already
// been parsed; a missing label here is not a crash, it is an unusable trail.

func TestNotePrincipalLabelsTheCaller(t *testing.T) {
	ctx, tr := audit.StartTrace(t.Context(), audit.Event{EventID: "e1"})
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil).WithContext(ctx)

	notePrincipal(req, &authz.Principal{UserID: "u-1", Email: "ada@example.com", Name: "Ada"})
	ev := tr.Snapshot()
	if ev.ActorKind != audit.ActorUser || ev.ActorID != "u-1" {
		t.Fatalf("actor = %q/%q", ev.ActorKind, ev.ActorID)
	}
	// The email is the label an operator recognises; the row keeps both ids so a
	// rename does not orphan the history.
	if ev.ActorLabel != "ada@example.com" || ev.OwnerID != "u-1" {
		t.Fatalf("label = %q / owner = %q", ev.ActorLabel, ev.OwnerID)
	}
}

func TestNotePrincipalFallsBackToTheName(t *testing.T) {
	ctx, tr := audit.StartTrace(t.Context(), audit.Event{EventID: "e1"})
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil).WithContext(ctx)

	notePrincipal(req, &authz.Principal{UserID: "u-2", Name: "Grace"})
	if got := tr.Snapshot().ActorLabel; got != "Grace" {
		t.Fatalf("label = %q, want the display name", got)
	}
}

func TestNotePrincipalMarksAnonymousCalls(t *testing.T) {
	// Two shapes mean anonymous: no principal at all, and a principal with no
	// user (an API key whose subject was revoked mid-request).
	for _, p := range []*authz.Principal{nil, {}} {
		ctx, tr := audit.StartTrace(t.Context(), audit.Event{EventID: "e1"})
		req := httptest.NewRequest(http.MethodGet, "/a2a", nil).WithContext(ctx)
		notePrincipal(req, p)
		ev := tr.Snapshot()
		if ev.ActorKind != audit.ActorAnonymous || ev.ActorID != "" {
			t.Fatalf("actor = %+v, want anonymous", ev)
		}
	}
}

func TestNotePrincipalWithoutATraceIsANoOp(t *testing.T) {
	// Most requests in a test or a CLI import never pass through Tap.
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	notePrincipal(req, &authz.Principal{UserID: "u-1"})
}

func TestIdentityOfMapsThePrincipal(t *testing.T) {
	cases := []struct {
		name string
		p    *authz.Principal
		want identity.Identity
	}{
		{"anonymous", nil, identity.Identity{}},
		{"user", &authz.Principal{UserID: "u-1"}, identity.Identity{OwnerID: "u-1"}},
		// The platform owner is a scope, not a tenant: it bypasses ownership
		// rather than owning rows, so anything it creates is administrator data.
		{"platform owner", &authz.Principal{UserID: "u-1", IsOwner: true},
			identity.Identity{Bypass: true}},
	}
	for _, c := range cases {
		if got := identityOf(c.p); got != c.want {
			t.Fatalf("%s: identity = %+v, want %+v", c.name, got, c.want)
		}
	}
}
