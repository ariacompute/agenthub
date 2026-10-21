package secure

import "testing"

func TestRoundTrip(t *testing.T) {
	Configure("a-strong-static-encryption-key-at-least-32-bytes", "")
	ct, err := EncryptDefault("hunter2-pii-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if ct == "hunter2-pii-value" {
		t.Fatal("ciphertext must not equal plaintext")
	}
	pt, err := DecryptDefault(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if pt != "hunter2-pii-value" {
		t.Fatalf("round trip mismatch: %q", pt)
	}
}

func TestKeyRotationFallback(t *testing.T) {
	// Ciphertext encrypted under the legacy key must still decrypt after the
	// preferred key is configured, so rotating keys never orphans old data.
	Configure("", "legacy-secret-value")
	old, err := Encrypt("value", "legacy-secret-value")
	if err != nil {
		t.Fatalf("encrypt legacy: %v", err)
	}
	Configure("new-preferred-key-at-least-32-bytes-long", "legacy-secret-value")
	pt, err := DecryptDefault(old)
	if err != nil {
		t.Fatalf("decrypt old with fallback: %v", err)
	}
	if pt != "value" {
		t.Fatalf("fallback decrypt mismatch: %q", pt)
	}
	if _, err := EncryptDefault("x"); err != nil {
		t.Fatalf("encrypt under preferred: %v", err)
	}
}
