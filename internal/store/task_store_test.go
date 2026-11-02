package store

import (
	"context"
	"errors"
	"testing"

	"agenthub/internal/a2a"
	"agenthub/internal/hub"
	"agenthub/internal/identity"
)

// The task repository is the one place a task, its artifacts, its spans, its
// parked question and its callback config are actually stored, and it repeats
// the owner filter in Go on purpose: RLS protects the database, this filter keeps
// the answer right when an operator runs with AGENTHUB_RLS=off. Both halves are
// exercised here, the owner isolation included: a row belonging to somebody else
// must come back as absent, never as another user's task.

func taskOwnerCtx(owner string) context.Context {
	return identity.With(context.Background(), identity.Identity{OwnerID: owner})
}

func newTestTask(id, owner, resource string) *a2a.Task {
	return &a2a.Task{
		Kind:       "task",
		ID:         id,
		ContextID:  "ctx-" + id,
		ResourceID: resource,
		Status:     a2a.TaskStatus{State: a2a.TaskStateSubmitted},
		History:    []a2a.Message{{Role: "user", Parts: []a2a.Part{a2a.NewTextPart("hello")}}},
		Metadata:   map[string]any{"trace": id},
	}
}

func TestTaskRepositoryRoundTripsATask(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-owner")
	id := uniqueName("task")
	ctx := taskOwnerCtx(owner)

	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != id || got.ResourceID != "demo-agent" || got.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("task = %+v", got)
	}
	if len(got.History) != 1 || got.History[0].Parts[0].Text != "hello" {
		t.Fatalf("history = %+v", got.History)
	}
	if got.Metadata["trace"] != id {
		t.Fatalf("metadata = %v", got.Metadata)
	}

	// The task was written with the caller's identity as its owner, which is what
	// the policies in 026 compare against.
	var ownerID string
	if err := db.Raw(`SELECT owner_id FROM tasks WHERE id = ?`, id).Scan(&ownerID).Error; err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if ownerID != owner {
		t.Fatalf("owner_id = %q, want %q", ownerID, owner)
	}

	// Somebody else's id is an absence, not a permission error: existence is not
	// leaked, and the handler answers 404 either way.
	if _, err := repo.Get(taskOwnerCtx(uniqueName("task-other")), id); !errors.Is(err, hub.ErrTaskNotFound) {
		t.Fatalf("Get as another owner = %v, want ErrTaskNotFound", err)
	}
	// The process identity is not a user: it sees every row.
	if sys, err := repo.Get(identity.System(), id); err != nil || sys.ID != id {
		t.Fatalf("Get as the system identity = %v, %v", sys, err)
	}
	if _, err := repo.Get(ctx, id+"-missing"); !errors.Is(err, hub.ErrTaskNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrTaskNotFound", err)
	}
}

func TestTaskRepositoryRejectsAnEmptyTask(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	ctx := identity.System()

	if err := repo.Create(ctx, nil); err == nil {
		t.Fatal("Create(nil) must fail")
	}
	if err := repo.Create(ctx, &a2a.Task{}); err == nil {
		t.Fatal("Create without an id must fail")
	}
}

