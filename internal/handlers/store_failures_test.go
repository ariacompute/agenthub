package handlers

// What a handler does when the database is not answering.
//
// Every surface in this package delegates to a Store port, and each one reports
// a failed call through `httputil.WriteErr`. That mapping is the only thing
// standing between a dropped connection and a page that renders nothing: if a
// handler swallowed the error and wrote an empty list, the console would look
// healthy while the whole feature was down, and nobody would page.
//
// The stores below wrap the in-memory fakes and fail every method, so the
// assertions are about the answer rather than about which call failed: a 5xx
// with a machine-readable code, never a 200, never a panic. Cases that cannot be
// driven this way — the services that are concrete types (router, sandbox,
// backups) — are listed in task.md instead of being faked past.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"agenthub/internal/channels"
	"agenthub/internal/computers"
	"agenthub/internal/knowledge"
	"agenthub/internal/schedules"
	"agenthub/internal/threads"
)

// errStoreDown is what a repository returns when the connection is gone; the
// handlers must not care which error it is, only that it is an error.
var errStoreDown = errors.New("store is not answering")

// ---------------------------------------------------------------------------
// failing stores: embed the working fake, override what the handler calls
// ---------------------------------------------------------------------------

type failingThreads struct{ *memThreads }

func (failingThreads) ListThreads(context.Context, string) ([]*threads.Thread, error) {
	return nil, errStoreDown
}
func (failingThreads) CreateThread(context.Context, *threads.Thread) (*threads.Thread, error) {
	return nil, errStoreDown
}
func (failingThreads) GetThread(context.Context, string, string) (*threads.Thread, error) {
	return nil, errStoreDown
}
func (failingThreads) SetParticipants(context.Context, string, []string) error { return errStoreDown }
func (failingThreads) AddMessage(context.Context, *threads.Message) (*threads.Message, error) {
	return nil, errStoreDown
}
func (failingThreads) ListMessages(context.Context, string, int64) ([]*threads.Message, error) {
	return nil, errStoreDown
}
func (failingThreads) UpsertAgentMessage(context.Context, string, string, string, string) (*threads.Message, error) {
	return nil, errStoreDown
}

type failingChannels struct{ *fakeChannels }

func (failingChannels) List(context.Context, string) ([]*channels.Channel, error) {
	return nil, errStoreDown
}
func (failingChannels) Get(context.Context, string, string) (*channels.Channel, error) {
	return nil, errStoreDown
}
func (failingChannels) GetUnscoped(context.Context, string) (*channels.Channel, error) {
	return nil, errStoreDown
}
func (failingChannels) Create(context.Context, *channels.Channel) (*channels.Channel, error) {
	return nil, errStoreDown
}
func (failingChannels) Update(context.Context, *channels.Channel, string) (*channels.Channel, error) {
	return nil, errStoreDown
}
func (failingChannels) Delete(context.Context, string, string) error { return errStoreDown }
func (failingChannels) RecordDelivery(context.Context, *channels.ChannelMessage) (bool, error) {
	return false, errStoreDown
}
func (failingChannels) ListDeliveries(context.Context, string) ([]*channels.ChannelMessage, error) {
	return nil, errStoreDown
}
func (failingChannels) GetDelivery(context.Context, string, string) (*channels.ChannelMessage, error) {
	return nil, errStoreDown
}
func (failingChannels) UpdateDelivery(context.Context, *channels.ChannelMessage) (bool, error) {
	return false, errStoreDown
}

type failingComputers struct{ *memComputers }

func (failingComputers) List(context.Context, string) ([]*computers.Computer, error) {
	return nil, errStoreDown
}
func (failingComputers) Get(context.Context, string, string) (*computers.Computer, error) {
	return nil, errStoreDown
}
func (failingComputers) Create(context.Context, *computers.Computer) (*computers.Computer, error) {
	return nil, errStoreDown
}
func (failingComputers) Delete(context.Context, string, string) error { return errStoreDown }

type failingSchedules struct{ *memSchedules }

