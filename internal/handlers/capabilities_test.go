package handlers

import (
	"net/http"
	"testing"

	"agenthub/internal/authz"
	"agenthub/internal/computers"
	"agenthub/internal/schedules"
)

// ---------------------------------------------------------------------------
// schedules: the proactive-goal surface
// ---------------------------------------------------------------------------

func scheduleHandler(t *testing.T, store schedules.Store) http.Handler {
	t.Helper()
	return agentcapHandler(t, &memComputers{}, store, &memKnowledge{})
}

func TestScheduleCreateListGet(t *testing.T) {
	h := scheduleHandler(t, &memSchedules{})

	// A goal is the whole point of a schedule, so an empty one is refused.
	w := do(t, h, http.MethodPost, "/api/schedules", `{"cronExpr":"0 9 * * *"}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create without goal: status = %d (%s)", w.Code, w.Body.String())
	}
	if code := errorCode(t, w); code != "schedule_invalid" {
		t.Fatalf("code = %q, want schedule_invalid", code)
	}

	// An unknown trigger degrades to manual rather than being stored verbatim.
	w = do(t, h, http.MethodPost, "/api/schedules", `{"goal":"weekly report","trigger":"nonsense","cronExpr":"0 9 * * 1"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d (%s)", w.Code, w.Body.String())
	}
	var created schedules.Schedule
	decodeJSON(t, w, &created)
	if created.ID == "" || created.Goal != "weekly report" {
		t.Fatalf("created = %+v", created)
	}
	if created.Trigger != string(schedules.TriggerManual) {
		t.Fatalf("trigger = %q, want manual", created.Trigger)
	}

	w = do(t, h, http.MethodGet, "/api/schedules", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status = %d", w.Code)
	}
	var list []*schedules.Schedule
	decodeJSON(t, w, &list)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	w = do(t, h, http.MethodGet, "/api/schedules/"+created.ID, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d", w.Code)
	}

	w = do(t, h, http.MethodGet, "/api/schedules/sched-missing", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get unknown: status = %d", w.Code)
	}
	if code := errorCode(t, w); code != "schedule_not_found" {
		t.Fatalf("code = %q, want schedule_not_found", code)
	}
}

func TestScheduleUpdateKeepsWhatTheRequestLeavesOut(t *testing.T) {
	store := &memSchedules{}
	h := scheduleHandler(t, store)

	w := do(t, h, http.MethodPost, "/api/schedules", `{"goal":"triage","trigger":"cron","cronExpr":"0 9 * * *","enabled":true}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d (%s)", w.Code, w.Body.String())
	}
	var created schedules.Schedule
	decodeJSON(t, w, &created)

	// Blank goal and an unknown trigger must leave the stored values alone; the
	// cron expression and the enabled flag are replaced wholesale.
	w = do(t, h, http.MethodPut, "/api/schedules/"+created.ID, `{"goal":"","trigger":"nonsense","cronExpr":"30 8 * * *","enabled":false}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d (%s)", w.Code, w.Body.String())
	}
	var updated schedules.Schedule
	decodeJSON(t, w, &updated)
	if updated.Goal != "triage" {
		t.Fatalf("goal = %q, want the stored one", updated.Goal)
	}
	if updated.Trigger != string(schedules.TriggerCron) {
		t.Fatalf("trigger = %q, want cron", updated.Trigger)
	}
	if updated.CronExpr != "30 8 * * *" || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}

	w = do(t, h, http.MethodPut, "/api/schedules/sched-missing", `{"goal":"x"}`, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("update unknown: status = %d", w.Code)
	}
}

