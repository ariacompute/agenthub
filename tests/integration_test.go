// Package tests holds integration cases that require a real PostgreSQL.
//
// Gating rule: when DATABASE_URL is unset every case skips — agenthub never
// fabricates a result. Local database: `make db-up` (docker compose, host port
// 5434), then `make test-integration`.
package tests

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"agenthub/internal/a2a"
	"agenthub/internal/adapter"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/hub"
	"agenthub/internal/model"
	"agenthub/internal/registry"
	"agenthub/internal/store"

	"gorm.io/gorm"
)

// openDBReal opens the real database and runs the embedded migrations; it skips
// when DATABASE_URL is unset.
func openDBReal(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping DB-backed integration test (make db-up)")
	}
	db, err := store.Open(dsn, false)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(db); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return db
}

func testIntegrationConfig() *config.Config {
	return &config.Config{
		HTTPPort:        "3500",
		Env:             "dev",
		MaxParallel:     4,
		CLITimeout:      time.Second,
		CLIAllowlist:    []string{"echo", "ls"},
		MaxOutputBytes:  4096,
		EventBuffer:     64,
		PushEnabled:     true,
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		PushRecordLimit: 100,
	}
}

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	db := openDBReal(t)

	// Running the full migration set again must be a no-op.
	if err := store.Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var applied []model.SchemaMigration
	if err := db.Order("filename ASC").Find(&applied).Error; err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if len(applied) < 4 {
		t.Fatalf("applied migrations = %d, want at least 4", len(applied))
	}
	for _, row := range applied {
		if !strings.HasSuffix(row.Filename, ".sql") {
			t.Fatalf("recorded migration %q is not a sql file", row.Filename)
		}
		if row.AppliedAt.IsZero() {
			t.Fatalf("migration %q has no applied_at", row.Filename)
		}
	}
	// Every table the models rely on must exist.
	for _, table := range []string{
		"resources", "tasks", "task_artifacts", "task_spans",
		"task_pending", "task_push", "events", "push_records",
	} {
		var name string
		err := db.Raw("SELECT to_regclass(?::text)::text", "public."+table).Scan(&name).Error
		if err != nil || name == "" {
			t.Fatalf("table %s missing (got %q, err %v)", table, name, err)
		}
	}
}

