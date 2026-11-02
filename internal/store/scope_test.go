package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"agenthub/internal/identity"
	"agenthub/internal/registry"
	"agenthub/internal/threads"
)

// The scope layer is where row level security either holds or leaks. It is the
// only place a call's identity reaches the database session, and `ownedWhere` is
// the only thing between a by-id lookup and somebody else's row. Both are
// exercised against a real PostgreSQL: a fake would only prove the fake, and the
// property that matters — "the setting is gone when the transaction ends" — is
// the database's behaviour, not this package's.

// The policies compare these exact strings, so "true" or "1" would read as off
// and silently widen access.
func TestFlagSpeaksThePolicysVocabulary(t *testing.T) {
	if got := flag(true); got != "on" {
		t.Fatalf("flag(true) = %q, want %q", got, "on")
	}
	if got := flag(false); got != "off" {
		t.Fatalf("flag(false) = %q, want %q", got, "off")
	}
}

func TestScopedWithoutAHandleRefuses(t *testing.T) {
	called := false
	_, err := scoped(context.Background(), nil, func(*gorm.DB) (int, error) {
		called = true
		return 1, nil
	})
	if !errors.Is(err, gorm.ErrInvalidDB) {
		t.Fatalf("scoped on a nil handle = %v, want gorm.ErrInvalidDB", err)
	}
	if called {
		t.Fatal("the work must not run without a handle")
	}
}

// A context with no identity is not an error: the migration path and the unit
// tests genuinely have no scope, and giving them one would be worse than the
// alternative. What must hold is that no identity is published either.
func TestScopedWithoutAnIdentityPublishesNothing(t *testing.T) {
	db := openGovernanceDB(t)

	got, err := scoped(context.Background(), db, func(tx *gorm.DB) (string, error) {
		var owner string
		err := tx.Raw(`SELECT coalesce(current_setting('app.owner_id', true), '')`).Scan(&owner).Error
		return owner, err
	})
	if err != nil {
		t.Fatalf("scoped without an identity: %v", err)
	}
	if got != "" {
		t.Fatalf("app.owner_id = %q, want an empty setting", got)
	}
}

func TestScopedPublishesTheIdentityForOneTransactionOnly(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := identity.With(context.Background(), identity.Identity{
		OwnerID:   "owner-scope-test",
		Bypass:    true,
		Retention: true,
	})

	type settings struct{ owner, bypass, system, audit, retention string }
	inside, err := scoped(ctx, db, func(tx *gorm.DB) (settings, error) {
		var s settings
		row := tx.Raw(`SELECT
			coalesce(current_setting('app.owner_id', true), ''),
			coalesce(current_setting('app.bypass', true), ''),
			coalesce(current_setting('app.system', true), ''),
			coalesce(current_setting('app.audit_read', true), ''),
			coalesce(current_setting('app.retention', true), '')`).Row()
		return s, row.Scan(&s.owner, &s.bypass, &s.system, &s.audit, &s.retention)
	})
	if err != nil {
		t.Fatalf("scoped: %v", err)
	}
	if inside.owner != "owner-scope-test" {
		t.Fatalf("app.owner_id = %q, want the callers identity", inside.owner)
	}
	if inside.bypass != "on" || inside.retention != "on" {
		t.Fatalf("bypass/retention = %q/%q, want on/on", inside.bypass, inside.retention)
	}
	// Both were left at their zero value on the Identity, so they must read off
	// rather than keeping whatever the previous transaction wrote.
	if inside.system != "off" || inside.audit != "off" {
		t.Fatalf("system/audit_read = %q/%q, want off/off", inside.system, inside.audit)
	}

	// is_local = true is the whole safety argument: the settings die with the
	// transaction, so the next borrower of that connection starts clean.
	var leaked string
	if err := db.Raw(`SELECT coalesce(current_setting('app.owner_id', true), '')`).Scan(&leaked).Error; err != nil {
		t.Fatalf("read the setting after commit: %v", err)
	}
	if leaked != "" {
		t.Fatalf("app.owner_id survived the transaction as %q", leaked)
	}
}

