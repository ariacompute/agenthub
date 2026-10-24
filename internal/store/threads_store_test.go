package store

import (
	"context"
	"errors"
	"testing"

	"agenthub/internal/threads"
)

// TestThreadsStore exercises the GORM-backed collaboration store against a real
// PostgreSQL. It skips without DATABASE_URL, the same gate every DB-backed store
// test in this package uses.
func TestThreadsStore(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewThreadsStore(db)
	ctx := context.Background()

	// Owner is mandatory.
	if _, err := s.CreateThread(ctx, &threads.Thread{Title: "x"}); err == nil {
		t.Fatal("CreateThread without owner must fail")
	}

	th, err := s.CreateThread(ctx, &threads.Thread{Title: "collab", OwnerID: "dev", Participants: []string{"a1", "a2"}})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if th.ID == "" || th.OwnerID != "dev" || len(th.Participants) != 2 {
		t.Fatalf("created = %+v", th)
	}

	// Empty title falls back to the default.
	def, err := s.CreateThread(ctx, &threads.Thread{OwnerID: "dev"})
	if err != nil {
		t.Fatalf("CreateThread default title: %v", err)
	}
	if def.Title != "New thread" {
		t.Fatalf("default title = %q", def.Title)
	}

	// GetThread is owner-scoped.
	if got, err := s.GetThread(ctx, th.ID, "dev"); err != nil || got.ID != th.ID {
		t.Fatalf("GetThread owner: err=%v got=%+v", err, got)
	}
	if _, err := s.GetThread(ctx, th.ID, "other"); !errors.Is(err, threads.ErrNotFound) {
		t.Fatalf("other owner: err=%v, want ErrNotFound", err)
	}
	if _, err := s.GetThread(ctx, "missing", "dev"); !errors.Is(err, threads.ErrNotFound) {
		t.Fatalf("missing: err=%v, want ErrNotFound", err)
	}

	// ListThreads only returns the caller's threads.
	list, err := s.ListThreads(ctx, "dev")
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("ListThreads = %d, want at least 2", len(list))
	}

	// AddMessage + ListMessages, including the sinceSeq cursor.
	first, err := s.AddMessage(ctx, &threads.Message{ThreadID: th.ID, AuthorType: "user", AuthorID: "dev", Body: "hi"})
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if first.ID == "" {
		t.Fatal("message id is empty")
	}
	if _, err := s.AddMessage(ctx, &threads.Message{ThreadID: th.ID, AuthorType: "user", AuthorID: "dev", Body: "again"}); err != nil {
		t.Fatalf("AddMessage second: %v", err)
	}
	all, err := s.ListMessages(ctx, th.ID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListMessages = %d, want 2", len(all))
	}
	since, err := s.ListMessages(ctx, th.ID, parseSeq(first.ID))
	if err != nil {
		t.Fatalf("ListMessages since: %v", err)
	}
	if len(since) != 1 || since[0].Body != "again" {
		t.Fatalf("ListMessages since = %+v, want [again]", since)
	}

	// UpsertAgentMessage creates keyed by task id, then updates the body in place.
	created, err := s.UpsertAgentMessage(ctx, th.ID, "task-1", "agent-1", "")
	if err != nil {
		t.Fatalf("UpsertAgentMessage create: %v", err)
	}
	if created.AuthorType != "agent" || created.TaskID != "task-1" || created.ResourceID != "agent-1" {
		t.Fatalf("created = %+v", created)
	}
	updated, err := s.UpsertAgentMessage(ctx, th.ID, "task-1", "agent-1", "the reply")
	if err != nil {
		t.Fatalf("UpsertAgentMessage update: %v", err)
	}
	if updated.ID != created.ID || updated.Body != "the reply" {
		t.Fatalf("update did not edit in place: created=%+v updated=%+v", created, updated)
	}

	// SetParticipants persists and round-trips.
	if err := s.SetParticipants(ctx, th.ID, []string{"a3"}); err != nil {
		t.Fatalf("SetParticipants: %v", err)
	}
	got, _ := s.GetThread(ctx, th.ID, "dev")
	if len(got.Participants) != 1 || got.Participants[0] != "a3" {
		t.Fatalf("participants = %v", got.Participants)
	}

	// UpsertAgentMessage requires a task id.
	if _, err := s.UpsertAgentMessage(ctx, th.ID, "", "agent-1", "x"); err == nil {
		t.Fatal("empty task id must fail")
	}
}

// parseSeq converts the string message id (a decimal integer) into the numeric
// cursor ListMessages expects.
func parseSeq(s string) int64 {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int64(r-'0')
	}
	return n
}
