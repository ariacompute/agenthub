package handlers

// The channel surface under the two conditions the happy-path tests never reach:
// the feature is not wired at all, and the store answers with an error. Both are
// about the same promise — a 503 or a 500 with a code, never an empty list that
// reads as "you have no channels".

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"agenthub/internal/channels"
	"agenthub/internal/httputil"
)

type toggledChannels struct {
	*fakeChannels
	updateErr         error
	listDeliveriesErr error
	getDeliveryErr    error
	recordErr         error
	updateDeliveryErr error
	getUnscopedErr    error
}

func (f *toggledChannels) Update(ctx context.Context, c *channels.Channel, ownerID string) (*channels.Channel, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.fakeChannels.Update(ctx, c, ownerID)
}

func (f *toggledChannels) ListDeliveries(ctx context.Context, channelID, ownerID string) ([]*channels.ChannelMessage, error) {
	if f.listDeliveriesErr != nil {
		return nil, f.listDeliveriesErr
	}
	return f.fakeChannels.ListDeliveries(ctx, channelID, ownerID)
}

func (f *toggledChannels) GetDelivery(ctx context.Context, channelID, id, ownerID string) (*channels.ChannelMessage, error) {
	if f.getDeliveryErr != nil {
		return nil, f.getDeliveryErr
	}
	return f.fakeChannels.GetDelivery(ctx, channelID, id, ownerID)
}

func (f *toggledChannels) RecordDelivery(ctx context.Context, m *channels.ChannelMessage) (bool, error) {
	if f.recordErr != nil {
		return false, f.recordErr
	}
	return f.fakeChannels.RecordDelivery(ctx, m)
}

func (f *toggledChannels) UpdateDelivery(ctx context.Context, m *channels.ChannelMessage, ownerID string) (bool, error) {
	if f.updateDeliveryErr != nil {
		return false, f.updateDeliveryErr
	}
	return f.fakeChannels.UpdateDelivery(ctx, m, ownerID)
}

func (f *toggledChannels) GetUnscoped(ctx context.Context, id string) (*channels.Channel, error) {
	if f.getUnscopedErr != nil {
		return nil, f.getUnscopedErr
	}
	return f.fakeChannels.GetUnscoped(ctx, id)
}

// TestChannelEndpointsNeedTheFeatureWired: with no store every channel route is
// a 503 with a machine code. The routes are only registered when the feature is
// on, so the guard inside each handler is what a misconfigured deployment (or a
// test that wires the store late) actually hits.
func TestChannelEndpointsNeedTheFeatureWired(t *testing.T) {
	d, _ := newDeps(t)
	d.Channels = nil

	for _, c := range []struct {
		name   string
		handle http.HandlerFunc
		method string
		target string
	}{
		{"list", d.handleListChannels, http.MethodGet, "/api/channels"},
		{"get", d.handleGetChannel, http.MethodGet, "/api/channels/c1"},
		{"create", d.handleCreateChannel, http.MethodPost, "/api/channels"},
		{"update", d.handleUpdateChannel, http.MethodPut, "/api/channels/c1"},
		{"delete", d.handleDeleteChannel, http.MethodDelete, "/api/channels/c1"},
		{"deliveries", d.handleListDeliveries, http.MethodGet, "/api/channels/c1/deliveries"},
		{"webhook", d.handleChannelWebhook, http.MethodPost, "/api/channels/c1/webhook"},
		{"deliver", d.handleDeliverChannel, http.MethodPost, "/api/channels/c1/deliver"},
		{"retry", d.handleRetryDelivery, http.MethodPost, "/api/channels/c1/deliveries/d1/retry"},
	} {
		w := sandboxRecorder()
		c.handle(w, sandboxRequest(c.method, c.target, "", nil, "c1"))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", c.name, w.Code)
			continue
		}
		if code := errorCode(t, w); code != string(httputil.CodeChannelDisabled) {
			t.Errorf("%s: code = %q", c.name, code)
		}
	}
}

