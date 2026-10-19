package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"agenthub/internal/registry"
	"gorm.io/gorm"
)

// The persistence layer is the only place the governance rules are actually
// enforced — the unique index, the jsonb round trip, the sync bookkeeping — so
// it is covered here against a real database. The gate is the same as everywhere
// else in the repo: without DATABASE_URL these cases skip, and nothing is ever
// fabricated to make a number move.

func openGovernanceDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set: skipping DB-backed store test (make db-up)")
	}
	db, err := Open(dsn, false)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(db); err != nil {
			t.Errorf("close db: %v", err)
		}
	})
	return db
}

// uniqueName keeps two concurrent runs — or two runs in a row — from colliding
// on the dedup key.
func uniqueName(prefix string) string {
	return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
}

// uniqueID builds an id for a case that writes straight through the store
// without the facade to generate one. An id ends up in URLs and in the primary
// key, so it stays a slug.
func uniqueID(kind string) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return kind + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return kind + "-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + hex.EncodeToString(buf)
}

func TestStoreResourceGovernanceColumnsRoundTrip(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()
	rs := NewResourceStore(db)

	// The store takes the id as given: generating one is the facade's job.
	e := &registry.Entry{
		ID:          uniqueID("model"),
		Kind:        registry.KindModel,
		Name:        uniqueName("store-gov"),
		Version:     "1.0.0",
		Namespace:   "default",
		Status:      registry.StatusPending,
		DisplayName: "Store governance",
		SourceID:    "src-x",
		Health:      registry.HealthUnknown,
		Tags:        []string{"a", "b"},
		KindConfig: &registry.KindConfig{Model: &registry.ModelConfig{
			ProviderMode: registry.ProviderModeOpenAICompatible,
			Endpoint:     "https://router.test/v1",
			ModelID:      "gpt-4o-mini",
			APIKeyEnv:    "OPENAI_API_KEY",
			Targets:      []registry.ModelTarget{{Name: "primary", Weight: 3}},
			TimeoutMS:    1500,
		}},
	}
	stored, err := rs.Upsert(ctx, e)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, stored.ID) })

	reread, err := rs.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Every governance column has to survive the round trip, and so does the
	// per-kind block: it is one jsonb column, not a set of tables.
	if reread.Namespace != "default" || reread.Status != registry.StatusPending {
		t.Fatalf("governance columns = %+v", reread)
	}
	if reread.DisplayName != "Store governance" || reread.SourceID != "src-x" {
		t.Fatalf("display name / source = %+v", reread)
	}
	if reread.KindConfig == nil || reread.KindConfig.Model == nil {
		t.Fatalf("kind config did not survive: %+v", reread.KindConfig)
	}
	m := reread.KindConfig.Model
	if m.ProviderMode != registry.ProviderModeOpenAICompatible || m.ModelID != "gpt-4o-mini" ||
		m.APIKeyEnv != "OPENAI_API_KEY" || m.TimeoutMS != 1500 || len(m.Targets) != 1 || m.Targets[0].Weight != 3 {
		t.Fatalf("model config = %+v", m)
	}
	if len(reread.Tags) != 2 {
		t.Fatalf("tags = %v", reread.Tags)
	}

	// The listing honours the namespace and the status.
	filtered, err := rs.List(ctx, registry.Filter{Namespace: "default", Status: string(registry.StatusPending)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, got := range filtered {
		if got.ID == stored.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the pending record is missing from the filtered list (%d rows)", len(filtered))
	}
	// And it disappears from a status it is not in.
	other, err := rs.List(ctx, registry.Filter{Status: string(registry.StatusApproved)})
	if err != nil {
		t.Fatalf("list approved: %v", err)
	}
	for _, got := range other {
		if got.ID == stored.ID {
			t.Fatal("a pending record is listed as approved")
		}
	}

	if err := rs.SetHealth(ctx, stored.ID, registry.HealthHealthy); err != nil {
		t.Fatalf("set health: %v", err)
	}
	byKind, byHealth, err := rs.Counts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if byKind[string(registry.KindModel)] < 1 || byHealth[string(registry.HealthHealthy)] < 1 {
		t.Fatalf("counts = %v / %v", byKind, byHealth)
	}
	if n, err := rs.Size(ctx); err != nil || n < 1 {
		t.Fatalf("size = %d, %v", n, err)
	}
	if err := rs.Delete(ctx, stored.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := rs.Get(ctx, stored.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

// The dedup key is a real database constraint, not only a check in the facade:
// two writers racing is exactly the case the index settles.
func TestStoreRejectsADuplicateDedupKey(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()
	rs := NewResourceStore(db)

	name := uniqueName("store-dedup")
	first := &registry.Entry{ID: uniqueID("skill"), Kind: registry.KindSkill, Name: name, Version: "1.0.0", Namespace: "default", Health: registry.HealthUnknown}
	stored, err := rs.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, stored.ID) })

	if _, err := rs.Upsert(ctx, &registry.Entry{
		ID: uniqueID("skill"), Kind: registry.KindSkill, Name: name, Version: "1.0.0", Namespace: "default", Health: registry.HealthUnknown,
	}); !errors.Is(err, registry.ErrDuplicate) {
		t.Fatalf("duplicate = %v, want ErrDuplicate", err)
	}
	// Another version is a different record…
	second := &registry.Entry{ID: uniqueID("skill"), Kind: registry.KindSkill, Name: name, Version: "1.1.0", Namespace: "default", Health: registry.HealthUnknown}
	if _, err := rs.Upsert(ctx, second); err != nil {
		t.Fatalf("new version: %v", err)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, second.ID) })
	// …and so is the same name and version in another namespace.
	third := &registry.Entry{ID: uniqueID("skill"), Kind: registry.KindSkill, Name: name, Version: "1.0.0", Namespace: "other", Health: registry.HealthUnknown}
	if _, err := rs.Upsert(ctx, third); err != nil {
		t.Fatalf("other namespace: %v", err)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, third.ID) })
}

