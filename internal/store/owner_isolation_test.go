package store

import (
	"context"
	"errors"
	"testing"

	"agenthub/internal/channels"
	"agenthub/internal/computers"
	"agenthub/internal/files"
	"agenthub/internal/model"
	"agenthub/internal/schedules"
	"agenthub/internal/threads"

	"gorm.io/gorm"
)

// Ownership is enforced twice — by a Go predicate (ownedWhere / ownedChildWhere)
// and by a row level security policy — and the two are independent. These cases
// exercise the predicate. openGovernanceDB opens the shared database with
// RLSEnforce: false, which means the DSN carries app.system=on and the policies
// step aside; a row that comes back here came back because the query said so, not
// because a policy hid it. tests/rls_test.go is the other half: the same tables,
// RLSEnforce: true, and a role the policies bind.
//
// Nothing here runs in parallel: the cases share one database and one pool, and
// every row is namespaced through uniqueID/uniqueName so a second run — or a
// second checkout against the same instance — does not collide on a primary key.

// ownerIsolationCase describes one owner-scoped store in terms the harness can
// assert uniformly. The accessors return domain values, not rows, so the same
// five steps can be written once.
type ownerIsolationCase struct {
	name string
	// create writes one row for owner and returns the id the store assigned.
	create func(t *testing.T, ctx context.Context, owner string) string
	// list reports the ids owner can see through the list method.
	list func(ctx context.Context, owner string) ([]string, error)
	// listUnscoped records whether List("") means "every row". Schedules,
	// channels and computers treat an empty owner as "no predicate"; files and
	// threads always compare, so their empty-owner list is empty by design and a
	// list cannot be used to enumerate the table.
	listUnscoped bool
	// get reports whether the row is visible to owner. A port that documents its
	// own not-found sentinel (threads.ErrNotFound) is normalised to (false, nil)
	// inside the accessor, so every case asserts the same thing: not visible,
	// and no error that would tell the caller the row exists.
	get func(ctx context.Context, id, owner string) (bool, error)
	// update rewrites the row's marker field for owner, or is nil when the port
	// has no update. The return value is deliberately dropped: channels echoes
	// the payload it was given even when no row matched, so only a re-read
	// proves whether anything was written.
	update func(ctx context.Context, id, owner string) error
	// marker reads the field update writes, for the true owner.
	marker func(ctx context.Context, id, owner string) (string, error)
	// remove attempts the delete for owner. Ports without a boolean (computers,
	// threads) report false; the authoritative check is whether the row is still
	// readable by its owner afterwards.
	remove func(ctx context.Context, id, owner string) (bool, error)
}

