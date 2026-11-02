// Package identity carries the database row access scope of one call.
//
// It exists because PostgreSQL row level security needs the identity inside the
// database session, and a connection pool only hands one out for the duration of
// a transaction. The value therefore travels on context.Context: the HTTP layer
// attaches it once per request, and internal/store turns it into the
// set_config('app.*', ..., true) calls that the policies in 026_rls_policies.sql
// read.
//
// The package has no dependencies at all: hub, handlers and store may all import
// it without inverting the layering the rest of the codebase keeps.
package identity

import "context"

// Identity is the row visibility of a single database call.
type Identity struct {
	// OwnerID is the user the rows belong to. Empty means "no owner": rows
	// written by an anonymous A2A peer or by the instance itself.
	OwnerID string
	// Bypass is the platform owner or an administrator acting on any row.
	Bypass bool
	// System is a process owned path: the task state machine, the event
	// broker, seeding, migration, backup and the audit writer.
	System bool
	// AuditRead allows reading the audit trail. It is separate from Bypass so
	// a role can be granted audit:read without gaining cross-tenant access to
	// tasks.
	AuditRead bool
	// Retention allows expiring old audit rows: the only DELETE the policies
	// accept.
	Retention bool
}

// Anonymous is the identity of a caller with no session. It is deliberately not
// "no identity at all": an anonymous request still opens a scoped transaction,
// it just sees only rows without an owner.
func Anonymous() Identity { return Identity{} }

type key struct{}

// With attaches id to ctx.
func With(ctx context.Context, id Identity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, key{}, id)
}

// From returns the identity attached to ctx and whether there was one.
func From(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(key{}).(Identity)
	return id, ok
}

// Owner returns the owner id carried by ctx, or "" when the identity is absent,
// system owned or bypassing.
func Owner(ctx context.Context) string {
	id, ok := From(ctx)
	if !ok || id.System || id.Bypass {
		return ""
	}
	return id.OwnerID
}

// System returns a fresh background context carrying the system identity. It is
// what the task state machine and the event broker use: they must keep writing
// after the request that started the run is gone, so they cannot inherit the
// caller's identity (or its cancellation).
func System() context.Context {
	return With(context.Background(), Identity{System: true})
}

// SystemWith returns a background context with the system identity plus the
// given extras (audit read, retention). Handy for one-off maintenance work.
func SystemWith(id Identity) context.Context {
	id.System = true
	return With(context.Background(), id)
}

// IsSystem reports whether ctx belongs to a process owned path.
func IsSystem(ctx context.Context) bool {
	id, ok := From(ctx)
	return ok && id.System
}
