// Row level security, asserted against a real PostgreSQL.
//
// 026_rls_policies.sql calls itself "the database's last line of defence", and
// until this file existed nothing executable checked that claim: the repository
// tests open the database with RLS relaxed (see integration_test.go), so a
// policy that silently stopped matching would have gone unnoticed.
//
// Two layers are asserted, because they fail differently:
//
//  1. structure — the tables really carry ENABLE + FORCE row level security and
//     the expected policies exist. A forgotten ALTER TABLE shows up here.
//  2. behaviour — what a session sees under each of the settings the policies
//     read. A policy that is present but wrong shows up here.
//
// Gating matches the rest of the package: without DATABASE_URL every case
// skips. Two preconditions have to hold for the assertions to mean anything,
// and openRLSDB establishes both: the connection must be opened with
// RLSEnforce: true (the zero value of OpenOptions relaxes RLS through
// store.relaxRLS), and its session role must be one the policies bind — a
// superuser or a BYPASSRLS role reads the whole table no matter what 026 says.
package tests

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"agenthub/internal/identity"
	"agenthub/internal/store"
)

// rlsSettings publishes the five settings every policy reads in one statement,
// mirroring internal/store/scope.go so the tests exercise the real contract and
// not a private copy of it. is_local=true keeps them inside the transaction.
const rlsSettings = `SELECT
	set_config('app.owner_id',   ?, true),
	set_config('app.bypass',     ?, true),
	set_config('app.system',     ?, true),
	set_config('app.audit_read', ?, true),
	set_config('app.retention',  ?, true)`

// rlsIdentity is the test's spelling of identity.Identity: the same fields, so a
// reader can compare the two without translation.
type rlsIdentity struct {
	OwnerID   string
	Bypass    bool
	System    bool
	AuditRead bool
	Retention bool
}

func (r rlsIdentity) apply(tx *gorm.DB) error {
	return tx.Exec(rlsSettings,
		r.OwnerID, rlsFlag(r.Bypass), rlsFlag(r.System), rlsFlag(r.AuditRead), rlsFlag(r.Retention),
	).Error
}

