package schedules

import (
	"time"

	"agenthub/internal/backup"
)

// The five field cron parser already exists for database checkpoints. Rather than
// a second implementation — or a new dependency, which agenthub does not take —
// the schedule package reuses it. The two features mean the same thing by an
// expression, so one parser is the honest answer.

// NextRun reports the first instant after from that matches expr.
//
// A caller that is computing "when should this fire next?" wants exactly this.
// A caller asking "is it due?" compares the stored NextRun with now instead, so a
// restart never re-fires a slot that already ran.
func NextRun(expr string, from time.Time) (time.Time, error) {
	parsed, err := backup.ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.Next(from)
}

// ValidateCron reports whether expr is a parsable five field expression. The
// empty string is valid: it means "no cron", which a manual schedule uses.
func ValidateCron(expr string) error {
	if expr == "" {
		return nil
	}
	_, err := backup.ParseCron(expr)
	return err
}

// Advance returns the next run for a schedule saved now, or the empty string when
// the schedule is not a cron one. Storing the answer at save time is what makes
// next_run a real field instead of a decorative one.
func Advance(s *Schedule, from time.Time) string {
	if s == nil || !s.Enabled || s.Trigger != string(TriggerCron) || s.CronExpr == "" {
		return ""
	}
	next, err := NextRun(s.CronExpr, from)
	if err != nil {
		return ""
	}
	return next.UTC().Format(time.RFC3339)
}
