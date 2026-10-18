package store

import (
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"gorm.io/gorm"

	"agenthub/internal/model"
	"agenthub/migrations"
)

// Migrate applies every embedded migration that has not run yet.
//
// Each file executes inside one transaction and is recorded in
// schema_migrations on success, so a restart skips what is already applied. The
// SQL itself is idempotent (CREATE TABLE / INDEX IF NOT EXISTS), which keeps a
// manual rollback-and-replay safe.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("store: migrate: db is nil")
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		filename  TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`).Error; err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("store: read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("store: no embedded migrations found")
	}

	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}
	for _, name := range names {
		if applied[name] {
			slog.Debug("store: migration already applied", "file", name)
			continue
		}
		if err := applyMigration(db, name); err != nil {
			return err
		}
	}
	return nil
}

// Applied reports the migration filenames this database has run, oldest first.
//
// The backup layer records the same list inside every archive, which is how a
// restore can tell an archive taken at a newer schema from one that is merely
// older than this build.
func Applied(db *gorm.DB) ([]string, error) {
	applied, err := appliedMigrations(db)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(applied))
	for name := range applied {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func appliedMigrations(db *gorm.DB) (map[string]bool, error) {
	var rows []model.SchemaMigration
	if err := db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Filename] = true
	}
	return out, nil
}

func applyMigration(db *gorm.DB, name string) error {
	data, err := migrations.FS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("store: read migration %s: %w", name, err)
	}
	stmts := splitStatements(string(data))
	if len(stmts) == 0 {
		return fmt.Errorf("store: migration %s has no statements", name)
	}
	tx := db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("store: begin migration %s: %w", name, tx.Error)
	}
	for _, stmt := range stmts {
		if err := tx.Exec(stmt).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s failed: %w", name, err)
		}
	}
	if err := tx.Create(&model.SchemaMigration{Filename: name}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("store: record migration %s: %w", name, err)
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("store: commit migration %s: %w", name, err)
	}
	slog.Info("store: migration applied", "file", name, "statements", len(stmts))
	return nil
}

// splitStatements splits SQL on line-ending semicolons, skipping comments and
// blank lines. The migrations are plain DDL, so a line based split is enough
// and keeps the files readable without a SQL parser dependency.
func splitStatements(content string) []string {
	var out []string
	var buf strings.Builder
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		buf.WriteString(line)
		buf.WriteString(" ")
		if strings.HasSuffix(line, ";") {
			if stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(buf.String()), ";")); stmt != "" {
				out = append(out, stmt)
			}
			buf.Reset()
		}
	}
	if tail := strings.TrimSpace(buf.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}