func TestStoreOwnerIsolation(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()

	for _, tc := range ownerIsolationCases(db) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ownerA, ownerB := uniqueName("owner-a"), uniqueName("owner-b")
			idA := tc.create(t, ctx, ownerA)
			idB := tc.create(t, ctx, ownerB)

			// Cleanup is the owner's own delete: it exercises the write path a
			// second time and leaves nothing behind when an assertion below
			// fails.
			t.Cleanup(func() {
				if _, err := tc.remove(ctx, idA, ownerA); err != nil {
					t.Errorf("cleanup %s: %v", idA, err)
				}
				if _, err := tc.remove(ctx, idB, ownerB); err != nil {
					t.Errorf("cleanup %s: %v", idB, err)
				}
			})

			// 1. The list is scoped to the caller's owner.
			for _, step := range []struct {
				owner   string
				want    string
				notWant string
			}{
				{ownerA, idA, idB},
				{ownerB, idB, idA},
			} {
				got, err := tc.list(ctx, step.owner)
				if err != nil {
					t.Fatalf("list(%s): %v", step.owner, err)
				}
				if !hasID(got, step.want) || hasID(got, step.notWant) {
					t.Fatalf("list(%s) = %v, want %s and not %s", step.owner, got, step.want, step.notWant)
				}
			}

			// 2. An empty owner is not a wildcard everywhere: files and threads
			// always compare, so the list comes back empty rather than
			// enumerating other accounts' rows.
			all, err := tc.list(ctx, "")
			if err != nil {
				t.Fatalf("list(\"\"): %v", err)
			}
			switch {
			case tc.listUnscoped && (!hasID(all, idA) || !hasID(all, idB)):
				t.Fatalf("list(\"\") = %v, want both %s and %s", all, idA, idB)
			case !tc.listUnscoped && (hasID(all, idA) || hasID(all, idB)):
				t.Fatalf("list(\"\") = %v, want no row: this store always compares owners", all)
			}

			// 3. A by-id read of somebody else's row is "not found" — never a
			// row, and never an error that distinguishes it from a missing id.
			visible, err := tc.get(ctx, idB, ownerA)
			if err != nil {
				t.Fatalf("cross-owner get: %v", err)
			}
			if visible {
				t.Fatalf("cross-owner get returned %s to %s", idB, ownerA)
			}
			if visible, err := tc.get(ctx, idB, ownerB); err != nil || !visible {
				t.Fatalf("owner's own get: visible=%v err=%v", visible, err)
			}

			// 4. A by-id write cannot reach another owner's row, and the same
			// call with the true owner does change the marker — otherwise the
			// check would pass simply because update is a no-op.
			if tc.update != nil {
				before, err := tc.marker(ctx, idB, ownerB)
				if err != nil {
					t.Fatalf("marker before update: %v", err)
				}
				if err := tc.update(ctx, idB, ownerA); err != nil {
					t.Fatalf("cross-owner update: %v", err)
				}
				after, err := tc.marker(ctx, idB, ownerB)
				if err != nil {
					t.Fatalf("marker after cross-owner update: %v", err)
				}
				if after != before {
					t.Fatalf("cross-owner update rewrote %s: %q -> %q", idB, before, after)
				}
				if err := tc.update(ctx, idB, ownerB); err != nil {
					t.Fatalf("owner update: %v", err)
				}
				if changed, err := tc.marker(ctx, idB, ownerB); err != nil || changed == before {
					t.Fatalf("owner update left the marker at %q (err=%v)", changed, err)
				}
			}

			// 5. A cross-owner delete reports nothing removed and leaves the row
			// readable by the account that does own it.
			reported, err := tc.remove(ctx, idB, ownerA)
			if err != nil {
				t.Fatalf("cross-owner delete: %v", err)
			}
			if reported {
				t.Fatalf("cross-owner delete of %s reported success", idB)
			}
			if visible, err := tc.get(ctx, idB, ownerB); err != nil || !visible {
				t.Fatalf("row %s did not survive a cross-owner delete (visible=%v err=%v)", idB, visible, err)
			}
		})
	}
}