func rlsFlag(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// asUser is what an ordinary signed-in request publishes.
func asUser(owner string) rlsIdentity { return rlsIdentity{OwnerID: owner} }

// constrainedRole is the login role the behavioural assertions run as.
//
// A superuser or a BYPASSRLS role walks through every policy no matter what
// 026_rls_policies.sql says, and the policies bind the table owner only because
// that migration adds FORCE. A suite that connected as the superuser a compose
// file or the CI service container hands out would therefore assert "everybody
// sees everything" and call it a pass. Deployments point DATABASE_URL at
// scripts/provision-db-roles.sql's agenthub_app for exactly that reason; when
// the suite is handed a privileged role instead, it provisions this throwaway
// one and reconnects as it, which is the same shape as production.
const (
	constrainedRole     = "agenthub_rls_test"
	constrainedPassword = "agenthub_rls_test"
)

// openRLSDB opens the database with the policies enforced, runs the migrations
// and hands back a handle whose connections the policies actually bind. It is
// deliberately separate from openDBReal: a relaxed connection would make this
// whole file meaningless.
func openRLSDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping RLS integration test (make db-up)")
	}
	db, err := store.OpenWithOptions(dsn, store.OpenOptions{RLSEnforce: true})
	if err != nil {
		t.Fatalf("open db with RLS enforced: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		// A runtime role (agenthub_app) may not run DDL at all. If the schema is
		// already there — the production shape — the assertions still hold.
		if !schemaExists(db) {
			_ = store.Close(db)
			t.Fatalf("migrate: %v", err)
		}
		t.Logf("rls: schema already migrated, skipping DDL: %v", err)
	}
	if !bypassesRLS(t, db) {
		t.Cleanup(func() {
			if err := store.Close(db); err != nil {
				t.Errorf("close db: %v", err)
			}
		})
		return db
	}
	// Handed a role the policies do not bind: reconnect as one they do.
	dsn = reconnectAsConstrainedRole(t, db, dsn)
	enforced, err := store.OpenWithOptions(dsn, store.OpenOptions{RLSEnforce: true})
	if err != nil {
		t.Fatalf("open db as %s: %v", constrainedRole, err)
	}
	t.Cleanup(func() {
		if err := store.Close(enforced); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return enforced
}

// bypassesRLS reports whether the session role ignores every policy.
func bypassesRLS(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var role string
	var super, bypass bool
	err := db.Raw(
		`SELECT rolname, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Row().Scan(&role, &super, &bypass)
	if err != nil {
		t.Fatalf("read the session role: %v", err)
	}
	t.Logf("rls: connected as %q (superuser=%v bypassrls=%v)", role, super, bypass)
	return super || bypass
}

// schemaExists reports whether the migrations have run at least once.
func schemaExists(db *gorm.DB) bool {
	var name string
	if err := db.Raw(`SELECT to_regclass('public.tasks')::text`).Scan(&name).Error; err != nil {
		return false
	}
	return name != ""
}

// reconnectAsConstrainedRole provisions constrainedRole and returns a DSN that
// logs in as it. The role is created only in a database the suite already
// writes to, and it exists to be denied: NOSUPERUSER, NOBYPASSRLS, no DDL.
func reconnectAsConstrainedRole(t *testing.T, admin *gorm.DB, dsn string) string {
	t.Helper()
	var database string
	if err := admin.Raw(`SELECT current_database()`).Scan(&database).Error; err != nil {
		_ = store.Close(admin)
		t.Fatalf("read the database name: %v", err)
	}
	statements := []string{
		`DO $$
		 BEGIN
		   IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '` + constrainedRole + `') THEN
		     CREATE ROLE ` + constrainedRole + ` LOGIN;
		   END IF;
		 END $$`,
		`ALTER ROLE ` + constrainedRole + ` LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE PASSWORD '` + constrainedPassword + `'`,
		`GRANT CONNECT ON DATABASE ` + quoteIdent(database) + ` TO ` + constrainedRole,
		`GRANT USAGE ON SCHEMA public TO ` + constrainedRole,
		// DELETE on audit_log is part of the grant on purpose: the case below
		// has to prove that the policy and the append-only trigger refuse an
		// expiry without app.retention, so a missing privilege must not be what
		// stops it (and the retention path must still be able to go through).
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + constrainedRole,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + constrainedRole,
	}
	for _, stmt := range statements {
		if err := admin.Exec(stmt).Error; err != nil {
			_ = store.Close(admin)
			t.Skipf("cannot provision %s for the row level security assertions (%v): point DATABASE_URL at a role the policies bind (scripts/provision-db-roles.sql)", constrainedRole, err)
		}
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		_ = store.Close(admin)
		t.Skipf("DATABASE_URL is not a URL this suite can re-point at %s (%v): use postgres://user:pass@host/db", constrainedRole, err)
	}
	u.User = url.UserPassword(constrainedRole, constrainedPassword)
	if err := store.Close(admin); err != nil {
		t.Errorf("close the privileged handle: %v", err)
	}
	return u.String()
}

// quoteIdent quotes an SQL identifier (double quotes, doubled inside).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// rlsTag keeps ids unique per test and per run: the suite shares one database
// with the seed and with parallel cases, and a collision would turn a policy
// failure into a primary key error.
func rlsTag(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", "_", "-", " ", "-", "#", "-").Replace(t.Name())
	return "rls-" + name + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// inScope runs fn in a transaction that first publishes scope. Returning the
// error instead of failing inside the callback keeps the transaction closable.
func inScope(db *gorm.DB, scope rlsIdentity, fn func(*gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := scope.apply(tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// execIn is the write half: it fails the test unless the statement succeeds.
func execIn(t *testing.T, db *gorm.DB, scope rlsIdentity, stmt string, args ...any) {
	t.Helper()
	err := inScope(db, scope, func(tx *gorm.DB) error {
		return tx.Exec(stmt, args...).Error
	})
	if err != nil {
		t.Fatalf("exec %q as %+v: %v", stmt, scope, err)
	}
}

// execErrIn is the refusal half: it returns whatever the policy or trigger said
// so a case can assert that a write was rejected.
func execErrIn(db *gorm.DB, scope rlsIdentity, stmt string, args ...any) error {
	return inScope(db, scope, func(tx *gorm.DB) error {
		return tx.Exec(stmt, args...).Error
	})
}

// affectedIn reports how many rows a write touched. A refusal counts as zero:
// "the policy filtered the row out" and "the trigger raised" are different
// sentences for the same outcome, and the cases that use this assert the
// outcome (nothing changed) rather than which of the two fired.
func affectedIn(t *testing.T, db *gorm.DB, scope rlsIdentity, stmt string, args ...any) int64 {
	t.Helper()
	var rows int64
	err := inScope(db, scope, func(tx *gorm.DB) error {
		res := tx.Exec(stmt, args...)
		rows = res.RowsAffected
		return res.Error
	})
	if err != nil {
		t.Logf("write refused as %+v (that is one of the two expected outcomes): %v", scope, err)
		return 0
	}
	return rows
}

// countIn answers "how many rows does this session see?".
func countIn(t *testing.T, db *gorm.DB, scope rlsIdentity, query string, args ...any) int64 {
	t.Helper()
	var n int64
	err := inScope(db, scope, func(tx *gorm.DB) error {
		return tx.Raw(query, args...).Scan(&n).Error
	})
	if err != nil {
		t.Fatalf("count %q as %+v: %v", query, scope, err)
	}
	return n
}

// rlsSeedTask writes one task per owner plus one child row on each of the five
// child tables, all with an empty owner_id so the trigger has to fill it in.
func rlsSeedTask(t *testing.T, db *gorm.DB, id, owner string) {
	t.Helper()
	system := rlsIdentity{System: true}
	execIn(t, db, system, `INSERT INTO tasks (id, state, owner_id) VALUES (?, 'submitted', ?)`, id, owner)
	execIn(t, db, system, `INSERT INTO task_artifacts (task_id, artifact_id) VALUES (?, 'art-1')`, id)
	execIn(t, db, system, `INSERT INTO task_spans (id, task_id, kind) VALUES (?, ?, 'step')`, id+"-span", id)
	execIn(t, db, system, `INSERT INTO task_pending (task_id, prompt) VALUES (?, 'approve?')`, id)
	execIn(t, db, system, `INSERT INTO task_push (task_id) VALUES (?)`, id)
	execIn(t, db, system, `INSERT INTO events (seq, task_id, kind) VALUES (1, ?, 'status')`, id)
}

// rlsDropTask removes the seeded rows. Cleanup runs as system because a policy
// would (correctly) refuse to delete somebody else's task.
func rlsDropTask(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	if err := execErrIn(db, rlsIdentity{System: true}, `DELETE FROM tasks WHERE id = ?`, id); err != nil {
		t.Logf("cleanup of %s failed: %v", id, err)
	}
}

// TestRLSEveryPolicyTableIsForced is the structural half: FORCE is what makes
// the policies apply to the table owner too, and a table that only has ENABLE is
// a policy a superuser or a migration role can walk around.
func TestRLSEveryPolicyTableIsForced(t *testing.T) {
	db := openRLSDB(t)

	expected := []string{
		"tasks", "task_artifacts", "task_spans", "task_pending", "task_push",
		"events", "knowledge", "audit_log",
	}
	for _, table := range expected {
		var enabled, forced bool
		err := db.Raw(
			`SELECT relrowsecurity, relforcerowsecurity
			   FROM pg_class
			  WHERE relname = ? AND relkind = 'r'`,
			table,
		).Row().Scan(&enabled, &forced)
		if err != nil {
			t.Fatalf("read the security flags of %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Fatalf("%s must be ENABLE + FORCE row level security, got enabled=%v forced=%v", table, enabled, forced)
		}
	}

	// The policies themselves, by name: a table with RLS on but no policy denies
	// everything, which would hide the bug behind "no rows for anyone".
	names := map[string]bool{}
	rows, err := db.Raw(`SELECT policyname FROM pg_policies WHERE schemaname = current_schema()`).Rows()
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan policy name: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate policies: %v", err)
	}
	for _, want := range []string{
		"tasks_owner_access", "task_artifacts_owner_access", "task_spans_owner_access",
		"task_pending_owner_access", "task_push_owner_access", "events_owner_access",
		"knowledge_owner_access",
		"audit_log_read", "audit_log_append", "audit_log_retention",
	} {
		if !names[want] {
			t.Fatalf("policy %q is missing, got %v", want, names)
		}
	}
}

// TestRLSWithoutAnIdentitySeesNothing is the fail-closed default: a connection
// that forgot to carry an identity must not fall back to "every row".
func TestRLSWithoutAnIdentitySeesNothing(t *testing.T) {
	db := openRLSDB(t)
	id := rlsTag(t)
	rlsSeedTask(t, db, id, "alice-"+id)
	t.Cleanup(func() { rlsDropTask(t, db, id) })

	// No settings at all: every comparison yields NULL, which a policy reads as
	// false. This is the shape a psql session or a future handler would have.
	var n int64
	if err := db.Raw(`SELECT count(*) FROM tasks WHERE id = ?`, id).Scan(&n).Error; err != nil {
		t.Fatalf("count without an identity: %v", err)
	}
	if n != 0 {
		t.Fatalf("a connection with no identity saw %d rows, want 0", n)
	}

	// An identity that is present but names nobody behaves the same way.
	if got := countIn(t, db, rlsIdentity{}, `SELECT count(*) FROM tasks WHERE id = ?`, id); got != 0 {
		t.Fatalf("an empty identity saw %d rows, want 0", got)
	}
}

// TestRLSEachOwnerSeesOnlyTheirOwnRows covers the seven session settings the
// policies read, on the task table first.
func TestRLSEachOwnerSeesOnlyTheirOwnRows(t *testing.T) {
	db := openRLSDB(t)
	aliceID, bobID := rlsTag(t)+"-a", rlsTag(t)+"-b"
	rlsSeedTask(t, db, aliceID, "alice-"+aliceID)
	rlsSeedTask(t, db, bobID, "bob-"+bobID)
	t.Cleanup(func() {
		rlsDropTask(t, db, aliceID)
		rlsDropTask(t, db, bobID)
	})

	if got := countIn(t, db, asUser("alice-"+aliceID), `SELECT count(*) FROM tasks WHERE id = ?`, aliceID); got != 1 {
		t.Fatalf("the owner saw %d of their own rows, want 1", got)
	}
	if got := countIn(t, db, asUser("alice-"+aliceID), `SELECT count(*) FROM tasks WHERE id = ?`, bobID); got != 0 {
		t.Fatalf("a user saw %d rows of another account, want 0", got)
	}
	// A plain SELECT * must not leak either: an unfiltered listing is exactly the
	// query that used to hand out the whole table.
	visible := countIn(t, db, asUser("alice-"+aliceID), `SELECT count(*) FROM tasks`)
	if visible != 1 {
		t.Fatalf("an unfiltered listing showed %d rows, want the owner's single row", visible)
	}
	if got := countIn(t, db, rlsIdentity{Bypass: true}, `SELECT count(*) FROM tasks WHERE id IN (?, ?)`, aliceID, bobID); got != 2 {
		t.Fatalf("the platform owner saw %d of 2 rows", got)
	}
	if got := countIn(t, db, rlsIdentity{System: true}, `SELECT count(*) FROM tasks WHERE id IN (?, ?)`, aliceID, bobID); got != 2 {
		t.Fatalf("a process owned path saw %d of 2 rows", got)
	}
}

// TestRLSChildRowsInheritTheirTaskOwner pins the trigger from
// 022_task_ownership.sql: every writer that forgets owner_id still produces a
// row the right person can read.
func TestRLSChildRowsInheritTheirTaskOwner(t *testing.T) {
	db := openRLSDB(t)
	owner := "carol-" + rlsTag(t)
	id := rlsTag(t)
	rlsSeedTask(t, db, id, owner)
	t.Cleanup(func() { rlsDropTask(t, db, id) })

	for _, table := range []string{
		"task_artifacts", "task_spans", "task_pending", "task_push", "events",
	} {
		var stored string
		err := inScope(db, rlsIdentity{System: true}, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT owner_id FROM `+table+` WHERE task_id = ?`, id).Scan(&stored).Error
		})
		if err != nil {
			t.Fatalf("read %s owner: %v", table, err)
		}
		if stored != owner {
			t.Fatalf("%s stored owner %q, want the task's owner %q", table, stored, owner)
		}
		if got := countIn(t, db, asUser(owner), `SELECT count(*) FROM `+table+` WHERE task_id = ?`, id); got != 1 {
			t.Fatalf("the owner saw %d rows in %s, want 1", got, table)
		}
		if got := countIn(t, db, asUser("dave-"+id), `SELECT count(*) FROM `+table+` WHERE task_id = ?`, id); got != 0 {
			t.Fatalf("another account saw %d rows in %s, want 0", got, table)
		}
	}
}

