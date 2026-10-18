package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub/internal/authz"
	"agenthub/internal/backup"
)

// The backup API is the only place in agenthub where one request can rewrite
// every table, so its tests are about the guardrails rather than about the
// dump: who may call it, what happens when the feature is off, and which
// refusal each broken archive produces.

// ---------------------------------------------------------------------------
// fakes
// ---------------------------------------------------------------------------

// stubEngine writes a few deterministic bytes and reads them back. The Engine
// port is what lets this file test the whole HTTP surface without PostgreSQL.
type stubEngine struct {
	name     string
	payload  []byte
	failDump bool
	restored int
}

func (e *stubEngine) Name() string { return e.name }

func (e *stubEngine) Dump(ctx context.Context, w io.Writer) (backup.DumpResult, error) {
	if e.failDump {
		return backup.DumpResult{}, backup.ErrEngineFailed
	}
	if _, err := w.Write(e.payload); err != nil {
		return backup.DumpResult{}, err
	}
	return backup.DumpResult{
		Tables:    []string{"resources", "tasks"},
		RowCounts: map[string]int64{"resources": 3, "tasks": 1},
		Schema:    []string{"007_backups.sql"},
	}, nil
}

func (e *stubEngine) Restore(ctx context.Context, r io.Reader, m backup.Manifest) error {
	got, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, e.payload) {
		return backup.ErrChecksumMismatch
	}
	e.restored++
	return nil
}

// memBackupStore is the catalogue without a database: the rows the console
// lists, in the order a real store returns them (newest first).
type memBackupStore struct {
	mu    sync.Mutex
	rows  []backup.Checkpoint
	sched *backup.Schedule
}

func newMemBackupStore() *memBackupStore { return &memBackupStore{} }

func (s *memBackupStore) index(id string) int {
	for i := range s.rows {
		if s.rows[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *memBackupStore) Create(_ context.Context, c *backup.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append([]backup.Checkpoint{*c}, s.rows...)
	return nil
}

func (s *memBackupStore) Finish(_ context.Context, c *backup.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(c.ID); i >= 0 {
		s.rows[i] = *c
	}
	return nil
}

func (s *memBackupStore) Get(_ context.Context, id string) (*backup.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i >= 0 {
		c := s.rows[i]
		return &c, nil
	}
	return nil, backup.ErrNotFound
}

func (s *memBackupStore) List(_ context.Context, limit int) ([]backup.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]backup.Checkpoint, len(s.rows))
	copy(out, s.rows)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memBackupStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.index(id); i < 0 {
		return backup.ErrNotFound
	} else {
		s.rows = append(s.rows[:i], s.rows[i+1:]...)
	}
	return nil
}

func (s *memBackupStore) LastSuccess(_ context.Context) (*backup.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].Status == backup.StatusOK {
			c := s.rows[i]
			return &c, nil
		}
	}
	return nil, nil
}

func (s *memBackupStore) MarkInterrupted(_ context.Context, olderThan time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for i := range s.rows {
		if s.rows[i].Status == backup.StatusRunning && s.rows[i].CreatedAt.Before(olderThan) {
			s.rows[i].Status = backup.StatusInterrupted
			s.rows[i].ErrorCode = "backup_interrupted"
			n++
		}
	}
	return n, nil
}

func (s *memBackupStore) LoadSchedule(context.Context) (*backup.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sched == nil {
		return nil, nil
	}
	cp := *s.sched
	return &cp, nil
}

func (s *memBackupStore) SaveSchedule(_ context.Context, sched backup.Schedule, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sched = &sched
	return nil
}

// backupTestDeps wires a real identity stack with a checkpoint service over a
// temporary directory, which is the closest a unit test gets to a mounted
// volume.
func backupTestDeps(t *testing.T) (Deps, *stubEngine, *memBackupStore) {
	t.Helper()
	d, _ := authTestDeps(t, "")
	eng := &stubEngine{name: backup.EngineNative, payload: []byte("agenthub checkpoint payload\n")}
	st := newMemBackupStore()
	d.Backups = backup.New(backup.Options{
		Dir:    t.TempDir(),
		Engine: eng,
		Store:  st,
		Log:    testLogger(),
	})
	return d, eng, st
}

