package model

import "time"

// AuditLog is one recorded API call.
//
// Column names follow the CloudTrail vocabulary the domain package documents;
// the hash columns make the table tamper evident and the trigger installed by
// migration 025 refuses UPDATE and DELETE outright.
type AuditLog struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	EventID      string    `gorm:"size:64;not null;default:'';uniqueIndex"`
	Ts           time.Time `gorm:"not null;default:now();index:idx_audit_log_ts,sort:desc"`
	RequestID    string    `gorm:"size:64;not null;default:''"`
	Source       string    `gorm:"size:16;not null;default:''"`
	ActorKind    string    `gorm:"size:16;not null;default:''"`
	ActorID      string    `gorm:"size:64;not null;default:'';index:idx_audit_log_actor"`
	ActorLabel   string    `gorm:"size:128;not null;default:''"`
	OwnerID      string    `gorm:"size:64;not null;default:''"`
	IP           string    `gorm:"size:64;not null;default:''"`
	Method       string    `gorm:"size:8;not null;default:''"`
	Path         string    `gorm:"size:512;not null;default:''"`
	Action       string    `gorm:"size:128;not null;default:'';index:idx_audit_log_action"`
	ResourceID   string    `gorm:"size:64;not null;default:'';index:idx_audit_log_resource"`
	ResourceKind string    `gorm:"size:32;not null;default:''"`
	Status       int       `gorm:"not null;default:0"`
	ErrorCode    string    `gorm:"size:64;not null;default:''"`
	ReadOnly     bool      `gorm:"not null;default:false"`
	LatencyMS    int       `gorm:"not null;default:0"`
	UserAgent    string    `gorm:"size:256;not null;default:''"`
	Metadata     JSONField `gorm:"type:jsonb"`
	PrevHash     string    `gorm:"size:64;not null;default:''"`
	Hash         string    `gorm:"size:64;not null;default:''"`
}

// TableName returns the audit_log table name.
func (AuditLog) TableName() string { return "audit_log" }

// AuditChainAnchor remembers the last row retention removed. The surviving
// head of the chain still points at it through prev_hash, so the verifier needs
// this hash as its seed. The table holds exactly one row (id = 1); see 033.
type AuditChainAnchor struct {
	ID       int16     `gorm:"primaryKey"`
	Hash     string    `gorm:"size:64;not null;default:''"`
	EventID  string    `gorm:"size:64;not null;default:''"`
	PrunedAt time.Time `gorm:"not null;default:now()"`
}

// TableName returns the audit_chain_anchor table name.
func (AuditChainAnchor) TableName() string { return "audit_chain_anchor" }
