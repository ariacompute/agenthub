package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"agenthub/internal/httputil"
	"agenthub/internal/router"
)

// The router control plane. Every endpoint hangs off tasks:read / tasks:write, the
// same pair the schedule endpoints use: a scheduler is a way of running tasks, so
// it needs no permission of its own.

// routerReady short-circuits the endpoints when the router is not wired in.
func (d Deps) routerReady(w http.ResponseWriter) bool {
	if d.Router == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.CodeFeatureUnavailable)
		return false
	}
	return true
}

// writeRouterErr maps the router's sentinels onto status codes. Each code names the
// guardrail that stopped the call, so the console can localise precisely.
func writeRouterErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, router.ErrTicketNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeRouterTicketNotFound)
	case errors.Is(err, router.ErrThreadNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeRouterThreadNotFound)
	case errors.Is(err, router.ErrTicketClosed):
		httputil.WriteError(w, http.StatusConflict, httputil.CodeRouterTicketClosed)
	case errors.Is(err, router.ErrHandbackExhausted):
		httputil.WriteError(w, http.StatusConflict, httputil.CodeRouterHandbackExhausted)
	case errors.Is(err, router.ErrPolicyInvalid):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeRouterPolicyInvalid)
	case errors.Is(err, router.ErrResourceUnset):
		httputil.WriteError(w, http.StatusConflict, httputil.CodeRouterResourceUnset)
	default:
		httputil.WriteErr(w, err)
	}
}

// handleRouterStats is the water level: the queue, the tickets in hand, the last
// batch size and whether the beat is running.
func (d Deps) handleRouterStats(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	stats, err := d.Router.Stats(r.Context())
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, stats)
}

func (d Deps) handleListRouterTriggers(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	q := router.TriggerQuery{
		Source:      r.URL.Query().Get("source"),
		Disposition: r.URL.Query().Get("disposition"),
		ThreadID:    r.URL.Query().Get("thread"),
		TicketID:    r.URL.Query().Get("ticket"),
	}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	q.Normalize()
	list, err := d.Router.ListTriggers(r.Context(), q)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"triggers": list})
}

// routerTriggerRequest is a manual injection: the console's "run now" and a
// script's way in.
type routerTriggerRequest struct {
	Source   string         `json:"source"`
	SourceID string         `json:"sourceId,omitempty"`
	ThreadID string         `json:"threadId,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Body     string         `json:"body,omitempty"`
	Explicit bool           `json:"explicit,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

func (d Deps) handleCreateRouterTrigger(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	var req routerTriggerRequest
	if err := httputil.Decode(w, r, &req, 1<<20); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	source := req.Source
	if source == "" {
		source = string(router.SourceManual)
	}
	if router.NormalizeSource(source) == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeRouterPolicyInvalid)
		return
	}
	p := PrincipalFrom(r)
	trig := &router.Trigger{
		Source:    source,
		SourceID:  req.SourceID,
		ThreadID:  req.ThreadID,
		ActorID:   p.UserID,
		ActorName: p.Name,
		ActorKind: router.ActorHuman,
		Kind:      req.Kind,
		Body:      req.Body,
		Explicit:  true,
		Meta:      req.Meta,
	}
	created, fresh, err := d.Router.Inject(r.Context(), trig)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	if !fresh || created == nil {
		// A duplicate is not an error: the caller learns nothing was queued.
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"duplicate": true})
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, created)
}

func (d Deps) handleGetRouterTrigger(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	trig, err := d.Router.GetTrigger(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	if trig == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeRouterTriggerNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, trig)
}

func (d Deps) handleListRouterTickets(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	q := router.TicketQuery{State: r.URL.Query().Get("state")}
	q.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	q.Open = r.URL.Query().Get("open") == "true"
	q.Normalize()
	list, err := d.Router.ListTickets(r.Context(), q)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"tickets": list})
}

func (d Deps) handleGetRouterTicket(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	detail, err := d.Router.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, detail)
}