// backupOwner logs the first account in, which becomes the owner and therefore
// holds every permission including backups:manage.
func backupOwner(t *testing.T, h http.Handler) string {
	t.Helper()
	do(t, h, http.MethodPost, "/api/auth/register", `{"email":"owner@example.com","password":"correct horse"}`, nil)
	return sessionOf(t, h, "owner@example.com", "correct horse")
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	decodeJSON(t, w, &body)
	return body.Code
}

// ---------------------------------------------------------------------------
// disabled state
// ---------------------------------------------------------------------------

// TestBackupsReportDisabledWhenNothingIsConfigured is the state most
// deployments are in: no AGENTHUB_BACKUP_DIR, so no service. The console has to
// be able to render the tab without an error.
func TestBackupsReportDisabledWhenNothingIsConfigured(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)
	token := backupOwner(t, h)

	w := do(t, h, http.MethodGet, "/api/backups", "", authHeader(token))
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want 200", w.Code)
	}
	var body backupOverview
	decodeJSON(t, w, &body)
	if body.Enabled {
		t.Fatal("enabled = true, want false when no backup directory is configured")
	}
	if body.Reason != "backups_disabled" {
		t.Fatalf("reason = %q, want backups_disabled", body.Reason)
	}
	if len(body.Checkpoints) != 0 {
		t.Fatalf("checkpoints = %d, want an empty list", len(body.Checkpoints))
	}

	// A mutating route must say it is disabled, not pretend to work.
	for _, probe := range []struct{ method, target, body string }{
		{http.MethodPost, "/api/backups", `{"label":"x"}`},
		{http.MethodGet, "/api/backups/schedule", ""},
		{http.MethodPut, "/api/backups/schedule", `{"mode":"off"}`},
		{http.MethodGet, "/api/backups/bkp-x", ""},
		{http.MethodDelete, "/api/backups/bkp-x", ""},
		{http.MethodGet, "/api/backups/bkp-x/download", ""},
		{http.MethodPost, "/api/backups/bkp-x/restore", `{"confirm":"bkp-x"}`},
	} {
		w := do(t, h, probe.method, probe.target, probe.body, authHeader(token))
		if w.Code != http.StatusConflict {
			t.Fatalf("%s %s: status = %d, want 409", probe.method, probe.target, w.Code)
		}
		if code := errCode(t, w); code != "backups_disabled" {
			t.Fatalf("%s %s: code = %q, want backups_disabled", probe.method, probe.target, code)
		}
	}
}

// ---------------------------------------------------------------------------
// permissions
// ---------------------------------------------------------------------------

// TestBackupRoutesNeedTheManagePermission pins the gate: taking a checkpoint
// and restoring the database are the same level of trust, so they share one
// permission — and it is not implied by any other.
func TestBackupRoutesNeedTheManagePermission(t *testing.T) {
	d, az := authTestDeps(t, "")
	eng := &stubEngine{name: backup.EngineNative}
	d.Backups = backup.New(backup.Options{
		Dir:    t.TempDir(),
		Engine: eng,
		Store:  newMemBackupStore(),
		Log:    testLogger(),
	})
	h := Register(d)
	owner := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/users", `{"email":"op@example.com","password":"correct horse","name":"Op"}`, authHeader(owner))
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeJSON(t, w, &created)
	op := sessionOf(t, h, "op@example.com", "correct horse")

	if w := do(t, h, http.MethodGet, "/api/backups", "", authHeader(op)); w.Code != http.StatusForbidden {
		t.Fatalf("without the permission: status = %d, want 403", w.Code)
	}
	// One unrelated permission is not the whole set.
	az.mu.Lock()
	az.roles[created.User.ID] = []string{"role-operator"}
	az.rolePerm["role-operator"] = []string{authz.PermUsersManage}
	az.mu.Unlock()
	if w := do(t, h, http.MethodPost, "/api/backups", `{"label":"x"}`, authHeader(op)); w.Code != http.StatusForbidden {
		t.Fatalf("users:manage must not imply backups:manage: status = %d, want 403", w.Code)
	}

	az.mu.Lock()
	az.rolePerm["role-operator"] = []string{authz.PermBackupsManage}
	az.mu.Unlock()
	if w := do(t, h, http.MethodGet, "/api/backups", "", authHeader(op)); w.Code != http.StatusOK {
		t.Fatalf("with backups:manage: status = %d, want 200", w.Code)
	}
}

