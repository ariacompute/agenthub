package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"agenthub/internal/auth"
	"agenthub/internal/channels"
	"agenthub/internal/httputil"
	"agenthub/internal/registry"
	"agenthub/internal/router"
	"agenthub/internal/threads"
)

// ---------------------------------------------------------------------------
// delivery receipts
// ---------------------------------------------------------------------------

func TestListDeliveriesReturnsWhatTheChannelRecorded(t *testing.T) {
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	if _, err := fake.Create(context.Background(), &channels.Channel{
		ID: "c1", OwnerID: "dev", Kind: channels.KindSlack, Name: "sl", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.RecordDelivery(context.Background(), &channels.ChannelMessage{
		ChannelID: "c1", ExternalRef: "out-1", Direction: "outbound", Status: channels.StatusFailed,
	}); err != nil {
		t.Fatal(err)
	}
	// A channel that never sent anything, and somebody else's channel that has:
	// the two cases the endpoint used to confuse.
	for _, c := range []*channels.Channel{
		{ID: "c-empty", OwnerID: "dev", Kind: channels.KindSlack, Name: "empty", Enabled: true},
		{ID: "c-theirs", OwnerID: "someone-else", Kind: channels.KindSlack, Name: "theirs", Enabled: true},
	} {
		if _, err := fake.Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fake.RecordDelivery(context.Background(), &channels.ChannelMessage{
		ChannelID: "c-theirs", ExternalRef: "out-9", Direction: "outbound",
		Status: channels.StatusFailed, Payload: map[string]any{"text": "THEIR_SECRET"},
	}); err != nil {
		t.Fatal(err)
	}

	code, raw := doReq(t, srv, "GET", "/api/channels/c1/deliveries", "")
	if code != http.StatusOK {
		t.Fatalf("deliveries = %d (%s)", code, raw)
	}
	var list []*channels.ChannelMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if len(list) != 1 || list[0].ExternalRef != "out-1" {
		t.Fatalf("list = %+v", list)
	}

	// A channel that never sent anything answers with an empty list, not a 404.
	code, raw = doReq(t, srv, "GET", "/api/channels/c-empty/deliveries", "")
	if code != http.StatusOK {
		t.Fatalf("empty deliveries = %d (%s)", code, raw)
	}

	// Somebody else's channel is "not found" — not an empty list, and never the
	// message bodies and provider error text the receipts carry.
	for _, id := range []string{"c-theirs", "nope"} {
		code, raw = doReq(t, srv, "GET", "/api/channels/"+id+"/deliveries", "")
		if code != http.StatusNotFound {
			t.Fatalf("deliveries of %q = %d (%s), want 404", id, code, raw)
		}
		if bytes.Contains(raw, []byte("THEIR_SECRET")) {
			t.Fatalf("the receipt body of %q leaked: %s", id, raw)
		}
	}
}

// itoa is the tiny integer formatter the webhook parser uses to build a
// Telegram idempotency key. It exists because strconv was the only reason
// this file needed an import.
func TestItoa(t *testing.T) {
	cases := map[int]string{
		0:           "0",
		7:           "7",
		42:          "42",
		-7:          "-7",
		1234567890:  "1234567890",
		-2147483648: "-2147483648",
	}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
	// Parity with the standard library is the whole contract.
	for _, n := range []int{0, 1, -1, 99, -99, 1000, -1000} {
		if got, want := itoa(n), strconv.Itoa(n); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// delegateThread: a channel message reaching the agents in a thread
// ---------------------------------------------------------------------------

// threadWithParticipants seeds a platform thread: GetThread matches on the
// owner, and the channel path asks with none.
func threadWithParticipants(t *testing.T, mt *memThreads, id string, participants ...string) {
	t.Helper()
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.threads[id] = &threads.Thread{ID: id, Title: "t", Participants: participants}
}

func TestDelegateThreadLeavesAMissingThreadAlone(t *testing.T) {
	d, _ := newDeps(t)
	d.Threads = newMemThreads()

	// Nothing to delegate to: the call must return without touching the hub.
	d.delegateThread(httptest.NewRequest(http.MethodPost, "/", nil), "thread-missing", "hello")
	if tasks := d.Hub.Store().List(context.Background()); len(tasks) != 0 {
		t.Fatalf("a missing thread must open no task, got %d", len(tasks))
	}
}

func TestDelegateThreadHandsTheMessageToTheScheduler(t *testing.T) {
	mt := newMemThreads()
	threadWithParticipants(t, mt, "th-1", "mcp1")

	d, _ := newDeps(t)
	d.Threads = mt
	d.Router = routerService(t)

	// AddMessage runs before delegation in the webhook path; here the thread is
	// seeded directly, so the delegation is what is under test.
	d.delegateThread(httptest.NewRequest(http.MethodPost, "/", nil), "th-1", "@mcp1 please look")

	triggers, err := d.Router.ListTriggers(context.Background(), router.TriggerQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	trig := triggers[0]
	if trig.ThreadID != "th-1" || trig.Source != "channel" {
		t.Fatalf("trigger = %+v", trig)
	}
	if !trig.Explicit {
		t.Fatal("an @mention must mark the trigger explicit")
	}
	// The first participant is offered as the worker, so a thread full of agents
	// does not open one ticket per agent.
	if got, _ := trig.Meta["resource"].(string); got != "mcp1" {
		t.Fatalf("meta resource = %v, want mcp1", trig.Meta["resource"])
	}
}

func TestDelegateThreadFansOutToEveryParticipantWithoutAScheduler(t *testing.T) {
	// A participant is only woken when it is a registered, published resource,
	// and the thread names resources by id.
	entry := &registry.Entry{Kind: registry.KindMCP, Name: "mcp1", Endpoint: "stdio://mcp1"}
	d := testDeps(t, openConfig(""), entry)
	mt := newMemThreads()
	threadWithParticipants(t, mt, "th-2", entry.ID)
	d.Threads = mt
	d.Router = nil

	d.delegateThread(httptest.NewRequest(http.MethodPost, "/", nil), "th-2", "status please")

	tasks := d.Hub.Store().List(context.Background())
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want one per participant", len(tasks))
	}
	if tasks[0].ID == "" {
		t.Fatalf("the delegated task must have an id: %+v", tasks[0])
	}
}

func TestDelegateThreadHonoursTheMentionFilterWithoutAScheduler(t *testing.T) {
	entry := &registry.Entry{Kind: registry.KindMCP, Name: "mcp1", Endpoint: "stdio://mcp1"}
	d := testDeps(t, openConfig(""), entry)
	mt := newMemThreads()
	// Two participants, only one named: the other must not be woken.
	threadWithParticipants(t, mt, "th-3", entry.ID, "agent-unregistered")
	d.Threads = mt

	d.delegateThread(httptest.NewRequest(http.MethodPost, "/", nil), "th-3", "@"+entry.ID+" only you")

	if tasks := d.Hub.Store().List(context.Background()); len(tasks) != 1 {
		t.Fatalf("tasks = %d, want only the mentioned participant", len(tasks))
	}
}

// ---------------------------------------------------------------------------
// the OAuth redirect vocabulary
// ---------------------------------------------------------------------------

func TestRedirectWithCodeSendsTheBrowserToLogin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/github/callback?code=x", nil)
	redirectWithCode(w, r, string(httputil.CodeOAuthFailed))

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	want := "/login?error=" + string(httputil.CodeOAuthFailed)
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAuthCodeOfMapsEveryOAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"not configured", auth.ErrOAuthNotConfigured, string(httputil.CodeOAuthNotConfigured)},
		{"state mismatch", auth.ErrOAuthStateMismatch, string(httputil.CodeOAuthStateMismatch)},
		{"no email", auth.ErrOAuthNoEmail, string(httputil.CodeOAuthNoEmail)},
		{"user disabled", auth.ErrUserDisabled, string(httputil.CodeUserDisabled)},
		{"anything else", errors.New("provider exploded"), string(httputil.CodeOAuthFailed)},
		{"wrapped", wrappedOAuthErr{}, string(httputil.CodeOAuthFailed)},
	}
	for _, tc := range cases {
		if got := authCodeOf(tc.err); got != tc.want {
			t.Fatalf("%s: authCodeOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// wrappedOAuthErr is a stand-in for the transport errors the callback sees.
type wrappedOAuthErr struct{}

func (wrappedOAuthErr) Error() string { return "oauth: transport closed" }
