package store

import "gorm.io/gorm"

// ownedWhere narrows a by-id lookup to the row the caller owns.
//
// The comparison is exact, including the empty owner: an empty ownerID matches
// only the rows written without an owner (owner_id = ''), never "any owner".
// That is the difference between a scoped read and a fail-open one, and it is
// the same rule migrations/026_rls_policies.sql encodes in
// agenthub_row_visible: a policy comparison against NULL/'' is false.
//
// Every by-id repository method that serves a request goes through here, so a
// handler that forgets to compare owners cannot turn a lookup into a read of
// somebody else's row — the row simply comes back as absent.
func ownedWhere(q *gorm.DB, id, ownerID string) *gorm.DB {
	return q.Where("id = ? AND owner_id = ?", id, ownerID)
}
