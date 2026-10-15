// Package store wraps PostgreSQL connectivity, the embedded migrations, the
// demo seed and the GORM repository implementations the registry and the hub
// delegate to.
//
// Persistence is mandatory for a real deployment: Open refuses an empty DSN and
// config.Validate refuses to start without DATABASE_URL. Unit tests never touch
// this package — the registry and the hub keep their in-memory behaviour unless
// a repository is injected.
package store

import (
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrNoDatabase indicates DATABASE_URL is unset: callers must fail fast rather
// than degrade silently to a state that looks healthy.
var ErrNoDatabase = errors.New("DATABASE_URL is not configured")

// Redacted is the placeholder written over every secret in a logged DSN.
const Redacted = "REDACTED"

// Open establishes a PostgreSQL connection and verifies it with a ping.
func Open(dsn string, debug bool) (*gorm.DB, error) {
	if dsn == "" {
		return nil, ErrNoDatabase
	}
	level := gormlogger.Warn
	if debug {
		level = gormlogger.Info
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		// Record-not-found is an expected outcome here (it maps onto
		// ErrNotFound), so it must not be logged as a failure.
		Logger: gormlogger.New(log.New(os.Stdout, "", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  level,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
		PrepareStmt: true,
	})
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("store: sql handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: ping postgres: %w", err)
	}
	slog.Info("store: connected", "dsn", RedactDSN(dsn))
	return db, nil
}

// Ping verifies the connection is still usable.
func Ping(db *gorm.DB) error {
	if db == nil {
		return ErrNoDatabase
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

// Close releases the connection pool.
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	slog.Info("store: disconnected")
	return nil
}

// RedactDSN scrubs the password from a connection string so a DSN can be
// logged. Secrets never reach the log verbatim (see AGENTS.md).
func RedactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		// A DSN url.Parse cannot split must degrade to a placeholder instead of
		// being echoed back with its secrets intact.
		return "<dsn " + Redacted + ">"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), Redacted)
		}
	}
	q := u.Query()
	for _, key := range []string{"password", "sslpassword", "passfile"} {
		if _, ok := q[key]; ok {
			q.Set(key, Redacted)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
