package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agenthub/internal/auth"
	"gorm.io/gorm"
)

// purgeUsers removes the rows left by a test run: an account and everything
// that points at it, so the database ends up as it started.
func purgeUsers(db *gorm.DB) func() {
	return func() {
		for _, stmt := range []string{
			"DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email LIKE 'cover-%')",
			"DELETE FROM user_roles WHERE user_id IN (SELECT id FROM users WHERE email LIKE 'cover-%')",
			"DELETE FROM user_groups WHERE user_id IN (SELECT id FROM users WHERE email LIKE 'cover-%')",
			"DELETE FROM users WHERE email LIKE 'cover-%'",
		} {
			_ = db.Exec(stmt).Error
		}
	}
}

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

// TestUserStoreCreateGuards covers the rejections the store answers on its own,
// without the auth facade in front: a malformed argument must fail loudly and a
// taken address must map to ErrEmailTaken, because the caller keys its 409 on
// that sentinel rather than on driver text.
func TestUserStoreCreateGuards(t *testing.T) {
	db := openGovernanceDB(t)
	defer purgeUsers(db)()
	users := NewUserStore(db)
	ctx := context.Background()

	if err := users.Create(ctx, nil); err == nil || !strings.Contains(err.Error(), "user or id is empty") {
		t.Fatalf("create nil: err = %v, want empty-user rejection", err)
	}
	if err := users.Create(ctx, &auth.User{Email: uniqueName("cover") + "@example.com"}); err == nil {
		t.Fatal("create without id was accepted")
	}

	email := uniqueName("cover") + "@example.com"
	u := &auth.User{ID: auth.NewID("user"), Email: email, Name: "Cover", Status: auth.StatusActive}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Addresses reach the store already normalized (auth.NormalizeEmail runs in
	// the facade), so uniqueness is a plain column comparison here.
	dup := &auth.User{ID: auth.NewID("user"), Email: email, Name: "Dup", Status: auth.StatusActive}
	if err := users.Create(ctx, dup); !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("duplicate email: err = %v, want %v", err, auth.ErrEmailTaken)
	}

	// Lookups are case- and space-insensitive so a differently typed address
	// still lands on the same account instead of reading as "not registered".
	got, err := users.GetByEmail(ctx, "  "+strings.ToUpper(email)+"  ")
	if err != nil || got == nil {
		t.Fatalf("get by email: user = %v, err = %v", got, err)
	}
	if got.ID != u.ID {
		t.Fatalf("get by email returned %q, want %q", got.ID, u.ID)
	}

	if miss, err := users.Get(ctx, uniqueID("user")); err != nil || miss != nil {
		t.Fatalf("get unknown id: user = %v, err = %v", miss, err)
	}
	if miss, err := users.GetByEmail(ctx, "   "); err != nil || miss != nil {
		t.Fatalf("get by blank email: user = %v, err = %v", miss, err)
	}

	// A rename onto another account collides the same way a create does.
	other := &auth.User{ID: auth.NewID("user"), Email: uniqueName("cover") + "@example.com", Name: "Other", Status: auth.StatusActive}
	if err := users.Create(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	other.Email = email
	if err := users.Update(ctx, other); !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("rename onto taken email: err = %v, want %v", err, auth.ErrEmailTaken)
	}
}

// TestUserStoreLoginLock walks the brute-force defence end to end: the counter
// only locks on the threshold attempt, the lock lands in the future, and a
// successful login clears both. It lives here because the counting happens in
// SQL, so an in-memory fake cannot prove it.
func TestUserStoreLoginLock(t *testing.T) {
	db := openGovernanceDB(t)
	defer purgeUsers(db)()
	users := NewUserStore(db)
	ctx := context.Background()

	email := uniqueName("cover") + "@example.com"
	u := &auth.User{ID: auth.NewID("user"), Email: email, Name: "Lock", Status: auth.StatusActive}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}

	if until, err := users.LockedUntil(ctx, email); err != nil || until != nil {
		t.Fatalf("fresh account: until = %v, err = %v", until, err)
	}

	for i := 1; i < auth.MaxLoginAttempts; i++ {
		if err := users.RecordLoginFailure(ctx, strings.ToUpper(email)); err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
		if until, err := users.LockedUntil(ctx, email); err != nil || until != nil {
			t.Fatalf("after %d/%d failures: until = %v, err = %v", i, auth.MaxLoginAttempts, until, err)
		}
	}

	if err := users.RecordLoginFailure(ctx, email); err != nil {
		t.Fatalf("record locking failure: %v", err)
	}
	until, err := users.LockedUntil(ctx, email)
	if err != nil {
		t.Fatalf("locked until: %v", err)
	}
	if until == nil {
		t.Fatalf("account not locked after %d failures", auth.MaxLoginAttempts)
	}
	window := until.Sub(time.Now().UTC())
	if window < time.Duration(auth.AccountLockMinutes-1)*time.Minute ||
		window > time.Duration(auth.AccountLockMinutes+1)*time.Minute {
		t.Fatalf("lock expires in %v, want about %d minutes", window, auth.AccountLockMinutes)
	}

	if err := users.ResetLoginAttempts(ctx, email); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if until, err := users.LockedUntil(ctx, email); err != nil || until != nil {
		t.Fatalf("after reset: until = %v, err = %v", until, err)
	}

	// An address nobody registered has no lock rather than an error: login can
	// ask about it before deciding the account does not exist.
	if until, err := users.LockedUntil(ctx, uniqueName("cover")+"@example.com"); err != nil || until != nil {
		t.Fatalf("unknown account: until = %v, err = %v", until, err)
	}
}

// TestUserStoreStatusSwitch pins the admin enable/disable switch: it moves
// status and disabled_at together, so the two columns cannot disagree whichever
// entry point wrote them.
func TestUserStoreStatusSwitch(t *testing.T) {
	db := openGovernanceDB(t)
	defer purgeUsers(db)()
	users := NewUserStore(db).(*userStore)
	ctx := context.Background()

	u := &auth.User{ID: auth.NewID("user"), Email: uniqueName("cover") + "@example.com", Name: "Switch", Status: auth.StatusActive}
	if err := users.Create(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := users.SetStatus(ctx, u.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("disable: %v", err)
	}
	off, err := users.Get(ctx, u.ID)
	if err != nil || off == nil {
		t.Fatalf("get disabled: %v", err)
	}
	if off.Status != auth.StatusDisabled || off.DisabledAt == nil {
		t.Fatalf("after disable: status = %q, disabled_at = %v", off.Status, off.DisabledAt)
	}

	if err := users.SetStatus(ctx, u.ID, auth.StatusActive); err != nil {
		t.Fatalf("enable: %v", err)
	}
	on, err := users.Get(ctx, u.ID)
	if err != nil || on == nil {
		t.Fatalf("get enabled: %v", err)
	}
	if on.Status != auth.StatusActive || on.DisabledAt != nil {
		t.Fatalf("after enable: status = %q, disabled_at = %v", on.Status, on.DisabledAt)
	}

	// Delete is a soft disable, never a row removal: grants, tasks and sessions
	// keep pointing at an account that still resolves.
	if err := users.Delete(ctx, u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	gone, err := users.Get(ctx, u.ID)
	if err != nil || gone == nil {
		t.Fatalf("get after delete: user = %v, err = %v", gone, err)
	}
	if gone.Status != auth.StatusDisabled || gone.DisabledAt == nil {
		t.Fatalf("after delete: status = %q, disabled_at = %v", gone.Status, gone.DisabledAt)
	}
}
