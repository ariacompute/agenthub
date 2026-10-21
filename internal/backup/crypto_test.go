package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain.bin")
	enc := filepath.Join(dir, "plain.bin.enc")
	dec := filepath.Join(dir, "plain.bin.dec")
	content := []byte("agenthub backup payload — secret rows")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}
	key := []byte("a-static-encryption-key-at-least-32-bytes-long")
	if err := EncryptFile(src, enc, key); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// The ciphertext must differ from the plaintext.
	ct, err := os.ReadFile(enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(ct) == string(content) {
		t.Fatal("ciphertext must not equal plaintext")
	}
	if err := DecryptFile(enc, dec, key); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	got, err := os.ReadFile(dec)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("round trip mismatch: %q", got)
	}
	// A wrong key must fail to open.
	if err := DecryptFile(enc, dec, []byte("a-different-key-but-still-at-least-32-b")); err == nil {
		t.Fatal("decrypt with wrong key must fail")
	}
}
