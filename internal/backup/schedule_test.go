package backup

import (
	"errors"
	"testing"
	"time"
)

func TestParseCronAcceptsTheDocumentedForms(t *testing.T) {
	cases := []struct {
		expr string
		next string // expected next run from the reference instant, RFC3339
	}{
		{"*/15 * * * *", "2026-03-02T10:15:00Z"},
		{"0 3 * * *", "2026-03-03T03:00:00Z"},
		{"30 9 * * 1", "2026-03-09T09:30:00Z"}, // next Monday
		{"0 0 1 1 *", "2027-01-01T00:00:00Z"},  // 1 January
		{"0 0 * * 0", "2026-03-08T00:00:00Z"},  // Sunday, via 0
		{"0 0 * * 7", "2026-03-08T00:00:00Z"},  // Sunday, via 7
		{"5,55 12 * * *", "2026-03-02T12:05:00Z"},
		{"0 9-17/4 * * *", "2026-03-02T13:00:00Z"},
	}
	// Monday 2 March 2026, 10:07 UTC.
	from := time.Date(2026, 3, 2, 10, 7, 0, 0, time.UTC)
	for _, tc := range cases {
		expr, err := ParseCron(tc.expr)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", tc.expr, err)
		}
		got, err := expr.Next(from)
		if err != nil {
			t.Fatalf("Next(%q): %v", tc.expr, err)
		}
		if want, _ := time.Parse(time.RFC3339, tc.next); !got.Equal(want) {
			t.Fatalf("Next(%q) = %s, want %s", tc.expr, got.Format(time.RFC3339), tc.next)
		}
	}
}

func TestParseCronRejectsMalformedExpressions(t *testing.T) {
	for _, expr := range []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"5-1 * * * *",
		"a * * * *",
	} {
		if _, err := ParseCron(expr); !errors.Is(err, ErrCron) {
			t.Fatalf("ParseCron(%q) = %v, want a cron error", expr, err)
		}
	}
}

