package model

import "time"

// The router tables. A trigger is one inbound fact, a ticket one unit of work, an
// assignment one run of that work, and a decision the verdict that connects them.
// Keeping them apart is what makes "why did this happen" answerable months later:
// every hop is a row, not a log line.

// RouterTrigger is one inbound event, stored once. DedupKey is unique among
// non-empty values, so a redelivered webhook or a replayed cron tick appends
// nothing.
type RouterTrigger struct {
	ID          string    `gorm:"primaryKey;size:64"`
	Source      string    `gorm:"size:32;index;not null"`
	SourceID    string    `gorm:"size:64;index;not null;default:''"`
	ThreadID    string    `gorm:"size:64;index;not null;default:''"`
	ActorID     string    `gorm:"size:64;not null;default:''"`
	ActorName   string    `gorm:"size:256;not null;default:''"`
	ActorKind   string    `gorm:"size:16;not null;default:system"`
	Kind        string    `gorm:"size:64;not null;default:''"`
	Body        string    `gorm:"type:text;not null;default:''"`
	Explicit    bool      `gorm:"not null;default:false"`
	Meta        JSONField `gorm:"type:jsonb"`
	DedupKey    string    `gorm:"size:256;not null;default:''"`
	Disposition string    `gorm:"size:32;index;not null;default:pending"`
	DecisionID  string    `gorm:"size:64;not null;default:''"`
	TicketID    string    `gorm:"size:64;index;not null;default:''"`
	Handbacks   int       `gorm:"not null;default:0"`
	DueAt       time.Time `gorm:"index;not null"`
	At          time.Time `gorm:"index;not null"`
	UpdatedAt   time.Time
}

// TableName returns the router_triggers table name.
func (RouterTrigger) TableName() string { return "router_triggers" }

// RouterThread is the routing state of one conversation. The conversation itself
// lives in threads / thread_messages.
type RouterThread struct {
	ID           string `gorm:"primaryKey;size:64"`
	Source       string `gorm:"size:32;not null;default:''"`
	Title        string `gorm:"size:512;not null;default:''"`
	OpenedBy     string `gorm:"size:64;index;not null;default:''"`
	Closed       bool   `gorm:"index;not null;default:false"`
	CloseOutcome string `gorm:"size:64;not null;default:''"`
	UpdatedAt    time.Time
}

// TableName returns the router_threads table name.
func (RouterThread) TableName() string { return "router_threads" }

// RouterSubscription binds a ticket to a thread. Own marks the thread the ticket
// opened; Context marks an observation-only binding.
type RouterSubscription struct {
	ThreadID string    `gorm:"primaryKey;size:64"`
	TicketID string    `gorm:"primaryKey;size:64;index"`
	Own      bool      `gorm:"not null;default:false"`
	Context  bool      `gorm:"not null;default:false"`
	Closed   bool      `gorm:"not null;default:false"`
	At       time.Time `gorm:"not null"`
}

// TableName returns the router_subscriptions table name.
func (RouterSubscription) TableName() string { return "router_subscriptions" }

// RouterTicket is one unit of work the router decided on.
type RouterTicket struct {
	ID              string `gorm:"primaryKey;size:64"`
	Title           string `gorm:"size:512;not null;default:''"`
	Goal            string `gorm:"type:text;not null;default:''"`
	OwnerID         string `gorm:"size:64;index;not null;default:''"`
	State           string `gorm:"size:32;index;not null"`
	Resource        string `gorm:"size:64;not null;default:''"`
	TaskID          string `gorm:"size:64;not null;default:''"`
	OriginTriggerID string `gorm:"size:64;index;not null;default:''"`
	BatchID         string `gorm:"size:64;not null;default:''"`
	Focus           string `gorm:"size:64;not null;default:''"`
	Attempts        int    `gorm:"not null;default:0"`
	NextTryAt       time.Time
	LastError       string    `gorm:"type:text;not null;default:''"`
	WaitsForPerson  bool      `gorm:"not null;default:false"`
	CreatedAt       time.Time `gorm:"index"`
	UpdatedAt       time.Time
	EndedAt         *time.Time
}

// TableName returns the router_tickets table name.
func (RouterTicket) TableName() string { return "router_tickets" }

// RouterAssignment is one run of a ticket.
type RouterAssignment struct {
	ID        string `gorm:"primaryKey;size:64"`
	TicketID  string `gorm:"size:64;index;not null"`
	State     string `gorm:"size:32;not null;default:started"`
	TaskID    string `gorm:"size:64;not null;default:''"`
	Note      string `gorm:"type:text;not null;default:''"`
	Error     string `gorm:"type:text;not null;default:''"`
	TriggerID string `gorm:"size:64;not null;default:''"`
	StartedAt time.Time
	EndedAt   *time.Time
}

// TableName returns the router_assignments table name.
func (RouterAssignment) TableName() string { return "router_assignments" }

// RouterDecision is the verdict recorded for one trigger.
type RouterDecision struct {
	ID        string    `gorm:"primaryKey;size:64"`
	TriggerID string    `gorm:"size:64;index;not null"`
	BatchID   string    `gorm:"size:64;index;not null;default:''"`
	Kind      string    `gorm:"size:32;not null"`
	TicketID  string    `gorm:"size:64;index;not null;default:''"`
	Resource  string    `gorm:"size:64;not null;default:''"`
	By        string    `gorm:"size:16;not null;default:rule"`
	Reason    string    `gorm:"size:256;not null;default:''"`
	At        time.Time `gorm:"index"`
}

// TableName returns the router_decisions table name.
func (RouterDecision) TableName() string { return "router_decisions" }

// RouterPolicy is the program layer configuration for one source.
type RouterPolicy struct {
	Source     string    `gorm:"primaryKey;size:32"`
	Mode       string    `gorm:"size:32;not null;default:candidate"`
	Allow      JSONField `gorm:"type:jsonb"`
	Keywords   JSONField `gorm:"type:jsonb"`
	QuietStart int       `gorm:"not null;default:-1"`
	QuietEnd   int       `gorm:"not null;default:-1"`
	Resource   string    `gorm:"size:64;not null;default:''"`
	UpdatedAt  time.Time
}

// TableName returns the router_policies table name.
func (RouterPolicy) TableName() string { return "router_policies" }

// RouterEvent is one domain event, persisted so a console that reconnects with
// since=<seq> replays exactly what it missed.
type RouterEvent struct {
	Seq     uint64    `gorm:"primaryKey;autoIncrement"`
	Kind    string    `gorm:"size:64;index;not null"`
	Subject string    `gorm:"size:64;index;not null;default:''"`
	At      time.Time `gorm:"index"`
	Payload JSONField `gorm:"type:jsonb"`
}

// TableName returns the router_events table name.
func (RouterEvent) TableName() string { return "router_events" }
