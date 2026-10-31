package model

import "time"

// Schedule is one goal the hub works on proactively, on a cron or external trigger.
type Schedule struct {
	ID         string `gorm:"primaryKey;size:64"`
	Goal       string `gorm:"type:text;not null;default:''"`
	Trigger    string `gorm:"size:32;not null;default:manual"`
	CronExpr   string `gorm:"size:128;not null;default:''"`
	Enabled    bool   `gorm:"not null;default:false"`
	NextRun    *time.Time
	OwnerID    string `gorm:"size:64;index;not null;default:''"`
	LastRunAt  *time.Time
	LastStatus string    `gorm:"size:32;not null;default:''"`
	CreatedAt  time.Time `gorm:"index"`
	UpdatedAt  time.Time
}

// TableName returns the schedules table name.
func (Schedule) TableName() string { return "schedules" }
