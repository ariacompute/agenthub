package model

import "time"

// Computer is one agent sandbox (browser / shell / screen) the console may attach to.
type Computer struct {
	ID         string     `gorm:"primaryKey;size:64"`
	OwnerID    string     `gorm:"size:64;index;not null;default:''"`
	AgentID    string     `gorm:"size:64;index;not null;default:''"`
	Kind       string     `gorm:"size:32;not null"`
	SessionRef string     `gorm:"size:256;not null;default:''"`
	Provider   string     `gorm:"size:64;not null;default:''"`
	LeaseID    string     `gorm:"size:64;index;not null;default:''"`
	NodeID     string     `gorm:"size:64;not null;default:''"`
	ExpiresAt  *time.Time `gorm:"index"`
	Status     string     `gorm:"size:32;not null;default:idle"`
	LastAction string     `gorm:"type:text;not null;default:''"`
	CreatedAt  time.Time  `gorm:"index"`
}

// TableName returns the computers table name.
func (Computer) TableName() string { return "computers" }
