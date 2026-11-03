package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub/internal/adapter"
	"agenthub/internal/channels"
	"agenthub/internal/config"
	"agenthub/internal/httputil"
	"agenthub/internal/hub"
	"agenthub/internal/registry"
)

// ---- in-memory channels.Store for handler tests (no database) ----

type fakeChannels struct {
	mu         sync.Mutex
	byID       map[string]*channels.Channel
	deliveries map[string]*channels.ChannelMessage
}

func newFakeChannels() *fakeChannels {
	return &fakeChannels{
		byID:       map[string]*channels.Channel{},
		deliveries: map[string]*channels.ChannelMessage{},
	}
}

func (f *fakeChannels) List(_ context.Context, ownerID string) ([]*channels.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*channels.Channel, 0, len(f.byID))
	for _, c := range f.byID {
		if ownerID == "" || c.OwnerID == ownerID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeChannels) Get(_ context.Context, id, ownerID string) (*channels.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.byID[id]
	if c == nil || c.OwnerID != ownerID {
		// Ownership is part of the key: another account's row is reported as
		// absent, exactly like the GORM implementation's WHERE clause.
		return nil, nil
	}
	return c, nil
}

func (f *fakeChannels) GetUnscoped(_ context.Context, id string) (*channels.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byID[id], nil
}

func (f *fakeChannels) Create(_ context.Context, c *channels.Channel) (*channels.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeChannels) Update(_ context.Context, c *channels.Channel, ownerID string) (*channels.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.byID[c.ID]
	if old == nil || old.OwnerID != ownerID {
		return nil, nil
	}
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeChannels) Delete(_ context.Context, id, ownerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.byID[id]; c != nil && c.OwnerID == ownerID {
		delete(f.byID, id)
	}
	return nil
}

func (f *fakeChannels) RecordDelivery(_ context.Context, m *channels.ChannelMessage) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := m.ChannelID + "/" + m.ExternalRef
	if _, ok := f.deliveries[key]; ok {
		return false, nil
	}
	// The real store copies the owner off the parent channel; mirroring that here
	// is what lets the handler tests assert ownership instead of assuming it.
	if c := f.byID[m.ChannelID]; c != nil {
		m.OwnerID = c.OwnerID
	}
	f.deliveries[key] = m
	return true, nil
}

