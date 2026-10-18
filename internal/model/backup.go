package model

import "time"

// BackupCheckpoint is one row of the backup catalogue: the archive on disk plus
// everything the console needs to decide whether it can be restored.
//
// A row is created with status "running" before the dump starts, so an
// interrupted run is visible rather than missing. Only ok rows carry a path,
// a checksum and row counts.
type BackupCheckpoint struct {
	ID         string `gorm:"primaryKey;size:64"`
	Kind       string `gorm:"size:32;index;not null"`
	Engine     string `gorm:"size:32;not null"`
	Status     string `gorm:"size:32;index;not null"`
	Label      string `gorm:"type:text;not null"`
	Path       string `gorm:"type:text;not null"`
	SizeBytes  int64  `gorm:"not null"`
	Checksum   string `gorm:"size:128;not null"`
	RowCount   int64  `gorm:"not null"`
	TableCount int    `gorm:"not null"`
	// Schema is the comma separated list of migration files the archive was
	// taken at. The column is schema_version: "schema" is a reserved-ish word
	// in enough SQL dialects that spelling the mapping out is worth it.
	Schema     string    `gorm:"column:schema_version;type:text;not null"`
	DurationMS int64     `gorm:"not null"`
	ErrorCode  string    `gorm:"size:64;not null"`
	CreatedAt  time.Time `gorm:"index"`
	FinishedAt *time.Time
	RestoredAt *time.Time
	RestoredBy string `gorm:"size:64;not null"`
}

// TableName returns the backup_checkpoints table name.
func (BackupCheckpoint) TableName() string { return "backup_checkpoints" }

// BackupSchedule is the single settings row the console edits. It survives a
// restart so a schedule set from the UI is not lost with the process.
type BackupSchedule struct {
	ID          string `gorm:"primaryKey;size:32"`
	Mode        string `gorm:"size:16;not null"`
	Enabled     bool   `gorm:"not null;default:false"`
	IntervalSec int64  `gorm:"not null"`
	DailyTime   string `gorm:"size:8;not null"`
	Timezone    string `gorm:"size:64;not null"`
	CronExpr    string `gorm:"type:text;not null"`
	KeepLast    int    `gorm:"not null"`
	MaxAgeDays  int    `gorm:"not null"`
	UpdatedAt   time.Time
	UpdatedBy   string `gorm:"size:64;not null"`
}

// TableName returns the backup_schedules table name.
func (BackupSchedule) TableName() string { return "backup_schedules" }
