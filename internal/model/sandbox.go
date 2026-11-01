package model

import "time"

// The sandbox tables. A lease is one execution environment held for a scope, an
// exec is one receipt from inside it, a node is a machine that can run work, and
// an event is one frame of the console's live stream.
//
// Two deliberate omissions: no stdout and no stderr. The audit trail keeps
// digests, because the alternative is a database that quietly accumulates the
// output of every command anybody ever ran — including the ones that printed a
// token.

// SandboxLease is one leased execution environment.
type SandboxLease struct {
	ID          string    `gorm:"primaryKey;size:64"`
	OwnerID     string    `gorm:"size:64;index;not null;default:''"`
	ResourceID  string    `gorm:"size:64;index;not null;default:''"`
	Scope       string    `gorm:"size:128;index;not null;default:''"`
	Provider    string    `gorm:"size:64;index;not null;default:''"`
	Runtime     string    `gorm:"size:64;not null;default:''"`
	NodeID      string    `gorm:"size:64;not null;default:''"`
	State       string    `gorm:"size:32;index;not null;default:starting"`
	Ref         string    `gorm:"size:256;not null;default:''"`
	Profile     JSONField `gorm:"type:jsonb"`
	Execs       int       `gorm:"not null;default:0"`
	LastExit    int       `gorm:"not null;default:0"`
	LastCommand string    `gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time `gorm:"index;not null"`
	RenewedAt   time.Time `gorm:"not null"`
	ExpiresAt   time.Time `gorm:"index;not null"`
}

// TableName returns the sandbox_leases table name.
func (SandboxLease) TableName() string { return "sandbox_leases" }

// SandboxExec is one execution receipt.
type SandboxExec struct {
	ID           string    `gorm:"primaryKey;size:64"`
	LeaseID      string    `gorm:"size:64;index;not null"`
	OwnerID      string    `gorm:"size:64;index;not null;default:''"`
	Provider     string    `gorm:"size:64;not null;default:''"`
	Command      string    `gorm:"type:text;not null;default:''"`
	ExitCode     int       `gorm:"not null;default:0"`
	DurationMS   int64     `gorm:"not null;default:0"`
	Truncated    bool      `gorm:"not null;default:false"`
	StdoutSHA256 string    `gorm:"size:64;not null;default:''"`
	StderrSHA256 string    `gorm:"size:64;not null;default:''"`
	DryRun       bool      `gorm:"not null;default:false"`
	At           time.Time `gorm:"index;not null"`
}

// TableName returns the sandbox_execs table name.
func (SandboxExec) TableName() string { return "sandbox_execs" }

// SandboxNode is one registered execution node.
type SandboxNode struct {
	ID           string    `gorm:"primaryKey;size:64"`
	OwnerID      string    `gorm:"size:64;index;not null;default:''"`
	Name         string    `gorm:"size:256;not null;default:''"`
	APIKeyID     string    `gorm:"size:64;not null;default:''"`
	Capabilities JSONField `gorm:"type:jsonb"`
	Status       string    `gorm:"size:32;not null;default:offline"`
	LastSeenAt   time.Time `gorm:"index;not null"`
	CreatedAt    time.Time `gorm:"not null"`
}

// TableName returns the sandbox_nodes table name.
func (SandboxNode) TableName() string { return "sandbox_nodes" }

// SandboxJob is one finished job handed to a node. Queued work lives in memory:
// a node's poll is the queue, and a persisted pending row would only become a
// task nobody is polling for.
type SandboxJob struct {
	ID         string     `gorm:"primaryKey;size:64"`
	NodeID     string     `gorm:"size:64;index;not null;default:''"`
	LeaseID    string     `gorm:"size:64;index;not null;default:''"`
	Provider   string     `gorm:"size:64;not null;default:''"`
	Op         string     `gorm:"size:32;not null;default:''"`
	State      string     `gorm:"size:32;not null;default:''"`
	Command    string     `gorm:"type:text;not null;default:''"`
	ExitCode   int        `gorm:"not null;default:0"`
	Error      string     `gorm:"type:text;not null;default:''"`
	CreatedAt  time.Time  `gorm:"index;not null"`
	FinishedAt *time.Time `gorm:"index"`
}

// TableName returns the sandbox_jobs table name.
func (SandboxJob) TableName() string { return "sandbox_jobs" }

// SandboxEvent is one frame of the live stream, numbered so a reconnecting
// console can replay from `since`.
type SandboxEvent struct {
	Seq     uint64    `gorm:"primaryKey;autoIncrement"`
	Subject string    `gorm:"size:128;index;not null"`
	Kind    string    `gorm:"size:64;index;not null"`
	LeaseID string    `gorm:"size:64;not null;default:''"`
	Payload JSONField `gorm:"type:jsonb"`
	At      time.Time `gorm:"index;not null"`
}

// TableName returns the sandbox_events table name.
func (SandboxEvent) TableName() string { return "sandbox_events" }