func TestIntegrationResourceCRUD(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	rs := store.NewResourceStore(db)

	entry := &registry.Entry{
		ID:           "it-" + registry.NewID("res"),
		Kind:         registry.KindMCP,
		Name:         "it-mcp",
		Description:  "integration resource",
		Endpoint:     "stdio://it",
		Transport:    registry.TransportStdio,
		Version:      "1.2.3",
		Tags:         []string{"it"},
		Capabilities: []string{"tools/list"},
		AuthSchemes:  []string{"bearer"},
		Env:          map[string]string{"TOKEN": "x"},
		Metadata:     map[string]string{"command": "it"},
		Health:       registry.HealthHealthy,
	}
	stored, err := rs.Upsert(ctx, entry)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, entry.ID) })

	if stored.ID != entry.ID || stored.Kind != registry.KindMCP || stored.CreatedAt.IsZero() {
		t.Fatalf("stored = %+v", stored)
	}

	got, err := rs.Get(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Structured columns must survive the jsonb round trip.
	if len(got.Tags) != 1 || got.Tags[0] != "it" {
		t.Fatalf("tags = %+v", got.Tags)
	}
	if got.Env["TOKEN"] != "x" || got.Metadata["command"] != "it" {
		t.Fatalf("maps = %+v %+v", got.Env, got.Metadata)
	}
	if got.Transport != registry.TransportStdio || got.Version != "1.2.3" {
		t.Fatalf("entry = %+v", got)
	}

	// Updating must refresh the payload but keep the original created_at.
	got.Name = "it-mcp-renamed"
	got.Tags = []string{"it", "renamed"}
	if _, err := rs.Upsert(ctx, got); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	again, err := rs.Get(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if again.Name != "it-mcp-renamed" || len(again.Tags) != 2 {
		t.Fatalf("after update = %+v", again)
	}
	// created_at must survive an update; Postgres stores microseconds, so the
	// comparison is truncated to the persisted precision.
	if !again.CreatedAt.Truncate(time.Microsecond).Equal(stored.CreatedAt.Truncate(time.Microsecond)) {
		t.Fatalf("created_at changed: %s -> %s", stored.CreatedAt, again.CreatedAt)
	}

	// Listing filters by kind.
	mcps, err := rs.List(ctx, string(registry.KindMCP))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, e := range mcps {
		if e.ID == entry.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("resource %s missing from kind listing", entry.ID)
	}

	byKind, byHealth, err := rs.Counts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if byKind[string(registry.KindMCP)] < 1 {
		t.Fatalf("kind counts = %+v", byKind)
	}
	if byHealth[string(registry.HealthHealthy)] < 1 {
		t.Fatalf("health counts = %+v", byHealth)
	}
	// Absent buckets report zero instead of disappearing from /healthz.
	if _, ok := byKind[string(registry.KindSkill)]; !ok {
		t.Fatalf("kind counts missing the skill bucket: %+v", byKind)
	}

	if err := rs.SetHealth(ctx, entry.ID, registry.HealthDegraded); err != nil {
		t.Fatalf("set health: %v", err)
	}
	if _, byHealth, err := rs.Counts(ctx); err != nil || byHealth[string(registry.HealthDegraded)] < 1 {
		t.Fatalf("health after update = %+v (%v)", byHealth, err)
	}

	// The registry facade must behave exactly like the in-memory one.
	reg := registry.NewWithStore(rs)
	if _, ok := reg.Get(entry.ID); !ok {
		t.Fatal("registry facade could not read the persisted entry")
	}
	if len(reg.List(string(registry.KindMCP), "it-mcp-renamed")) == 0 {
		t.Fatal("registry facade free text search found nothing")
	}
	if reg.Size() < 1 {
		t.Fatalf("size = %d", reg.Size())
	}

	if err := rs.Delete(ctx, entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := rs.Get(ctx, entry.ID); err == nil {
		t.Fatal("deleted resource still readable")
	}
	if err := rs.Delete(ctx, entry.ID); err == nil {
		t.Fatal("deleting twice must report not found")
	}
	if err := rs.SetHealth(ctx, entry.ID, registry.HealthHealthy); err == nil {
		t.Fatal("health of a missing resource must fail")
	}
}

func TestIntegrationTaskLifecyclePersists(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	repo := store.NewTaskRepository(db)
	tasks := hub.NewTaskStoreWithRepository(repo)

	task := tasks.Create("res-it", a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("hello")))
	if task == nil {
		t.Fatal("create returned no task")
	}
	t.Cleanup(func() { _ = db.Where("id = ?", task.ID).Delete(&model.Task{}) })

	if _, err := repo.Get(ctx, "it-does-not-exist"); err == nil {
		t.Fatal("get of a missing task must fail")
	}

	// Illegal transitions are rejected in Go (CanTransition) before the
	// conditional update reaches the database.
	if updated, err := tasks.SetState(task.ID, a2a.TaskStateCompleted, nil); err == nil {
		t.Fatalf("illegal transition accepted: %+v", updated)
	} else if httputil.AsError(err).Code != hub.CodeInvalidStateTransition {
		t.Fatalf("illegal transition error = %v", err)
	}

	working, err := tasks.SetState(task.ID, a2a.TaskStateWorking, nil)
	if err != nil {
		t.Fatalf("submit -> working: %v", err)
	}
	if working.Status.State != a2a.TaskStateWorking {
		t.Fatalf("state = %s", working.Status.State)
	}
	// A stale expectation must not clobber the row.
	if _, err := repo.SetState(ctx, task.ID, a2a.TaskStateSubmitted, a2a.TaskStateWorking, nil); !errors.Is(err, hub.ErrStateConflict) {
		t.Fatalf("conditional update with a stale state = %v, want ErrStateConflict", err)
	}
	if _, err := tasks.SetState("it-missing", a2a.TaskStateWorking, nil); !errors.Is(err, registry.ErrNotFound) &&
		httputil.AsError(err).Code != hub.CodeTaskNotFound {
		t.Fatalf("transition of a missing task = %v", err)
	}

	reply := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("done"))
	if _, err := tasks.AddArtifact(task.ID, a2a.Artifact{
		ArtifactID: "art-1", Name: "result", Parts: []a2a.Part{a2a.NewTextPart("done")}, Index: 0,
	}); err != nil {
		t.Fatalf("add artifact: %v", err)
	}
	if err := tasks.AppendHistory(task.ID, reply); err != nil {
		t.Fatalf("append history: %v", err)
	}

	span := tasks.StartSpan(task.ID, "res-it", "", "invoke:res-it")
	tasks.FinishSpan(span, hub.SpanStatusOK, "")

	parked := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("approve?"))
	if _, err := tasks.SetState(task.ID, a2a.TaskStateInputRequired, &parked); err != nil {
		t.Fatalf("working -> input-required: %v", err)
	}
	if err := tasks.SetPending(task.ID, "approve?"); err != nil {
		t.Fatalf("set pending: %v", err)
	}

	cfg := &a2a.PushNotificationConfig{ID: "push-1", URL: "http://callback.example", Token: "t"}
	if err := tasks.SetPush(task.ID, cfg); err != nil {
		t.Fatalf("set push: %v", err)
	}

	// Everything above must be readable back from the database.
	reloaded := tasks.Get(task.ID)
	if reloaded == nil {
		t.Fatal("reload: task vanished")
	}
	if reloaded.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s", reloaded.Status.State)
	}
	if reloaded.Status.Message == nil || reloaded.Status.Message.Text() != "approve?" {
		t.Fatalf("status message = %+v", reloaded.Status.Message)
	}
	if len(reloaded.Artifacts) != 1 || reloaded.Artifacts[0].ArtifactID != "art-1" {
		t.Fatalf("artifacts = %+v", reloaded.Artifacts)
	}
	if len(reloaded.Artifacts) == 1 && len(reloaded.Artifacts[0].Parts) != 1 {
		t.Fatalf("artifact parts = %+v", reloaded.Artifacts[0].Parts)
	}
	if len(reloaded.History) != 2 || reloaded.History[1].Text() != "done" {
		t.Fatalf("history = %+v", reloaded.History)
	}

	spans := tasks.Spans(task.ID)
	if len(spans) != 1 || spans[0].ID != span.ID || spans[0].Status != hub.SpanStatusOK {
		t.Fatalf("spans = %+v", spans)
	}

	pending := tasks.Pending(task.ID)
	if pending == nil || pending.Prompt != "approve?" {
		t.Fatalf("pending = %+v", pending)
	}
	pushCfg := tasks.Push(task.ID)
	if pushCfg == nil || pushCfg.URL != cfg.URL {
		t.Fatalf("push = %+v", pushCfg)
	}

	// Resuming clears the parked question: A2A goes input-required -> working
	// -> completed, never straight to a terminal state.
	if _, err := tasks.SetState(task.ID, a2a.TaskStateWorking, nil); err != nil {
		t.Fatalf("input-required -> working: %v", err)
	}
	done := a2a.NewMessage(a2a.RoleAgent, a2a.NewTextPart("completed"))
	if _, err := tasks.SetState(task.ID, a2a.TaskStateCompleted, &done); err != nil {
		t.Fatalf("working -> completed: %v", err)
	}
	if p := tasks.Pending(task.ID); p != nil {
		t.Fatalf("pending after completion = %+v", p)
	}
	if gone, err := repo.Pending(ctx, task.ID); err != nil || gone != nil {
		t.Fatalf("pending row after completion = %+v (%v)", gone, err)
	}

	all := tasks.List()
	var listed *a2a.Task
	for _, c := range all {
		if c.ID == task.ID {
			listed = c
		}
	}
	if listed == nil {
		t.Fatalf("task %s missing from the listing", task.ID)
	}
	if len(listed.Artifacts) != 1 {
		t.Fatalf("listing lost the artifacts: %+v", listed.Artifacts)
	}

	// Child rows cascade with the task.
	if err := db.Where("id = ?", task.ID).Delete(&model.Task{}).Error; err != nil {
		t.Fatalf("delete task: %v", err)
	}
	var leftover int64
	if err := db.Model(&model.TaskSpan{}).Where("task_id = ?", task.ID).Count(&leftover).Error; err != nil {
		t.Fatalf("count orphan spans: %v", err)
	}
	if leftover != 0 {
		t.Fatalf("%d spans outlived their task", leftover)
	}
}

