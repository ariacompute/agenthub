package handlers

import (
	"net/http"
	"strconv"
	"time"

	"agenthub/internal/audit"
	"agenthub/internal/httputil"
)

// handleListAudit returns the audit trail, newest first, with the summary the
// console shows above the table.
//
// Every filter is optional and they compose: an operator investigating one
// incident starts from a time window, then narrows by actor, action or outcome.
func (d Deps) handleListAudit(w http.ResponseWriter, r *http.Request) {
	if d.Audit == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	q, err := auditQueryFrom(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	events, err := d.Audit.List(r.Context(), q)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	stats, err := d.Audit.Stats(r.Context(), q)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	next := int64(0)
	if len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"stats":  stats,
		"next":   next,
	})
}

// handleGetAudit returns one event.
func (d Deps) handleGetAudit(w http.ResponseWriter, r *http.Request) {
	if d.Audit == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	ev, err := d.Audit.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if ev == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, ev)
}

// handleVerifyAudit recomputes the hash chain over a window and reports the first
// row that does not match.
func (d Deps) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	if d.Audit == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return
	}
	from, to, err := windowFrom(r)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeBadRequest)
		return
	}
	result, err := d.Audit.Verify(r.Context(), from, to, 0)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

// auditQueryFrom parses the listing filters.
func auditQueryFrom(r *http.Request) (audit.Query, error) {
	from, to, err := windowFrom(r)
	if err != nil {
		return audit.Query{}, err
	}
	q := audit.Query{
		From:       from,
		To:         to,
		ActorID:    r.URL.Query().Get("actor"),
		Action:     r.URL.Query().Get("action"),
		ResourceID: r.URL.Query().Get("resource"),
		Source:     r.URL.Query().Get("source"),
		Outcome:    r.URL.Query().Get("outcome"),
		Search:     r.URL.Query().Get("q"),
	}
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return audit.Query{}, err
		}
		q.Before = before
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return audit.Query{}, err
		}
		q.Limit = limit
	}
	return q, nil
}

// windowFrom reads the time range. Both ends are optional; a missing "from"
// means "the whole trail", which is what the verifier wants.
func windowFrom(r *http.Request) (time.Time, time.Time, error) {
	var from, to time.Time
	if raw := r.URL.Query().Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return from, to, err
		}
		from = t
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return from, to, err
		}
		to = t
	}
	return from, to, nil
}
