package secure

import "testing"

// With no dedicated key configured the legacy key is the only one there is:
// EncryptDefault must seal under it, not fail or produce an empty string.
func TestEncryptDefaultFallsBackToTheLegacyKey(t *testing.T) {
	Configure("", "legacy-secret-value")
	ct, err := EncryptDefault("value")
	if err != nil {
		t.Fatalf("encrypt default: %v", err)
	}
	pt, err := DecryptDefault(ct)
	if err != nil {
		t.Fatalf("decrypt default: %v", err)
	}
	if pt != "value" {
		t.Fatalf("round trip = %q", pt)
	}

	// The ciphertext must be the legacy key's, which is what a rotation later
	// relies on when it falls back for old rows.
	want, err := Decrypt(ct, "legacy-secret-value")
	if err != nil || want != "value" {
		t.Fatalf("ciphertext was not sealed with the legacy key: %q (%v)", want, err)
	}
}