func TestIntegrationEventReplay(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	tasks := store.NewTaskRepository(db)
	events := store.NewEventRepository(db)

	task := a2a.NewTask("it-"+registry.NewID("task"), a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("stream")), "res-it")
	if err := tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = db.Where("id = ?", task.ID).Delete(&model.Task{}) })

	for i := uint64(1); i <= 3; i++ {
		ev := hub.Event{
			Seq: i, TaskID: task.ID, Kind: hub.EventKindStatus, At: a2a.Timestamp(time.Now()),
			Status: &a2a.TaskStatusUpdateEvent{
				Kind: a2a.KindStatusUpdate, TaskID: task.ID,
				Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, Final: i == 3,
			},
		}
		if err := events.Append(ctx, ev); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	// A duplicate seq must be ignored, not fail the writer.
	if err := events.Append(ctx, hub.Event{Seq: 2, TaskID: task.ID, Kind: hub.EventKindStatus}); err != nil {
		t.Fatalf("duplicate append: %v", err)
	}

	maxSeq, err := events.MaxSeq(ctx)
	if err != nil {
		t.Fatalf("max seq: %v", err)
	}
	if maxSeq < 3 {
		t.Fatalf("max seq = %d, want >= 3", maxSeq)
	}

	got, err := events.Since(ctx, task.ID, 0, 10)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("since(0) = %d events, want 3", len(got))
	}
	if got[0].Seq != 1 || got[2].Seq != 3 {
		t.Fatalf("replay order = %d %d %d", got[0].Seq, got[1].Seq, got[2].Seq)
	}
	if got[2].Status == nil || !got[2].Status.Final {
		t.Fatalf("payload of the last frame = %+v", got[2].Status)
	}

	resumed, err := events.Since(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatalf("since(1): %v", err)
	}
	if len(resumed) != 2 || resumed[0].Seq != 2 {
		t.Fatalf("since(1) = %+v", resumed)
	}
	empty, err := events.Since(ctx, task.ID, 99, 10)
	if err != nil {
		t.Fatalf("since(99): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("since(99) = %+v, want empty", empty)
	}
}

