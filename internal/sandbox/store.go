package sandbox

import (
	"context"
	"time"
)

// Store persists leases, execution receipts, nodes and the event stream.
//
// The port is declared here, in the domain package, and implemented twice:
// memory.go (what the unit tests run on) and internal/store (PostgreSQL). The
// same suite runs against both, which is what keeps the two honest.
//
// There is one writer — the Manager — so the port carries plain reads and
// writes instead of conditional updates. The one exception is the node job
// queue, which is in process by design (see requirements-sandbox.md §4).
type Store interface {
	SaveLease(ctx context.Context, l *Lease) error
	GetLease(ctx context.Context, id string) (*Lease, error)
	// FindLease is the reuse lookup: the most recently renewed live lease for a
	// (owner, resource, scope, provider) key, or nil when there is none.
	FindLease(ctx context.Context, ownerID, resourceID, scope, provider string) (*Lease, error)
	ListLeases(ctx context.Context, ownerID string) ([]*Lease, error)
	DeleteLease(ctx context.Context, id string) error
	// ExpiredLeases returns leases past now, oldest deadline first, so a sweep
	// can reclaim in bounded batches.
	ExpiredLeases(ctx context.Context, now time.Time, limit int) ([]*Lease, error)
	// CountLeases returns how many live leases an owner holds ("" means all),
	// which is what enforces the per-owner and global ceilings.
	CountLeases(ctx context.Context, ownerID string) (int, error)

	AppendExec(ctx context.Context, r *ExecRecord) error
	ListExecs(ctx context.Context, leaseID string, limit int) ([]*ExecRecord, error)
	CountExecsSince(ctx context.Context, since time.Time) (int, error)

	SaveNode(ctx context.Context, n *Node) error
	GetNode(ctx context.Context, id string) (*Node, error)
	ListNodes(ctx context.Context) ([]*Node, error)
	DeleteNode(ctx context.Context, id string) error

	// SaveJob writes a finished job, for the audit trail. Queued work lives in
	// memory: a node's poll is the queue.
	SaveJob(ctx context.Context, j *Job) error

	// AppendEvent assigns the frame's sequence number and stores it, so a
	// reconnecting console can replay from `since`.
	AppendEvent(ctx context.Context, e *Event) error
	EventsSince(ctx context.Context, subject string, since uint64, limit int) ([]*Event, error)
	MaxSeq(ctx context.Context) (uint64, error)
}

// NopStore is what an instance without a database runs on. It satisfies the port
// and keeps nothing: the Manager's in-memory index still leases correctly, so a
// single-process instance works and simply has no audit trail to offer.
func NopStore() Store { return &nopStore{} }

type nopStore struct{}

func (nopStore) SaveLease(context.Context, *Lease) error          { return nil }
func (nopStore) GetLease(context.Context, string) (*Lease, error) { return nil, nil }
func (nopStore) FindLease(context.Context, string, string, string, string) (*Lease, error) {
	return nil, nil
}
func (nopStore) ListLeases(context.Context, string) ([]*Lease, error) { return nil, nil }
func (nopStore) DeleteLease(context.Context, string) error            { return nil }
func (nopStore) ExpiredLeases(context.Context, time.Time, int) ([]*Lease, error) {
	return nil, nil
}
func (nopStore) CountLeases(context.Context, string) (int, error) { return 0, nil }
func (nopStore) AppendExec(context.Context, *ExecRecord) error    { return nil }
func (nopStore) ListExecs(context.Context, string, int) ([]*ExecRecord, error) {
	return nil, nil
}
func (nopStore) CountExecsSince(context.Context, time.Time) (int, error) { return 0, nil }
func (nopStore) SaveNode(context.Context, *Node) error                   { return nil }
func (nopStore) GetNode(context.Context, string) (*Node, error)          { return nil, nil }
func (nopStore) ListNodes(context.Context) ([]*Node, error)              { return nil, nil }
func (nopStore) DeleteNode(context.Context, string) error                { return nil }
func (nopStore) SaveJob(context.Context, *Job) error                     { return nil }
func (nopStore) AppendEvent(context.Context, *Event) error               { return nil }
func (nopStore) EventsSince(context.Context, string, uint64, int) ([]*Event, error) {
	return nil, nil
}
func (nopStore) MaxSeq(context.Context) (uint64, error) { return 0, nil }
