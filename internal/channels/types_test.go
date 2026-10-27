package channels

import "testing"

func TestNormalizeKind(t *testing.T) {
	for _, k := range []Kind{KindSlack, KindTeams, KindTelegram, KindSMS} {
		if got := NormalizeKind(string(k)); got != k {
			t.Fatalf("NormalizeKind(%q) = %q, want %q", k, got, k)
		}
	}
	// Matching is an exact, case-sensitive comparison against the canonical set.
	if got := NormalizeKind("SLACK"); got != Kind("") {
		t.Fatalf("NormalizeKind(\"SLACK\") = %q, want empty (case-insensitive matching is intentional)", got)
	}
	// Anything unrecognised collapses to the empty kind so a malformed inbound
	// request is rejected rather than silently coerced.
	if got := NormalizeKind("bogus"); got != Kind("") {
		t.Fatalf("NormalizeKind(\"bogus\") = %q, want empty", got)
	}
	if got := NormalizeKind(""); got != Kind("") {
		t.Fatalf("NormalizeKind(\"\") = %q, want empty", got)
	}
}

func TestStatusesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []string{StatusReceived, StatusDelivered, StatusFailed} {
		if seen[s] {
			t.Fatalf("duplicate status %q", s)
		}
		seen[s] = true
	}
}
