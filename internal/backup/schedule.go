// Package backup is the database checkpoint layer of agenthub.
//
// A checkpoint is a complete dump of the agenthub schema into one tar.gz on
// disk, described by a manifest (tables, row counts, checksum, the migration
// set it was taken at). Checkpoints are taken manually, by the scheduler, or
// automatically before a restore — and any of them can be restored, which
// rewrites the database and reloads the in-memory state.
//
// The package declares the ports it needs (BackupStore, Engine) and stays free
// of GORM and of internal/config: cmd/agenthub-server wires the real pieces in.
package backup

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Schedule modes. "off" is what an instance runs with until someone turns it
// on: no backup directory means no backups, and no backups means no surprise
// files in a container nobody configured.
const (
	ModeOff      = "off"
	ModeInterval = "interval"
	ModeDaily    = "daily"
	ModeCron     = "cron"
)

// Checkpoint kinds, recorded so the console can tell an operator why a
// checkpoint exists.
const (
	KindManual     = "manual"
	KindSchedule   = "schedule"
	KindPreRestore = "pre-restore"
)

// Checkpoint statuses.
const (
	StatusRunning     = "running"
	StatusOK          = "ok"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// Errors reported by the scheduler. They become machine readable codes on the
// wire, so they carry no operator facing prose.
var (
	ErrNotScheduled = errors.New("backup: no run is scheduled")
	ErrNoOccurrence = errors.New("backup: the expression never matches")
	ErrInvalidMode  = errors.New("backup: mode must be off, interval, daily or cron")
	ErrInvalidTime  = errors.New("backup: time must be HH:MM")
	ErrInterval     = errors.New("backup: interval must be at least one minute")
	ErrKeepLast     = errors.New("backup: keep_last must be at least 1")
	ErrMaxAge       = errors.New("backup: max_age_days must be at least 1")
	ErrTimezone     = errors.New("backup: unknown timezone")
	ErrCron         = errors.New("backup: invalid cron expression")
)

// ValidMode reports whether mode is one of the schedule modes.
func ValidMode(mode string) bool {
	switch mode {
	case ModeOff, ModeInterval, ModeDaily, ModeCron:
		return true
	}
	return false
}

// ParseDailyTime parses the "HH:MM" daily schedule field. Anything else would
// silently shift a backup into the middle of the working day, so it is refused.
func ParseDailyTime(value string) (hour, minute int, err error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, ErrInvalidTime
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, ErrInvalidTime
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, ErrInvalidTime
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, ErrInvalidTime
	}
	return hour, minute, nil
}

// Schedule is the checkpoint policy: when to take one, and how many to keep. It
// is edited from the console and persisted, so a restart does not reset it.
//
// It carries no JSON tags on purpose: the API builds its own payload (the
// interval travels as whole seconds, not as a Go duration in nanoseconds), so a
// struct can never leak a unit the console does not expect.
type Schedule struct {
	Mode       string
	Enabled    bool
	Interval   time.Duration
	DailyTime  string
	Timezone   string
	CronExpr   string
	KeepLast   int
	MaxAgeDays int
}

// DefaultSchedule is what a fresh instance runs with: nothing scheduled. The
// retention defaults only matter once someone turns the schedule on.
func DefaultSchedule() Schedule {
	return Schedule{
		Mode:       ModeOff,
		Enabled:    false,
		Interval:   24 * time.Hour,
		DailyTime:  "03:00",
		Timezone:   "UTC",
		KeepLast:   7,
		MaxAgeDays: 30,
	}
}

// Validate refuses a schedule that could never run, so the console never
// accepts a policy that silently does nothing.
func (s Schedule) Validate() error {
	if !ValidMode(s.Mode) {
		return ErrInvalidMode
	}
	if s.KeepLast < 1 {
		return ErrKeepLast
	}
	if s.MaxAgeDays < 1 {
		return ErrMaxAge
	}
	if _, err := s.Location(); err != nil {
		return err
	}
	switch s.Mode {
	case ModeInterval:
		if s.Interval < time.Minute {
			return ErrInterval
		}
	case ModeDaily:
		if _, _, err := ParseDailyTime(s.DailyTime); err != nil {
			return err
		}
	case ModeCron:
		if strings.TrimSpace(s.CronExpr) == "" {
			return ErrCron
		}
		if _, err := ParseCron(s.CronExpr); err != nil {
			return err
		}
	}
	return nil
}

// Location is the timezone the daily and cron modes are interpreted in. An
// unknown name is an error rather than a silent fallback to UTC: "every day at
// 03:00" means a different instant in a different zone.
func (s Schedule) Location() (*time.Location, error) {
	name := strings.TrimSpace(s.Timezone)
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrTimezone
	}
	return loc, nil
}

// Next reports when the next checkpoint is due.
//
// lastRun is the moment of the last successful checkpoint; it anchors the
// interval mode so a restart does not shorten the period, while the daily and
// cron modes are absolute and therefore self anchoring. ErrNotScheduled means
// the scheduler should simply wait.
func (s Schedule) Next(from time.Time, lastRun *time.Time) (time.Time, error) {
	if !s.Enabled || s.Mode == ModeOff {
		return time.Time{}, ErrNotScheduled
	}
	loc, err := s.Location()
	if err != nil {
		return time.Time{}, err
	}
	switch s.Mode {
	case ModeInterval:
		if s.Interval < time.Minute {
			return time.Time{}, ErrInterval
		}
		if lastRun == nil {
			return from, nil
		}
		// A long downtime must not fire a burst of catch-up backups: one is
		// enough, and it runs now.
		if next := lastRun.Add(s.Interval); next.After(from) {
			return next, nil
		}
		return from, nil
	case ModeDaily:
		hour, minute, err := ParseDailyTime(s.DailyTime)
		if err != nil {
			return time.Time{}, err
		}
		base := from.In(loc)
		next := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, loc)
		if !next.After(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil
	case ModeCron:
		expr, err := ParseCron(s.CronExpr)
		if err != nil {
			return time.Time{}, err
		}
		return expr.Next(from.In(loc))
	}
	return time.Time{}, ErrInvalidMode
}

