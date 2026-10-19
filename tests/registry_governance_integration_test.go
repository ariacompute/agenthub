package tests

import (
	"context"
	"errors"
	"testing"

	"agenthub/internal/registry"
	"agenthub/internal/store"
)

// Governance integration: namespaces, the dedup key, the per-kind descriptor and
// the upstream source table against a real PostgreSQL.
//
// Gating rule is the same as the rest of this package: without DATABASE_URL the
// cases skip, and no result is ever fabricated.

func TestIntegrationNamespaceCRUD(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	ns := store.NewNamespaceStore(db)

	// The migration seeds the default namespace; listing must always show it.
	all, err := ns.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, n := range all {
		if n.ID == registry.DefaultNamespace {
			found = true
		}
	}
	if !found {
		t.Fatalf("default namespace missing: %+v", all)
	}

	id := "it-ns-" + registry.NewID("ns")
	created, err := ns.UpsertNamespace(ctx, &registry.Namespace{
		ID: id, Name: "Integration namespace", Description: "created by a test", AutoApprove: false,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created.AutoApprove {
		t.Fatalf("auto_approve did not round trip: %+v", created)
	}
	got, err := ns.GetNamespace(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Integration namespace" || got.AutoApprove {
		t.Fatalf("get = %+v", got)
	}
	if _, err := ns.GetNamespace(ctx, "it-ns-does-not-exist"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("missing namespace = %v, want ErrNotFound", err)
	}
	if err := ns.DeleteNamespace(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := ns.DeleteNamespace(ctx, id); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestIntegrationDedupKeyAndGovernanceColumns(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	rs := store.NewResourceStore(db)

	base := "it-gov-" + registry.NewID("res")
	entry := &registry.Entry{
		ID:          base,
		Kind:        registry.KindModel,
		Name:        "governed-model",
		Version:     "1.0.0",
		Namespace:   "default",
		Status:      registry.StatusDraft,
		DisplayName: "Governed model",
		SourceID:    "src-it",
		Health:      registry.HealthHealthy,
		KindConfig: &registry.KindConfig{Model: &registry.ModelConfig{
			ProviderMode: registry.ProviderModeOpenAICompatible,
			Endpoint:     "https://router.test/v1",
			ModelID:      "gpt-4o-mini",
			Targets:      []registry.ModelTarget{{Name: "fallback", Model: "gpt-4o", Weight: 2}},
			TimeoutMS:    5000,
		}},
	}
	stored, err := rs.Upsert(ctx, entry)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if stored.ID != base {
		t.Fatalf("upsert returned id %q, want %q", stored.ID, base)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, base) })

	got, err := rs.Get(ctx, base)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Every governance column has to survive the jsonb and text round trip.
	if got.Namespace != "default" || got.Status != registry.StatusDraft {
		t.Fatalf("namespace/status = %q/%q", got.Namespace, got.Status)
	}
	if got.DisplayName != "Governed model" || got.SourceID != "src-it" {
		t.Fatalf("display/source = %q/%q", got.DisplayName, got.SourceID)
	}
	if got.KindConfig == nil || got.KindConfig.Model == nil {
		t.Fatalf("kind_config did not round trip: %+v", got.KindConfig)
	}
	model := got.KindConfig.Model
	if model.ProviderMode != registry.ProviderModeOpenAICompatible || model.ModelID != "gpt-4o-mini" {
		t.Fatalf("model config = %+v", model)
	}
	if len(model.Targets) != 1 || model.Targets[0].Model != "gpt-4o" || model.Targets[0].Weight != 2 {
		t.Fatalf("targets = %+v", model.Targets)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	// (namespace, name, version) is the dedup key: the same name at a new
	// version is a different record, the same name at the same version is not.
	nextVersion := *entry
	nextVersion.ID = base + "-v2"
	nextVersion.Version = "2.0.0"
	if _, err := rs.Upsert(ctx, &nextVersion); err != nil {
		t.Fatalf("new version: %v", err)
	}
	t.Cleanup(func() { _ = rs.Delete(ctx, nextVersion.ID) })

	clash := *entry
	clash.ID = base + "-clash"
	if _, err := rs.Upsert(ctx, &clash); !errors.Is(err, registry.ErrDuplicate) {
		t.Fatalf("duplicate = %v, want ErrDuplicate", err)
	}
}

func TestIntegrationSourceCRUDAndSyncOutcome(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	ss := store.NewSourceStore(db)

	id := "src-it-" + registry.NewID("src")
	created, err := ss.UpsertSource(ctx, &registry.RegistrySource{
		ID:         id,
		Namespace:  registry.DefaultNamespace,
		Kind:       registry.SourceKindMCP,
		Name:       "Integration source",
		URL:        "https://registry.test/servers",
		AuthScheme: registry.AuthSchemeBearer,
		// Only the variable name is stored, never the credential itself.
		AuthTokenEnv: "AGENTHUB_TEST_UPSTREAM_TOKEN",
		Fixture:      true,
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = ss.DeleteSource(ctx, id) })

	if created.AuthScheme != registry.AuthSchemeBearer || !created.Fixture {
		t.Fatalf("created = %+v", created)
	}
	got, err := ss.GetSource(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LastSyncStatus != "" {
		t.Fatalf("a fresh source claims a sync: %q", got.LastSyncStatus)
	}

	if err := ss.MarkSync(ctx, id, registry.SyncResult{
		Status:   registry.SyncStatusOK,
		Imported: 2,
		Updated:  1,
	}); err != nil {
		t.Fatalf("mark sync: %v", err)
	}
	got, err = ss.GetSource(ctx, id)
	if err != nil {
		t.Fatalf("get after sync: %v", err)
	}
	if got.LastSyncStatus != registry.SyncStatusOK || got.LastSyncCount != 3 || got.LastSyncAt == nil {
		t.Fatalf("after sync = %+v", got)
	}

	listed, err := ss.ListSources(ctx, registry.DefaultNamespace)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := false
	for _, s := range listed {
		if s.ID == id {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("source %s missing from the namespace listing", id)
	}

	if err := ss.DeleteSource(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := ss.DeleteSource(ctx, id); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

// TestIntegrationRegistryFacadeIsStoreBacked proves the in-memory facade and the
// PostgreSQL one agree: the same registry methods, the same dedup key and the
// same curation rules, so a unit test that is green cannot hide a behaviour that
// only differs in production.
func TestIntegrationRegistryFacadeIsStoreBacked(t *testing.T) {
	db := openDBReal(t)
	ctx := context.Background()
	reg := registry.NewWithStores(store.NewResourceStore(db), store.NewNamespaceStore(db))
	if err := reg.LoadNamespaces(ctx); err != nil {
		t.Fatalf("load namespaces: %v", err)
	}

	if _, err := reg.UpsertNamespace(&registry.Namespace{ID: "it-curated", AutoApprove: false}); err != nil {
		t.Fatalf("upsert namespace: %v", err)
	}
	t.Cleanup(func() { _ = reg.DeleteNamespace("it-curated") })

	name := "facade-" + registry.NewID("res")
	entry, err := reg.Upsert(&registry.Entry{Kind: registry.KindSkill, Name: name, Version: "1.0.0", Namespace: "it-curated"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	t.Cleanup(func() { _ = reg.Delete(entry.ID) })
	if entry.Status != registry.StatusDraft {
		t.Fatalf("status = %q, want draft", entry.Status)
	}
	// An unapproved record is absent from the discoverable catalogue...
	if servers := registry.MCPServerDescriptionsOf(reg.ListFiltered(registry.Filter{Namespace: "it-curated"})); len(servers) != 0 {
		t.Fatalf("draft leaked into the export: %+v", servers)
	}
	// ...and approving it is a two step walk.
	if _, err := reg.SetStatus(entry.ID, registry.StatusApproved); err == nil {
		t.Fatal("draft -> approved must be refused")
	}
	if _, err := reg.SetStatus(entry.ID, registry.StatusPending); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := reg.SetStatus(entry.ID, registry.StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if servers := registry.MCPServerDescriptionsOf(reg.ListFiltered(registry.Filter{Namespace: "it-curated"})); len(servers) != 1 {
		t.Fatalf("approved record missing from the export: %+v", servers)
	}
	// The dedup key is enforced through the store as well.
	if _, err := reg.Upsert(&registry.Entry{Kind: registry.KindSkill, Name: name, Version: "1.0.0", Namespace: "it-curated"}); err == nil {
		t.Fatal("duplicate (namespace, name, version) must be rejected")
	}
}
