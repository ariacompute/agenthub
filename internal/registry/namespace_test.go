package registry

import (
	"testing"

	"agenthub/internal/httputil"
)

func TestParseNamespaceID(t *testing.T) {
	// Empty collapses to the default namespace.
	id, err := ParseNamespaceID("")
	if err != nil || id != DefaultNamespace {
		t.Fatalf("ParseNamespaceID(\"\") = %q, %v; want %q", id, err, DefaultNamespace)
	}
	// Trimming + lower-casing.
	id, err = ParseNamespaceID("  Team-A  ")
	if err != nil || id != "team-a" {
		t.Fatalf("ParseNamespaceID trimmed = %q, %v; want team-a", id, err)
	}
	// An underscore is not a slug character, so it must be rejected.
	if _, err := ParseNamespaceID("team_a"); httputil.AsError(err).Code != CodeInvalidNamespace {
		t.Fatalf("ParseNamespaceID(team_a) code = %v, want %q", err, CodeInvalidNamespace)
	}
	// An empty prefix or a trailing dash is not a valid slug.
	if _, err := ParseNamespaceID("-team"); httputil.AsError(err).Code != CodeInvalidNamespace {
		t.Fatalf("ParseNamespaceID(-team) code = %v, want %q", err, CodeInvalidNamespace)
	}
}

func TestNamespaceCRUD(t *testing.T) {
	reg := New()

	ns, err := reg.UpsertNamespace(&Namespace{ID: "team", Name: "Team", AutoApprove: true})
	if err != nil {
		t.Fatalf("UpsertNamespace: %v", err)
	}
	if ns.ID != "team" || !ns.AutoApprove {
		t.Fatalf("namespace = %+v", ns)
	}
	// An empty name falls back to the id.
	if _, err := reg.UpsertNamespace(&Namespace{ID: "org"}); err != nil {
		t.Fatal(err)
	}
	got, ok := reg.GetNamespace("org")
	if !ok || got.Name != "org" {
		t.Fatalf("GetNamespace = %+v, %v", got, ok)
	}
	if _, ok := reg.GetNamespace("missing"); ok {
		t.Fatal("missing namespace should not be found")
	}

	// The default namespace is always present and listed first.
	list := reg.ListNamespaces()
	if len(list) < 2 || list[0].ID != DefaultNamespace {
		t.Fatalf("ListNamespaces[0] = %+v, want default first", list)
	}
	found := false
	for _, n := range list {
		if n.ID == "team" {
			found = true
		}
	}
	if !found {
		t.Fatal("team namespace missing from list")
	}
}

func TestNamespaceDelete(t *testing.T) {
	reg := New()
	if _, err := reg.UpsertNamespace(&Namespace{ID: "team"}); err != nil {
		t.Fatal(err)
	}
	// The default namespace is the fallback for records without one, so it is
	// permanently protected from deletion.
	if err := reg.DeleteNamespace(DefaultNamespace); httputil.AsError(err).Code != CodeInvalidNamespace {
		t.Fatalf("delete default code = %v, want %q", err, CodeInvalidNamespace)
	}
	if err := reg.DeleteNamespace("team"); err != nil {
		t.Fatalf("delete team: %v", err)
	}
	if _, ok := reg.GetNamespace("team"); ok {
		t.Fatal("team namespace should be gone")
	}
	if err := reg.DeleteNamespace("team"); httputil.AsError(err).Code != CodeNotFound {
		t.Fatalf("delete missing code = %v, want %q", err, CodeNotFound)
	}
}

func TestNamespaceAutoApproveAndDefaultStatus(t *testing.T) {
	reg := New()
	if _, err := reg.UpsertNamespace(&Namespace{ID: "auto", AutoApprove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.UpsertNamespace(&Namespace{ID: "curated", AutoApprove: false}); err != nil {
		t.Fatal(err)
	}
	if !reg.AutoApprove("auto") {
		t.Fatal("auto namespace must approve")
	}
	if reg.AutoApprove("curated") {
		t.Fatal("curated namespace must not auto-approve")
	}
	if reg.DefaultStatus("auto") != StatusApproved {
		t.Fatal("auto default status must be approved")
	}
	if reg.DefaultStatus("curated") != StatusDraft {
		t.Fatal("curated default status must be draft")
	}
	// An unknown namespace is permissive so legacy behaviour is preserved.
	if !reg.AutoApprove("unknown-namespace") {
		t.Fatal("unknown namespace must default to permissive")
	}
	// EnsureNamespace is idempotent and seeds the given id.
	reg.EnsureNamespace("seeded")
	if _, ok := reg.GetNamespace("seeded"); !ok {
		t.Fatal("EnsureNamespace must create the namespace")
	}
	reg.EnsureNamespace("seeded") // no-op second call
}

func TestUpsertNamespaceNilIsRejected(t *testing.T) {
	reg := New()
	if _, err := reg.UpsertNamespace(nil); httputil.AsError(err).Code != CodeInvalidNamespace {
		t.Fatalf("nil namespace code = %v, want %q", err, CodeInvalidNamespace)
	}
}