// A failed scoped call must leave nothing behind: the repositories inside rely
// on the transaction, not on their own cleanup.
func TestScopedRollsBackWhenTheWorkFails(t *testing.T) {
	db := openGovernanceDB(t)
	name := uniqueName("scope-rollback")
	want := errors.New("the work failed")

	_, err := scoped(identity.System(), db, func(tx *gorm.DB) (struct{}, error) {
		if err := tx.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name).Error; err != nil {
			return struct{}{}, err
		}
		return struct{}{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("scoped = %v, want the work's error", err)
	}

	var kept int64
	if err := db.Raw(`SELECT count(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&kept).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if kept != 0 {
		t.Fatal("a failed scoped call left its row behind")
	}
}

func TestWithIdentityReturnsOnlyTheError(t *testing.T) {
	db := openGovernanceDB(t)

	if err := withIdentity(identity.System(), db, func(tx *gorm.DB) error {
		var n int
		return tx.Raw(`SELECT 1`).Scan(&n).Error
	}); err != nil {
		t.Fatalf("withIdentity success path = %v", err)
	}

	want := errors.New("boom")
	if err := withIdentity(identity.System(), db, func(*gorm.DB) error { return want }); !errors.Is(err, want) {
		t.Fatalf("withIdentity = %v, want the work's error", err)
	}
}

// The empty owner is the dangerous case: it must match only rows written
// without an owner. If it ever turned into a wildcard, every by-id read in the
// package would become a read of any row.
func TestOwnedWhereComparesTheOwnerExactly(t *testing.T) {
	db := openGovernanceDB(t)
	s := NewThreadsStore(db)
	ctx := context.Background()

	mine, err := s.CreateThread(ctx, &threads.Thread{Title: "owned-where", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM threads WHERE id = ?`, mine.ID).Error })

	cases := []struct {
		owner string
		want  int64
		why   string
	}{
		{"alice", 1, "the owner sees their row"},
		{"bob", 0, "another user does not"},
		{"", 0, "an empty owner is not a wildcard"},
	}
	for _, c := range cases {
		var got int64
		if err := ownedWhere(db.Table("threads"), mine.ID, c.owner).Count(&got).Error; err != nil {
			t.Fatalf("count for owner %q: %v", c.owner, err)
		}
		if got != c.want {
			t.Errorf("ownedWhere(owner=%q) matched %d rows, want %d (%s)", c.owner, got, c.want, c.why)
		}
	}
}

// relaxRLS is a DSN rewrite, so it is testable without a server: what matters is
// that the option lands where libpq reads it and that an existing options
// parameter is extended rather than replaced.
func TestRelaxRLSAddsTheSystemOption(t *testing.T) {
	cases := []struct {
		name   string
		dsn    string
		expect string
	}{
		{"plain", "postgres://u:p@localhost:5434/db", "options=-c+app.system%3Don"},
		{
			"keeps an existing option",
			"postgres://u:p@localhost:5434/db?options=-c+search_path%3Dfoo",
			"options=-c+search_path%3Dfoo+-c+app.system%3Don",
		},
		// Not a URL: the value is appended in the keyword form libpq also accepts,
		// because a DSN this code cannot parse must still end up relaxed.
		{"keyword form", "host=localhost dbname=db", "options='-c app.system=on'"},
	}
	for _, c := range cases {
		got := relaxRLS(c.dsn)
		if !strings.Contains(got, c.expect) {
			t.Errorf("%s: relaxRLS = %q, want it to contain %q", c.name, got, c.expect)
		}
	}
}