func TestScheduleDelete(t *testing.T) {
	h := scheduleHandler(t, &memSchedules{})

	w := do(t, h, http.MethodPost, "/api/schedules", `{"goal":"cleanup"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d", w.Code)
	}
	var created schedules.Schedule
	decodeJSON(t, w, &created)

	w = do(t, h, http.MethodDelete, "/api/schedules/"+created.ID, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d (%s)", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodGet, "/api/schedules/"+created.ID, "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("the schedule must be gone, status = %d", w.Code)
	}
}

func TestScheduleDeleteHidesAnotherUsersSchedule(t *testing.T) {
	store := &memSchedules{}
	d := testDeps(t, openConfig(""))
	d.Schedules = store

	if _, err := store.Create(t.Context(), &schedules.Schedule{ID: "sched-owned", Goal: "theirs", OwnerID: "owner"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The handler is called directly because the harness middleware always
	// installs the dev principal: ownership can only be exercised here.
	r := sandboxRequest(http.MethodDelete, "/api/schedules/sched-owned", "", &authz.Principal{UserID: "stranger"}, "sched-owned")
	w := sandboxRecorder()
	d.handleDeleteSchedule(w, r)

	// A stranger is told the goal does not exist rather than that it is forbidden,
	// and the row stays exactly where it was.
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if code := errorCode(t, w); code != "schedule_not_found" {
		t.Fatalf("code = %q", code)
	}
	if list, _ := store.List(t.Context(), "owner"); len(list) != 1 {
		t.Fatalf("the schedule must survive a refused delete, %d left", len(list))
	}
}

func TestScheduleEndpointsReportTheFeatureAsMissing(t *testing.T) {
	// No store wired in: every schedule route says why, instead of pretending the
	// feature has no rows.
	h := scheduleHandler(t, nil)
	probes := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/api/schedules", ""},
		{http.MethodPost, "/api/schedules", `{"goal":"x"}`},
		{http.MethodGet, "/api/schedules/sched-1", ""},
		{http.MethodPut, "/api/schedules/sched-1", `{"goal":"x"}`},
		{http.MethodDelete, "/api/schedules/sched-1", ""},
	}
	for _, p := range probes {
		w := do(t, h, p.method, p.target, p.body, nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: status = %d", p.method, p.target, w.Code)
		}
		if code := errorCode(t, w); code != "feature_unavailable" {
			t.Fatalf("%s %s: code = %q", p.method, p.target, code)
		}
	}
}

// ---------------------------------------------------------------------------
// computers: deleting the card also closes the environment behind it
// ---------------------------------------------------------------------------

func TestDeleteComputerRemovesTheCard(t *testing.T) {
	store := &memComputers{}
	h := agentcapHandler(t, store, &memSchedules{}, &memKnowledge{})

	w := do(t, h, http.MethodPost, "/api/computers", `{"kind":"browser","agentId":"a1"}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d (%s)", w.Code, w.Body.String())
	}
	var created computers.Computer
	decodeJSON(t, w, &created)

	w = do(t, h, http.MethodDelete, "/api/computers/"+created.ID, "", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d (%s)", w.Code, w.Body.String())
	}
	list, err := store.List(t.Context(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("the card must be gone, %d left", len(list))
	}
}

func TestDeleteComputerAnswersNotFoundForAnUnknownID(t *testing.T) {
	h := agentcapHandler(t, &memComputers{}, &memSchedules{}, &memKnowledge{})
	w := do(t, h, http.MethodDelete, "/api/computers/comp-missing", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if code := errorCode(t, w); code != "computer_not_found" {
		t.Fatalf("code = %q", code)
	}
}

func TestDeleteComputerHidesAnotherUsersCard(t *testing.T) {
	store := &memComputers{}
	d := testDeps(t, openConfig(""))
	d.Computers = store

	if _, err := store.Create(t.Context(), &computers.Computer{ID: "comp-owned", OwnerID: "owner", Kind: computers.KindBrowser}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The handler is called directly because the harness middleware always
	// installs the dev principal: ownership can only be exercised here.
	r := sandboxRequest(http.MethodDelete, "/api/computers/comp-owned", "", &authz.Principal{UserID: "stranger"}, "comp-owned")
	w := sandboxRecorder()
	d.handleDeleteComputer(w, r)

	// A stranger is told the card does not exist rather than that it is forbidden:
	// the row must not leak through a 403.
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if code := errorCode(t, w); code != "computer_not_found" {
		t.Fatalf("code = %q", code)
	}
	if list, _ := store.List(t.Context(), ""); len(list) != 1 {
		t.Fatalf("the card must survive a refused delete, %d left", len(list))
	}
}

func TestDeleteComputerClosesTheLeaseAndSurvivesATerminateFailure(t *testing.T) {
	store := &memComputers{}
	d, _ := sandboxDeps(t, nil)
	d.Computers = store

	// The sweep may already have reclaimed the lease, so a failing Terminate is a
	// warning, not a reason to leave a card nobody can reach.
	if _, err := store.Create(t.Context(), &computers.Computer{
		ID: "comp-leased", OwnerID: "dev", Kind: computers.KindShell, LeaseID: "lease-gone",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := sandboxRequest(http.MethodDelete, "/api/computers/comp-leased", "", &authz.Principal{UserID: "dev"}, "comp-leased")
	w := sandboxRecorder()
	d.handleDeleteComputer(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if list, _ := store.List(t.Context(), ""); len(list) != 0 {
		t.Fatalf("the card must be gone, %d left", len(list))
	}
}

func TestDeleteComputerWithoutAStoreSaysWhy(t *testing.T) {
	d := testDeps(t, openConfig(""))
	d.Computers = nil
	r := sandboxRequest(http.MethodDelete, "/api/computers/comp-1", "", &authz.Principal{UserID: "dev"}, "comp-1")
	w := sandboxRecorder()
	d.handleDeleteComputer(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
