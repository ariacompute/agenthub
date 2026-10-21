// Package secure provides small symmetric encryption helpers used to protect
// sensitive at-rest values (e.g. future provider tokens or PII columns) with a
// key derived from the configured AGENTHUB_ENCRYPTION_KEY. It intentionally
// avoids heavy dependencies and mirrors the serve project's design so the two
// share one mental model.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log"
)

// preferredKey and legacyKey hold the static-at-rest encryption keys. When
// AGENTHUB_ENCRYPTION_KEY is configured we derive from it (preferred); otherwise
// we fall back to deriving from the session secret (legacy). Decryption tries the
// preferred key first and falls back to the legacy key so ciphertext encrypted
// before a key rotation stays readable.
var (
	preferredKey string
	legacyKey    string
)

// Configure sets the encryption keys. Call once at startup with the configured
// AGENTHUB_ENCRYPTION_KEY (may be empty) and session secret (always present).
// When no dedicated key is provided, a warning is logged: static secrets are then
// protected only by the session secret.
func Configure(preferred, legacy string) {
	preferredKey = preferred
	legacyKey = legacy
	if preferred == "" {
		log.Println("warning: AGENTHUB_ENCRYPTION_KEY not set; deriving static encryption key from the session secret (set AGENTHUB_ENCRYPTION_KEY for key isolation)")
	}
}

func deriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// EncryptDefault seals plaintext with the preferred key when configured,
// otherwise the legacy key. See Configure.
func EncryptDefault(plaintext string) (string, error) {
	if preferredKey != "" {
		return Encrypt(plaintext, preferredKey)
	}
	return Encrypt(plaintext, legacyKey)
}

// DecryptDefault reverses EncryptDefault, trying the preferred key and falling
// back to the legacy key for backward-compatible decryption of old ciphertext.
func DecryptDefault(encoded string) (string, error) {
	if preferredKey != "" {
		if pt, err := Decrypt(encoded, preferredKey); err == nil {
			return pt, nil
		}
	}
	return Decrypt(encoded, legacyKey)
}

// Encrypt seals plaintext with AES-256-GCM using a key derived from secret. The
// returned value is base64(nonce || ciphertext).
func Encrypt(plaintext, secret string) (string, error) {
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func Decrypt(encoded, secret string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := data[:ns], data[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