// The pool sizing is all defaults and clamping; getting it wrong is invisible
// until an operator wonders why their tuning did nothing.
func TestDBPoolFromEnvReadsAndClamps(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		open, idle, life := dbPoolFromEnv()
		if open != 25 || idle != 10 || life != 30*time.Minute {
			t.Fatalf("defaults = %d/%d/%s", open, idle, life)
		}
	})

	t.Run("env wins", func(t *testing.T) {
		t.Setenv("AGENTHUB_DB_MAX_OPEN_CONNS", "40")
		t.Setenv("AGENTHUB_DB_MAX_IDLE_CONNS", "5")
		t.Setenv("AGENTHUB_DB_CONN_MAX_LIFETIME", "90s")
		open, idle, life := dbPoolFromEnv()
		if open != 40 || idle != 5 || life != 90*time.Second {
			t.Fatalf("env = %d/%d/%s", open, idle, life)
		}
	})

	t.Run("idle above open is clamped", func(t *testing.T) {
		t.Setenv("AGENTHUB_DB_MAX_OPEN_CONNS", "4")
		t.Setenv("AGENTHUB_DB_MAX_IDLE_CONNS", "9")
		open, idle, _ := dbPoolFromEnv()
		if open != 4 || idle != 4 {
			t.Fatalf("clamped = %d/%d, want 4/4", open, idle)
		}
	})

	t.Run("nonsense falls back", func(t *testing.T) {
		t.Setenv("AGENTHUB_DB_MAX_OPEN_CONNS", "0")
		t.Setenv("AGENTHUB_DB_MAX_IDLE_CONNS", "not-a-number")
		t.Setenv("AGENTHUB_DB_CONN_MAX_LIFETIME", "-5m")
		open, idle, life := dbPoolFromEnv()
		if open != 25 || idle != 10 || life != 30*time.Minute {
			t.Fatalf("fallback = %d/%d/%s", open, idle, life)
		}
	})
}

func TestPingAndCloseTreatAMissingHandleAsNoDatabase(t *testing.T) {
	if err := Ping(nil); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("Ping(nil) = %v, want ErrNoDatabase", err)
	}
	if err := Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v, want nil", err)
	}

	db := openGovernanceDB(t)
	if err := Ping(db); err != nil {
		t.Fatalf("Ping(live) = %v", err)
	}
}

// Applied is what the backup layer records in every archive, so the list has to
// be the ledger's content and it has to be ordered.
func TestAppliedReportsTheLedgerInOrder(t *testing.T) {
	db := openGovernanceDB(t)

	names, err := Applied(db)
	if err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("Applied returned nothing after Migrate ran")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Applied is not sorted: %q before %q", names[i-1], names[i])
		}
	}
	var recorded int64
	if err := db.Raw(`SELECT count(*) FROM schema_migrations`).Scan(&recorded).Error; err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if int64(len(names)) != recorded {
		t.Fatalf("Applied returned %d names for %d ledger rows", len(names), recorded)
	}
}

// Re-applying a migration another process already recorded must be a no-op, not
// a second execution: the advisory lock is held, so the recheck decides.
func TestApplyMigrationIsANoOpWhenAlreadyRecorded(t *testing.T) {
	db := openGovernanceDB(t)
	if err := applyMigration(db, "001_resources.sql"); err != nil {
		t.Fatalf("re-applying a recorded migration = %v, want nil", err)
	}
}

func TestApplyMigrationReportsAnUnknownFile(t *testing.T) {
	db := openGovernanceDB(t)
	err := applyMigration(db, "999_this_file_does_not_exist.sql")
	if err == nil {
		t.Fatal("applying a file that is not embedded must fail")
	}
	if !strings.Contains(err.Error(), "read migration") {
		t.Fatalf("error = %v, want it to name the read failure", err)
	}
}

func TestSeedIsIdempotentAndPublishesTheDemoRows(t *testing.T) {
	db := openGovernanceDB(t)
	entries := DemoResources()

	if err := Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := Seed(db); err != nil {
		t.Fatalf("Seed twice: %v", err)
	}

	var count int64
	if err := db.Table("resources").Where("id LIKE ?", "demo-%").Count(&count).Error; err != nil {
		t.Fatalf("count demo rows: %v", err)
	}
	if count != int64(len(entries)) {
		t.Fatalf("demo rows = %d after two seeds, want %d", count, len(entries))
	}
	// Authorisation is fail closed: an unpublished demo row would break the
	// walkthrough and scripts/test-api.sh on a fresh instance.
	var unpublished int64
	if err := db.Table("resources").
		Where("id LIKE ? AND visibility <> ?", "demo-%", registry.VisibilityPublic).
		Count(&unpublished).Error; err != nil {
		t.Fatalf("count unpublished demo rows: %v", err)
	}
	if unpublished != 0 {
		t.Fatalf("%d demo rows are not public", unpublished)
	}
}

func TestSeedAndMigrateRefuseAMissingHandle(t *testing.T) {
	if err := Seed(nil); err == nil {
		t.Fatal("Seed(nil) must fail")
	}
	if err := Migrate(nil); err == nil {
		t.Fatal("Migrate(nil) must fail")
	}
}
