package store

import (
	"context"
	"testing"
	"time"

	"agenthub/internal/auth"
)

// TestUserStoreUpdateDerivesDisabledAt pins the one invariant the console cannot
// see but the data can contradict: status and disabled_at must never disagree.
// Only a real database proves it, because the rule lives in the UPDATE statement.
func TestUserStoreUpdateDerivesDisabledAt(t *testing.T) {
	db := openGovernanceDB(t)
	users := NewUserStore(db)
	ctx := context.Background()

	u := &auth.User{
		ID:     auth.NewID("user"),
		Email:  uniqueName("disabled") + "@example.com",
		Name:   "Disabled",
		Status: auth.StatusActive,
	}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DELETE FROM users WHERE id = ?", u.ID) })

	created, err := users.Get(ctx, u.ID)
	if err != nil || created == nil {
		t.Fatalf("get created: %v", err)
	}
	if created.DisabledAt != nil {
		t.Fatalf("active user carries disabled_at = %v", created.DisabledAt)
	}

	// Disabling stamps the moment, which is what "pre-provisioned but not yet
	// usable" looks like in the table.
	stamped := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	u.Status, u.DisabledAt = auth.StatusDisabled, &stamped
	if err := users.Update(ctx, u); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err := users.Get(ctx, u.ID)
	if err != nil || got == nil {
		t.Fatalf("get disabled: %v", err)
	}
	if got.Status != auth.StatusDisabled {
		t.Fatalf("status = %q, want %q", got.Status, auth.StatusDisabled)
	}
	if got.DisabledAt == nil || !got.DisabledAt.Equal(stamped) {
		t.Fatalf("disabled_at = %v, want %v", got.DisabledAt, stamped)
	}

	// A later edit that leaves the status alone must keep the original date
	// instead of quietly restamping it.
	if err := users.Update(ctx, got); err != nil {
		t.Fatalf("rename while disabled: %v", err)
	}
	again, err := users.Get(ctx, u.ID)
	if err != nil || again == nil {
		t.Fatalf("get renamed: %v", err)
	}
	if again.DisabledAt == nil || !again.DisabledAt.Equal(stamped) {
		t.Fatalf("disabled_at moved to %v, want %v", again.DisabledAt, stamped)
	}

	// Enabling clears it, so "active with a disabled date" cannot be read back.
	again.Status, again.DisabledAt = auth.StatusActive, nil
	if err := users.Update(ctx, again); err != nil {
		t.Fatalf("enable: %v", err)
	}
	enabled, err := users.Get(ctx, u.ID)
	if err != nil || enabled == nil {
		t.Fatalf("get enabled: %v", err)
	}
	if enabled.Status != auth.StatusActive || enabled.DisabledAt != nil {
		t.Fatalf("after enable: status = %q, disabled_at = %v", enabled.Status, enabled.DisabledAt)
	}
}