// ---------------------------------------------------------------------------
// checkpoint lifecycle
// ---------------------------------------------------------------------------

// TestBackupCheckpointLifecycle walks the tab: take one, see it in the list,
// download it, delete it. The archive that comes out of a download is the file
// that was written, which is what an operator takes off the box.
func TestBackupCheckpointLifecycle(t *testing.T) {
	d, eng, _ := backupTestDeps(t)
	h := Register(d)
	token := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/backups", `{"label":"before the upgrade"}`, authHeader(token))
	if w.Code != http.StatusCreated {
		t.Fatalf("checkpoint: status = %d, body = %s", w.Code, w.Body.String())
	}
	var made struct {
		Checkpoint backupView `json:"checkpoint"`
	}
	decodeJSON(t, w, &made)
	id := made.Checkpoint.ID
	if id == "" || made.Checkpoint.Status != backup.StatusOK {
		t.Fatalf("checkpoint = %+v, want an ok row with an id", made.Checkpoint)
	}
	if made.Checkpoint.Kind != backup.KindManual || made.Checkpoint.Label != "before the upgrade" {
		t.Fatalf("kind/label = %q/%q, want manual and the label that was sent", made.Checkpoint.Kind, made.Checkpoint.Label)
	}
	// The manifest is the source of the numbers the table shows.
	if made.Checkpoint.RowCount != 4 || made.Checkpoint.TableCount != 2 {
		t.Fatalf("rows/tables = %d/%d, want 4/2", made.Checkpoint.RowCount, made.Checkpoint.TableCount)
	}

	w = do(t, h, http.MethodGet, "/api/backups", "", authHeader(token))
	var listed backupOverview
	decodeJSON(t, w, &listed)
	if !listed.Enabled {
		t.Fatal("enabled = false, want true once a directory is configured")
	}
	if listed.Engine != backup.EngineNative {
		t.Fatalf("engine = %q, want %q", listed.Engine, backup.EngineNative)
	}
	if len(listed.Checkpoints) != 1 || listed.Checkpoints[0].ID != id {
		t.Fatalf("checkpoints = %+v, want the one just taken", listed.Checkpoints)
	}

	// The download is the archive on disk, named after the checkpoint.
	w = do(t, h, http.MethodGet, "/api/backups/"+id+"/download", "", authHeader(token))
	if w.Code != http.StatusOK {
		t.Fatalf("download: status = %d, want 200", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=") || !strings.Contains(cd, ".tar.gz") {
		t.Fatalf("content-disposition = %q, want an attachment named after the archive", cd)
	}
	if w.Body.Len() == 0 {
		t.Fatal("download returned an empty archive")
	}

	if w := do(t, h, http.MethodDelete, "/api/backups/"+id, "", authHeader(token)); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", w.Code)
	}
	w = do(t, h, http.MethodGet, "/api/backups", "", authHeader(token))
	decodeJSON(t, w, &listed)
	if len(listed.Checkpoints) != 0 {
		t.Fatalf("checkpoints = %d, want 0 after the delete", len(listed.Checkpoints))
	}
	_ = eng
}