func ownerIsolationCases(db *gorm.DB) []ownerIsolationCase {
	scheds := NewSchedulesStore(db)
	chans := NewChannelsStore(db)
	comps := NewComputersStore(db)
	fileStore := NewFilesStore(db)
	threadStore := NewThreadsStore(db)

	return []ownerIsolationCase{
		{
			name: "schedules",
			create: func(t *testing.T, ctx context.Context, owner string) string {
				t.Helper()
				s, err := scheds.Create(ctx, &schedules.Schedule{
					Goal:    "goal-" + owner,
					Trigger: string(schedules.TriggerManual),
					Enabled: true,
					OwnerID: owner,
				})
				if err != nil {
					t.Fatalf("create schedule: %v", err)
				}
				return s.ID
			},
			list: func(ctx context.Context, owner string) ([]string, error) {
				rows, err := scheds.List(ctx, owner)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(rows))
				for _, s := range rows {
					ids = append(ids, s.ID)
				}
				return ids, nil
			},
			listUnscoped: true,
			get: func(ctx context.Context, id, owner string) (bool, error) {
				s, err := scheds.Get(ctx, id, owner)
				return s != nil, err
			},
			update: func(ctx context.Context, id, owner string) error {
				_, err := scheds.Update(ctx, &schedules.Schedule{
					ID:      id,
					Goal:    "rewritten",
					Trigger: string(schedules.TriggerManual),
				}, owner)
				return err
			},
			marker: func(ctx context.Context, id, owner string) (string, error) {
				s, err := scheds.Get(ctx, id, owner)
				if err != nil {
					return "", err
				}
				if s == nil {
					return "", errors.New("schedule marker: row missing")
				}
				return s.Goal, nil
			},
			remove: func(ctx context.Context, id, owner string) (bool, error) {
				return scheds.Delete(ctx, id, owner)
			},
		},
		{
			name: "channels",
			create: func(t *testing.T, ctx context.Context, owner string) string {
				t.Helper()
				c, err := chans.Create(ctx, &channels.Channel{
					ID:      uniqueID("chan"),
					Kind:    channels.KindSlack,
					Name:    "channel-" + owner,
					Enabled: true,
					OwnerID: owner,
				})
				if err != nil {
					t.Fatalf("create channel: %v", err)
				}
				return c.ID
			},
			list: func(ctx context.Context, owner string) ([]string, error) {
				rows, err := chans.List(ctx, owner)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(rows))
				for _, c := range rows {
					ids = append(ids, c.ID)
				}
				return ids, nil
			},
			listUnscoped: true,
			get: func(ctx context.Context, id, owner string) (bool, error) {
				c, err := chans.Get(ctx, id, owner)
				return c != nil, err
			},
			update: func(ctx context.Context, id, owner string) error {
				_, err := chans.Update(ctx, &channels.Channel{
					ID:      id,
					Kind:    channels.KindSlack,
					Name:    "rewritten",
					Enabled: true,
				}, owner)
				return err
			},
			marker: func(ctx context.Context, id, owner string) (string, error) {
				c, err := chans.Get(ctx, id, owner)
				if err != nil {
					return "", err
				}
				if c == nil {
					return "", errors.New("channel marker: row missing")
				}
				return c.Name, nil
			},
			remove: func(ctx context.Context, id, owner string) (bool, error) {
				return chans.Delete(ctx, id, owner)
			},
		},
		{
			name: "computers",
			create: func(t *testing.T, ctx context.Context, owner string) string {
				t.Helper()
				c, err := comps.Create(ctx, &computers.Computer{
					Kind:       computers.KindShell,
					OwnerID:    owner,
					LastAction: "created-" + owner,
				})
				if err != nil {
					t.Fatalf("create computer: %v", err)
				}
				return c.ID
			},
			list: func(ctx context.Context, owner string) ([]string, error) {
				rows, err := comps.List(ctx, owner)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(rows))
				for _, c := range rows {
					ids = append(ids, c.ID)
				}
				return ids, nil
			},
			listUnscoped: true,
			get: func(ctx context.Context, id, owner string) (bool, error) {
				c, err := comps.Get(ctx, id, owner)
				return c != nil, err
			},
			update: func(ctx context.Context, id, owner string) error {
				_, err := comps.Update(ctx, &computers.Computer{ID: id, LastAction: "rewritten"}, owner)
				return err
			},
			marker: func(ctx context.Context, id, owner string) (string, error) {
				c, err := comps.Get(ctx, id, owner)
				if err != nil {
					return "", err
				}
				if c == nil {
					return "", errors.New("computer marker: row missing")
				}
				return c.LastAction, nil
			},
			remove: func(ctx context.Context, id, owner string) (bool, error) {
				// computers.Store.Delete reports the error only; the harness
				// checks survival instead.
				return false, comps.Delete(ctx, id, owner)
			},
		},
		{
			name: "files",
			create: func(t *testing.T, ctx context.Context, owner string) string {
				t.Helper()
				f, err := fileStore.Create(ctx, &files.File{
					ID:        uniqueID("file"),
					OwnerID:   owner,
					Name:      "file-" + owner,
					MIME:      "text/plain",
					Size:      3,
					SHA256:    uniqueID("sha"),
					Backend:   files.BackendLocal,
					ObjectKey: uniqueID("object"),
				})
				if err != nil {
					t.Fatalf("create file: %v", err)
				}
				return f.ID
			},
			list: func(ctx context.Context, owner string) ([]string, error) {
				rows, err := fileStore.List(ctx, owner)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(rows))
				for _, f := range rows {
					ids = append(ids, f.ID)
				}
				return ids, nil
			},
			// files.List always compares: an empty owner is a filter for rows
			// written without an owner, not "everything".
			listUnscoped: false,
			get: func(ctx context.Context, id, owner string) (bool, error) {
				f, err := fileStore.Get(ctx, id, owner)
				return f != nil, err
			},
			update: nil,
			marker: nil,
			remove: func(ctx context.Context, id, owner string) (bool, error) {
				return fileStore.Delete(ctx, id, owner)
			},
		},
		{
			name: "threads",
			create: func(t *testing.T, ctx context.Context, owner string) string {
				t.Helper()
				th, err := threadStore.CreateThread(ctx, &threads.Thread{
					Title:   "thread-" + owner,
					OwnerID: owner,
				})
				if err != nil {
					t.Fatalf("create thread: %v", err)
				}
				return th.ID
			},
			list: func(ctx context.Context, owner string) ([]string, error) {
				rows, err := threadStore.ListThreads(ctx, owner)
				if err != nil {
					return nil, err
				}
				ids := make([]string, 0, len(rows))
				for _, th := range rows {
					ids = append(ids, th.ID)
				}
				return ids, nil
			},
			listUnscoped: false,
			get: func(ctx context.Context, id, owner string) (bool, error) {
				th, err := threadStore.GetThread(ctx, id, owner)
				if errors.Is(err, threads.ErrNotFound) {
					// The thread port says "not found" rather than "nil, nil";
					// it is the same answer, so it is normalised here.
					return false, nil
				}
				return th != nil, err
			},
			update: nil,
			marker: nil,
			remove: func(ctx context.Context, id, owner string) (bool, error) {
				err := threadStore.DeleteThread(ctx, id, owner)
				if err != nil && !errors.Is(err, threads.ErrNotFound) {
					return false, err
				}
				return false, nil
			},
		},
	}
}