// Cron is a parsed five field expression: minute hour day-of-month month
// day-of-week, where day-of-week 0 and 7 are both Sunday. Hand rolled because
// agenthub keeps GORM as its only production dependency.
type Cron struct {
	Minute cronField
	Hour   cronField
	Dom    cronField
	Month  cronField
	Dow    cronField
}

// ParseCron parses "m h dom mon dow". Every field accepts `*`, `*/n`, `a`,
// `a-b`, `a-b/n` and comma separated lists of those.
func ParseCron(expr string) (Cron, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("%w: want 5 fields, got %d", ErrCron, len(fields))
	}
	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return Cron{}, fmt.Errorf("%w: minute: %v", ErrCron, err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return Cron{}, fmt.Errorf("%w: hour: %v", ErrCron, err)
	}
	dom, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return Cron{}, fmt.Errorf("%w: day of month: %v", ErrCron, err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return Cron{}, fmt.Errorf("%w: month: %v", ErrCron, err)
	}
	dow, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return Cron{}, fmt.Errorf("%w: day of week: %v", ErrCron, err)
	}
	return Cron{Minute: minute, Hour: hour, Dom: dom, Month: month, Dow: dow}, nil
}

// Next returns the first matching instant strictly after from, in from's own
// location. Days are filtered before hours and minutes are enumerated, so even
// a rare expression costs one pass over the calendar.
func (c Cron) Next(from time.Time) (time.Time, error) {
	loc := from.Location()
	start := from.Truncate(time.Minute).Add(time.Minute)
	y, m, d := start.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, loc)
	limit := start.AddDate(5, 0, 0)
	for ; day.Before(limit); day = day.AddDate(0, 0, 1) {
		if !c.matchDay(day) {
			continue
		}
		for _, h := range c.Hour.values(0, 23) {
			for _, mi := range c.Minute.values(0, 59) {
				cand := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, loc)
				if !cand.Before(start) {
					return cand, nil
				}
			}
		}
	}
	return time.Time{}, ErrNoOccurrence
}

// matchDay applies the standard rule: when both day-of-month and day-of-week
// are restricted, a day matches if either of them does; a `*` in one of them
// hands the decision to the other.
func (c Cron) matchDay(day time.Time) bool {
	if !c.Month.has(int(day.Month())) {
		return false
	}
	domRestricted := !c.Dom.any
	dowRestricted := !c.Dow.any
	switch {
	case domRestricted && dowRestricted:
		return c.Dom.has(day.Day()) || c.Dow.has(int(day.Weekday()))
	case domRestricted:
		return c.Dom.has(day.Day())
	case dowRestricted:
		return c.Dow.has(int(day.Weekday()))
	}
	return true
}

// cronField is a set of allowed values stored as a bitmask, with `any` marking
// an unrestricted field (a `*`), which the day-of-month / day-of-week rule
// needs to distinguish from "every value happens to be listed".
type cronField struct {
	bits uint64
	any  bool
}

func (f cronField) has(value int) bool {
	if value < 0 || value > 63 {
		return false
	}
	return f.bits&(1<<uint(value)) != 0
}

// values enumerates the allowed values in [min, max]; an unrestricted field
// yields every one of them, which keeps the enumeration bounded at 60 entries.
func (f cronField) values(min, max int) []int {
	out := make([]int, 0, max-min+1)
	for v := min; v <= max; v++ {
		if f.any || f.has(v) {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func parseCronField(expr string, min, max int) (cronField, error) {
	var f cronField
	for _, part := range strings.Split(expr, ",") {
		if strings.TrimSpace(part) == "" {
			return cronField{}, fmt.Errorf("empty field")
		}
		step := 1
		rangePart := part
		if i := strings.Index(part, "/"); i >= 0 {
			rangePart = part[:i]
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 {
				return cronField{}, fmt.Errorf("bad step %q", part[i+1:])
			}
			step = n
		}
		lo, hi, err := bounds(rangePart, min, max)
		if err != nil {
			return cronField{}, err
		}
		if lo > hi {
			return cronField{}, fmt.Errorf("descending range %q", rangePart)
		}
		for v := lo; v <= hi; v += step {
			f.bits |= 1 << uint(v)
		}
		// Only a bare "*" is unrestricted: "*/15" names four minutes, and
		// marking it `any` would make it fire every minute.
		if part == "*" {
			f.any = true
		}
	}
	// Sunday is both 0 and 7.
	if max == 7 && f.has(7) {
		f.bits |= 1 << 0
	}
	return f, nil
}

func bounds(part string, min, max int) (int, int, error) {
	if part == "*" {
		return min, max, nil
	}
	if i := strings.Index(part, "-"); i >= 0 {
		lo, err := strconv.Atoi(part[:i])
		if err != nil {
			return 0, 0, fmt.Errorf("bad range %q", part)
		}
		hi, err := strconv.Atoi(part[i+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("bad range %q", part)
		}
		if lo < min || hi > max {
			return 0, 0, fmt.Errorf("range %q outside %d..%d", part, min, max)
		}
		return lo, hi, nil
	}
	v, err := strconv.Atoi(part)
	if err != nil {
		return 0, 0, fmt.Errorf("bad value %q", part)
	}
	if v < min || v > max {
		return 0, 0, fmt.Errorf("value %q outside %d..%d", part, min, max)
	}
	return v, v, nil
}
