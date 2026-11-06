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

	"agenthub/internal/channels"
	"agenthub/internal/computers"
	"agenthub/internal/identity"
	"agenthub/internal/schedules"
	"agenthub/internal/store"
	"agenthub/internal/testdb"
	"agenthub/internal/threads"
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
		// The three per-owner surfaces joined in 028. They are the tables whose
		// by-id endpoints used to answer any id, so this list is also the
		// regression guard for that class of bug.
		"channels", "schedules", "computers",
		// 029 and 030 closed two more gaps: a delivery receipt and a thread
		// message were reached only through their parent row, so the parent was
		// the whole guarantee. Now the rows carry the owner themselves.
		"channel_messages", "threads", "thread_messages",
		// 031 added the shared-file surface; it carries its own owner.
		"shared_files",
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
		"channels_owner_access", "schedules_owner_access", "computers_owner_access",
		"channel_messages_owner_access", "threads_owner_access", "thread_messages_owner_access",
		"shared_files_owner_access",
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
	// The last assertion is that the retention scope may expire a row, so the
	// role has to hold DELETE on audit_log. The suite provisions it when it was
	// handed a superuser; the production runtime role never does — there, the
	// ledger belongs to the retention job and this case skips by name.
	testdb.RequireTablePrivilege(t, db, "audit_log", "DELETE")
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

