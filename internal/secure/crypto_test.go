package secure

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

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

// An at-rest value that cannot be read is a hard error, never an empty string:
// a silent "" would look like a legitimately empty column.
func TestDecryptRejectsMalformedCiphertext(t *testing.T) {
	Configure("a-strong-static-encryption-key-at-least-32-bytes", "legacy")

	cases := map[string]string{
		"not base64":         "!!!not-base64!!!",
		"shorter than nonce": base64.StdEncoding.EncodeToString([]byte("abc")),
	}
	for name, encoded := range cases {
		if pt, err := Decrypt(encoded, "a-strong-static-encryption-key-at-least-32-bytes"); err == nil {
			t.Fatalf("%s: err = nil, got %q", name, pt)
		}
		if _, err := DecryptDefault(encoded); err == nil {
			t.Fatalf("%s: DecryptDefault must fail too", name)
		}
	}
}

func TestDecryptRejectsATamperedCiphertext(t *testing.T) {
	const key = "a-strong-static-encryption-key-at-least-32-bytes"
	Configure(key, "")
	ct, err := Encrypt("value", key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Flip the last byte: GCM must refuse it rather than return garbage.
	raw[len(raw)-1] ^= 0xff
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, err := Decrypt(tampered, key); err == nil {
		t.Fatal("a tampered ciphertext must fail to decrypt")
	}
}

func TestDecryptWithTheWrongKeyFails(t *testing.T) {
	const key = "a-strong-static-encryption-key-at-least-32-bytes"
	ct, err := Encrypt("value", key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if pt, err := Decrypt(ct, "some-other-key"); err == nil {
		t.Fatalf("decrypt with the wrong key returned %q", pt)
	}
}

// failingReader stands in for a broken entropy source. crypto/rand.Reader is a
// package variable precisely so a caller can do this, and a test is such a
// caller.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestEncryptReportsAFailingRandomSource(t *testing.T) {
	orig := rand.Reader
	rand.Reader = failingReader{}
	defer func() { rand.Reader = orig }()

	if ct, err := Encrypt("value", "key"); err == nil {
		t.Fatalf("encrypt without entropy returned %q", ct)
	}
	if ct, err := EncryptDefault("value"); err == nil {
		t.Fatalf("encrypt default without entropy returned %q", ct)
	}
}