func (failingSchedules) List(context.Context, string) ([]*schedules.Schedule, error) {
	return nil, errStoreDown
}
func (failingSchedules) Get(context.Context, string, string) (*schedules.Schedule, error) {
	return nil, errStoreDown
}
func (failingSchedules) Create(context.Context, *schedules.Schedule) (*schedules.Schedule, error) {
	return nil, errStoreDown
}
func (failingSchedules) Update(context.Context, *schedules.Schedule, string) (*schedules.Schedule, error) {
	return nil, errStoreDown
}
func (failingSchedules) Delete(context.Context, string, string) error { return errStoreDown }

type failingKnowledge struct{ *memKnowledge }

func (failingKnowledge) List(context.Context, knowledge.Query) ([]*knowledge.Knowledge, error) {
	return nil, errStoreDown
}

// ---------------------------------------------------------------------------
// the matrix
// ---------------------------------------------------------------------------

// wireBrokenChannels enables the feature (the handlers answer 503 when it is
// off, which would hide the store call) and then breaks the store behind it.
func wireBrokenChannels(d *Deps) {
	d.Cfg.ChannelsEnabled = true
	d.Channels = failingChannels{newFakeChannels()}
}

// TestAStoreFailureIsReportedNotSwallowed walks every CRUD route on the five
// persisted surfaces with a store that fails every call. A handler that logged
// the error and wrote an empty collection would answer 200 here, which is
// exactly the bug this test exists to prevent: the console would show "nothing
// here" for a feature that is down.
func TestAStoreFailureIsReportedNotSwallowed(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		wire   func(d *Deps)
	}{
		{"threads list", http.MethodGet, "/api/threads", "", func(d *Deps) { d.Threads = failingThreads{newMemThreads()} }},
		{"threads create", http.MethodPost, "/api/threads", `{"title":"t"}`, func(d *Deps) { d.Threads = failingThreads{newMemThreads()} }},
		{"threads get", http.MethodGet, "/api/threads/thread-1", "", func(d *Deps) { d.Threads = failingThreads{newMemThreads()} }},
		{"threads post", http.MethodPost, "/api/threads/thread-1/messages", `{"body":"hi"}`, func(d *Deps) { d.Threads = failingThreads{newMemThreads()} }},

		{"channels list", http.MethodGet, "/api/channels", "", wireBrokenChannels},
		{"channels get", http.MethodGet, "/api/channels/ch-1", "", wireBrokenChannels},
		{"channels create", http.MethodPost, "/api/channels", `{"kind":"slack","name":"ops","signingKeyEnv":"SLACK_SIGNING_KEY","config":{"webhookUrl":"https://example.test/hook"}}`, wireBrokenChannels},
		{"channels update", http.MethodPut, "/api/channels/ch-1", `{"name":"ops"}`, wireBrokenChannels},
		{"channels delete", http.MethodDelete, "/api/channels/ch-1", "", wireBrokenChannels},
		{"channels deliveries", http.MethodGet, "/api/channels/ch-1/deliveries", "", wireBrokenChannels},
		{"channels deliver", http.MethodPost, "/api/channels/ch-1/deliver", `{"threadId":"thread-1","text":"hi"}`, wireBrokenChannels},
		{"channels retry", http.MethodPost, "/api/channels/ch-1/deliveries/d-1/retry", "", wireBrokenChannels},

		{"computers list", http.MethodGet, "/api/computers", "", func(d *Deps) { d.Computers = failingComputers{&memComputers{}} }},
		{"computers get", http.MethodGet, "/api/computers/comp-1", "", func(d *Deps) { d.Computers = failingComputers{&memComputers{}} }},
		{"computers create", http.MethodPost, "/api/computers", `{"kind":"browser","agentId":"a1"}`, func(d *Deps) { d.Computers = failingComputers{&memComputers{}} }},
		{"computers delete", http.MethodDelete, "/api/computers/comp-1", "", func(d *Deps) { d.Computers = failingComputers{&memComputers{}} }},

		{"schedules list", http.MethodGet, "/api/schedules", "", func(d *Deps) { d.Schedules = failingSchedules{&memSchedules{}} }},
		{"schedules get", http.MethodGet, "/api/schedules/s-1", "", func(d *Deps) { d.Schedules = failingSchedules{&memSchedules{}} }},
		{"schedules create", http.MethodPost, "/api/schedules", `{"goal":"weekly tidy"}`, func(d *Deps) { d.Schedules = failingSchedules{&memSchedules{}} }},
		{"schedules update", http.MethodPut, "/api/schedules/s-1", `{"goal":"weekly tidy"}`, func(d *Deps) { d.Schedules = failingSchedules{&memSchedules{}} }},
		{"schedules delete", http.MethodDelete, "/api/schedules/s-1", "", func(d *Deps) { d.Schedules = failingSchedules{&memSchedules{}} }},

		{"knowledge list", http.MethodGet, "/api/knowledge", "", func(d *Deps) { d.Knowledge = failingKnowledge{&memKnowledge{}} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testDeps(t, openConfig(""))
			tc.wire(&d)
			h := Register(d)

			w := do(t, h, tc.method, tc.path, tc.body, nil)
			if w.Code < http.StatusInternalServerError {
				t.Fatalf("%s %s: status = %d for a failing store, want 5xx (body %s)",
					tc.method, tc.path, w.Code, w.Body.String())
			}
			if code := errCode(t, w); code == "" {
				t.Fatalf("%s %s: a 5xx must carry a machine code, body = %s",
					tc.method, tc.path, w.Body.String())
			}
			// The body must not describe a successful empty write: the list routes
			// would otherwise look like "you have no threads".
			if w.Code == http.StatusOK {
				t.Fatalf("%s %s: a failed read answered 200", tc.method, tc.path)
			}
		})
	}
}

