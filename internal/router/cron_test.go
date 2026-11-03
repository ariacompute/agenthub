package router

import (
	"context"
	"testing"
	"time"

	"agenthub/internal/schedules"
)

// fakeSchedules is the schedules.Store port with no database behind it.
type fakeSchedules struct {
	rows []*schedules.Schedule
}

func (f *fakeSchedules) List(ctx context.Context, ownerID string) ([]*schedules.Schedule, error) {
	out := make([]*schedules.Schedule, 0, len(f.rows))
	for _, r := range f.rows {
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeSchedules) Get(ctx context.Context, id, ownerID string) (*schedules.Schedule, error) {
	for _, r := range f.rows {
		if r.ID == id && r.OwnerID == ownerID {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeSchedules) Create(ctx context.Context, s *schedules.Schedule) (*schedules.Schedule, error) {
	f.rows = append(f.rows, s)
	return s, nil
}

func (f *fakeSchedules) Update(ctx context.Context, s *schedules.Schedule, ownerID string) (*schedules.Schedule, error) {
	for i, r := range f.rows {
		if r.ID == s.ID && r.OwnerID == ownerID {
			cp := *s
			f.rows[i] = &cp
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeSchedules) Delete(ctx context.Context, id, ownerID string) (bool, error) {
	return false, nil
}

func TestCronSourceFiresADueScheduleOnce(t *testing.T) {
	e, _, c := newTestEngine(t)
	store := &fakeSchedules{rows: []*schedules.Schedule{{
		ID: "sched-1", Goal: "compile the daily digest", Trigger: string(schedules.TriggerCron),
		CronExpr: "0 * * * *", Enabled: true, OwnerID: "u-1",
	}}}
	src := NewCronSource(store, e)

	fired, err := src.Scan(context.Background(), c.now())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if fired != 1 {
		t.Fatalf("fired = %d, want 1", fired)
	}
	triggers, _ := e.ListTriggers(context.Background(), TriggerQuery{})
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	if triggers[0].Source != string(SourceCron) || metaString(triggers[0].Meta, "goal") != "compile the daily digest" {
		t.Fatalf("trigger = %+v", triggers[0])
	}
	// The row now carries a real next run instead of a placeholder.
	if store.rows[0].NextRun == "" {
		t.Fatal("the schedule was not advanced")
	}
	if store.rows[0].LastStatus != "triggered" {
		t.Fatalf("lastStatus = %q", store.rows[0].LastStatus)
	}

	// A second scan in the same minute must not open a second ticket.
	src.lastScan = time.Time{}
	if _, err := src.Scan(context.Background(), c.now().Add(2*time.Second)); err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	triggers, _ = e.ListTriggers(context.Background(), TriggerQuery{})
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want the firing to be idempotent", len(triggers))
	}
}

func TestCronSourceSkipsDisabledAndManualSchedules(t *testing.T) {
	e, _, c := newTestEngine(t)
	store := &fakeSchedules{rows: []*schedules.Schedule{
		{ID: "off", Trigger: string(schedules.TriggerCron), CronExpr: "0 * * * *", Enabled: false},
		{ID: "manual", Trigger: string(schedules.TriggerManual), Enabled: true},
		{ID: "no-expr", Trigger: string(schedules.TriggerCron), Enabled: true},
	}}
	src := NewCronSource(store, e)
	fired, err := src.Scan(context.Background(), c.now())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if fired != 0 {
		t.Fatalf("fired = %d, want 0", fired)
	}
}

func TestCronSourceMarksABrokenExpression(t *testing.T) {
	e, _, c := newTestEngine(t)
	store := &fakeSchedules{rows: []*schedules.Schedule{{
		ID: "broken", Goal: "x", Trigger: string(schedules.TriggerCron),
		CronExpr: "every morning", Enabled: true,
	}}}
	src := NewCronSource(store, e)
	if _, err := src.Scan(context.Background(), c.now()); err != nil {
		t.Fatalf("a broken expression must not fail the scan: %v", err)
	}
	if store.rows[0].LastStatus != "invalid_cron" {
		t.Fatalf("lastStatus = %q, want invalid_cron", store.rows[0].LastStatus)
	}
}
