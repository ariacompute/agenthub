package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"agenthub/internal/authz"
	"agenthub/internal/channels"
	"agenthub/internal/computers"
	"agenthub/internal/router"
	"agenthub/internal/schedules"
)

// Cross-tenant access to the three per-owner surfaces.
//
// All three had the same defect: the list endpoint filtered by owner while the
// by-id endpoints looked the row up by id alone, so any signed in user could
// read, rewrite, fire or delete another account's row. The fix moved ownership
// into the store key, which means the shape to pin is: every verb is "not
// found" for a stranger, the row survives untouched, and the owner is not
// locked out.
//
// The harness middleware always installs the dev principal, so the ownership
// checks are reached by calling the handler directly with a stranger's identity
// — the same technique the computers cases already use.

func principalAs(userID string, perms ...string) *authz.Principal {
	held := map[string]bool{}
	for _, p := range perms {
		held[p] = true
	}
	return &authz.Principal{UserID: userID, Permissions: held}
}

func TestSchedulesAreNotReachableAcrossOwners(t *testing.T) {
	store := &memSchedules{}
	d := testDeps(t, openConfig(""))
	d.Schedules = store
	if _, err := store.Create(context.Background(), &schedules.Schedule{
		ID:      "sched-alice",
		Goal:    "ALICE_SECRET_GOAL",
		Trigger: string(schedules.TriggerManual),
		OwnerID: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	bob := principalAs("bob", authz.PermTasksRead, authz.PermTasksWrite)

	// Read: not found, and the goal never appears in the response.
	r := sandboxRequest(http.MethodGet, "/api/schedules/sched-alice", "", bob, "sched-alice")
	w := sandboxRecorder()
	d.handleGetSchedule(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger read: status = %d (%s)", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("ALICE_SECRET_GOAL")) {
		t.Fatalf("the goal leaked: %s", w.Body.String())
	}

	// Rewrite: not found.
	r = sandboxRequest(http.MethodPut, "/api/schedules/sched-alice", `{"goal":"PWNED_BY_BOB"}`, bob, "sched-alice")
	w = sandboxRecorder()
	d.handleUpdateSchedule(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger update: status = %d (%s)", w.Code, w.Body.String())
	}

	// Fire: not found, and no ticket is opened in alice's name.
	svc := routerServiceWith(t, store)
	d.Router = svc
	r = sandboxRequest(http.MethodPost, "/api/schedules/sched-alice/run", "", bob, "sched-alice")
	w = sandboxRecorder()
	d.handleRunSchedule(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger run: status = %d (%s)", w.Code, w.Body.String())
	}
	if trigs, err := svc.ListTriggers(context.Background(), router.TriggerQuery{}); err != nil || len(trigs) != 0 {
		t.Fatalf("a refused run must not enqueue a trigger, got %d (err=%v)", len(trigs), err)
	}

	// Delete: the row survives. A delete of a row the caller does not own is a
	// no-op, not a cross-tenant delete.
	r = sandboxRequest(http.MethodDelete, "/api/schedules/sched-alice", "", bob, "sched-alice")
	w = sandboxRecorder()
	d.handleDeleteSchedule(w, r)
	if list, _ := store.List(context.Background(), "alice"); len(list) != 1 {
		t.Fatalf("the schedule must survive a refused delete, %d left", len(list))
	}
	if list, _ := store.List(context.Background(), "alice"); list[0].Goal != "ALICE_SECRET_GOAL" {
		t.Fatalf("the goal was rewritten: %q", list[0].Goal)
	}

	// The owner is not locked out.
	alice := principalAs("alice", authz.PermTasksRead, authz.PermTasksWrite)
	r = sandboxRequest(http.MethodGet, "/api/schedules/sched-alice", "", alice, "sched-alice")
	w = sandboxRecorder()
	d.handleGetSchedule(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("owner read: status = %d (%s)", w.Code, w.Body.String())
	}
}

func TestChannelsAreNotReachableAcrossOwners(t *testing.T) {
	t.Setenv("TEST_OWNERSHIP_SECRET", "shh")
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	d.Cfg.ChannelsEnabled = true
	if _, err := fake.Create(context.Background(), &channels.Channel{
		ID:            "chan-alice",
		OwnerID:       "alice",
		Kind:          channels.KindSlack,
		Name:          "ALICE_SECRET_CHANNEL",
		Enabled:       true,
		SigningKeyEnv: "TEST_OWNERSHIP_SECRET",
		Config:        map[string]any{"target_url": "https://hooks.example.test/alice"},
	}); err != nil {
		t.Fatal(err)
	}
	bob := principalAs("bob", authz.PermTasksRead, authz.PermTasksWrite)

	r := sandboxRequest(http.MethodGet, "/api/channels/chan-alice", "", bob, "chan-alice")
	w := sandboxRecorder()
	d.handleGetChannel(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger read: status = %d (%s)", w.Code, w.Body.String())
	}

	// Rewriting a channel is the worst of the three: the platform sends with the
	// victim's signing key, so redirecting the target url turns their channel
	// into an exfiltration path.
	r = sandboxRequest(http.MethodPut, "/api/channels/chan-alice", `{"name":"PWNED","enabled":true,"config":{"target_url":"https://attacker.example.com/hook"}}`, bob, "chan-alice")
	w = sandboxRecorder()
	d.handleUpdateChannel(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger update: status = %d (%s)", w.Code, w.Body.String())
	}

	r = sandboxRequest(http.MethodDelete, "/api/channels/chan-alice", "", bob, "chan-alice")
	w = sandboxRecorder()
	d.handleDeleteChannel(w, r)
	kept, _ := fake.GetUnscoped(context.Background(), "chan-alice")
	if kept == nil {
		t.Fatal("the channel must survive a refused delete")
	}
	if kept.Name != "ALICE_SECRET_CHANNEL" {
		t.Fatalf("the channel was rewritten: %+v", kept)
	}
	if target, _ := kept.Config["target_url"].(string); target != "https://hooks.example.test/alice" {
		t.Fatalf("the delivery target was re-pointed: %q", target)
	}

	// The inbound webhook is the one deliberately unscoped read: the platform
	// signs the body, so the row must be readable before any session exists.
	srv := httptest.NewServer(Register(d))
	defer srv.Close()
	payload := `{"text":"hello","event_id":"evt-own-1"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if code := postSlackWebhook(t, srv, "chan-alice", payload, slackSig("shh", ts, []byte(payload)), ts); code != http.StatusOK {
		t.Fatalf("the webhook of another owner's channel = %d, want 200", code)
	}
	if msgs, _ := fake.ListDeliveries(context.Background(), "chan-alice", "alice"); len(msgs) != 1 {
		t.Fatalf("deliveries = %d, want the inbound message recorded", len(msgs))
	}
}

func TestComputersAreNotReachableAcrossOwners(t *testing.T) {
	store := &memComputers{}
	d, _ := sandboxDeps(t, nil)
	d.Computers = store
	if _, err := store.Create(context.Background(), &computers.Computer{
		ID:         "comp-alice",
		OwnerID:    "alice",
		Kind:       computers.KindShell,
		LastAction: "ALICE_SECRET_COMPUTER",
	}); err != nil {
		t.Fatal(err)
	}
	bob := principalAs("bob", authz.PermResourcesRead, authz.PermResourcesWrite)

	r := sandboxRequest(http.MethodGet, "/api/computers/comp-alice", "", bob, "comp-alice")
	w := sandboxRecorder()
	d.handleGetComputer(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger read: status = %d (%s)", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("ALICE_SECRET_COMPUTER")) {
		t.Fatalf("the card leaked: %s", w.Body.String())
	}

	r = sandboxRequest(http.MethodDelete, "/api/computers/comp-alice", "", bob, "comp-alice")
	w = sandboxRecorder()
	d.handleDeleteComputer(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("stranger delete: status = %d (%s)", w.Code, w.Body.String())
	}
	if list, _ := store.List(context.Background(), "alice"); len(list) != 1 {
		t.Fatalf("the card must survive a refused delete, %d left", len(list))
	}
}
