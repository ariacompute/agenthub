package store

import (
	"testing"

	"agenthub/internal/channels"
	"agenthub/internal/model"
)

// toDomainChannel / toDomainMessage are the GORM <-> domain mappers in
// channels_store.go. They need no database, so we exercise them directly to keep
// the mapping honest without a live Postgres.

func TestToDomainChannelMapsFields(t *testing.T) {
	cfg, err := model.NewJSONField(map[string]any{"a": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	r := &model.Channel{
		ID:            "c1",
		Kind:          "slack",
		Name:          "My Slack",
		Enabled:       true,
		Config:        cfg,
		SigningKeyEnv: "SLACK_SECRET",
		ThreadID:      "th1",
		OwnerID:       "u1",
	}
	c := toDomainChannel(r)
	if c.ID != "c1" || c.Kind != channels.KindSlack || !c.Enabled {
		t.Fatalf("scalar mapping wrong: %+v", c)
	}
	if c.SigningKeyEnv != "SLACK_SECRET" || c.ThreadID != "th1" || c.OwnerID != "u1" {
		t.Fatalf("scalar mapping wrong: %+v", c)
	}
	if c.Config["a"] != float64(1) {
		t.Fatalf("config not carried: %+v", c.Config)
	}
}

func TestToDomainMessageMapsFields(t *testing.T) {
	payload, err := model.NewJSONField(map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	r := &model.ChannelMessage{
		ExternalRef: "ext-1",
		ChannelID:   "c1",
		ThreadID:    "th1",
		TaskID:      "t1",
		Direction:   "inbound",
		Payload:     payload,
		Status:      "received",
		OwnerID:     "u1",
	}
	m := toDomainMessage(r)
	if m.ChannelID != "c1" || m.ExternalRef != "ext-1" || m.Direction != "inbound" {
		t.Fatalf("scalar mapping wrong: %+v", m)
	}
	if m.Status != "received" || m.Payload["text"] != "hi" {
		t.Fatalf("scalar mapping wrong: %+v", m)
	}
	// The receipt's owner is part of the wire shape now: the console shows it and
	// the store scopes every read on it.
	if m.OwnerID != "u1" {
		t.Fatalf("owner not carried: %+v", m)
	}
}

// channelConfig / deliveryPayload guard the two jsonb columns declared
// `NOT NULL DEFAULT '{}'`. A typed nil map survives `v == nil` in
// NewJSONField (the interface is non-nil) and marshals to "null", which would
// be written as an explicit SQL NULL and rejected by the column. Both the
// normal and the nil path are pinned here so the guard cannot regress.
func TestChannelConfigNormalizesNilMap(t *testing.T) {
	got, err := channelConfig(nil)
	if err != nil {
		t.Fatalf("nil config should not fail: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("nil config should encode as an empty object, got %q", string(got))
	}
	if v, err := got.Value(); err != nil || v == nil {
		t.Fatalf("nil config must not reach the column as NULL: %v %v", v, err)
	}

	got, err = channelConfig(map[string]any{"webhook": "secret"})
	if err != nil {
		t.Fatalf("config encode failed: %v", err)
	}
	if string(got) != `{"webhook":"secret"}` {
		t.Fatalf("config not preserved: %q", string(got))
	}
}

func TestDeliveryPayloadNormalizesNilMap(t *testing.T) {
	got, err := deliveryPayload(nil)
	if err != nil {
		t.Fatalf("nil payload should not fail: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("nil payload should encode as an empty object, got %q", string(got))
	}
	if v, err := got.Value(); err != nil || v == nil {
		t.Fatalf("nil payload must not reach the column as NULL: %v %v", v, err)
	}

	got, err = deliveryPayload(map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("payload encode failed: %v", err)
	}
	if string(got) != `{"text":"hi"}` {
		t.Fatalf("payload not preserved: %q", string(got))
	}
}

// The guard only covers nil maps: a payload that cannot be marshalled must
// still surface as an error rather than being silently coerced to "{}".
func TestDeliveryPayloadPropagatesEncodeError(t *testing.T) {
	if _, err := deliveryPayload(map[string]any{"chan": make(chan int)}); err == nil {
		t.Fatal("unencodable payload should return an error")
	}
}