func (f *fakeChannels) ListDeliveries(_ context.Context, channelID, ownerID string) ([]*channels.ChannelMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*channels.ChannelMessage{}
	for _, m := range f.deliveries {
		if m.ChannelID == channelID && m.OwnerID == ownerID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeChannels) GetDelivery(_ context.Context, channelID, id, ownerID string) (*channels.ChannelMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.deliveries[channelID+"/"+id]
	if m == nil || m.OwnerID != ownerID {
		return nil, nil
	}
	return m, nil
}

func (f *fakeChannels) UpdateDelivery(_ context.Context, m *channels.ChannelMessage, ownerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.deliveries[m.ChannelID+"/"+m.ID]
	if !ok || old.OwnerID != ownerID {
		return false, nil
	}
	old.Status = m.Status
	old.Error = m.Error
	old.Payload = m.Payload
	return true, nil
}

// ---- shared handler harness ----

func testConfig() *config.Config {
	cfg := &config.Config{
		HTTPPort:        "3500",
		Env:             "dev",
		MaxParallel:     4,
		SessionTTL:      time.Hour,
		CLITimeout:      time.Second,
		CLIAllowlist:    []string{"echo", "ls"},
		MaxOutputBytes:  1024,
		EventBuffer:     64,
		PushEnabled:     true,
		ChannelsEnabled: true,
		SpeechMode:      registry.ProviderModeMock,
		AudioMaxBytes:   16 << 20,
		DatabaseURL:     "postgres://agenthub:agenthub@localhost:5434/agenthub",
		PushRecordLimit: 100,
	}
	if err := cfg.Validate(); err != nil {
		return nil
	}
	return cfg
}

func newDeps(t *testing.T) (Deps, *registry.Registry) {
	t.Helper()
	cfg := testConfig()
	if cfg == nil {
		t.Fatal("test config failed validation")
	}
	reg := registry.New()
	if _, err := reg.Upsert(&registry.Entry{
		Kind:     registry.KindMCP,
		Name:     "mcp1",
		Endpoint: "stdio://mcp1",
		Health:   registry.HealthHealthy,
	}); err != nil {
		t.Fatal(err)
	}
	pusher := &hub.MockNotifier{}
	h := hub.New(cfg, reg,
		adapter.Options{CLITimeout: time.Second, CLIAllowlist: []string{"echo", "ls"}, MaxOutputBytes: 1024},
		pusher, hub.Persistence{})
	d := Deps{
		Cfg: cfg,
		Hub: h,
		Reg: reg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return d, reg
}

func doReq(t *testing.T, srv *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func postSlackWebhook(t *testing.T, srv *httptest.Server, id, payload, sig, ts string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/channels/"+id+"/webhook", bytes.NewReader([]byte(payload)))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func slackSig(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestChannelCRUD(t *testing.T) {
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"kind":          "slack",
		"name":          "My Slack",
		"enabled":       true,
		"signingKeyEnv": "TEST_C_SECRET",
		"threadId":      "th1",
	})
	code, raw := doReq(t, srv, "POST", "/api/channels", string(body))
	if code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", code, raw)
	}
	var created channels.Channel
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Kind != channels.KindSlack {
		t.Fatalf("created = %+v", created)
	}

	code, raw = doReq(t, srv, "GET", "/api/channels", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	var list []*channels.Channel
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d", len(list))
	}

	code, _ = doReq(t, srv, "GET", "/api/channels/"+created.ID, "")
	if code != http.StatusOK {
		t.Fatalf("get = %d", code)
	}

	ub, _ := json.Marshal(map[string]any{
		"kind":          "slack",
		"name":          "Renamed",
		"enabled":       false,
		"signingKeyEnv": "TEST_C_SECRET",
	})
	code, raw = doReq(t, srv, "PUT", "/api/channels/"+created.ID, string(ub))
	if code != http.StatusOK {
		t.Fatalf("update = %d", code)
	}
	var updated channels.Channel
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Renamed" || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}

	code, _ = doReq(t, srv, "DELETE", "/api/channels/"+created.ID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	code, _ = doReq(t, srv, "GET", "/api/channels/"+created.ID, "")
	if code != http.StatusNotFound {
		t.Fatalf("after delete = %d", code)
	}
}

func TestChannelCreateRejectsInvalidKind(t *testing.T) {
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	code, raw := doReq(t, srv, "POST", "/api/channels", `{"kind":"bogus","name":"x","signingKeyEnv":"A"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid kind = %d", code)
	}
	if httputil.AsError(decodeErr(raw)).Code != httputil.CodeChannelPayloadInvalid {
		t.Fatalf("code = %q", httputil.AsError(decodeErr(raw)).Code)
	}
}

func TestChannelCreateRejectsBadEnvName(t *testing.T) {
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	code, raw := doReq(t, srv, "POST", "/api/channels", `{"kind":"slack","name":"x","signingKeyEnv":"has space"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad env name = %d", code)
	}
	if httputil.AsError(decodeErr(raw)).Code != httputil.CodeChannelPayloadInvalid {
		t.Fatalf("code = %q", httputil.AsError(decodeErr(raw)).Code)
	}
}

func TestChannelDisabledReturns503(t *testing.T) {
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	d.Cfg.ChannelsEnabled = false
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	code, raw := doReq(t, srv, "GET", "/api/channels", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("disabled = %d", code)
	}
	if httputil.AsError(decodeErr(raw)).Code != httputil.CodeChannelDisabled {
		t.Fatalf("code = %q", httputil.AsError(decodeErr(raw)).Code)
	}
}

func TestChannelWebhook(t *testing.T) {
	t.Setenv("TEST_WEBHOOK_SECRET", "shh")
	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	id := "chan-1"
	if _, err := fake.Create(context.Background(), &channels.Channel{
		ID:            id,
		Kind:          channels.KindSlack,
		Name:          "sl",
		Enabled:       true,
		SigningKeyEnv: "TEST_WEBHOOK_SECRET",
		ThreadID:      "th1",
	}); err != nil {
		t.Fatal(err)
	}

	payload := `{"text":"hello team","event_id":"evt-9"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	// Valid signature is accepted and recorded.
	code := postSlackWebhook(t, srv, id, payload, slackSig("shh", ts, []byte(payload)), ts)
	if code != http.StatusOK {
		t.Fatalf("valid webhook = %d", code)
	}
	if msgs, _ := fake.ListDeliveries(context.Background(), id, ""); len(msgs) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(msgs))
	}

	// A replay of the same external_ref is deduped (still only one delivery).
	postSlackWebhook(t, srv, id, payload, slackSig("shh", ts, []byte(payload)), ts)
	if msgs, _ := fake.ListDeliveries(context.Background(), id, ""); len(msgs) != 1 {
		t.Fatalf("after replay deliveries = %d, want 1", len(msgs))
	}

	// Wrong signature is rejected with 401.
	code = postSlackWebhook(t, srv, id, payload, slackSig("wrong", ts, []byte(payload)), ts)
	if code != http.StatusUnauthorized {
		t.Fatalf("invalid sig = %d", code)
	}

	// A disabled channel rejects with 403 even with a valid signature.
	fake.Create(context.Background(), &channels.Channel{
		ID: "chan-2", Kind: channels.KindSlack, Enabled: false, SigningKeyEnv: "TEST_WEBHOOK_SECRET",
	})
	code = postSlackWebhook(t, srv, "chan-2", payload, slackSig("shh", ts, []byte(payload)), ts)
	if code != http.StatusForbidden {
		t.Fatalf("disabled channel = %d", code)
	}

	// An unknown channel is 404.
	code = postSlackWebhook(t, srv, "nope", payload, slackSig("shh", ts, []byte(payload)), ts)
	if code != http.StatusNotFound {
		t.Fatalf("unknown channel = %d", code)
	}
}

// A send that never leaves still has to leave a receipt: it is the only record
// the console can offer a retry from, and it must carry the text to re-send.
func TestChannelDeliverRecordsFailureReceipt(t *testing.T) {
	orig := deliverOutbound
	deliverOutbound = func(context.Context, *channels.Channel, string) (*channels.DeliverResult, error) {
		return nil, errors.New("dial tcp 10.0.0.1:443: connect: refused")
	}
	defer func() { deliverOutbound = orig }()

	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	if _, err := fake.Create(context.Background(), &channels.Channel{
		ID: "c1", Kind: channels.KindSlack, Name: "sl", Enabled: true,
		Config:  map[string]any{"target_url": "https://example.test/hook"},
		OwnerID: "dev",
	}); err != nil {
		t.Fatal(err)
	}

	code, raw := doReq(t, srv, "POST", "/api/channels/c1/deliver", `{"message":"hi"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("deliver = %d (%s)", code, raw)
	}
	if got := httputil.AsError(decodeErr(raw)).Code; got != httputil.CodeDeliveryFailed {
		t.Fatalf("code = %q", got)
	}

	msgs, err := fake.ListDeliveries(context.Background(), "c1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("receipts = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Direction != "outbound" || m.Status != channels.StatusFailed {
		t.Fatalf("receipt = %+v", m)
	}
	if m.Error != string(httputil.CodeDeliveryFailed) {
		t.Fatalf("error = %q", m.Error)
	}
	if text, _ := m.Payload["text"].(string); text != "hi" {
		t.Fatalf("payload text = %q, want hi", text)
	}
}

func TestChannelRetryDelivery(t *testing.T) {
	orig := deliverOutbound
	fail := false
	deliverOutbound = func(context.Context, *channels.Channel, string) (*channels.DeliverResult, error) {
		if fail {
			return nil, errors.New("provider down")
		}
		return &channels.DeliverResult{Status: 200, Raw: `{"ok":true}`}, nil
	}
	defer func() { deliverOutbound = orig }()

	fake := newFakeChannels()
	d, _ := newDeps(t)
	d.Channels = fake
	srv := httptest.NewServer(Register(d))
	defer srv.Close()

	for _, c := range []*channels.Channel{
		{ID: "c1", OwnerID: "dev", Kind: channels.KindSlack, Name: "sl", Enabled: true, Config: map[string]any{"target_url": "https://example.test/hook"}},
		{ID: "off", OwnerID: "dev", Kind: channels.KindSlack, Name: "off", Enabled: false, Config: map[string]any{"target_url": "https://example.test/hook"}},
	} {
		if _, err := fake.Create(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(id, channelID, direction, status string, payload map[string]any) {
		t.Helper()
		if _, err := fake.RecordDelivery(context.Background(), &channels.ChannelMessage{
			ID: id, ExternalRef: id, ChannelID: channelID, Direction: direction,
			Status: status, Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("out-failed", "c1", "outbound", channels.StatusFailed, map[string]any{"text": "hello"})
	seed("out-done", "c1", "outbound", channels.StatusDelivered, map[string]any{"text": "hello"})
	seed("in-failed", "c1", "inbound", channels.StatusFailed, map[string]any{"text": "hello"})
	seed("out-notext", "c1", "outbound", channels.StatusFailed, map[string]any{})
	seed("out-off", "off", "outbound", channels.StatusFailed, map[string]any{"text": "hello"})

	// A receipt that never left is re-sent and flipped to delivered in place.
	code, raw := doReq(t, srv, "POST", "/api/channels/c1/deliveries/out-failed/retry", "")
	if code != http.StatusOK {
		t.Fatalf("retry = %d (%s)", code, raw)
	}
	var result channels.DeliverResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 {
		t.Fatalf("result = %+v", result)
	}
	got, _ := fake.GetDelivery(context.Background(), "c1", "out-failed", "dev")
	if got == nil || got.Status != channels.StatusDelivered || got.Error != "" {
		t.Fatalf("receipt after retry = %+v", got)
	}
	if got.Payload["raw"] != `{"ok":true}` {
		t.Fatalf("payload after retry = %+v", got.Payload)
	}
	if msgs, _ := fake.ListDeliveries(context.Background(), "c1", "dev"); len(msgs) != 4 {
		t.Fatalf("retry appended a receipt: %d rows", len(msgs))
	}

	// The three cases where a retry cannot mean anything.
	for name, tc := range map[string]struct{ path string }{
		"delivered":    {"/api/channels/c1/deliveries/out-done/retry"},
		"inbound":      {"/api/channels/c1/deliveries/in-failed/retry"},
		"without text": {"/api/channels/c1/deliveries/out-notext/retry"},
	} {
		code, raw := doReq(t, srv, "POST", tc.path, "")
		if code != http.StatusConflict {
			t.Fatalf("%s: status = %d (%s)", name, code, raw)
		}
		if got := httputil.AsError(decodeErr(raw)).Code; got != httputil.CodeDeliveryNotRetryable {
			t.Fatalf("%s: code = %q", name, got)
		}
	}

	// Unknown receipt, then a disabled channel.
	code, raw = doReq(t, srv, "POST", "/api/channels/c1/deliveries/nope/retry", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown receipt = %d (%s)", code, raw)
	}
	if got := httputil.AsError(decodeErr(raw)).Code; got != httputil.CodeDeliveryNotFound {
		t.Fatalf("code = %q", got)
	}
	code, raw = doReq(t, srv, "POST", "/api/channels/off/deliveries/out-off/retry", "")
	if code != http.StatusForbidden {
		t.Fatalf("disabled = %d (%s)", code, raw)
	}

	// A provider failure keeps the receipt retryable and says why.
	seed("out-boom", "c1", "outbound", channels.StatusFailed, map[string]any{"text": "hello"})
	fail = true
	code, raw = doReq(t, srv, "POST", "/api/channels/c1/deliveries/out-boom/retry", "")
	if code != http.StatusBadGateway {
		t.Fatalf("provider down = %d (%s)", code, raw)
	}
	if got := httputil.AsError(decodeErr(raw)).Code; got != httputil.CodeDeliveryFailed {
		t.Fatalf("code = %q", got)
	}
	got, _ = fake.GetDelivery(context.Background(), "c1", "out-boom", "dev")
	if got == nil || got.Status != channels.StatusFailed || got.Error != string(httputil.CodeDeliveryFailed) {
		t.Fatalf("receipt after failed retry = %+v", got)
	}
	if text, _ := got.Payload["text"].(string); text != "hello" {
		t.Fatalf("text lost after failed retry: %+v", got.Payload)
	}
}

func decodeErr(raw []byte) error {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	return &httputil.Error{Code: httputil.Code(e.Code)}
}

func TestMentionsOf(t *testing.T) {
	got := mentionsOf("hi @alice and @bob")
	if len(got) != 2 || !got["alice"] || !got["bob"] {
		t.Fatalf("got %v", got)
	}
	if mentionsOf("no mentions here") != nil {
		t.Fatal("expected nil for no mentions")
	}
}

func TestIsMultipart(t *testing.T) {
	if !isMultipart("multipart/form-data; boundary=xyz") {
		t.Fatal("should detect multipart")
	}
	if isMultipart("application/json") {
		t.Fatal("should not detect json as multipart")
	}
}

func TestValidEnvVarName(t *testing.T) {
	// Letters, digits and underscores are accepted, including a leading digit.
	for _, good := range []string{"ABC_123", "1ABC", "lowercase", "_secret"} {
		if !validEnvVarName(good) {
			t.Fatalf("valid name rejected: %q", good)
		}
	}
	// Anything outside [A-Za-z0-9_] is rejected; we only ever store the name.
	for _, bad := range []string{"", "has space", "x-y", "a.b", "a/b"} {
		if validEnvVarName(bad) {
			t.Fatalf("bad name accepted: %q", bad)
		}
	}
}