func TestIntegrationPushRecordsPersist(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	tasks := store.NewTaskRepository(db)
	pushes := store.NewPushRepository(db)

	task := a2a.NewTask("it-"+registry.NewID("task"), a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("push")), "res-it")
	if err := tasks.Create(ctx, task); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = db.Where("id = ?", task.ID).Delete(&model.Task{}) })

	notifier := &hub.MockNotifier{Repo: pushes}
	cfg := a2a.PushNotificationConfig{ID: "cfg-it", URL: "http://callback.example", Token: "t"}
	if err := notifier.Notify(ctx, cfg, task.ID, a2a.TaskStateCompleted); err != nil {
		t.Fatalf("notify: %v", err)
	}

	// Scoped to this task: the table is shared, so the assertions must never
	// depend on rows left behind by another case.
	filtered, err := pushes.List(ctx, task.ID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("list by task = %+v, want exactly one persisted callback", filtered)
	}
	record := filtered[0]
	if record.TaskID != task.ID || record.URL != cfg.URL || record.State != a2a.TaskStateCompleted {
		t.Fatalf("record = %+v", record)
	}
	if record.Config.Token != "t" {
		t.Fatalf("config snapshot = %+v", record.Config)
	}
	// The notifier reads the same rows back through the repository.
	if !containsRecord(notifier.Records(), task.ID, cfg.URL) {
		t.Fatalf("Records() did not surface the persisted callback: %+v", notifier.Records())
	}

	// The retention cap keeps only the newest rows per task.
	for i := 0; i < 4; i++ {
		if err := pushes.Append(ctx, hub.PushRecord{TaskID: task.ID, URL: cfg.URL, State: a2a.TaskStateWorking, Config: cfg}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := pushes.Prune(ctx, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}
	kept, err := pushes.List(ctx, task.ID, 100)
	if err != nil {
		t.Fatalf("list after prune: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("after prune = %d records, want 2", len(kept))
	}
	for _, r := range kept {
		if r.State != a2a.TaskStateWorking {
			t.Fatalf("prune kept the oldest record: %+v", r)
		}
	}
	if err := pushes.Prune(ctx, 0); err == nil {
		t.Fatal("prune with a non positive cap must fail")
	}
}

func containsRecord(records []hub.PushRecord, taskID, url string) bool {
	for _, r := range records {
		if r.TaskID == taskID && r.URL == url {
			return true
		}
	}
	return false
}

// TestIntegrationHubRunPersistsEveryLayer drives one A2A run through the hub
// with all three repositories attached and then reloads it from the database.
func TestIntegrationHubRunPersistsEveryLayer(t *testing.T) {
	db := openDBReal(t)
	if err := store.Seed(db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ctx := context.Background()

	reg := registry.NewWithStore(store.NewResourceStore(db))
	pusher := &hub.MockNotifier{Repo: store.NewPushRepository(db)}
	cfg := testIntegrationConfig()
	h := hub.New(cfg, reg, adapter.Options{
		CLITimeout:     cfg.CLITimeout,
		CLIAllowlist:   cfg.CLIAllowlist,
		MaxOutputBytes: cfg.MaxOutputBytes,
	}, pusher, hub.Persistence{
		Tasks:  store.NewTaskRepository(db),
		Events: store.NewEventRepository(db),
		Pushes: pusher.Repo,
	})
	defer h.Close()

	task, err := h.Send(ctx, a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.RoleUser, a2a.NewTextPart("persist this run")),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	t.Cleanup(func() { _ = db.Where("id = ?", task.ID).Delete(&model.Task{}) })
	if task.Status.State != a2a.TaskStateCompleted && task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state = %s", task.Status.State)
	}

	// Task + spans landed in the database.
	spans := h.Store().Spans(task.ID)
	if len(spans) == 0 {
		t.Fatal("expected at least one persisted span")
	}
	listed := h.Store().List()
	found := false
	for _, c := range listed {
		if c.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task %s missing from the persisted listing", task.ID)
	}

	// The event writer is asynchronous: give it a moment, then replay.
	deadline := time.Now().Add(5 * time.Second)
	var replayed []hub.Event
	for time.Now().Before(deadline) {
		replayed = h.Events(task.ID, 0, 100)
		if len(replayed) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(replayed) == 0 {
		t.Fatal("expected persisted events for the run")
	}
	seen := map[string]bool{}
	for _, ev := range replayed {
		seen[ev.Kind] = true
		if ev.TaskID != task.ID {
			t.Fatalf("event of another task leaked: %+v", ev)
		}
	}
	if !seen[hub.EventKindStatus] {
		t.Fatalf("replayed kinds = %+v, want at least a status frame", seen)
	}

	// A registered callback survives the round trip through push_records.
	pushCfg := &a2a.PushNotificationConfig{ID: "cfg-hub", URL: "http://callback.example/hub"}
	if err := h.Store().SetPush(task.ID, pushCfg); err != nil {
		t.Fatalf("set push: %v", err)
	}
	reloaded := h.Store().Get(task.ID)
	if reloaded == nil {
		t.Fatal("task vanished after the run")
	}
	if reloaded.Status.State != task.Status.State {
		t.Fatalf("reloaded state = %s, want %s", reloaded.Status.State, task.Status.State)
	}
	if got := h.Store().Push(task.ID); got == nil || got.URL != pushCfg.URL {
		t.Fatalf("push config = %+v", got)
	}
}
