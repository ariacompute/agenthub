package computers

import "testing"

func TestNormalizeKind(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{"browser", KindBrowser},
		{"shell", KindShell},
		{"screen", KindScreen},
		{"Browser", ""}, // case-sensitive, anything unknown falls back to empty
		{"", ""},
		{"telepathy", ""},
	}
	for _, c := range cases {
		if got := NormalizeKind(c.in); got != c.want {
			t.Errorf("NormalizeKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if len(Kinds) != 3 {
		t.Errorf("Kinds = %d, want 3", len(Kinds))
	}
}
