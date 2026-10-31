package model

import "time"

// Knowledge is one retrieved-or-stored chunk an agent may use as context (mock RAG).
type Knowledge struct {
	ID        string    `gorm:"primaryKey;size:64"`
	AgentID   string    `gorm:"size:64;index;not null;default:''"`
	ChunkText string    `gorm:"type:text;not null;default:''"`
	SourceRef string    `gorm:"size:256;not null;default:''"`
	CreatedAt time.Time `gorm:"index"`
}

// TableName returns the knowledge table name.
func (Knowledge) TableName() string { return "knowledge" }