// TestBackupDownloadRefusesAnArchiveThatChanged is the reason the checksum is
// re-read on every download: a volume that rotted must not be handed to an
// operator as if it were a way back.
func TestBackupDownloadRefusesAnArchiveThatChanged(t *testing.T) {
	d, _, _ := backupTestDeps(t)
	h := Register(d)
	token := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/backups", "{}", authHeader(token))
	var made struct {
		Checkpoint backupView `json:"checkpoint"`
	}
	decodeJSON(t, w, &made)
	id := made.Checkpoint.ID

	// Find the archive the way an operator would not: through the directory.
	entries, err := os.ReadDir(d.Backups.Dir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive directory = %d entries, err = %v, want exactly one archive", len(entries), err)
	}
	if err := os.WriteFile(filepath.Join(d.Backups.Dir(), entries[0].Name()), []byte("something else entirely"), 0o600); err != nil {
		t.Fatalf("overwrite archive: %v", err)
	}

	w = do(t, h, http.MethodGet, "/api/backups/"+id+"/download", "", authHeader(token))
	if w.Code != http.StatusConflict {
		t.Fatalf("download: status = %d, want 409", w.Code)
	}
	if code := errCode(t, w); code != "backup_checksum_mismatch" {
		t.Fatalf("code = %q, want backup_checksum_mismatch", code)
	}
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

// TestBackupRestoreNeedsTheCheckpointIdEchoedBack covers the last guardrail: a
// restore is refused until the caller retypes the id, because the whole
// database is what is at stake.
func TestBackupRestoreNeedsTheCheckpointIdEchoedBack(t *testing.T) {
	d, eng, _ := backupTestDeps(t)
	h := Register(d)
	token := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/backups", "{}", authHeader(token))
	var made struct {
		Checkpoint backupView `json:"checkpoint"`
	}
	decodeJSON(t, w, &made)
	id := made.Checkpoint.ID

	w = do(t, h, http.MethodPost, "/api/backups/"+id+"/restore", `{}`, authHeader(token))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no confirmation: status = %d, want 400", w.Code)
	}
	if code := errCode(t, w); code != "restore_confirm_required" {
		t.Fatalf("code = %q, want restore_confirm_required", code)
	}
	if eng.restored != 0 {
		t.Fatalf("restored = %d, want 0 before the id is echoed back", eng.restored)
	}

	// A different id is not a confirmation either.
	w = do(t, h, http.MethodPost, "/api/backups/"+id+"/restore", `{"confirm":"bkp-something-else"}`, authHeader(token))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirmation: status = %d, want 400", w.Code)
	}

	// With the id echoed back the restore runs, and it leaves a rollback
	// checkpoint behind — the way back if this was the wrong row.
	w = do(t, h, http.MethodPost, "/api/backups/"+id+"/restore", `{"confirm":"`+id+`"}`, authHeader(token))
	if w.Code != http.StatusOK {
		t.Fatalf("restore: status = %d, body = %s", w.Code, w.Body.String())
	}
	if eng.restored != 1 {
		t.Fatalf("restored = %d, want 1", eng.restored)
	}
	var listed backupOverview
	decodeJSON(t, do(t, h, http.MethodGet, "/api/backups", "", authHeader(token)), &listed)
	var rollback int
	for _, c := range listed.Checkpoints {
		if c.Kind == backup.KindPreRestore {
			rollback++
		}
	}
	if rollback != 1 {
		t.Fatalf("pre-restore checkpoints = %d, want exactly one (%+v)", rollback, listed.Checkpoints)
	}
}

// TestBackupRestoreRefusesAnUnknownCheckpoint keeps the failure honest: a wrong
// id is a 404 with a stable code, not a 500 from an unhandled path.
func TestBackupRestoreRefusesAnUnknownCheckpoint(t *testing.T) {
	d, eng, _ := backupTestDeps(t)
	h := Register(d)
	token := backupOwner(t, h)

	w := do(t, h, http.MethodPost, "/api/backups/bkp-nope/restore", `{"confirm":"bkp-nope"}`, authHeader(token))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if code := errCode(t, w); code != "backup_not_found" {
		t.Fatalf("code = %q, want backup_not_found", code)
	}
	if eng.restored != 0 {
		t.Fatalf("restored = %d, want 0 for a checkpoint that does not exist", eng.restored)
	}
}

// ---------------------------------------------------------------------------
// schedule
// ---------------------------------------------------------------------------

