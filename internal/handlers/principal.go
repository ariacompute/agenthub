package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"agenthub/internal/audit"
	"agenthub/internal/authz"
	"agenthub/internal/httputil"
	"agenthub/internal/identity"
	"agenthub/internal/registry"
)

// principalKey is the request scoped identity: middleware resolves it once and
// every handler reads it from the context, so a request never fans out into a
// permission query per check.
type principalKey struct{}

// withPrincipal attaches the resolved identity to a request.
func withPrincipal(r *http.Request, p *authz.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

// PrincipalFrom returns the identity of the request, or nil when the caller is
// anonymous.
func PrincipalFrom(r *http.Request) *authz.Principal {
	p, _ := r.Context().Value(principalKey{}).(*authz.Principal)
	return p
}

// identify resolves the caller and attaches it, without rejecting anybody. It
// is what makes public endpoints (Agent Card, MCP export) able to narrow their
// answer for a logged in user while still serving anonymous discovery.
//
// It also attaches the database row scope (identity.Identity) to the request
// context: everything downstream — every repository call — runs under it, so the
// row level security policies and the Go side agree on who the caller is without
// either having to re-resolve the session.
func (d Deps) identify(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := d.resolvePrincipal(r)
		r = withPrincipal(r, p)
		notePrincipal(r, p)
		next(w, r.WithContext(identity.With(r.Context(), identityOf(p))))
	}
}

// notePrincipal records the resolved caller on the audit trace. It runs in the
// identity middleware because that is the only place the session has already
// been parsed: asking the recorder to resolve it again would double the cookie
// work on every request for information we are holding.
func notePrincipal(r *http.Request, p *authz.Principal) {
	tr := audit.FromContext(r.Context())
	if tr == nil {
		return
	}
	if p == nil || p.UserID == "" {
		tr.NotePrincipal(audit.ActorAnonymous, "", "", "")
		return
	}
	label := p.Email
	if label == "" {
		label = p.Name
	}
	tr.NotePrincipal(audit.ActorUser, p.UserID, label, p.UserID)
}

// identityOf turns a principal into the row scope used by the database.
//
// Only the platform owner bypasses ownership: an administrator holding
// tasks:read still sees its own tasks, because cross-tenant reading is exactly
// what the ownership dimension exists to prevent.
func identityOf(p *authz.Principal) identity.Identity {
	if p == nil || p.UserID == "" {
		return identity.Anonymous()
	}
	if p.IsOwner {
		return identity.Identity{Bypass: true}
	}
	return identity.Identity{
		OwnerID:   p.UserID,
		AuditRead: p.Can(authz.PermAuditRead),
	}
}

// requireAuth rejects callers with no identity at all with a machine readable
// code.
//
// It asks Principal.Anonymous() rather than comparing with nil, because those
// are not the same question: Anonymous() also covers a principal that resolved
// but carries no user id. That shape is fail-open everywhere ownership is keyed
// on UserID — an empty owner id means "the row has no owner", never "any row" —
// so a session that lost its user must be refused here, once, instead of being
// trusted downstream.
func (d Deps) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return d.identify(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeUnauthenticated)
			return
		}
		next(w, r)
	})
}

// authenticated reports whether the request carries a usable identity. It is
// the same question Principal.Anonymous answers, asked in one place so the two
// cannot drift apart again.
func authenticated(r *http.Request) bool { return !PrincipalFrom(r).Anonymous() }

// callerID is the owner id every per-owner store call scopes on. The routes
// that reach those calls sit behind requireAuth, which refuses a principal
// without a user id, so this is never empty on a path that reads or writes a
// row by id.
func callerID(r *http.Request) string {
	if p := PrincipalFrom(r); p != nil {
		return p.UserID
	}
	return ""
}