func TestCronNextIsStrictlyAfterAndHonoursTheLocation(t *testing.T) {
	// "every day at 03:00" in Shanghai is 19:00 UTC of the previous day.
	expr, err := ParseCron("0 3 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("no tzdata for Asia/Shanghai: %v", err)
	}
	from := time.Date(2026, 3, 2, 12, 0, 0, 0, loc)
	got, err := expr.Next(from)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := time.Date(2026, 3, 3, 3, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
	if !got.After(from) {
		t.Fatal("Next must be strictly after the reference instant")
	}
}

func TestCronNextReportsAnExpressionThatNeverMatches(t *testing.T) {
	// 30 February: valid syntax, no occurrence in the next five years.
	expr, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if _, err := expr.Next(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrNoOccurrence) {
		t.Fatalf("Next = %v, want ErrNoOccurrence", err)
	}
}

func TestScheduleNextIntervalAnchorsOnTheLastRun(t *testing.T) {
	from := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	s := Schedule{Mode: ModeInterval, Enabled: true, Interval: 6 * time.Hour}

	// No run yet: it is already due.
	got, err := s.Next(from, nil)
	if err != nil || !got.Equal(from) {
		t.Fatalf("first run: got %v (%v), want %v", got, err, from)
	}

	// A run three hours ago: the next one is three hours from now.
	last := from.Add(-3 * time.Hour)
	got, err = s.Next(from, &last)
	if err != nil || !got.Equal(from.Add(3*time.Hour)) {
		t.Fatalf("mid interval: got %v (%v), want %v", got, err, from.Add(3*time.Hour))
	}

	// A week of downtime must not queue a burst of catch-up backups.
	long := from.Add(-7 * 24 * time.Hour)
	got, err = s.Next(from, &long)
	if err != nil || !got.Equal(from) {
		t.Fatalf("after a long gap: got %v (%v), want one run now", got, err)
	}
}

func TestScheduleNextDailyRoundsToTheNextDay(t *testing.T) {
	loc := time.UTC
	s := Schedule{Mode: ModeDaily, Enabled: true, DailyTime: "03:00", Timezone: "UTC"}

	before := time.Date(2026, 3, 2, 1, 0, 0, 0, loc)
	got, err := s.Next(before, nil)
	if err != nil || !got.Equal(time.Date(2026, 3, 2, 3, 0, 0, 0, loc)) {
		t.Fatalf("before the time: got %v (%v)", got, err)
	}

	after := time.Date(2026, 3, 2, 4, 0, 0, 0, loc)
	got, err = s.Next(after, nil)
	if err != nil || !got.Equal(time.Date(2026, 3, 3, 3, 0, 0, 0, loc)) {
		t.Fatalf("after the time: got %v (%v)", got, err)
	}
}

func TestScheduleOffAndDisabledNeverRun(t *testing.T) {
	from := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	off := DefaultSchedule()
	disabled := Schedule{Mode: ModeInterval, Enabled: false, Interval: time.Hour}
	for name, s := range map[string]Schedule{"off": off, "disabled": disabled} {
		if _, err := s.Next(from, nil); !errors.Is(err, ErrNotScheduled) {
			t.Fatalf("%s: got %v, want ErrNotScheduled", name, err)
		}
	}
}

func TestScheduleValidateRefusesAPolicyThatCannotRun(t *testing.T) {
	base := func() Schedule {
		s := DefaultSchedule()
		s.Enabled = true
		return s
	}

	badMode := base()
	badMode.Mode = "hourly"
	if err := badMode.Validate(); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("bad mode: %v", err)
	}

	shortInterval := base()
	shortInterval.Mode = ModeInterval
	shortInterval.Interval = 30 * time.Second
	if err := shortInterval.Validate(); !errors.Is(err, ErrInterval) {
		t.Fatalf("short interval: %v", err)
	}

	badTime := base()
	badTime.Mode = ModeDaily
	badTime.DailyTime = "25:00"
	if err := badTime.Validate(); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("bad daily time: %v", err)
	}

	badCron := base()
	badCron.Mode = ModeCron
	badCron.CronExpr = "* * * *"
	if err := badCron.Validate(); !errors.Is(err, ErrCron) {
		t.Fatalf("bad cron: %v", err)
	}

	emptyCron := base()
	emptyCron.Mode = ModeCron
	if err := emptyCron.Validate(); !errors.Is(err, ErrCron) {
		t.Fatalf("empty cron: %v", err)
	}

	badZone := base()
	badZone.Mode = ModeDaily
	badZone.Timezone = "Mars/Olympus"
	if err := badZone.Validate(); !errors.Is(err, ErrTimezone) {
		t.Fatalf("bad timezone: %v", err)
	}

	noRetention := base()
	noRetention.KeepLast = 0
	if err := noRetention.Validate(); !errors.Is(err, ErrKeepLast) {
		t.Fatalf("keep last: %v", err)
	}
	noRetention = base()
	noRetention.MaxAgeDays = 0
	if err := noRetention.Validate(); !errors.Is(err, ErrMaxAge) {
		t.Fatalf("max age: %v", err)
	}

	// The default is valid as it stands, so an untouched instance still boots.
	if err := DefaultSchedule().Validate(); err != nil {
		t.Fatalf("default schedule: %v", err)
	}
}

func TestParseDailyTimeCoversTheWholeDay(t *testing.T) {
	for _, tc := range []struct {
		in    string
		h, m  int
		valid bool
	}{
		{"00:00", 0, 0, true},
		{"23:59", 23, 59, true},
		{"03:00", 3, 0, true},
		{"24:00", 0, 0, false},
		{"12:60", 0, 0, false},
		{"3", 0, 0, false},
		{"03-00", 0, 0, false},
	} {
		h, m, err := ParseDailyTime(tc.in)
		if tc.valid && err != nil {
			t.Fatalf("ParseDailyTime(%q): %v", tc.in, err)
		}
		if !tc.valid {
			if !errors.Is(err, ErrInvalidTime) {
				t.Fatalf("ParseDailyTime(%q) = %v, want ErrInvalidTime", tc.in, err)
			}
			continue
		}
		if h != tc.h || m != tc.m {
			t.Fatalf("ParseDailyTime(%q) = %d:%d, want %d:%d", tc.in, h, m, tc.h, tc.m)
		}
	}
}