// TestBackupScheduleIsValidatedBeforeItIsStored is the console-side guard: a
// policy that cannot run is refused at save time, because one that silently
// does nothing is worse than one the form would not accept.
func TestBackupScheduleIsValidatedBeforeItIsStored(t *testing.T) {
	d, _, _ := backupTestDeps(t)
	h := Register(d)
	token := backupOwner(t, h)

	// A mode nobody knows is refused, and the stored policy stays the default.
	w := do(t, h, http.MethodPut, "/api/backups/schedule", `{"mode":"hourly"}`, authHeader(token))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("save: status = %d, want 400", w.Code)
	}
	if code := errCode(t, w); code != "backup_schedule_invalid" {
		t.Fatalf("code = %q, want backup_schedule_invalid", code)
	}

	// So is a cron expression that cannot be parsed, and a daily time that is
	// not a time.
	for _, bad := range []string{
		`{"mode":"cron","cronExpr":"every morning"}`,
		`{"mode":"daily","dailyTime":"25:00"}`,
		`{"mode":"interval","intervalSec":10}`,
		`{"mode":"daily","dailyTime":"03:00","timezone":"Mars/Olympus"}`,
		`{"mode":"interval","intervalSec":3600,"keepLast":0}`,
	} {
		if w := do(t, h, http.MethodPut, "/api/backups/schedule", bad, authHeader(token)); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", bad, w.Code)
		}
	}

	w = do(t, h, http.MethodPut, "/api/backups/schedule",
		`{"mode":"daily","enabled":true,"dailyTime":"04:30","timezone":"UTC","keepLast":5,"maxAgeDays":14}`, authHeader(token))
	if w.Code != http.StatusOK {
		t.Fatalf("save: status = %d, body = %s", w.Code, w.Body.String())
	}
	var saved struct {
		Schedule schedulePayload `json:"schedule"`
	}
	decodeJSON(t, w, &saved)
	if saved.Schedule.Mode != backup.ModeDaily || saved.Schedule.DailyTime != "04:30" {
		t.Fatalf("schedule = %+v, want daily at 04:30", saved.Schedule)
	}
	if saved.Schedule.KeepLast != 5 || saved.Schedule.MaxAgeDays != 14 {
		t.Fatalf("retention = %+v, want 5 keep / 14 days", saved.Schedule)
	}
	// The interval travels as whole seconds, never as a Go duration.
	if saved.Schedule.IntervalSec != 86400 {
		t.Fatalf("intervalSec = %d, want 86400", saved.Schedule.IntervalSec)
	}
	if saved.Schedule.NextRun == nil {
		t.Fatal("nextRun is empty, want the next occurrence the console can show")
	}

	// And it comes back the same way.
	w = do(t, h, http.MethodGet, "/api/backups/schedule", "", authHeader(token))
	var loaded struct {
		Schedule schedulePayload `json:"schedule"`
	}
	decodeJSON(t, w, &loaded)
	if loaded.Schedule.Mode != backup.ModeDaily || loaded.Schedule.KeepLast != 5 {
		t.Fatalf("stored schedule = %+v, want the one that was saved", loaded.Schedule)
	}
}

// TestBackupScheduleOffReportsNoNextRun is what the console shows for the
// default instance: nothing is scheduled, and "never" must not be rendered as
// a broken date.
func TestBackupScheduleOffReportsNoNextRun(t *testing.T) {
	d, _, _ := backupTestDeps(t)
	h := Register(d)
	token := backupOwner(t, h)

	w := do(t, h, http.MethodGet, "/api/backups/schedule", "", authHeader(token))
	var loaded struct {
		Schedule schedulePayload `json:"schedule"`
	}
	decodeJSON(t, w, &loaded)
	if loaded.Schedule.Mode != backup.ModeOff || loaded.Schedule.Enabled {
		t.Fatalf("schedule = %+v, want the schedule off", loaded.Schedule)
	}
	if loaded.Schedule.NextRun != nil {
		t.Fatalf("nextRun = %v, want empty when nothing is scheduled", *loaded.Schedule.NextRun)
	}
}
