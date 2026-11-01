package schedules

import (
	"testing"
	"time"
)

func TestNextRunFollowsTheExpression(t *testing.T) {
	from := time.Date(2026, 11, 1, 10, 7, 0, 0, time.UTC)
	cases := []struct {
		name string
		expr string
		want time.Time
	}{
		{"every fifteen minutes", "*/15 * * * *", time.Date(2026, 11, 1, 10, 15, 0, 0, time.UTC)},
		{"hourly on the hour", "0 * * * *", time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)},
		{"daily at 03:00", "0 3 * * *", time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextRun(tc.expr, from)
			if err != nil {
				t.Fatalf("next run: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("next run = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNextRunRejectsAMalformedExpression(t *testing.T) {
	if _, err := NextRun("every tuesday", time.Now()); err == nil {
		t.Fatal("want an error for a prose expression")
	}
}

func TestValidateCronAcceptsAnEmptyExpression(t *testing.T) {
	if err := ValidateCron(""); err != nil {
		t.Fatalf("an empty expression means no cron, not an error: %v", err)
	}
	if err := ValidateCron("0 3 * *"); err == nil {
		t.Fatal("want an error for four fields")
	}
}

func TestAdvanceOnlyAnswersForCronSchedules(t *testing.T) {
	from := time.Date(2026, 11, 1, 10, 7, 0, 0, time.UTC)
	cron := &Schedule{Enabled: true, Trigger: string(TriggerCron), CronExpr: "0 3 * * *"}
	if got := Advance(cron, from); got != "2026-11-02T03:00:00Z" {
		t.Fatalf("advance = %q", got)
	}
	manual := &Schedule{Enabled: true, Trigger: string(TriggerManual)}
	if got := Advance(manual, from); got != "" {
		t.Fatalf("a manual schedule has no next run, got %q", got)
	}
	broken := &Schedule{Enabled: true, Trigger: string(TriggerCron), CronExpr: "nope"}
	if got := Advance(broken, from); got != "" {
		t.Fatalf("a broken expression yields no next run, got %q", got)
	}
}
