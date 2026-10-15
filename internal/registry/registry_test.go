package registry

import (
	"testing"

	"agenthub/internal/httputil"
)

func sampleEntry(kind Kind, name string) *Entry {
	return &Entry{Kind: kind, Name: name, Endpoint: "https://example.test", Health: HealthHealthy}
}

func TestUpsertGeneratesIDAndDefaults(t *testing.T) {
	reg := New()
	entry, err := reg.Upsert(sampleEntry(KindMCP, "hr-mcp"))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if entry.ID == "" || entry.Health != HealthHealthy || entry.CreatedAt.IsZero() {
		t.Fatalf("entry = %+v", entry)
	}
	if reg.Size() != 1 {
		t.Fatalf("size = %d", reg.Size())
	}
}

func TestUpsertRejectsInvalidEntries(t *testing.T) {
	reg := New()
	cases := []struct {
		name  string
		entry *Entry
		code  httputil.Code
	}{
		{"nil entry", nil, CodeInvalidEntry},
		{"bad kind", &Entry{Kind: "bogus", Name: "x"}, CodeInvalidKind},
		{"missing name", &Entry{Kind: KindModel}, CodeInvalidEntry},
		{"bad transport", &Entry{Kind: KindMCP, Name: "x", Transport: "carrier-pigeon"}, CodeInvalidTransport},
		{"bad health", &Entry{Kind: KindModel, Name: "x", Health: "meh"}, CodeInvalidHealth},
	}
	for _, c := range cases {
		if _, err := reg.Upsert(c.entry); err == nil {
			t.Fatalf("%s: expected error", c.name)
		} else if got := httputil.AsError(err).Code; got != c.code {
			t.Fatalf("%s: code = %q, want %q", c.name, got, c.code)
		}
	}
}

func TestCRUDAndFilters(t *testing.T) {
	reg := New()
	model, _ := reg.Upsert(sampleEntry(KindModel, "afm"))
	if _, err := reg.Upsert(sampleEntry(KindCLI, "git")); err != nil {
		t.Fatal(err)
	}

	if got := reg.List(string(KindModel), ""); len(got) != 1 || got[0].ID != model.ID {
		t.Fatalf("kind filter = %+v", got)
	}
	if got := reg.List("", "git"); len(got) != 1 {
		t.Fatalf("query filter = %+v", got)
	}
	if got := reg.List("", "nothing-matches"); len(got) != 0 {
		t.Fatalf("empty query should match nothing, got %+v", got)
	}
	if got := reg.List("bogus", ""); got != nil {
		t.Fatalf("invalid kind should return nil, got %+v", got)
	}

	if err := reg.Delete(model.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := reg.Delete(model.ID); httputil.AsError(err).Code != CodeNotFound {
		t.Fatalf("second delete = %v", err)
	}
	if _, ok := reg.Get(model.ID); ok {
		t.Fatal("deleted entry still present")
	}
}

func TestRegisterRejectsDuplicateID(t *testing.T) {
	reg := New()
	first, _ := reg.Upsert(sampleEntry(KindAgent, "aria"))
	if _, err := reg.Register(&Entry{ID: first.ID, Kind: KindAgent, Name: "other"}); httputil.AsError(err).Code != CodeDuplicate {
		t.Fatalf("duplicate register = %v", err)
	}
}

func TestSetHealthAndCounts(t *testing.T) {
	reg := New()
	entry, _ := reg.Upsert(sampleEntry(KindSkill, "sop"))
	if err := reg.SetHealth(entry.ID, HealthDegraded); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}
	if got := reg.HealthCounts()[string(HealthDegraded)]; got != 1 {
		t.Fatalf("health counts = %+v", reg.HealthCounts())
	}
	if err := reg.SetHealth(entry.ID, Health("bogus")); httputil.AsError(err).Code != CodeInvalidHealth {
		t.Fatalf("invalid health = %v", err)
	}
	if err := reg.SetHealth("missing", HealthHealthy); httputil.AsError(err).Code != CodeNotFound {
		t.Fatalf("missing entry = %v", err)
	}
	if got := reg.CountsByKind()[string(KindSkill)]; got != 1 {
		t.Fatalf("kind counts = %+v", reg.CountsByKind())
	}
}

func TestExportAgentCardAndMCP(t *testing.T) {
	reg := New()
	if _, err := reg.Upsert(&Entry{Kind: KindModel, Name: "afm", Transport: "", Tags: []string{"llm"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Upsert(&Entry{Kind: KindMCP, Name: "hr-mcp", Transport: TransportStreamableHTTP}); err != nil {
		t.Fatal(err)
	}
	card := AgentCard(reg, "http://localhost:3500", "0.1.0", "0.2.0")
	if len(card.Skills) != 2 {
		t.Fatalf("skills = %+v", card.Skills)
	}
	if !card.Capabilities.Streaming || !card.Capabilities.StateTransitionHistory {
		t.Fatalf("capabilities = %+v", card.Capabilities)
	}
	servers := MCPServerDescriptions(reg)
	if len(servers) != 2 {
		t.Fatalf("servers = %+v", servers)
	}
}
