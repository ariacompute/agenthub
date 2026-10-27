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
	}
	m := toDomainMessage(r)
	if m.ChannelID != "c1" || m.ExternalRef != "ext-1" || m.Direction != "inbound" {
		t.Fatalf("scalar mapping wrong: %+v", m)
	}
	if m.Status != "received" || m.Payload["text"] != "hi" {
		t.Fatalf("scalar mapping wrong: %+v", m)
	}
}
