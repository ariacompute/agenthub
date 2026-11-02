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
	// A nil entry is private too: IsPublic/EffectiveVisibility are called from the
	// store on rows that may not exist, and "no record" must never read as public.
	var nilEntry *Entry
	if nilEntry.IsPublic() || nilEntry.EffectiveVisibility() != VisibilityRestricted {
		t.Fatal("a nil entry must be restricted")
	}
}

// TestVisibilityValid pins the predicate the store uses when it writes the
// column: it is the last line of defence for a value that reached SQL without
// going through ParseVisibility (a hand-written UPDATE, a restore, a migration).
func TestVisibilityValid(t *testing.T) {
	if !VisibilityRestricted.Valid() || !VisibilityPublic.Valid() {
		t.Fatal("both known states must validate")
	}
	for _, raw := range []Visibility{"", "PUBLIC", "open", "internal", "restricted "} {
		if raw.Valid() {
			t.Fatalf("%q must not validate", raw)
		}
	}
}