func TestTaskRepositoryListGroupsArtifactsPerTask(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-list-owner")
	ctx := taskOwnerCtx(owner)

	first := uniqueName("task-a")
	second := uniqueName("task-b")
	for _, id := range []string{first, second} {
		id := id
		if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })
		if _, err := repo.AddArtifact(ctx, id, a2a.Artifact{
			ArtifactID: "agent.a2a",
			Name:       "agent.a2a",
			Parts:      []a2a.Part{a2a.NewTextPart(id)},
		}); err != nil {
			t.Fatalf("AddArtifact %s: %v", id, err)
		}
	}

	tasks, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]int{}
	for _, task := range tasks {
		if task.ID == first || task.ID == second {
			seen[task.ID] = len(task.Artifacts)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("List returned %d of this owner's tasks", len(seen))
	}
	for id, arts := range seen {
		if arts != 1 {
			t.Fatalf("task %s came back with %d artifacts, want 1", id, arts)
		}
	}

	// An owner with nothing gets an empty list, not nil and not somebody else's
	// rows: the console renders "no tasks" from that.
	empty, err := repo.List(taskOwnerCtx(uniqueName("task-nobody")))
	if err != nil {
		t.Fatalf("List as a stranger: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("List as a stranger = %v, want an empty slice", empty)
	}
}

// The state update is conditional on the state the caller believed it was: two
// writers advancing the same task must not both succeed.
func TestTaskRepositorySetStateIsConditional(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-state-owner")
	id := uniqueName("task-state")
	ctx := taskOwnerCtx(owner)

	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	msg := &a2a.Message{Role: "agent", Parts: []a2a.Part{a2a.NewTextPart("working on it")}}
	moved, err := repo.SetState(ctx, id, a2a.TaskStateSubmitted, a2a.TaskStateWorking, msg)
	if err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if moved.Status.State != a2a.TaskStateWorking {
		t.Fatalf("state = %q, want working", moved.Status.State)
	}
	if moved.Status.Message == nil || moved.Status.Message.Parts[0].Text != "working on it" {
		t.Fatalf("status message = %+v", moved.Status.Message)
	}

	// A second writer that still believes the task is submitted loses: the row it
	// wanted to update no longer matches, and it is told so rather than
	// overwriting the newer state.
	if _, err := repo.SetState(ctx, id, a2a.TaskStateSubmitted, a2a.TaskStateCompleted, nil); !errors.Is(err, hub.ErrStateConflict) {
		t.Fatalf("stale SetState = %v, want ErrStateConflict", err)
	}
	if _, err := repo.SetState(ctx, id+"-missing", a2a.TaskStateSubmitted, a2a.TaskStateWorking, nil); !errors.Is(err, hub.ErrTaskNotFound) {
		t.Fatalf("SetState(missing) = %v, want ErrTaskNotFound", err)
	}
	// Another owner's task does not exist for this caller either.
	if _, err := repo.SetState(taskOwnerCtx(uniqueName("task-stranger")), id, a2a.TaskStateWorking, a2a.TaskStateCompleted, nil); !errors.Is(err, hub.ErrTaskNotFound) {
		t.Fatalf("SetState as another owner = %v, want ErrTaskNotFound", err)
	}
}

func TestTaskRepositoryAddArtifactNeedsATask(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-art-owner")
	id := uniqueName("task-art")
	ctx := taskOwnerCtx(owner)

	if _, err := repo.AddArtifact(ctx, id, a2a.Artifact{ArtifactID: "x"}); !errors.Is(err, hub.ErrTaskNotFound) {
		t.Fatalf("AddArtifact on a missing task = %v, want ErrTaskNotFound", err)
	}
	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	appendFlag, lastChunk := true, false
	got, err := repo.AddArtifact(ctx, id, a2a.Artifact{
		ArtifactID:  "agent.a2a",
		Name:        "agent.a2a",
		Description: "the answer",
		Parts:       []a2a.Part{a2a.NewTextPart("done")},
		Index:       2,
		Append:      &appendFlag,
		LastChunk:   &lastChunk,
		Metadata:    map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatalf("AddArtifact: %v", err)
	}
	if len(got.Artifacts) != 1 {
		t.Fatalf("artifacts = %+v, want one", got.Artifacts)
	}
	art := got.Artifacts[0]
	if art.ArtifactID != "agent.a2a" || art.Index != 2 || art.Parts[0].Text != "done" {
		t.Fatalf("artifact = %+v", art)
	}
	if art.Append == nil || !*art.Append || art.LastChunk == nil || *art.LastChunk {
		t.Fatalf("streaming flags did not round trip: %+v", art)
	}
}

func TestTaskRepositoryAppendHistoryKeepsEveryTurn(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-hist-owner")
	id := uniqueName("task-hist")
	ctx := taskOwnerCtx(owner)

	if err := repo.AppendHistory(ctx, id, a2a.Message{Role: "user"}); !errors.Is(err, hub.ErrTaskNotFound) {
		t.Fatalf("AppendHistory on a missing task = %v, want ErrTaskNotFound", err)
	}
	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	for _, text := range []string{"second", "third"} {
		if err := repo.AppendHistory(ctx, id, a2a.Message{
			Role:  "user",
			Parts: []a2a.Part{a2a.NewTextPart(text)},
		}); err != nil {
			t.Fatalf("AppendHistory %s: %v", text, err)
		}
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The first turn came from the task itself; the other two were appended in a
	// read-modify-write transaction, and losing either would lose a turn of the
	// conversation.
	if len(got.History) != 3 {
		t.Fatalf("history = %+v, want three turns", got.History)
	}
	if got.History[1].Parts[0].Text != "second" || got.History[2].Parts[0].Text != "third" {
		t.Fatalf("history order = %+v", got.History)
	}
}

func TestTaskRepositorySpansIgnoreDuplicates(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-span-owner")
	id := uniqueName("task-span")
	ctx := taskOwnerCtx(owner)

	if err := repo.AddSpan(ctx, hub.Span{TaskID: id}); err == nil {
		t.Fatal("AddSpan without an id must fail")
	}
	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	span := hub.Span{
		ID:         uniqueName("span"),
		TaskID:     id,
		ResourceID: "demo-agent",
		Kind:       "adapter",
		Label:      "google-adk",
		StartAt:    "2026-01-01T00:00:00Z",
		EndAt:      "2026-01-01T00:00:02Z",
		DurationMS: 2000,
		Status:     "ok",
	}
	// A retried run re-sends its span; the (id) key makes that a no-op rather than
	// a duplicate on the timeline.
	for i := 0; i < 2; i++ {
		if err := repo.AddSpan(ctx, span); err != nil {
			t.Fatalf("AddSpan #%d: %v", i, err)
		}
	}

	spans, err := repo.Spans(ctx, id)
	if err != nil {
		t.Fatalf("Spans: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want one after a retried write", spans)
	}
	if spans[0].Label != "google-adk" || spans[0].DurationMS != 2000 {
		t.Fatalf("span = %+v", spans[0])
	}
	// A stranger sees no spans, which is the owner filter working a second time.
	if other, err := repo.Spans(taskOwnerCtx(uniqueName("task-stranger")), id); err != nil || len(other) != 0 {
		t.Fatalf("Spans as another owner = %v, %v", other, err)
	}
}

func TestTaskRepositoryPendingQuestionIsOptional(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-pend-owner")
	id := uniqueName("task-pend")
	ctx := taskOwnerCtx(owner)

	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	if pending, err := repo.Pending(ctx, id); err != nil || pending != nil {
		t.Fatalf("Pending before any question = %v, %v", pending, err)
	}
	// Clearing a question that was never asked is a no-op, not an error: resume
	// calls it unconditionally.
	if err := repo.SetPending(ctx, id, nil); err != nil {
		t.Fatalf("SetPending(nil): %v", err)
	}

	if err := repo.SetPending(ctx, id, &hub.PendingInput{Prompt: "which env?", AskedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("SetPending: %v", err)
	}
	got, err := repo.Pending(ctx, id)
	if err != nil || got == nil || got.Prompt != "which env?" {
		t.Fatalf("Pending = %+v, %v", got, err)
	}
	// A re-asked question replaces the row (task_id is the conflict key) rather
	// than stacking a second one.
	if err := repo.SetPending(ctx, id, &hub.PendingInput{Prompt: "which region?", AskedAt: "2026-01-02T00:00:00Z"}); err != nil {
		t.Fatalf("SetPending again: %v", err)
	}
	if got, err := repo.Pending(ctx, id); err != nil || got.Prompt != "which region?" {
		t.Fatalf("Pending after a re-ask = %+v, %v", got, err)
	}
	if err := repo.SetPending(ctx, id, nil); err != nil {
		t.Fatalf("SetPending(clear): %v", err)
	}
	if pending, err := repo.Pending(ctx, id); err != nil || pending != nil {
		t.Fatalf("Pending after the answer = %v, %v", pending, err)
	}
}

func TestTaskRepositoryPushConfigIsOptional(t *testing.T) {
	db := openGovernanceDB(t)
	repo := NewTaskRepository(db)
	owner := uniqueName("task-push-owner")
	id := uniqueName("task-push")
	ctx := taskOwnerCtx(owner)

	if err := repo.Create(ctx, newTestTask(id, owner, "demo-agent")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM tasks WHERE id = ?`, id).Error })

	if cfg, err := repo.Push(ctx, id); err != nil || cfg != nil {
		t.Fatalf("Push before any config = %v, %v", cfg, err)
	}
	if err := repo.SetPush(ctx, id, &a2a.PushNotificationConfig{URL: "https://example.com/hook", Token: "t"}); err != nil {
		t.Fatalf("SetPush: %v", err)
	}
	cfg, err := repo.Push(ctx, id)
	if err != nil || cfg == nil || cfg.URL != "https://example.com/hook" || cfg.Token != "t" {
		t.Fatalf("Push = %+v, %v", cfg, err)
	}
	if err := repo.SetPush(ctx, id, nil); err != nil {
		t.Fatalf("SetPush(nil): %v", err)
	}
	if cfg, err := repo.Push(ctx, id); err != nil || cfg != nil {
		t.Fatalf("Push after clearing = %v, %v", cfg, err)
	}
}
