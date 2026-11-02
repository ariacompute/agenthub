package registry

import "testing"

// TestParseVisibilityAcceptsTheTwoStates is the happy path.
func TestParseVisibilityAcceptsTheTwoStates(t *testing.T) {
	for _, raw := range []string{"public", "PUBLIC", " public ", "restricted", "Restricted"} {
		if _, err := ParseVisibility(raw); err != nil {
			t.Fatalf("ParseVisibility(%q): %v", raw, err)
		}
	}
}

// TestParseVisibilityRejectsAnythingElse: a typo must not silently become one of
// the two states, because "restricted" and "public" are the whole authorization
// decision.
func TestParseVisibilityRejectsAnythingElse(t *testing.T) {
	for _, raw := range []string{"", "open", "private", "true", "1"} {
		if _, err := ParseVisibility(raw); err == nil {
			t.Fatalf("ParseVisibility(%q) accepted an unknown value", raw)
		}
	}
}

// TestEffectiveVisibilityDefaultsToRestricted is the fail-closed rule: a record
// written before the column existed, or by a client that sent nothing, is
// private.
func TestEffectiveVisibilityDefaultsToRestricted(t *testing.T) {
	if got := (&Entry{}).EffectiveVisibility(); got != VisibilityRestricted {
		t.Fatalf("default visibility = %q, want restricted", got)
	}
	if (&Entry{}).IsPublic() {
		t.Fatal("an entry without visibility must not be public")
	}
	if !(&Entry{Visibility: VisibilityPublic}).IsPublic() {
		t.Fatal("an explicitly published entry must be public")
	}
}
