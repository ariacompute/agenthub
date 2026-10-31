// Package schedules defines the domain types and persistence port for the proactive
// goals the hub works on, on a cron or an external trigger.
//
// The port (Store) is defined here, in the domain package, and implemented in
// internal/store.
package schedules

import "context"

// Trigger enumerates how a schedule is started.
type Trigger string

const (
	TriggerManual Trigger = "manual"
	TriggerCron   Trigger = "cron"
	TriggerEvent  Trigger = "event"
)

// Triggers lists every supported trigger.
var Triggers = []Trigger{TriggerManual, TriggerCron, TriggerEvent}

// NormalizeTrigger returns a valid trigger, falling back to the empty string for
// anything unrecognised.
func NormalizeTrigger(s string) Trigger {
	for _, t := range Triggers {
		if string(t) == s {
			return t
		}
	}
	return Trigger("")
}

// Schedule is one goal the hub works on proactively.
type Schedule struct {
	ID         string `json:"id"`
	Goal       string `json:"goal"`
	Trigger    string `json:"trigger"`
	CronExpr   string `json:"cronExpr"`
	Enabled    bool   `json:"enabled"`
	NextRun    string `json:"nextRun"`
	OwnerID    string `json:"ownerId"`
	LastRunAt  string `json:"lastRunAt"`
	LastStatus string `json:"lastStatus"`
	CreatedAt  string `json:"createdAt"`
}

// Store persists schedules. Implemented in internal/store.
type Store interface {
	List(ctx context.Context, ownerID string) ([]*Schedule, error)
	Get(ctx context.Context, id string) (*Schedule, error)
	Create(ctx context.Context, s *Schedule) (*Schedule, error)
	Update(ctx context.Context, s *Schedule) (*Schedule, error)
	Delete(ctx context.Context, id string) error
}