// TestRLSOwnedSurfacesAreScopedByOwner walks channels, schedules and computers
// through their real repositories. The three share one shape: the list endpoint
// always carried the owner, the by-id endpoints did not, and the tables had no
// policy behind them either — so a missing owner in a handler was invisible.
//
// The assertions are therefore two-sided: the SQL scoping must answer "absent"
// for a stranger, and the policy must agree. Only the webhook read is
// deliberately unscoped, and it is asserted explicitly so nobody narrows it
// later without discovering why it exists.
func TestRLSOwnedSurfacesAreScopedByOwner(t *testing.T) {
	db := openRLSDB(t)
	id := rlsTag(t)
	alice := "alice-" + id
	bob := "bob-" + id
	aliceCtx := identity.With(t.Context(), identity.Identity{OwnerID: alice})
	bobCtx := identity.With(t.Context(), identity.Identity{OwnerID: bob})
	system := identity.System()

	chans := store.NewChannelsStore(db)
	scheds := store.NewSchedulesStore(db)
	comps := store.NewComputersStore(db)

	channel, err := chans.Create(aliceCtx, &channels.Channel{
		Kind: channels.KindSlack, Name: "webhook-" + id, Enabled: true,
		SigningKeyEnv: "RLS_TEST_" + id, OwnerID: alice,
		Config: map[string]any{"target_url": "https://example.test/hook"},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sched, err := scheds.Create(aliceCtx, &schedules.Schedule{
		Goal: "goal-" + id, Trigger: "cron", CronExpr: "0 9 * * *", Enabled: true, OwnerID: alice,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	comp, err := comps.Create(aliceCtx, &computers.Computer{
		Kind: computers.KindShell, LastAction: "action-" + id, OwnerID: alice,
	})
	if err != nil {
		t.Fatalf("create computer: %v", err)
	}
	t.Cleanup(func() {
		// Cleanup runs as the process: a policy would refuse to delete another
		// owner's row, which is the behaviour under test.
		_, _ = chans.Delete(system, channel.ID, alice)
		_, _ = scheds.Delete(system, sched.ID, alice)
		_ = comps.Delete(system, comp.ID, alice)
	})

	// The owner reads their own rows.
	if got, err := chans.Get(aliceCtx, channel.ID, alice); err != nil || got == nil {
		t.Fatalf("the owner cannot read their channel: %v", err)
	}
	if got, err := scheds.Get(aliceCtx, sched.ID, alice); err != nil || got == nil {
		t.Fatalf("the owner cannot read their schedule: %v", err)
	}
	if got, err := comps.Get(aliceCtx, comp.ID, alice); err != nil || got == nil {
		t.Fatalf("the owner cannot read their computer: %v", err)
	}

	// A stranger reads nothing, through either layer.
	if got, err := chans.Get(bobCtx, channel.ID, bob); err != nil || got != nil {
		t.Fatalf("stranger channel read = %+v (err=%v), want absent", got, err)
	}
	if got, err := scheds.Get(bobCtx, sched.ID, bob); err != nil || got != nil {
		t.Fatalf("stranger schedule read = %+v (err=%v), want absent", got, err)
	}
	if got, err := comps.Get(bobCtx, comp.ID, bob); err != nil || got != nil {
		t.Fatalf("stranger computer read = %+v (err=%v), want absent", got, err)
	}
	if list, err := scheds.List(bobCtx, bob); err != nil || len(list) != 0 {
		t.Fatalf("stranger schedule list = %d (err=%v), want 0", len(list), err)
	}

	// A stranger's write and delete touch nothing.
	if _, err := scheds.Update(bobCtx, &schedules.Schedule{ID: sched.ID, Goal: "hijacked"}, bob); err != nil {
		t.Fatalf("a refused update must not error, got %v", err)
	}
	// A refused delete answers "matched nothing" rather than an error: the row
	// exists, it is simply not bob's, and the two are one answer.
	if removed, err := chans.Delete(bobCtx, channel.ID, bob); err != nil || removed {
		t.Fatalf("a refused delete = %v (err=%v), want (false, nil)", removed, err)
	}
	if removed, err := scheds.Delete(bobCtx, sched.ID, bob); err != nil || removed {
		t.Fatalf("a refused schedule delete = %v (err=%v), want (false, nil)", removed, err)
	}
	if kept, err := scheds.Get(aliceCtx, sched.ID, alice); err != nil || kept == nil || kept.Goal != "goal-"+id {
		t.Fatalf("the schedule was rewritten by a stranger: %+v (err=%v)", kept, err)
	}
	if kept, err := chans.Get(aliceCtx, channel.ID, alice); err != nil || kept == nil {
		t.Fatalf("the channel was deleted by a stranger: %+v (err=%v)", kept, err)
	}

	// The webhook's read: unscoped on purpose, admitted because it runs as the
	// process. Without the system identity the policy would deny it, which is
	// the reason this method is named GetUnscoped and called from exactly one
	// place.
	if got, err := chans.GetUnscoped(system, channel.ID); err != nil || got == nil {
		t.Fatalf("the webhook cannot read a channel: %+v (err=%v)", got, err)
	}

	// Fail closed: a connection that forgot its identity sees none of the three.
	for _, table := range []string{"channels", "schedules", "computers"} {
		if n := countIn(t, db, rlsIdentity{}, `SELECT count(*) FROM `+table+` WHERE id IS NOT NULL`); n != 0 {
			t.Fatalf("a connection with no identity saw %d rows of %s, want 0", n, table)
		}
	}
}

// TestRLSReceiptsAndThreadMessagesInheritTheParentOwner covers what 029 and 030
// added: the two child tables that used to be reachable only through a parent row
// now carry the owner themselves, and the owner is copied from that parent — not
// from whatever the caller claimed.
func TestRLSReceiptsAndThreadMessagesInheritTheParentOwner(t *testing.T) {
	db := openRLSDB(t)
	id := rlsTag(t)
	alice := "alice-" + id
	bob := "bob-" + id
	aliceCtx := identity.With(t.Context(), identity.Identity{OwnerID: alice})
	bobCtx := identity.With(t.Context(), identity.Identity{OwnerID: bob})
	system := identity.System()

	chans := store.NewChannelsStore(db)
	threadStore := store.NewThreadsStore(db)

	channel, err := chans.Create(aliceCtx, &channels.Channel{
		Kind: channels.KindSlack, Name: "receipts-" + id, Enabled: true,
		SigningKeyEnv: "RLS_TEST_" + id, OwnerID: alice,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	thread, err := threadStore.CreateThread(aliceCtx, &threads.Thread{
		Title: "thread-" + id, OwnerID: alice, Participants: []string{"mcp-1"},
	})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	t.Cleanup(func() {
		_ = execErrIn(db, rlsIdentity{System: true}, `DELETE FROM channel_messages WHERE channel_id = ?`, channel.ID)
		_ = execErrIn(db, rlsIdentity{System: true}, `DELETE FROM threads WHERE id = ?`, thread.ID)
		_, _ = chans.Delete(system, channel.ID, alice)
	})

	ref := "in-" + id
	if _, err := chans.RecordDelivery(aliceCtx, &channels.ChannelMessage{
		ChannelID: channel.ID, ExternalRef: ref, Direction: "inbound", Status: channels.StatusReceived,
	}); err != nil {
		t.Fatalf("record delivery: %v", err)
	}

	// The written row carries the channel's owner, so the policy can see it even
	// though the receipt never mentions an owner in the request.
	if n := countIn(t, db, rlsIdentity{OwnerID: alice},
		`SELECT count(*) FROM channel_messages WHERE channel_id = ? AND owner_id = ?`, channel.ID, alice); n != 1 {
		t.Fatalf("the receipt does not carry the channel owner (%d rows)", n)
	}

	// The owner reads their own receipts; a stranger reads none of them, whether
	// they scope by their own id or by the real owner's.
	if list, err := chans.ListDeliveries(aliceCtx, channel.ID, alice); err != nil || len(list) != 1 {
		t.Fatalf("owner deliveries = %d (err=%v), want 1", len(list), err)
	}
	if list, err := chans.ListDeliveries(bobCtx, channel.ID, bob); err != nil || len(list) != 0 {
		t.Fatalf("stranger deliveries = %d (err=%v), want 0", len(list), err)
	}
	if got, err := chans.GetDelivery(bobCtx, channel.ID, ref, alice); err != nil || got != nil {
		t.Fatalf("stranger read with the real owner id = %+v (err=%v), want absent", got, err)
	}
	if ok, err := chans.UpdateDelivery(bobCtx, &channels.ChannelMessage{
		ID: ref, ChannelID: channel.ID, Status: channels.StatusDelivered,
	}, alice); err != nil || ok {
		t.Fatalf("stranger retry touched the receipt (ok=%v err=%v)", ok, err)
	}

	// Thread messages take the thread's owner. A stranger cannot append to a
	// thread they cannot see, and cannot read it back either.
	if _, err := threadStore.AddMessage(aliceCtx, &threads.Message{
		ThreadID: thread.ID, AuthorType: "user", AuthorID: alice, Body: "hello",
	}); err != nil {
		t.Fatalf("owner append: %v", err)
	}
	if _, err := threadStore.AddMessage(bobCtx, &threads.Message{
		ThreadID: thread.ID, AuthorType: "user", AuthorID: bob, Body: "pwned",
	}); err == nil {
		t.Fatal("a stranger appended a message to somebody else's thread")
	}
	if msgs, err := threadStore.ListMessages(aliceCtx, thread.ID, 0); err != nil || len(msgs) != 1 {
		t.Fatalf("owner messages = %d (err=%v), want 1", len(msgs), err)
	}
	if msgs, err := threadStore.ListMessages(bobCtx, thread.ID, 0); err != nil || len(msgs) != 0 {
		t.Fatalf("stranger messages = %d (err=%v), want 0", len(msgs), err)
	}

	// The webhook's unscoped read is process-only, exactly like GetUnscoped.
	if got, err := threadStore.GetThreadUnscoped(system, thread.ID); err != nil || got == nil {
		t.Fatalf("the webhook cannot read a thread: %+v (err=%v)", got, err)
	}
	if got, err := threadStore.GetThreadUnscoped(bobCtx, thread.ID); err == nil || got != nil {
		t.Fatalf("a signed in stranger read a thread unscoped: %+v (err=%v)", got, err)
	}

	// Fail closed on all three tables.
	for _, table := range []string{"channel_messages", "threads", "thread_messages"} {
		if n := countIn(t, db, rlsIdentity{}, `SELECT count(*) FROM `+table+` WHERE owner_id <> ''`); n != 0 {
			t.Fatalf("a connection with no identity saw %d rows of %s, want 0", n, table)
		}
	}
}
