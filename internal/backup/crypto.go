package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

// backupKey derives the 32-byte AES key from the provided secret. Reusing one
// derivation keeps the backup encryption consistent with the rest of the
// project's "sha256 of the configured secret" convention.
func backupKey(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// EncryptFile encrypts the file at src into dst with AES-256-GCM. The output is
// nonce || ciphertext || tag, so the whole archive is opaque at rest and a stolen
// backup volume yields nothing without the key.
func EncryptFile(src, dst string, secret []byte) error {
	plain, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(backupKey(secret))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := out.Write(nonce); err != nil {
		return err
	}
	if _, err := out.Write(ciphertext); err != nil {
		return err
	}
	return out.Sync()
}

// DecryptFile reverses EncryptFile.
func DecryptFile(src, dst string, secret []byte) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(backupKey(secret))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return errors.New("backup: encrypted file too short")
	}
	nonce, ct := data[:ns], data[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, plain, 0o600)
}