func (d Deps) handleCancelRouterTicket(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	if err := d.Router.CancelTicket(r.Context(), r.PathValue("id")); err != nil {
		writeRouterErr(w, err)
		return
	}
	detail, err := d.Router.GetTicket(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, detail)
}

// routerHandbackRequest is a worker saying "this trigger is not mine".
type routerHandbackRequest struct {
	TriggerID string `json:"triggerId"`
	Reason    string `json:"reason,omitempty"`
}

func (d Deps) handleHandbackRouterTrigger(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	var req routerHandbackRequest
	if err := httputil.Decode(w, r, &req, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	if req.TriggerID == "" {
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeRouterPolicyInvalid)
		return
	}
	if err := d.Router.Handback(r.Context(), r.PathValue("id"), req.TriggerID, req.Reason); err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"requeued": true})
}

// handleDropRouterSubscription unsubscribes every ticket from one thread.
func (d Deps) handleDropRouterSubscription(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	if err := d.Router.UnsubscribeThread(r.Context(), r.PathValue("id")); err != nil {
		writeRouterErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleListRouterPolicies(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	list, err := d.Router.Policies(r.Context())
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"policies": list})
}

// routerPolicyRequest is the editable half of a policy. Source comes from the path
// so a body can never retarget another source's rules.
type routerPolicyRequest struct {
	Mode       string   `json:"mode"`
	Allow      []string `json:"allow,omitempty"`
	Keywords   []string `json:"keywords,omitempty"`
	QuietStart *int     `json:"quietStart,omitempty"`
	QuietEnd   *int     `json:"quietEnd,omitempty"`
	Resource   string   `json:"resource,omitempty"`
}

func (d Deps) handleSaveRouterPolicy(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	var req routerPolicyRequest
	if err := httputil.Decode(w, r, &req, 1<<16); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	p := &router.Policy{
		Source:     r.PathValue("source"),
		Mode:       req.Mode,
		Allow:      req.Allow,
		Keywords:   req.Keywords,
		QuietStart: -1,
		QuietEnd:   -1,
		Resource:   req.Resource,
	}
	if p.Mode == "" {
		p.Mode = router.PolicyCandidate
	}
	if req.QuietStart != nil {
		p.QuietStart = *req.QuietStart
	}
	if req.QuietEnd != nil {
		p.QuietEnd = *req.QuietEnd
	}
	saved, err := d.Router.SavePolicy(r.Context(), p)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, saved)
}

// handleKickRouterPass runs one beat immediately instead of waiting for the tick.
func (d Deps) handleKickRouterPass(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	d.Router.Kick()
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"kicked": true})
}

// handleRouterEvents streams the committed domain events, replaying what the
// client missed first. Live and replay carry the same seq, so a reconnecting
// console never sees a gap or a duplicate.
func (d Deps) handleRouterEvents(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	flusher, ok := httputil.Stream(w)
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
		return
	}
	since := sinceSeq(r.URL.Query().Get("since"))
	subject := r.URL.Query().Get("subject")

	replayed, err := d.Router.EventsSince(r.Context(), subject, since, 200)
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	for _, ev := range replayed {
		if err := httputil.WriteSSE(w, flusher, "router_event", ev); err != nil {
			return
		}
		since = ev.Seq
	}

	ch, cancel := d.Router.Subscribe(subject, 64)
	defer cancel()
	go httputil.KeepAlive(r.Context(), w, flusher, 15*time.Second)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Seq <= since {
				continue
			}
			if err := httputil.WriteSSE(w, flusher, "router_event", ev); err != nil {
				return
			}
			since = ev.Seq
		}
	}
}

// handleRunSchedule fires a schedule now, whatever its expression says.
func (d Deps) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	if !d.routerReady(w) {
		return
	}
	trig, err := d.Router.RunSchedule(r.Context(), r.PathValue("id"), callerID(r))
	if err != nil {
		writeRouterErr(w, err)
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, trig)
}