// TestChannelDeliveriesOwnerIsolation covers the child table: a receipt has no id
// of its own, so its owner is copied from the parent channel inside the insert
// transaction and every read compares (channel_id, owner_id).
func TestChannelDeliveriesOwnerIsolation(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()
	chans := NewChannelsStore(db)
	ownerA, ownerB := uniqueName("owner-a"), uniqueName("owner-b")

	newChannel := func(owner string) *channels.Channel {
		t.Helper()
		c, err := chans.Create(ctx, &channels.Channel{
			ID:      uniqueID("chan"),
			Kind:    channels.KindTelegram,
			Name:    "deliveries-" + owner,
			Enabled: true,
			OwnerID: owner,
		})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		return c
	}
	chA, chB := newChannel(ownerA), newChannel(ownerB)
	refA, refB := uniqueID("ref"), uniqueID("ref")

	record := func(c *channels.Channel, ref string) {
		t.Helper()
		written, err := chans.RecordDelivery(ctx, &channels.ChannelMessage{
			ChannelID:   c.ID,
			ExternalRef: ref,
			Direction:   "inbound",
			Status:      channels.StatusReceived,
		})
		if err != nil || !written {
			t.Fatalf("record delivery on %s: written=%v err=%v", c.ID, written, err)
		}
	}
	record(chA, refA)
	record(chB, refB)

	t.Cleanup(func() {
		// channel_messages has no foreign key to channels, so the receipts go
		// first and explicitly.
		if err := db.Where("channel_id IN ?", []string{chA.ID, chB.ID}).
			Delete(&model.ChannelMessage{}).Error; err != nil {
			t.Errorf("cleanup receipts: %v", err)
		}
		for _, c := range []*channels.Channel{chA, chB} {
			if ok, err := chans.Delete(ctx, c.ID, c.OwnerID); err != nil || !ok {
				t.Errorf("cleanup channel %s: deleted=%v err=%v", c.ID, ok, err)
			}
		}
	})

	// A redelivery of the same external ref is a no-op, not a second row and not
	// an error the webhook would have to swallow.
	if written, err := chans.RecordDelivery(ctx, &channels.ChannelMessage{
		ChannelID:   chB.ID,
		ExternalRef: refB,
		Direction:   "inbound",
		Status:      channels.StatusReceived,
	}); err != nil || written {
		t.Fatalf("duplicate delivery: written=%v err=%v", written, err)
	}

	// A receipt for a channel that does not exist has no owner to copy.
	if _, err := chans.RecordDelivery(ctx, &channels.ChannelMessage{
		ChannelID:   uniqueID("chan"),
		ExternalRef: uniqueID("ref"),
		Direction:   "inbound",
	}); !errors.Is(err, channels.ErrChannelNotFound) {
		t.Fatalf("delivery on a missing channel: err = %v, want ErrChannelNotFound", err)
	}

	// The list is scoped by (channel_id, owner_id), not by channel alone.
	if got, err := chans.ListDeliveries(ctx, chB.ID, ownerA); err != nil || len(got) != 0 {
		t.Fatalf("cross-owner ListDeliveries = %+v, err=%v", got, err)
	}
	got, err := chans.ListDeliveries(ctx, chB.ID, ownerB)
	if err != nil {
		t.Fatalf("owner ListDeliveries: %v", err)
	}
	if len(got) != 1 || got[0].ExternalRef != refB {
		t.Fatalf("owner ListDeliveries = %+v", got)
	}
	// The receipt carries the channel's owner, read inside the insert
	// transaction — never a value handed in by the caller.
	if got[0].OwnerID != ownerB {
		t.Fatalf("receipt owner = %q, want %q", got[0].OwnerID, ownerB)
	}

	if m, err := chans.GetDelivery(ctx, chB.ID, refB, ownerA); err != nil || m != nil {
		t.Fatalf("cross-owner GetDelivery = %+v, err=%v", m, err)
	}
	if m, err := chans.GetDelivery(ctx, chB.ID, refB, ownerB); err != nil || m == nil {
		t.Fatalf("owner GetDelivery = %+v, err=%v", m, err)
	}

	// A cross-owner retry reports false and rewrites nothing.
	updated, err := chans.UpdateDelivery(ctx, &channels.ChannelMessage{
		ID:        refB,
		ChannelID: chB.ID,
		Status:    channels.StatusDelivered,
	}, ownerA)
	if err != nil || updated {
		t.Fatalf("cross-owner UpdateDelivery: updated=%v err=%v", updated, err)
	}
	if m, err := chans.GetDelivery(ctx, chB.ID, refB, ownerB); err != nil || m == nil || m.Status != channels.StatusReceived {
		t.Fatalf("cross-owner retry changed the receipt: %+v, err=%v", m, err)
	}

	// The owner's own retry updates the row in place — one line per message.
	updated, err = chans.UpdateDelivery(ctx, &channels.ChannelMessage{
		ID:        refB,
		ChannelID: chB.ID,
		Status:    channels.StatusFailed,
		Error:     "boom",
	}, ownerB)
	if err != nil || !updated {
		t.Fatalf("owner UpdateDelivery: updated=%v err=%v", updated, err)
	}
	if m, err := chans.GetDelivery(ctx, chB.ID, refB, ownerB); err != nil || m == nil || m.Status != channels.StatusFailed || m.Error != "boom" {
		t.Fatalf("owner retry did not stick: %+v, err=%v", m, err)
	}
}

