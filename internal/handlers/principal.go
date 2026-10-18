package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"agenthub/internal/authz"
	"agenthub/internal/httputil"
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
func (d Deps) identify(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, withPrincipal(r, d.resolvePrincipal(r)))
	}
}

// requireAuth rejects anonymous callers with a machine readable code.
func (d Deps) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return d.identify(func(w http.ResponseWriter, r *http.Request) {
		if PrincipalFrom(r) == nil {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeUnauthenticated)
			return
		}
		next(w, r)
	})
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