// TestRLSRefusesToWriteAnotherOwnersRow is the WITH CHECK half: reading is not
// the only way to cross a tenant boundary.
func TestRLSRefusesToWriteAnotherOwnersRow(t *testing.T) {
	db := openRLSDB(t)
	id := rlsTag(t)
	t.Cleanup(func() { rlsDropTask(t, db, id) })

	err := execErrIn(db, asUser("mallory-"+id), `INSERT INTO tasks (id, state, owner_id) VALUES (?, 'submitted', ?)`, id, "alice-"+id)
	if err == nil {
		t.Fatal("an INSERT naming another owner must be refused by the WITH CHECK policy")
	}

	// The owner can write their own row, and only theirs.
	execIn(t, db, asUser("alice-"+id), `INSERT INTO tasks (id, state, owner_id) VALUES (?, 'submitted', ?)`, id, "alice-"+id)

	// An UPDATE that would hand the row to somebody else is an error, not a
	// silent no-op: USING lets the owner load the row, and WITH CHECK then
	// rejects the *new* owner. Either way the row must stay where it was.
	if err := execErrIn(db, asUser("alice-"+id), `UPDATE tasks SET owner_id = ? WHERE id = ?`, "bob-"+id, id); err == nil {
		t.Fatal("an UPDATE naming another owner must be refused by the WITH CHECK policy")
	}
	if got := countIn(t, db, asUser("alice-"+id), `SELECT count(*) FROM tasks WHERE id = ? AND owner_id = ?`, id, "alice-"+id); got != 1 {
		t.Fatalf("the row moved or vanished: %d of the owner's rows remain", got)
	}

	// Another account cannot delete it either.
	if err := execErrIn(db, asUser("bob-"+id), `DELETE FROM tasks WHERE id = ?`, id); err != nil {
		t.Fatalf("a filtered delete must succeed while removing nothing: %v", err)
	}
	if got := countIn(t, db, asUser("alice-"+id), `SELECT count(*) FROM tasks WHERE id = ?`, id); got != 1 {
		t.Fatalf("the row disappeared after another account's delete, %d remain", got)
	}
}