// systemScope is the process identity attached to the caller's context: the
// request keeps its cancellation and deadline, but row level security sees a
// process owned path instead of a signed in (or absent) user. It exists for the
// one public endpoint that must read a tenant row before a session can exist —
// the channel webhook, which has to read the row to learn which secret to verify
// the body against.
func systemScope(r *http.Request) context.Context {
	return identity.With(r.Context(), identity.Identity{System: true})
}

// requirePermission is requireAuth plus one capability check. The permission
// name is the whole vocabulary: handlers never ask about roles or groups.
func (d Deps) requirePermission(perm string, next http.HandlerFunc) http.HandlerFunc {
	return d.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFrom(r)
		if !p.Can(perm) {
			d.Log.Debug("handler: permission denied", "user", p.UserID, "permission", perm)
			httputil.WriteError(w, http.StatusForbidden, httputil.CodeForbidden)
			return
		}
		next(w, r)
	})
}

// resolvePrincipal turns the session cookie into an identity. Without an auth
// service wired in (tests, in-memory mode) every caller is the dev principal,
// which is the open behaviour agenthub had before identities existed.
func (d Deps) resolvePrincipal(r *http.Request) *authz.Principal {
	if d.Auth == nil {
		return authz.DevPrincipal()
	}
	p, _, err := d.Auth.Principal(r.Context(), r)
	if err != nil {
		// A dead or revoked session is not an error worth logging loudly: it is
		// what every expired cookie looks like.
		slog.Debug("handlers: session not usable", "error", err)
		return nil
	}
	return p
}

// visibleEntries narrows a catalogue listing to what the caller may read.
//
// The registry itself is authorization agnostic: filtering happens here, once,
// so list, counts, export and Agent Card all describe the same world.
func (d Deps) visibleEntries(r *http.Request, entries []*registry.Entry) []*registry.Entry {
	p := PrincipalFrom(r)
	if p == nil {
		p = authz.AnonymousPrincipal()
	}
	if d.Authz == nil || p.IsOwner {
		return entries
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	visible, err := d.Authz.VisibleResourceIDs(r.Context(), p, ids)
	if err != nil {
		// Fail closed: an authorization outage must not turn into "everybody
		// sees everything".
		d.Log.Error("handler: resolve visible resources", "user", p.UserID, "error", err)
		return nil
	}
	out := make([]*registry.Entry, 0, len(entries))
	for _, e := range entries {
		if e.IsPublic() {
			// Published records need no grant: that is what public means.
			out = append(out, e)
			continue
		}
		if _, ok := visible[e.ID]; ok {
			out = append(out, e)
		}
	}
	return out
}

// canAccess reports whether the caller may read (or write) one resource. Write
// paths call it explicitly: filtering a list is not what stops a direct PUT to
// an id the caller never saw.
func (d Deps) canAccess(r *http.Request, resourceID string, write bool) bool {
	p := PrincipalFrom(r)
	if p == nil {
		p = authz.AnonymousPrincipal()
	}
	if d.Authz == nil || p.IsOwner {
		return true
	}
	// A published record is readable and invocable by anybody — including an
	// anonymous A2A peer. Publishing says nothing about writing, so that stays
	// on the grant path below.
	if !write && d.Reg != nil {
		if e, ok := d.Reg.Get(resourceID); ok && e.IsPublic() {
			return true
		}
	}
	ok, err := d.Authz.CanAccessResource(r.Context(), p, resourceID, write)
	if err != nil {
		d.Log.Error("handler: resource access check", "user", p.UserID, "resource", resourceID, "error", err)
		return false
	}
	return ok
}

// countsOf recomputes the sidebar counters from what the caller can actually
// see, so a restricted resource never inflates a total.
func countsOf(entries []*registry.Entry) (map[string]int, map[string]int) {
	byKind := map[string]int{}
	for _, k := range registry.Kinds {
		byKind[string(k)] = 0
	}
	byHealth := map[string]int{}
	for _, h := range registry.Healths {
		byHealth[string(h)] = 0
	}
	for _, e := range entries {
		byKind[string(e.Kind)]++
		byHealth[string(e.Health)]++
	}
	return byKind, byHealth
}
