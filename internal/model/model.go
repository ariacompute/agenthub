// Package model defines the GORM persistence models of agenthub.
//
// The schema itself is owned by the idempotent SQL under migrations/: this
// package only describes the shape GORM reads and writes. Structured payloads
// (A2A parts, artifacts, span timelines, metadata) are stored as jsonb through
// JSONField so the relational schema stays flat instead of growing one child
// table per nested type.
package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// JSONField is a raw JSON document persisted in a jsonb column.
type JSONField []byte

// NewJSONField marshals v. Nil and JSON null both map to a SQL NULL column so
// "absent" round trips as absent instead of as the literal text "null".
func NewJSONField(v any) (JSONField, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("model: encode json field: %w", err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return JSONField(raw), nil
}

// Decode unmarshals the document into dst; an empty column is a no-op.
func (j JSONField) Decode(dst any) error {
	if len(j) == 0 || string(j) == "null" {
		return nil
	}
	if err := json.Unmarshal(j, dst); err != nil {
		return fmt.Errorf("model: decode json field: %w", err)
	}
	return nil
}

// Value implements driver.Valuer.
func (j JSONField) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// Scan implements sql.Scanner.
func (j *JSONField) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j = nil
	case []byte:
		cp := make(JSONField, len(v))
		copy(cp, v)
		*j = cp
	case string:
		*j = JSONField(v)
	default:
		return fmt.Errorf("model: unsupported jsonb source %T", src)
	}
	return nil
}

// GormDBDataType pins the column type to jsonb.
func (JSONField) GormDBDataType(*gorm.DB, *schema.Field) string { return "jsonb" }

// Resource is one registered interconnectable asset (model / agent / mcp /
// skill / cli). The entry model follows the MCP Registry convention.
type Resource struct {
	ID           string    `gorm:"primaryKey;size:64"`
	Kind         string    `gorm:"size:32;index;not null"`
	Name         string    `gorm:"size:256;not null"`
	Description  string    `gorm:"type:text"`
	Endpoint     string    `gorm:"size:512"`
	Version      string    `gorm:"size:64"`
	Repository   string    `gorm:"size:512"`
	Tags         JSONField `gorm:"type:jsonb"`
	Capabilities JSONField `gorm:"type:jsonb"`
	AuthSchemes  JSONField `gorm:"type:jsonb"`
	Env          JSONField `gorm:"type:jsonb"`
	Metadata     JSONField `gorm:"type:jsonb"`
	Transport    string    `gorm:"size:32"`
	Health       string    `gorm:"size:32;index;not null;default:unknown"`
	CreatedAt    time.Time `gorm:"index"`
	UpdatedAt    time.Time
}

// TableName returns the resources table name.
func (Resource) TableName() string { return "resources" }

// Task is the A2A unit of work. Artifacts, spans, the parked question and the
// push configuration live in their own tables keyed by task id.
type Task struct {
	ID              string    `gorm:"primaryKey;size:64"`
	Kind            string    `gorm:"size:32"`
	ContextID       string    `gorm:"size:64;index"`
	SessionID       string    `gorm:"size:64"`
	ResourceID      string    `gorm:"size:64;index"`
	State           string    `gorm:"size:32;index;not null"`
	StatusMessage   JSONField `gorm:"type:jsonb"`
	StatusTimestamp string    `gorm:"size:40"`
	History         JSONField `gorm:"type:jsonb"`
	Metadata        JSONField `gorm:"type:jsonb"`
	CreatedAt       time.Time `gorm:"index"`
	UpdatedAt       time.Time
}

// TableName returns the tasks table name.
func (Task) TableName() string { return "tasks" }

// TaskArtifact is one named output produced while running a task. Insertion
// order is preserved by the autoincrement id.
type TaskArtifact struct {
	ID          uint      `gorm:"primaryKey;autoIncrement"`
	TaskID      string    `gorm:"size:64;index;not null"`
	ArtifactID  string    `gorm:"size:64"`
	Name        string    `gorm:"size:256"`
	Description string    `gorm:"type:text"`
	Parts       JSONField `gorm:"type:jsonb"`
	Idx         int
	Append      *bool
	LastChunk   *bool
	Metadata    JSONField `gorm:"type:jsonb"`
	CreatedAt   time.Time
}

// TableName returns the task_artifacts table name.
func (TaskArtifact) TableName() string { return "task_artifacts" }

// TaskSpan is one step of a run: the console replays these as the timeline.
type TaskSpan struct {
	ID         string `gorm:"primaryKey;size:64"`
	TaskID     string `gorm:"size:64;index;not null"`
	ResourceID string `gorm:"size:64"`
	Kind       string `gorm:"size:64"`
	Label      string `gorm:"size:256"`
	StartAt    string `gorm:"size:40"`
	EndAt      string `gorm:"size:40"`
	DurationMS int64
	Status     string    `gorm:"size:32;index"`
	Code       string    `gorm:"size:128"`
	CreatedAt  time.Time `gorm:"index"`
}

// TableName returns the task_spans table name.
func (TaskSpan) TableName() string { return "task_spans" }

// TaskPending is the question a task parked in input-required is waiting for.
type TaskPending struct {
	TaskID    string `gorm:"primaryKey;size:64"`
	Prompt    string `gorm:"type:text"`
	AskedAt   string `gorm:"size:40"`
	UpdatedAt time.Time
}

// TableName returns the task_pending table name.
func (TaskPending) TableName() string { return "task_pending" }

// TaskPush is the push notification callback registered for a task.
type TaskPush struct {
	TaskID    string    `gorm:"primaryKey;size:64"`
	Config    JSONField `gorm:"type:jsonb"`
	UpdatedAt time.Time
}

// TableName returns the task_push table name.
func (TaskPush) TableName() string { return "task_push" }

// PushRecord is one callback captured by the mock notifier: the notification is
// recorded so a restart keeps the full callback history of a task.
type PushRecord struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	TaskID    string    `gorm:"size:64;index;not null"`
	URL       string    `gorm:"type:text;not null"`
	State     string    `gorm:"size:32;index;not null"`
	Config    JSONField `gorm:"type:jsonb"`
	CreatedAt time.Time `gorm:"index"`
}

// TableName returns the push_records table name.
func (PushRecord) TableName() string { return "push_records" }

// Event is one frame broadcast for a task, persisted so a client that
// reconnects with since=<seq> can replay what it missed.
type Event struct {
	Seq       uint64    `gorm:"primaryKey;autoIncrement:false"`
	TaskID    string    `gorm:"primaryKey;size:64"`
	Kind      string    `gorm:"size:32;index"`
	At        string    `gorm:"size:40"`
	Payload   JSONField `gorm:"type:jsonb"`
	CreatedAt time.Time `gorm:"index"`
}

// TableName returns the events table name.
func (Event) TableName() string { return "events" }

// SchemaMigration records which embedded migration files already ran.
type SchemaMigration struct {
	Filename  string    `gorm:"primaryKey;size:255"`
	AppliedAt time.Time `gorm:"not null;default:now()"`
}

// TableName returns the schema_migrations table name.
func (SchemaMigration) TableName() string { return "schema_migrations" }