// TestRLSCoversKnowledgeChunks covers the table added for F14: a chunk belongs
// to the account that indexed it, not only to the agent it mentions.
func TestRLSCoversKnowledgeChunks(t *testing.T) {
	db := openRLSDB(t)
	aliceID, bobID := rlsTag(t)+"-a", rlsTag(t)+"-b"
	system := rlsIdentity{System: true}
	execIn(t, db, system, `INSERT INTO knowledge (id, agent_id, chunk_text, owner_id) VALUES (?, 'agent-1', 'alice fact', ?)`, aliceID, "alice-"+aliceID)
	execIn(t, db, system, `INSERT INTO knowledge (id, agent_id, chunk_text, owner_id) VALUES (?, 'agent-1', 'bob fact', ?)`, bobID, "bob-"+bobID)
	t.Cleanup(func() {
		for _, id := range []string{aliceID, bobID} {
			_ = execErrIn(db, system, `DELETE FROM knowledge WHERE id = ?`, id)
		}
	})

	if got := countIn(t, db, asUser("alice-"+aliceID), `SELECT count(*) FROM knowledge WHERE agent_id = 'agent-1'`); got != 1 {
		t.Fatalf("the owner saw %d chunks of a shared agent, want 1", got)
	}
	if got := countIn(t, db, asUser("bob-"+bobID), `SELECT count(*) FROM knowledge WHERE id = ?`, aliceID); got != 0 {
		t.Fatalf("another account read %d chunks, want 0", got)
	}
}