// TestChannelStoreFailuresReachTheCaller walks the read and write paths with the
// store refusing. A swallowed error here shows an empty receipt list, and the
// console then offers a retry for a delivery it never saw.
func TestChannelStoreFailuresReachTheCaller(t *testing.T) {
	store := &toggledChannels{fakeChannels: newFakeChannels()}
	d, _ := newDeps(t)
	d.Channels = store
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	if _, err := store.Create(context.Background(), &channels.Channel{
		ID: "c1", OwnerID: "dev", Kind: channels.KindSlack, Name: "sl", Enabled: true,
		Config: map[string]any{"target_url": "https://example.test/hook"},
	}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := store.RecordDelivery(context.Background(), &channels.ChannelMessage{
		ID: "d1", ExternalRef: "d1", ChannelID: "c1", Direction: "outbound",
		Status: channels.StatusFailed, Payload: map[string]any{"text": "hello"},
	}); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}

	store.listDeliveriesErr = errStoreDown
	if code, raw := doReq(t, srv, http.MethodGet, "/api/channels/c1/deliveries", ""); code != http.StatusInternalServerError {
		t.Fatalf("list deliveries = %d (%s)", code, raw)
	}
	store.listDeliveriesErr = nil

	store.updateErr = errStoreDown
	body := `{"name":"renamed","enabled":true}`
	if code, raw := doReq(t, srv, http.MethodPut, "/api/channels/c1", body); code != http.StatusInternalServerError {
		t.Fatalf("update = %d (%s)", code, raw)
	}
	store.updateErr = nil

	store.getDeliveryErr = errStoreDown
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/c1/deliveries/d1/retry", ""); code != http.StatusInternalServerError {
		t.Fatalf("retry read = %d (%s)", code, raw)
	}
	store.getDeliveryErr = nil

	// A failed retry still records the fresh failure; a receipt store that
	// refuses that write must not change the answer to the caller.
	orig := deliverOutbound
	deliverOutbound = func(context.Context, *channels.Channel, string) (*channels.DeliverResult, error) {
		return nil, errors.New("provider down")
	}
	defer func() { deliverOutbound = orig }()
	store.updateDeliveryErr = errStoreDown
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/c1/deliveries/d1/retry", ""); code != http.StatusBadGateway {
		t.Fatalf("retry with a failing receipt write = %d (%s)", code, raw)
	}
	store.updateDeliveryErr = nil

	// The delivered path: the message did leave, so a stale receipt is logged and
	// the caller still gets 200.
	deliverOutbound = func(context.Context, *channels.Channel, string) (*channels.DeliverResult, error) {
		return &channels.DeliverResult{Status: 200, Raw: `{"ok":true}`}, nil
	}
	store.updateDeliveryErr = errStoreDown
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/c1/deliveries/d1/retry", ""); code != http.StatusOK {
		t.Fatalf("retry with a failing receipt write after a successful send = %d (%s)", code, raw)
	}
	store.updateDeliveryErr = nil

	// The webhook reads the channel unscoped before anything is verified; a
	// failing read must not be reported as "channel disabled".
	store.getUnscopedErr = errStoreDown
	req := httptest.NewRequest(http.MethodPost, "/api/channels/c1/webhook", strings.NewReader(`{"text":"hi","event_id":"e1"}`))
	req.SetPathValue("id", "c1")
	w := sandboxRecorder()
	d.handleChannelWebhook(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("webhook read = %d (%s)", w.Code, w.Body.String())
	}
	store.getUnscopedErr = nil

	// And the write behind a verified webhook.
	t.Setenv("TEST_CHAN_PATHS_SECRET", "shh")
	store.signWith("c1", "TEST_CHAN_PATHS_SECRET")
	store.recordErr = errStoreDown
	code := postSignedWebhook(t, srv, "c1", `{"text":"hi","event_id":"e2"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("webhook write = %d", code)
	}
}

// signWith points a seeded channel at an env var holding its signing secret.
func (f *toggledChannels) signWith(id, env string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.byID[id]; c != nil {
		c.SigningKeyEnv = env
	}
}

func postSignedWebhook(t *testing.T, srv *httptest.Server, id, payload string) int {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	return postSlackWebhook(t, srv, id, payload, slackSig("shh", ts, []byte(payload)), ts)
}

// TestChannelBodiesAndGuards covers the request shapes that must be refused
// before any store call: undecodable JSON, and a delivery with nothing to say.
func TestChannelBodiesAndGuards(t *testing.T) {
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	if _, err := fake.Create(context.Background(), &channels.Channel{
		ID: "c1", OwnerID: "dev", Kind: channels.KindSlack, Name: "sl", Enabled: true,
		Config: map[string]any{"target_url": "https://example.test/hook"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels", `{"kind":`); code != http.StatusBadRequest {
		t.Fatalf("create decode = %d (%s)", code, raw)
	}
	if code, raw := doReq(t, srv, http.MethodPut, "/api/channels/c1", `{"kind":`); code != http.StatusBadRequest {
		t.Fatalf("update decode = %d (%s)", code, raw)
	}

	// Config and thread binding are replaced when present, and left alone when
	// the request omits them.
	body := `{"enabled":true,"config":{"target_url":"https://example.test/two"},"threadId":"th-2"}`
	if code, raw := doReq(t, srv, http.MethodPut, "/api/channels/c1", body); code != http.StatusOK {
		t.Fatalf("update config/thread = %d (%s)", code, raw)
	}
	got, _ := fake.Get(context.Background(), "c1", "dev")
	if got.ThreadID != "th-2" || got.Config["target_url"] != "https://example.test/two" {
		t.Fatalf("after update = %+v", got)
	}

	// Deliver: unknown channel, disabled channel, undecodable body, empty text.
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/nope/deliver", `{"message":"hi"}`); code != http.StatusNotFound {
		t.Fatalf("deliver unknown = %d (%s)", code, raw)
	}
	off, _ := fake.Create(context.Background(), &channels.Channel{
		ID: "off", OwnerID: "dev", Kind: channels.KindSlack, Name: "off", Enabled: false,
	})
	if off == nil {
		t.Fatal("seed disabled channel")
	}
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/off/deliver", `{"message":"hi"}`); code != http.StatusForbidden {
		t.Fatalf("deliver disabled = %d (%s)", code, raw)
	}
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/c1/deliver", `{"message":`); code != http.StatusBadRequest {
		t.Fatalf("deliver decode = %d (%s)", code, raw)
	}
	if code, raw := doReq(t, srv, http.MethodPost, "/api/channels/c1/deliver", `{"message":""}`); code != http.StatusBadRequest {
		t.Fatalf("deliver empty = %d (%s)", code, raw)
	}

	// A successful send records the delivered receipt and answers with the
	// provider's own result.
	orig := deliverOutbound
	deliverOutbound = func(context.Context, *channels.Channel, string) (*channels.DeliverResult, error) {
		return &channels.DeliverResult{Status: 201, Raw: `{"ok":true}`}, nil
	}
	defer func() { deliverOutbound = orig }()
	code, raw := doReq(t, srv, http.MethodPost, "/api/channels/c1/deliver", `{"message":"hi"}`)
	if code != http.StatusOK {
		t.Fatalf("deliver = %d (%s)", code, raw)
	}
	msgs, _ := fake.ListDeliveries(context.Background(), "c1", "dev")
	if len(msgs) != 1 || msgs[0].Status != channels.StatusDelivered {
		t.Fatalf("delivered receipt = %+v", msgs)
	}

	// The webhook body that carries no idempotency key is refused: without one
	// every replay would look new.
	t.Setenv("TEST_CHAN_PATHS_SECRET", "shh")
	got2, _ := fake.GetUnscoped(context.Background(), "c1")
	got2.SigningKeyEnv = "TEST_CHAN_PATHS_SECRET"
	code = postSignedWebhook(t, srv, "c1", `{"text":"hi"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("webhook without a ref = %d", code)
	}

	// A body that cannot be read is a 500, not a signature failure.
	w := sandboxRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/channels/c1/webhook", nil)
	req.SetPathValue("id", "c1")
	req.Body = failingReader{}
	d.handleChannelWebhook(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("unreadable body = %d (%s)", w.Code, w.Body.String())
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("body exploded") }
func (failingReader) Close() error             { return nil }

var _ io.ReadCloser = failingReader{}
