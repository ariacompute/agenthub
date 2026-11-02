package registry

import (
	"strings"
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

// WithModelRouterMode carries the instance default into Upsert, so a record is
// validated the way the instance actually intends to run it.
func TestRegistryModelRouterModeReachesValidation(t *testing.T) {
	entry := func() *Entry {
		return &Entry{Kind: KindModel, Name: "m", Version: "1.0.0",
			KindConfig: &KindConfig{Model: &ModelConfig{ModelID: "gpt-4o-mini"}}}
	}

	if _, err := New().Upsert(entry()); err != nil {
		t.Fatalf("default instance: %v", err)
	}
	if _, err := New().WithModelRouterMode(ProviderModeOpenAICompatible).Upsert(entry()); err == nil {
		t.Fatal("an openai_compatible instance must reject a model without an endpoint")
	}
	// An unknown mode must degrade to the offline default rather than propagate.
	if _, err := New().WithModelRouterMode("anthropic").Upsert(entry()); err != nil {
		t.Fatalf("unknown mode must degrade to the offline default: %v", err)
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
	card := AgentCard(reg, "http://localhost:3500", "0.1.0", "1.0.0")
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

func TestNormalizePersona(t *testing.T) {
	// A nil or all-empty persona stays nil so the wire shape stays compact.
	if normalizePersona(nil) != nil {
		t.Fatal("nil persona must stay nil")
	}
	if normalizePersona(&Persona{}) != nil {
		t.Fatal("empty persona must become nil")
	}

	// Whitespace is trimmed and a valid http(s) avatar is kept.
	p := normalizePersona(&Persona{DisplayName: "  Bob  ", Title: "Dr", AvatarURL: "https://cdn.example.com/bob.png"})
	if p == nil || p.DisplayName != "Bob" || p.Title != "Dr" || p.AvatarURL != "https://cdn.example.com/bob.png" {
		t.Fatalf("trim/keep = %+v", p)
	}

	// A non-http avatar (or garbage) makes the whole persona invalid, so callers
	// must fix the value rather than accidentally ship an xss-risky URL.
	if p := normalizePersona(&Persona{AvatarURL: "javascript:alert(1)"}); p != nil {
		t.Fatalf("bad avatar should invalidate the persona, got %+v", p)
	}

	// Overlong fields are capped at 256 runes.
	long := strings.Repeat("x", 300)
	p = normalizePersona(&Persona{DisplayName: long})
	if p == nil || len(p.DisplayName) != 256 {
		t.Fatalf("display name length = %d, want 256", len(p.DisplayName))
	}
}

// TestNormalizePersonaKeepsTheConsoleFields covers the four fields the desktop
// editor sends (F10.2). Before they existed the server dropped them on the floor:
// the form looked like it saved, and the value was gone after the next reload.
func TestNormalizePersonaKeepsTheConsoleFields(t *testing.T) {
	p := normalizePersona(&Persona{
		Role:        "  Research lead  ",
		Instruction: "  always cite sources  ",
		MBTI:        " intj ",
		AvatarRef:   "  avatar-2  ",
	})
	if p == nil {
		t.Fatal("a persona made only of console fields must be kept")
	}
	if p.Role != "Research lead" || p.Instruction != "always cite sources" || p.AvatarRef != "avatar-2" {
		t.Fatalf("trim = %+v", p)
	}
	if p.MBTI != "INTJ" {
		t.Fatalf("MBTI = %q, want the upper-cased known type", p.MBTI)
	}
}

func TestNormalizePersonaClearsAnInventedMBTI(t *testing.T) {
	// The console renders a fixed badge per type, so an unknown code has to be
	// dropped rather than stored: a badge nobody can label is worse than none.
	for _, code := range []string{"XXXX", "INFP-T", "int", "🎉"} {
		if got := NormalizeMBTI(code); got != "" {
			t.Fatalf("NormalizeMBTI(%q) = %q, want empty", code, got)
		}
	}
	for _, code := range MBTITypes {
		if got := NormalizeMBTI(strings.ToLower(code)); got != code {
			t.Fatalf("NormalizeMBTI(%q) = %q, want %q", strings.ToLower(code), got, code)
		}
	}
	if p := normalizePersona(&Persona{MBTI: "NOPE"}); p != nil {
		t.Fatalf("a persona with only an invalid type must become nil, got %+v", p)
	}
}

func TestNormalizePersonaCapsTheInstruction(t *testing.T) {
	// The instruction is a paragraph, not a label, so it gets its own limit: a
	// shared 256 byte cap would cut a real instruction in half.
	long := strings.Repeat("y", maxInstructionLen+50)
	p := normalizePersona(&Persona{Instruction: long})
	if p == nil || len(p.Instruction) != maxInstructionLen {
		t.Fatalf("instruction length = %d, want %d", len(p.Instruction), maxInstructionLen)
	}
}

func TestNormalizeConnectorPolicy(t *testing.T) {
	cases := map[string]string{
		"allowed":   ConnectorPolicyAllowed,
		"ask_first": ConnectorPolicyAskFirst,
		"off":       ConnectorPolicyOff,
		"":          ConnectorPolicyOff,
		"bogus":     ConnectorPolicyOff,
		"ALLOWED":   ConnectorPolicyAllowed,
	}
	for in, want := range cases {
		if got := NormalizeConnectorPolicy(in); got != want {
			t.Fatalf("NormalizeConnectorPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}
