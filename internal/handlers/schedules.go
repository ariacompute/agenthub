package handlers

import (
	"net/http"

	"agenthub/internal/httputil"
	"agenthub/internal/schedules"
)

// schedulesReady short-circuits schedule endpoints when the store is absent.
func (d Deps) schedulesReady(w http.ResponseWriter) bool {
	if d.Schedules == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return false
	}
	return true
}

type scheduleRequest struct {
	Goal     string `json:"goal"`
	Trigger  string `json:"trigger"`
	CronExpr string `json:"cronExpr"`
	Enabled  bool   `json:"enabled"`
}

func (d Deps) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if !d.schedulesReady(w) {
		return
	}
	list, err := d.Schedules.List(r.Context(), PrincipalFrom(r).UserID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, list)
}

func (d Deps) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	if !d.schedulesReady(w) {
		return
	}
	var req scheduleRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if req.Goal == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeScheduleInvalid)
		return
	}
	trig := schedules.NormalizeTrigger(req.Trigger)
	if trig == "" {
		trig = schedules.TriggerManual
	}
	s := &schedules.Schedule{
		Goal:     req.Goal,
		Trigger:  string(trig),
		CronExpr: req.CronExpr,
		Enabled:  req.Enabled,
		OwnerID:  PrincipalFrom(r).UserID,
	}
	// TODO(real): a real scheduler would register s.CronExpr with APScheduler and
	// run the goal as a hub task. The first release only stores the intent.
	created, err := d.Schedules.Create(r.Context(), s)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, created)
}

func (d Deps) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	if !d.schedulesReady(w) {
		return
	}
	s, err := d.Schedules.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if s == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeScheduleNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, s)
}

func (d Deps) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if !d.schedulesReady(w) {
		return
	}
	s, err := d.Schedules.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if s == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeScheduleNotFound)
		return
	}
	var req scheduleRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if req.Goal != "" {
		s.Goal = req.Goal
	}
	if t := schedules.NormalizeTrigger(req.Trigger); t != "" {
		s.Trigger = string(t)
	}
	s.CronExpr = req.CronExpr
	s.Enabled = req.Enabled
	updated, err := d.Schedules.Update(r.Context(), s)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, updated)
}

func (d Deps) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if !d.schedulesReady(w) {
		return
	}
	if err := d.Schedules.Delete(r.Context(), r.PathValue("id")); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