// TestThreadMessagesOwnerIsolation covers the message child table. The Go side
// scopes the parent read (ClearMessages) and the insert's owner lookup
// (AddMessage); listing the messages themselves is left to the policy in
// migrations/030_threads_rls.sql, which tests/rls_test.go asserts with
// RLSEnforce: true (a second owner's ListMessages comes back empty there).
func TestThreadMessagesOwnerIsolation(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()
	thr := NewThreadsStore(db)
	ownerA, ownerB := uniqueName("owner-a"), uniqueName("owner-b")

	newThread := func(owner string) *threads.Thread {
		t.Helper()
		th, err := thr.CreateThread(ctx, &threads.Thread{Title: "messages-" + owner, OwnerID: owner})
		if err != nil {
			t.Fatalf("create thread: %v", err)
		}
		return th
	}
	tA, tB := newThread(ownerA), newThread(ownerB)

	t.Cleanup(func() {
		for _, th := range []*threads.Thread{tA, tB} {
			if err := thr.DeleteThread(ctx, th.ID, th.OwnerID); err != nil && !errors.Is(err, threads.ErrNotFound) {
				t.Errorf("cleanup thread %s: %v", th.ID, err)
			}
		}
	})

	add := func(th *threads.Thread, body string) {
		t.Helper()
		if _, err := thr.AddMessage(ctx, &threads.Message{
			ThreadID:   th.ID,
			AuthorType: "user",
			AuthorID:   th.OwnerID,
			Body:       body,
		}); err != nil {
			t.Fatalf("add message: %v", err)
		}
	}
	add(tA, "hello from A")
	add(tB, "hello from B")

	msgs, err := thr.ListMessages(ctx, tA.ID, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "hello from A" {
		t.Fatalf("owner ListMessages = %+v, err=%v", msgs, err)
	}

	// ClearMessages reads the parent through ownedWhere in the same transaction,
	// so a thread the caller cannot see answers ErrNotFound rather than a silent
	// 204 over zero deleted rows.
	if err := thr.ClearMessages(ctx, tA.ID, ownerB); !errors.Is(err, threads.ErrNotFound) {
		t.Fatalf("cross-owner ClearMessages: err = %v, want ErrNotFound", err)
	}
	if msgs, err := thr.ListMessages(ctx, tA.ID, 0); err != nil || len(msgs) != 1 {
		t.Fatalf("cross-owner ClearMessages removed the history: %+v, err=%v", msgs, err)
	}

	// The owner's own clear does empty the history and leaves the thread.
	if err := thr.ClearMessages(ctx, tB.ID, ownerB); err != nil {
		t.Fatalf("owner ClearMessages: %v", err)
	}
	if msgs, err := thr.ListMessages(ctx, tB.ID, 0); err != nil || len(msgs) != 0 {
		t.Fatalf("owner ClearMessages left %+v, err=%v", msgs, err)
	}
	if th, err := thr.GetThread(ctx, tB.ID, ownerB); err != nil || th == nil {
		t.Fatalf("thread did not survive ClearMessages: %+v, err=%v", th, err)
	}

	// A message on a thread that does not exist has no owner to copy, and the
	// insert refuses rather than writing an ownerless row.
	if _, err := thr.AddMessage(ctx, &threads.Message{
		ThreadID:   uniqueID("thread"),
		AuthorType: "user",
		Body:       "lost",
	}); !errors.Is(err, threads.ErrNotFound) {
		t.Fatalf("message on a missing thread: err = %v, want ErrNotFound", err)
	}
}

// hasID is a linear scan; the lists here hold a handful of rows and a set would
// only hide which id was found.
func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