func TestStoreNamespaceCRUD(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()
	ns := NewNamespaceStore(db)

	// The migration seeds the default namespace, and it is not deletable.
	all, err := ns.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	hasDefault := false
	for _, n := range all {
		if n.ID == registry.DefaultNamespace {
			hasDefault = true
		}
	}
	if !hasDefault {
		t.Fatalf("default namespace missing: %d rows", len(all))
	}

	id := strings.ToLower(uniqueName("ns"))
	id = strings.ReplaceAll(id, ".", "-")
	created, err := ns.UpsertNamespace(ctx, &registry.Namespace{
		ID: id, Name: "Curated", Description: "needs a look", AutoApprove: false,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = ns.DeleteNamespace(ctx, id) })
	// A false boolean must come back false: this is the GORM zero value trap.
	if created.AutoApprove {
		t.Fatalf("auto_approve = %v, want false", created.AutoApprove)
	}

	got, err := ns.GetNamespace(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Curated" || got.AutoApprove {
		t.Fatalf("namespace = %+v", got)
	}

	// Updating keeps the id and flips the policy.
	got.AutoApprove = true
	if _, err := ns.UpsertNamespace(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = ns.GetNamespace(ctx, id)
	if err != nil {
		t.Fatalf("reget: %v", err)
	}
	if !got.AutoApprove {
		t.Fatal("auto_approve did not update")
	}

	if err := ns.DeleteNamespace(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ns.GetNamespace(ctx, id); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestStoreSourceCRUDAndSyncOutcome(t *testing.T) {
	db := openGovernanceDB(t)
	ctx := context.Background()
	ss := NewSourceStore(db)

	src := &registry.RegistrySource{
		ID:         "src-" + strings.ReplaceAll(uniqueName(""), ".", "-"),
		Namespace:  registry.DefaultNamespace,
		Kind:       registry.SourceKindMCP,
		Name:       "Store source",
		URL:        "https://registry.test/servers",
		AuthScheme: registry.AuthSchemeNone,
		Fixture:    true,
		Enabled:    true,
	}
	stored, err := ss.UpsertSource(ctx, src)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = ss.DeleteSource(ctx, stored.ID) })

	// A fresh source carries no sync outcome.
	if stored.LastSyncStatus != "" || stored.LastSyncCount != 0 || stored.LastSyncAt != nil {
		t.Fatalf("fresh source = %+v", stored)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := ss.MarkSync(ctx, stored.ID, registry.SyncResult{
		Status: registry.SyncStatusOK, Imported: 2, Updated: 1, Total: 3, At: now,
	}); err != nil {
		t.Fatalf("mark sync: %v", err)
	}
	got, err := ss.GetSource(ctx, stored.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastSyncStatus != registry.SyncStatusOK || got.LastSyncCount != 3 {
		t.Fatalf("sync outcome = %+v", got)
	}
	if got.LastSyncAt == nil || !got.LastSyncAt.Equal(now) {
		t.Fatalf("last sync at = %v, want %v", got.LastSyncAt, now)
	}

	// A failure is recorded as one, with the machine readable reason.
	if err := ss.MarkSync(ctx, stored.ID, registry.SyncResult{
		Status: registry.SyncStatusFailed, Error: "upstream_unreachable", At: now,
	}); err != nil {
		t.Fatalf("mark failure: %v", err)
	}
	got, err = ss.GetSource(ctx, stored.ID)
	if err != nil {
		t.Fatalf("reget: %v", err)
	}
	if got.LastSyncStatus != registry.SyncStatusFailed || got.LastSyncError != "upstream_unreachable" {
		t.Fatalf("failure outcome = %+v", got)
	}

	listed, err := ss.ListSources(ctx, registry.DefaultNamespace)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, s := range listed {
		if s.ID == stored.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("source missing from the listing (%d rows)", len(listed))
	}

	if err := ss.DeleteSource(ctx, stored.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ss.GetSource(ctx, stored.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