// TestRLSAuditTrailIsReadOnlyAndAppendOnly covers the three audit policies and
// the append-only trigger behind them: a normal request scope cannot even see
// the trail, an auditor can read but not append, and only the retention job may
// expire rows.
func TestRLSAuditTrailIsReadOnlyAndAppendOnly(t *testing.T) {
	db := openRLSDB(t)
	eventID := rlsTag(t)
	system := rlsIdentity{System: true}
	execIn(t, db, system,
		`INSERT INTO audit_log (event_id, source, action, owner_id) VALUES (?, 'api', 'GET /api/tasks', 'alice')`,
		eventID)
	t.Cleanup(func() {
		_ = execErrIn(db, rlsIdentity{System: true, Retention: true}, `DELETE FROM audit_log WHERE event_id = ?`, eventID)
	})

	read := func(scope rlsIdentity) int64 {
		return countIn(t, db, scope, `SELECT count(*) FROM audit_log WHERE event_id = ?`, eventID)
	}
	if got := read(asUser("alice")); got != 0 {
		t.Fatalf("a normal request saw %d audit rows, want 0", got)
	}
	if got := read(rlsIdentity{AuditRead: true}); got != 1 {
		t.Fatalf("an auditor saw %d audit rows, want 1", got)
	}
	if got := read(rlsIdentity{Bypass: true}); got != 1 {
		t.Fatalf("the platform owner saw %d audit rows, want 1", got)
	}

	// Nobody may append on behalf of a request, not even the auditor: the writer
	// is a process owned path.
	if err := execErrIn(db, rlsIdentity{AuditRead: true},
		`INSERT INTO audit_log (event_id, source, action) VALUES (?, 'api', 'forged')`, eventID+"-forged"); err == nil {
		t.Fatal("an audit reader must not be able to append")
	}
	execIn(t, db, system, `INSERT INTO audit_log (event_id, source, action) VALUES (?, 'api', 'allowed')`, eventID+"-ok")

	// A process identity alone may not expire a row. On a connection the
	// policies bind, the DELETE policy filters the row out, so the statement
	// succeeds while touching nothing; a privileged connection never reaches
	// the policy and is refused by the append-only trigger instead. Either way
	// the retention setting is what a delete needs, and the row has to survive.
	if rows := affectedIn(t, db, system, `DELETE FROM audit_log WHERE event_id = ?`, eventID+"-ok"); rows != 0 {
		t.Fatalf("a process identity expired %d audit rows without app.retention", rows)
	}
	if got := countIn(t, db, system, `SELECT count(*) FROM audit_log WHERE event_id = ?`, eventID+"-ok"); got != 1 {
		t.Fatalf("the row a process identity tried to delete is gone (count %d)", got)
	}
	// An edit is refused the same way: no policy grants UPDATE, and the trigger
	// behind the policy rejects one for every role, including the owner's.
	if rows := affectedIn(t, db, rlsIdentity{Bypass: true, Retention: true}, `UPDATE audit_log SET action = 'edited' WHERE event_id = ?`, eventID); rows != 0 {
		t.Fatal("the append-only trail accepted an edit")
	}
	if got := countIn(t, db, system, `SELECT count(*) FROM audit_log WHERE event_id = ? AND action = 'edited'`, eventID); got != 0 {
		t.Fatal("the append-only trail kept an edit")
	}
	// The retention path works: the policy admits it and the trigger allows it.
	execIn(t, db, rlsIdentity{System: true, Retention: true}, `DELETE FROM audit_log WHERE event_id = ?`, eventID+"-ok")
	if got := read(system); got != 1 {
		t.Fatalf("after the retention delete %d rows remain, want the untouched one", got)
	}
}

// TestRLSRepositoryQueriesAreScoped closes the loop with the production path:
// identity.Identity travels on the context, internal/store publishes it, and the
// same repository call answers differently per identity. A test that only
// exercised raw SQL would not notice a broken set_config.
func TestRLSRepositoryQueriesAreScoped(t *testing.T) {
	db := openRLSDB(t)
	id := rlsTag(t)
	owner := "erin-" + id
	rlsSeedTask(t, db, id, owner)
	t.Cleanup(func() { rlsDropTask(t, db, id) })

	repo := store.NewTaskRepository(db)
	alice := identity.With(t.Context(), identity.Identity{OwnerID: owner})
	stranger := identity.With(t.Context(), identity.Identity{OwnerID: "frank-" + id})

	own, err := repo.Get(alice, id)
	if err != nil {
		t.Fatalf("the owner cannot read their own task: %v", err)
	}
	if own.ID != id {
		t.Fatalf("read the wrong task: %q", own.ID)
	}
	if _, err := repo.Get(stranger, id); err == nil {
		t.Fatal("another account read the task through the repository")
	}
}
