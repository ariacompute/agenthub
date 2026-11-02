package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/router"
	"agenthub/internal/schedules"
)

// routerService builds a scheduler on an in-memory store with the beat stopped:
// the tests drive EvaluateDue themselves, so nothing depends on wall clock timing.
func routerService(t *testing.T) *router.Service {
	t.Helper()
	return routerServiceWith(t, nil)
}

func routerServiceWith(t *testing.T, store schedules.Store) *router.Service {
	t.Helper()
	svc := router.NewService(router.ServiceOptions{
		Store:     router.NewMemoryStore(),
		Schedules: store,
		Interval:  time.Hour,
		Resource:  "agent-1",
	})
	t.Cleanup(svc.Stop)
	return svc
}

func routerHandler(t *testing.T, svc *router.Service, store schedules.Store) http.Handler {
	t.Helper()
	d := testDeps(t, openConfig(""))
	d.Router = svc
	d.Schedules = store
	return Register(d)
}

func TestRouterFeatureDisabled(t *testing.T) {
	// No service wired in: the endpoints report the feature as unavailable rather
	// than pretending the route does not exist.
	h := routerHandler(t, nil, nil)
	w := do(t, h, http.MethodGet, "/api/router/stats", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var body capErrBody
	decodeJSON(t, w, &body)
	if body.Code != "feature_unavailable" {
		t.Fatalf("code = %q, want feature_unavailable", body.Code)
	}
}

func TestRouterInjectsATriggerAndShowsIt(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	w := do(t, h, http.MethodPost, "/api/router/triggers",
		`{"source":"thread","threadId":"th-1","body":"fix the failing test","explicit":true}`, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("inject: status = %d (%s)", w.Code, w.Body.String())
	}
	var trig router.Trigger
	decodeJSON(t, w, &trig)
	if trig.ID == "" || trig.Source != "thread" || trig.ThreadID != "th-1" {
		t.Fatalf("trigger = %+v", trig)
	}
	if trig.ActorKind != router.ActorHuman {
		t.Fatalf("actorKind = %q, want human: the injection is attributed to the caller", trig.ActorKind)
	}

	w = do(t, h, http.MethodGet, "/api/router/triggers?source=thread", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d", w.Code)
	}
	var list struct {
		Triggers []*router.Trigger `json:"triggers"`
	}
	decodeJSON(t, w, &list)
	if len(list.Triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(list.Triggers))
	}

	if w := do(t, h, http.MethodGet, "/api/router/triggers/missing", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing trigger: status = %d, want 404", w.Code)
	}

	// An unknown source is rejected before it reaches the engine.
	if w := do(t, h, http.MethodPost, "/api/router/triggers", `{"source":"telepathy","body":"hi"}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad source: status = %d, want 400", w.Code)
	}
}

func TestRouterTicketEndpoints(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)
	ctx := context.Background()

	w := do(t, h, http.MethodPost, "/api/router/triggers", `{"source":"manual","body":"build the digest"}`, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("inject: status = %d", w.Code)
	}
	if _, err := svc.Engine().EvaluateDue(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	w = do(t, h, http.MethodGet, "/api/router/tickets", "", nil)
	var list struct {
		Tickets []*router.Ticket `json:"tickets"`
	}
	decodeJSON(t, w, &list)
	if len(list.Tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(list.Tickets))
	}
	ticket := list.Tickets[0]
	if ticket.State != router.TicketQueued || ticket.Resource != "agent-1" {
		t.Fatalf("ticket = %+v", ticket)
	}

	w = do(t, h, http.MethodGet, "/api/router/tickets/"+ticket.ID, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("detail: status = %d", w.Code)
	}
	var detail router.TicketDetail
	decodeJSON(t, w, &detail)
	if len(detail.Subscriptions) != 0 {
		t.Fatalf("a manual trigger binds no thread, got %d subscriptions", len(detail.Subscriptions))
	}
	if detail.Origin == nil || detail.Origin.ID != ticket.OriginTriggerID {
		t.Fatal("the detail does not carry the trigger that opened it")
	}

	cancel := do(t, h, http.MethodPost, "/api/router/tickets/"+ticket.ID+"/cancel", "", nil)
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel: status = %d (%s)", cancel.Code, cancel.Body.String())
	}
	var canceled router.TicketDetail
	decodeJSON(t, cancel, &canceled)
	if canceled.State != router.TicketCanceled {
		t.Fatalf("state = %q, want canceled", canceled.State)
	}
	// A closed ticket is immutable: a second command is a conflict, not a
	// silent success.
	if again := do(t, h, http.MethodPost, "/api/router/tickets/"+ticket.ID+"/cancel", "", nil); again.Code != http.StatusConflict {
		t.Fatalf("cancel twice: status = %d, want 409", again.Code)
	}
	if missing := do(t, h, http.MethodGet, "/api/router/tickets/missing", "", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("missing ticket: status = %d, want 404", missing.Code)
	}
}

func TestRouterHandbackIsAllowedOnce(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)
	ctx := context.Background()

	w := do(t, h, http.MethodPost, "/api/router/triggers", `{"source":"thread","threadId":"th-2","body":"do it","explicit":true}`, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("inject: status = %d", w.Code)
	}
	var trig router.Trigger
	decodeJSON(t, w, &trig)
	if _, err := svc.Engine().EvaluateDue(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	tickets, _ := svc.ListTickets(ctx, router.TicketQuery{})
	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
	path := "/api/router/tickets/" + tickets[0].ID + "/handback"

	if w := do(t, h, http.MethodPost, path, `{"triggerId":"`+trig.ID+`","reason":"not mine"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("handback: status = %d (%s)", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodPost, path, `{"triggerId":"`+trig.ID+`"}`, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("second handback: status = %d, want 409", w.Code)
	}
	var body capErrBody
	decodeJSON(t, w, &body)
	if body.Code != "router_handback_exhausted" {
		t.Fatalf("code = %q, want router_handback_exhausted", body.Code)
	}

	// A body without a trigger id is a validation error, not a 500.
	if w := do(t, h, http.MethodPost, path, `{}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("empty handback: status = %d, want 400", w.Code)
	}
}

func TestRouterPolicyEndpoints(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	w := do(t, h, http.MethodGet, "/api/router/policies", "", nil)
	var list struct {
		Policies []*router.Policy `json:"policies"`
	}
	decodeJSON(t, w, &list)
	if len(list.Policies) != len(router.Sources) {
		t.Fatalf("policies = %d, want the defaults for every source", len(list.Policies))
	}

	// A source that does not exist is refused: the path names the rule set.
	if w := do(t, h, http.MethodPut, "/api/router/policies/telepathy", `{"mode":"off"}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown source: status = %d, want 400", w.Code)
	}

	w = do(t, h, http.MethodPut, "/api/router/policies/thread",
		`{"mode":"request_only","keywords":["deploy"],"quietStart":1320,"quietEnd":420}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("save: status = %d (%s)", w.Code, w.Body.String())
	}
	var saved router.Policy
	decodeJSON(t, w, &saved)
	if saved.Mode != router.PolicyRequestOnly || len(saved.Keywords) != 1 {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.QuietStart != 1320 || saved.QuietEnd != 420 {
		t.Fatalf("quiet window = %d-%d", saved.QuietStart, saved.QuietEnd)
	}
}

func TestRouterRunScheduleNow(t *testing.T) {
	store := &memSchedules{}
	created, err := store.Create(context.Background(), &schedules.Schedule{
		Goal: "weekly rollup", Trigger: string(schedules.TriggerCron), CronExpr: "0 9 * * 1", Enabled: true,
		// The harness runs as the dev principal, and a manual firing is scoped to
		// the caller: a row without that owner is somebody else's goal.
		OwnerID: "dev",
	})
	if err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	svc := routerServiceWith(t, store)
	h := routerHandler(t, svc, store)

	w := do(t, h, http.MethodPost, "/api/schedules/"+created.ID+"/run", "", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("run: status = %d (%s)", w.Code, w.Body.String())
	}
	var trig router.Trigger
	decodeJSON(t, w, &trig)
	if trig.Source != string(router.SourceManual) || trig.SourceID != created.ID {
		t.Fatalf("trigger = %+v", trig)
	}
	if trig.Body != "weekly rollup" {
		t.Fatalf("body = %q, want the schedule goal", trig.Body)
	}

	if w := do(t, h, http.MethodPost, "/api/schedules/missing/run", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing schedule: status = %d, want 404", w.Code)
	}
}

func TestRouterPassKickAndStats(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	if w := do(t, h, http.MethodPost, "/api/router/passes", "", nil); w.Code != http.StatusAccepted {
		t.Fatalf("kick: status = %d", w.Code)
	}
	w := do(t, h, http.MethodGet, "/api/router/stats", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stats: status = %d", w.Code)
	}
	var stats router.Stats
	decodeJSON(t, w, &stats)
	if stats.Live {
		t.Fatal("the beat was not started, so Live must be false")
	}
	if stats.Pending != 0 {
		t.Fatalf("pending = %d, want 0", stats.Pending)
	}
}

func TestRouterStreamReplaysThenFollows(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	// One committed frame to replay, so the reconnect path has something to serve.
	if w := do(t, h, http.MethodPost, "/api/router/triggers", `{"body":"replay me"}`, nil); w.Code != http.StatusAccepted {
		t.Fatalf("inject: status = %d (%s)", w.Code, w.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/router/events?since=0", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()

	time.Sleep(50 * time.Millisecond)
	// A live frame published while the stream is open.
	if w2 := do(t, h, http.MethodPost, "/api/router/triggers", `{"body":"live one"}`, nil); w2.Code != http.StatusAccepted {
		t.Fatalf("inject: status = %d (%s)", w2.Code, w2.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("the stream must be SSE, got %q", got)
	}
	body := w.Body.String()
	if strings.Count(body, "event: router_event") < 2 {
		t.Fatalf("want the replayed frame and the live one, got %q", body)
	}
	if !strings.Contains(body, "trigger.received") {
		t.Fatalf("the intake frame must be streamed, got %q", body)
	}
}

func TestRouterStreamFiltersBySubject(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/router/events?subject=tkt-other", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()

	time.Sleep(50 * time.Millisecond)
	// This frame belongs to another subject and must not reach the stream.
	do(t, h, http.MethodPost, "/api/router/triggers", `{"body":"not yours"}`, nil)
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if body := w.Body.String(); strings.Contains(body, "trigger.received") {
		t.Fatalf("a subject filter must drop unrelated frames, got %q", body)
	}
}

func TestRouterDropsThreadSubscriptions(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	// A thread with no subscriptions is a no-op, not a 404: the console sends this
	// after a ticket closes and should not have to know whether one existed.
	w := do(t, h, http.MethodDelete, "/api/router/threads/thr-unknown/subscription", "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 carries no body, got %q", w.Body.String())
	}

	// Twice is also fine, for the same reason.
	if w := do(t, h, http.MethodDelete, "/api/router/threads/thr-unknown/subscription", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("second drop: status = %d", w.Code)
	}
}

func TestRouterErrorsCarryTheirOwnCode(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{
			name: "an unknown trigger", method: http.MethodGet, path: "/api/router/triggers/trg-missing",
			status: http.StatusNotFound, code: "router_trigger_not_found",
		},
		{
			name: "an unknown ticket", method: http.MethodPost, path: "/api/router/tickets/tkt-missing/cancel",
			status: http.StatusNotFound, code: "router_ticket_not_found",
		},
		{
			name: "a handback without a trigger", method: http.MethodPost, path: "/api/router/tickets/tkt-1/handback",
			body: `{"reason":"no trigger id"}`, status: http.StatusBadRequest, code: "router_policy_invalid",
		},
		{
			name: "an injected trigger from an unknown source", method: http.MethodPost, path: "/api/router/triggers",
			body: `{"source":"carrier-pigeon","body":"hi"}`, status: http.StatusBadRequest, code: "router_policy_invalid",
		},
		{
			name: "a policy with an unknown mode", method: http.MethodPut, path: "/api/router/policies/channel",
			body: `{"mode":"sometimes"}`, status: http.StatusBadRequest, code: "router_policy_invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, tc.path, tc.body, nil)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.status, w.Body.String())
			}
			var body capErrBody
			decodeJSON(t, w, &body)
			if body.Code != tc.code {
				t.Fatalf("code = %q, want %q", body.Code, tc.code)
			}
		})
	}
}

func TestRouterDuplicateInjectionIsNotAnError(t *testing.T) {
	svc := routerService(t)
	h := routerHandler(t, svc, nil)

	first := do(t, h, http.MethodPost, "/api/router/triggers", `{"body":"once","sourceId":"src-1"}`, nil)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first inject: %d (%s)", first.Code, first.Body.String())
	}
	// The same source, body and minute dedupe to one trigger; the console learns it
	// was a redelivery from the body rather than from an error status.
	second := do(t, h, http.MethodPost, "/api/router/triggers", `{"body":"once","sourceId":"src-1"}`, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second inject: %d (%s)", second.Code, second.Body.String())
	}
	var body map[string]any
	decodeJSON(t, second, &body)
	if body["duplicate"] != true {
		t.Fatalf("want a duplicate flag, got %+v", body)
	}
}
