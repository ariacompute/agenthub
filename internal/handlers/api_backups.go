package handlers

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"agenthub/internal/backup"
	"agenthub/internal/httputil"
)

// The backup API is the console's "Backups" tab: the catalogue of checkpoints,
// the policy that takes them, and the one endpoint that rewinds the database.
//
// Every route is behind backups:manage, and every mutating route is a
// destructive operation on the whole database, so each of them is logged with
// who asked. The responses carry no prose: the console localises the status,
// kind and error codes itself.

// ---------------------------------------------------------------------------
// payloads
// ---------------------------------------------------------------------------

// backupView is one checkpoint as the console lists it. The archive path stays
// server side: the only way out is the download route, which re-checks the
// checksum before it hands a file over.
type backupView struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Engine     string     `json:"engine"`
	Status     string     `json:"status"`
	Label      string     `json:"label,omitempty"`
	SizeBytes  int64      `json:"sizeBytes"`
	RowCount   int64      `json:"rowCount"`
	TableCount int        `json:"tableCount"`
	DurationMS int64      `json:"durationMs"`
	Checksum   string     `json:"checksum"`
	Schema     string     `json:"schema"`
	ErrorCode  string     `json:"errorCode,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	RestoredAt *time.Time `json:"restoredAt,omitempty"`
	RestoredBy string     `json:"restoredBy,omitempty"`
}

func backupViewOf(c *backup.Checkpoint) backupView {
	return backupView{
		ID:         c.ID,
		Kind:       c.Kind,
		Engine:     c.Engine,
		Status:     c.Status,
		Label:      c.Label,
		SizeBytes:  c.SizeBytes,
		RowCount:   c.RowCount,
		TableCount: c.TableCount,
		DurationMS: c.DurationMS,
		Checksum:   shortChecksum(c.Checksum),
		Schema:     c.Schema,
		ErrorCode:  c.ErrorCode,
		CreatedAt:  c.CreatedAt,
		FinishedAt: c.FinishedAt,
		RestoredAt: c.RestoredAt,
		RestoredBy: c.RestoredBy,
	}
}

// shortChecksum trims the sha256 to what an operator can compare by eye. The
// full value stays on the server: it is what a restore verifies against.
func shortChecksum(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// backupOverview is the strip above the table: where archives go, what writes
// them, when the next one is due, and what the last one did. "reason" says why
// the feature is off, which is the only thing the console can show then.
type backupOverview struct {
	Enabled      bool         `json:"enabled"`
	Reason       string       `json:"reason,omitempty"`
	Dir          string       `json:"dir,omitempty"`
	Engine       string       `json:"engine,omitempty"`
	NextRun      *time.Time   `json:"nextRun,omitempty"`
	ActiveTasks  int          `json:"activeTasks"`
	Checkpoints  []backupView `json:"checkpoints"`
	Retention    int          `json:"keepLast"`
	MaxAgeDays   int          `json:"maxAgeDays"`
	ScheduleMode string       `json:"scheduleMode"`
}

// schedulePayload is the policy. The interval travels as whole seconds rather
// than a Go duration: a unit the console would have to guess is a bug waiting
// to happen, and backup.Schedule deliberately carries no JSON tags.
type schedulePayload struct {
	Mode        string     `json:"mode"`
	Enabled     bool       `json:"enabled"`
	IntervalSec int64      `json:"intervalSec"`
	DailyTime   string     `json:"dailyTime"`
	Timezone    string     `json:"timezone"`
	CronExpr    string     `json:"cronExpr"`
	KeepLast    int        `json:"keepLast"`
	MaxAgeDays  int        `json:"maxAgeDays"`
	NextRun     *time.Time `json:"nextRun,omitempty"`
}

// scheduleInput is what a save accepts: the same fields, minus the derived
// nextRun the console is only ever told.
type scheduleInput struct {
	Mode        string `json:"mode"`
	Enabled     bool   `json:"enabled"`
	IntervalSec int64  `json:"intervalSec"`
	DailyTime   string `json:"dailyTime"`
	Timezone    string `json:"timezone"`
	CronExpr    string `json:"cronExpr"`
	KeepLast    int    `json:"keepLast"`
	MaxAgeDays  int    `json:"maxAgeDays"`
}

func scheduleView(s backup.Schedule, next *time.Time) schedulePayload {
	return schedulePayload{
		Mode:        s.Mode,
		Enabled:     s.Enabled,
		IntervalSec: int64(s.Interval / time.Second),
		DailyTime:   s.DailyTime,
		Timezone:    s.Timezone,
		CronExpr:    s.CronExpr,
		KeepLast:    s.KeepLast,
		MaxAgeDays:  s.MaxAgeDays,
		NextRun:     next,
	}
}

func scheduleOf(in scheduleInput) backup.Schedule {
	def := backup.DefaultSchedule()
	// A form only sends the fields its mode shows. Filling the rest from the
	// defaults keeps the row complete, so switching mode later never starts
	// from an empty time or an unknown timezone.
	interval := time.Duration(in.IntervalSec) * time.Second
	if interval <= 0 {
		interval = def.Interval
	}
	if strings.TrimSpace(in.DailyTime) == "" {
		in.DailyTime = def.DailyTime
	}
	if strings.TrimSpace(in.Timezone) == "" {
		in.Timezone = def.Timezone
	}
	return backup.Schedule{
		Mode:       in.Mode,
		Enabled:    in.Enabled,
		Interval:   interval,
		DailyTime:  in.DailyTime,
		Timezone:   in.Timezone,
		CronExpr:   in.CronExpr,
		KeepLast:   in.KeepLast,
		MaxAgeDays: in.MaxAgeDays,
	}
}

// ---------------------------------------------------------------------------
// catalogue
// ---------------------------------------------------------------------------

// handleListBackups answers the whole tab in one payload: the overview strip
// and the catalogue.
//
// An instance without a backup directory answers 200 with enabled=false rather
// than an error: nothing is broken, the feature is simply not configured, and
// the console shows the reason instead of an empty table.
func (d Deps) handleListBackups(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteJSON(w, http.StatusOK, backupOverview{
			Enabled:     false,
			Reason:      string(httputil.CodeBackupsDisabled),
			Checkpoints: []backupView{},
		})
		return
	}
	rows, err := svc.List(r.Context())
	if err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	views := make([]backupView, 0, len(rows))
	for i := range rows {
		views = append(views, backupViewOf(&rows[i]))
	}
	sched, err := svc.Schedule(r.Context())
	if err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	out := backupOverview{
		Enabled:      true,
		Dir:          svc.Dir(),
		Engine:       svc.EngineName(),
		NextRun:      d.nextBackupRun(r),
		Checkpoints:  views,
		Retention:    sched.KeepLast,
		MaxAgeDays:   sched.MaxAgeDays,
		ScheduleMode: sched.Mode,
	}
	if quiesce, ok := svc.Quiesce(); ok {
		out.ActiveTasks = quiesce.ActiveTaskCount()
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// handleCreateBackup takes a checkpoint now. It is the same code path the
// scheduler uses, so a manual checkpoint proves the policy would have worked.
func (d Deps) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := decodeOptional(w, r, &body, 1<<10); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	label := strings.TrimSpace(body.Label)
	if len(label) > 120 {
		label = label[:120]
	}
	c, err := svc.Checkpoint(r.Context(), backup.KindManual, label, actorOf(r))
	if err != nil {
		d.Log.Warn("handler: checkpoint failed", "user", actorOf(r), "error", err)
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"checkpoint": backupViewOf(c)})
}

func (d Deps) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	c, err := svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"checkpoint": backupViewOf(c)})
}

// handleDownloadBackup streams one archive out. The path is re-verified against
// the checksum recorded when it was written: an operator must never be handed a
// file that silently rotted on disk.
func (d Deps) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	id := r.PathValue("id")
	path, err := svc.Path(r.Context(), id)
	if err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	// Quoted, because even a generated name is passed through a header.
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, path)
	d.Log.Info("handler: checkpoint downloaded", "checkpoint", id, "user", actorOf(r))
}

func (d Deps) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	id := r.PathValue("id")
	if err := svc.Delete(r.Context(), id); err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	d.Log.Info("handler: checkpoint deleted", "checkpoint", id, "user", actorOf(r))
	w.WriteHeader(http.StatusNoContent)
}

// handleRestoreBackup rewinds the database to a checkpoint. It is the only
// endpoint in agenthub that destroys data, which is why the caller has to echo
// the checkpoint id back and why every attempt is logged with who asked.
func (d Deps) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	// An empty body is not a decode error here: it is a restore nobody
	// confirmed, which is the answer the console must be able to show.
	if err := decodeOptional(w, r, &body, 1<<10); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	id := r.PathValue("id")
	actor := actorOf(r)
	d.Log.Info("handler: restore requested", "checkpoint", id, "user", actor)
	c, err := svc.Restore(r.Context(), id, body.Confirm, actor)
	if err != nil {
		d.Log.Error("handler: restore failed", "checkpoint", id, "user", actor, "error", err)
		writeBackupErr(w, httputil.CodeRestoreBusy, err)
		return
	}
	d.Log.Info("handler: restore finished", "checkpoint", id, "user", actor)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"checkpoint": backupViewOf(c)})
}

// ---------------------------------------------------------------------------
// schedule
// ---------------------------------------------------------------------------

func (d Deps) handleGetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	sched, err := svc.Schedule(r.Context())
	if err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"schedule": scheduleView(sched, d.nextBackupRun(r))})
}

func (d Deps) handleSaveBackupSchedule(w http.ResponseWriter, r *http.Request) {
	svc := d.Backups
	if svc == nil || !svc.Enabled() {
		httputil.WriteError(w, http.StatusConflict, httputil.CodeBackupsDisabled)
		return
	}
	var body scheduleInput
	if err := httputil.Decode(w, r, &body, 1<<10); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	saved, err := svc.SaveSchedule(r.Context(), scheduleOf(body), actorOf(r))
	if err != nil {
		writeBackupErr(w, httputil.CodeBackupInProgress, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"schedule": scheduleView(saved, d.nextBackupRun(r))})
}

// nextBackupRun is when the scheduler will next take a checkpoint, or nil when
// nothing is due — which is the normal state of an instance with the schedule
// off, not an error worth reporting.
func (d Deps) nextBackupRun(r *http.Request) *time.Time {
	if d.Backups == nil {
		return nil
	}
	next, err := d.Backups.NextRun(r.Context())
	if err != nil || next.IsZero() {
		return nil
	}
	return &next
}

// ---------------------------------------------------------------------------
// errors
// ---------------------------------------------------------------------------

// decodeOptional reads a body that may be absent. "Take a checkpoint now" has
// nothing to send, and a restore nobody confirmed must reach the confirmation
// check rather than the JSON decoder.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if r.ContentLength == 0 {
		return nil
	}
	return httputil.Decode(w, r, dst, maxBytes)
}

// writeBackupErr maps a backup error onto a machine readable code and lets
// httputil pick the status, so the code→status table stays in one place.
//
// busy is the code used when another operation holds the database: a checkpoint
// that cannot start and a restore that cannot start are different answers to
// the console even though they are the same error underneath.
func writeBackupErr(w http.ResponseWriter, busy httputil.Code, err error) {
	var coded *httputil.Error
	if errors.As(err, &coded) {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteErr(w, &httputil.Error{Code: backupCode(err, busy)})
}

func backupCode(err error, busy httputil.Code) httputil.Code {
	switch {
	case errors.Is(err, backup.ErrDisabled):
		return httputil.CodeBackupsDisabled
	case errors.Is(err, backup.ErrNotFound):
		return httputil.CodeBackupNotFound
	case errors.Is(err, backup.ErrBusy):
		return busy
	case errors.Is(err, backup.ErrRestoreConfirm):
		return httputil.CodeRestoreConfirm
	case errors.Is(err, backup.ErrSchemaMismatch):
		return httputil.CodeBackupSchemaMismatch
	case errors.Is(err, backup.ErrChecksumMismatch):
		return httputil.CodeBackupChecksum
	case errors.Is(err, backup.ErrArchive), errors.Is(err, backup.ErrUnknownTable):
		return httputil.CodeBackupInvalidArchive
	case errors.Is(err, backup.ErrEngineUnavailable):
		return httputil.CodeBackupEngineUnavail
	case errors.Is(err, backup.ErrEngineFailed):
		return httputil.CodeBackupEngineFailed
	case errors.Is(err, backup.ErrRestoreFailed):
		return httputil.CodeRestoreFailed
	case errors.Is(err, backup.ErrInvalidMode), errors.Is(err, backup.ErrInvalidTime),
		errors.Is(err, backup.ErrInterval), errors.Is(err, backup.ErrKeepLast),
		errors.Is(err, backup.ErrMaxAge), errors.Is(err, backup.ErrTimezone),
		errors.Is(err, backup.ErrCron), errors.Is(err, backup.ErrNoOccurrence):
		// A policy that cannot run is refused, never stored: one that silently
		// does nothing is worse than one the console would not accept.
		return httputil.CodeBackupScheduleInvalid
	default:
		return httputil.CodeBackupFailed
	}
}

// actorOf is who asked, for the audit trail of a destructive operation.
func actorOf(r *http.Request) string {
	if p := PrincipalFrom(r); p != nil {
		return p.UserID
	}
	return "anonymous"
}
