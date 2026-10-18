package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Password hashing parameters. PBKDF2-HMAC-SHA256 is not the newest choice but
// it is the strongest KDF the standard library offers without pulling in a
// dependency, and agenthub deliberately keeps GORM as its only production
// dependency. The parameters travel with the hash, so raising them later is a
// matter of writing new hashes while old ones keep verifying.
const (
	pwIterations = 210_000
	pwKeyLen     = 32
	pwSaltLen    = 16
)

// MinPasswordLength is the only strength rule enforced server side; it is
// surfaced to the client as the weak_password code.
const MinPasswordLength = 8

var (
	// ErrInvalidCredentials is returned for both unknown emails and wrong
	// passwords so the response cannot be used to enumerate accounts.
	ErrInvalidCredentials = errors.New("invalid_credentials")
	// ErrWeakPassword rejects too short passwords at the boundary.
	ErrWeakPassword = errors.New("weak_password")
	// ErrNoPassword is returned when an OAuth-only account tries to log in with
	// a password.
	ErrNoPassword = errors.New("no_password_set")
)

// HashPassword derives an encoded hash from a plaintext password. The encoding
// is "pbkdf2-sha256$<iterations>$<salt>$<key>", all base64 raw-url.
func HashPassword(password string) (string, error) {
	salt := make([]byte, pwSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := pbkdf2([]byte(password), salt, pwIterations, pwKeyLen)
	return strings.Join([]string{
		"pbkdf2-sha256",
		strconv.Itoa(pwIterations),
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword reports whether password matches an encoded hash produced by
// HashPassword. Comparison is constant time.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters <= 0 {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2([]byte(password), salt, iters, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CheckPassword returns ErrWeakPassword for passwords that are too short.
func CheckPassword(password string) error {
	if len(password) < MinPasswordLength {
		return ErrWeakPassword
	}
	return nil
}

// pbkdf2 is PBKDF2-HMAC-SHA256 (RFC 8018) with the standard library only.
func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	mac := hmac.New(sha256.New, password)
	hLen := mac.Size()
	numBlocks := (keyLen + hLen - 1) / hLen
	var buf [4]byte
	out := make([]byte, 0, numBlocks*hLen)
	block := make([]byte, hLen)
	for i := 1; i <= numBlocks; i++ {
		mac.Reset()
		mac.Write(salt)
		buf[0] = byte(i >> 24)
		buf[1] = byte(i >> 16)
		buf[2] = byte(i >> 8)
		buf[3] = byte(i)
		mac.Write(buf[:])
		copy(block, mac.Sum(nil))
		for j := 1; j < iter; j++ {
			mac.Reset()
			mac.Write(block)
			mac.Sum(block[:0])
		}
		out = append(out, block...)
	}
	return out[:keyLen]
}