// Deleting a row that is already gone is a success, not a 404: a console that
// retries after a dropped response must not be told it made a mistake. Schedules
// and channels both answer 204 — reading a missing id is a 404, deleting it is
// not, and the difference is deliberate.
func TestDeletingAMissingRowIsStillASuccess(t *testing.T) {
	cases := []struct {
		name string
		path string
		wire func(d *Deps)
	}{
		{"schedule", "/api/schedules/nope", func(d *Deps) { d.Schedules = &memSchedules{} }},
		{"channel", "/api/channels/nope", func(d *Deps) {
			d.Cfg.ChannelsEnabled = true
			d.Channels = newFakeChannels()
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testDeps(t, openConfig(""))
			tc.wire(&d)
			h := Register(d)

			w := do(t, h, http.MethodDelete, tc.path, "", nil)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204 (body %s)", w.Code, w.Body.String())
			}
		})
	}
}

// TestAnUnknownIdIsANotFoundForEverySurface keeps the other half of the contract
// straight: an id that does not exist is a 404 with the surface's own code, not
// a 500 and not an empty 200. The console switches on these codes to decide
// whether to say "gone" or "cannot reach the server".
func TestAnUnknownIdIsANotFoundForEverySurface(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		code   string
		wire   func(d *Deps)
	}{
		{"computer", http.MethodGet, "/api/computers/nope", "", "computer_not_found",
			func(d *Deps) { d.Computers = &memComputers{} }},
		{"computer delete", http.MethodDelete, "/api/computers/nope", "", "computer_not_found",
			func(d *Deps) { d.Computers = &memComputers{} }},
		{"schedule", http.MethodGet, "/api/schedules/nope", "", "schedule_not_found",
			func(d *Deps) { d.Schedules = &memSchedules{} }},
		{"channel", http.MethodGet, "/api/channels/nope", "", "channel_not_found",
			func(d *Deps) { d.Cfg.ChannelsEnabled = true; d.Channels = newFakeChannels() }},
		{"channel deliveries", http.MethodGet, "/api/channels/nope/deliveries", "", "channel_not_found",
			func(d *Deps) { d.Cfg.ChannelsEnabled = true; d.Channels = newFakeChannels() }},
		{"thread", http.MethodGet, "/api/threads/nope", "", "not_found",
			func(d *Deps) { d.Threads = newMemThreads() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testDeps(t, openConfig(""))
			tc.wire(&d)
			h := Register(d)

			w := do(t, h, tc.method, tc.path, tc.body, nil)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: status = %d, want 404 (body %s)",
					tc.method, tc.path, w.Code, w.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			decodeJSON(t, w, &body)
			if body.Code != tc.code {
				t.Fatalf("%s %s: code = %q, want %q", tc.method, tc.path, body.Code, tc.code)
			}
		})
	}
}
