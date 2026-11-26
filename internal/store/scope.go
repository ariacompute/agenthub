package store

import (
	"context"

	"gorm.io/gorm"

	"agenthub/internal/identity"
)

// scopeSettings is the ordered set of settings every scoped transaction writes.
// One statement instead of five round trips: a transaction that has to pay four
// extra network hops per query would make row level security more expensive than
// the feature it protects.
const scopeSettings = `SELECT
	set_config('app.owner_id',   ?, true),
	set_config('app.bypass',     ?, true),
	set_config('app.system',     ?, true),
	set_config('app.audit_read', ?, true),
	set_config('app.retention',  ?, true)`

// scoped runs fn with a handle that carries the identity of ctx.
//
// When ctx has no identity the handle is used as is: the repositories that do
// not touch a policy protected table keep working with no extra transaction, and
// a caller that genuinely has no scope (a unit test, the migration path) is not
// silently given one.
//
// When an identity is present the work runs inside a transaction that first
// publishes it to the session via set_config(..., is_local = true). Local scope
// is what makes this safe on a pooled connection: the settings die with the
// transaction, so the next borrower of that connection starts clean.
func scoped[T any](ctx context.Context, db *gorm.DB, fn func(*gorm.DB) (T, error)) (T, error) {
	var zero T
	if db == nil {
		return zero, gorm.ErrInvalidDB
	}
	base := db.WithContext(ctx)
	id, ok := identity.From(ctx)
	if !ok {
		return fn(base)
	}
	var out T
	err := base.Transaction(func(tx *gorm.DB) error {
		if err := applyIdentity(tx, id); err != nil {
			return err
		}
		v, err := fn(tx)
		out = v
		return err
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

// scopedTx is scoped with the transaction always opened.
//
// A transaction-scoped advisory lock is only legal inside a transaction block,
// and the audit writer needs one even when the caller carries no identity (a
// unit test, the retention sweep before the first request). scoped's "no
// identity, no transaction" shortcut would therefore turn into an error there,
// so callers that need the lock ask for this variant instead.
func scopedTx[T any](ctx context.Context, db *gorm.DB, fn func(*gorm.DB) (T, error)) (T, error) {
	var zero T
	if db == nil {
		return zero, gorm.ErrInvalidDB
	}
	var out T
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if id, ok := identity.From(ctx); ok {
			if err := applyIdentity(tx, id); err != nil {
				return err
			}
		}
		v, err := fn(tx)
		out = v
		return err
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

// applyIdentity publishes id to the current transaction.
func applyIdentity(tx *gorm.DB, id identity.Identity) error {
	return tx.Exec(scopeSettings,
		id.OwnerID, flag(id.Bypass), flag(id.System), flag(id.AuditRead), flag(id.Retention),
	).Error
}

// flag renders a boolean the way the policies compare it (on / off).
func flag(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// withIdentity is the non-generic form for callers that only need the error.
func withIdentity(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	_, err := scoped(ctx, db, func(tx *gorm.DB) (struct{}, error) {
		return struct{}{}, fn(tx)
	})
	return err
}
