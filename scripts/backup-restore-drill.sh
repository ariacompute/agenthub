#!/usr/bin/env bash
#
# backup-restore-drill.sh — prove an agenthub backup can actually be restored.
#
# It picks the newest archive in the backup directory (plaintext .tar.gz or
# AES-256-GCM encrypted .enc), decrypts it when needed, unpacks the payload and
# restores it into a throwaway database (DRILL_DB_URL), then reports row counts
# for a few key tables. It never touches the live database.
#
# Prerequisites:
#   - pg_dump / pg_restore / psql on PATH (the pg_dump engine produces custom
#     format dumps; the COPY engine produces plain SQL).
#   - go on PATH (only needed for encrypted archives, to mirror the server's
#     native AES-256-GCM layout: nonce || ciphertext).
#
# Environment:
#   BACKUP_DIR        directory written by agenthub (default ./backups)
#   DRILL_DB_URL      a dedicated, EMPTY database to restore into
#   AGENTHUB_ENCRYPTION_KEY  the same key agenthub uses; required for .enc files
#
# Example:
#   BACKUP_DIR=/var/backups DRILL_DB_URL='postgres://postgres@localhost:5432/drill' \
#     AGENTHUB_ENCRYPTION_KEY="$(cat /run/secrets/backup_key)" \
#     ./scripts/backup-restore-drill.sh

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-./backups}"
DRILL_DB_URL="${DRILL_DB_URL:-}"
KEY="${AGENTHUB_ENCRYPTION_KEY:-}"

fail() { echo "drill: error: $*" >&2; exit 1; }

command -v pg_restore >/dev/null 2>&1 || fail "pg_restore not found on PATH"
command -v psql >/dev/null 2>&1 || fail "psql not found on PATH"
[ -d "$BACKUP_DIR" ] || fail "BACKUP_DIR $BACKUP_DIR does not exist"
[ -n "$DRILL_DB_URL" ] || fail "DRILL_DB_URL must be set to an empty drill database"

# --- newest archive ---------------------------------------------------------
latest="$(ls -1t "$BACKUP_DIR"/checkpoint-*.tar.gz "$BACKUP_DIR"/checkpoint-*.tar.gz.enc 2>/dev/null | head -n1)"
[ -n "$latest" ] || fail "no checkpoint archive found in $BACKUP_DIR"
echo "drill: using $latest"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

archive="$latest"
if [[ "$latest" == *.enc ]]; then
  [ -n "$KEY" ] || fail "archive is encrypted but AGENTHUB_ENCRYPTION_KEY is empty"
  command -v go >/dev/null 2>&1 || fail "go is required to decrypt an encrypted archive"
  plaintext="$work/archive.tar.gz"
  # Mirror internal/backup crypto.go: AES-256-GCM, output = nonce || ciphertext.
  cat > "$work/dec.go" <<'GO'
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"os"
)

func main() {
	key := sha256.Sum256([]byte(os.Args[1]))
	in, err := os.ReadFile(os.Args[2])
	if err != nil { panic(err) }
	block, err := aes.NewCipher(key[:])
	if err != nil { panic(err) }
	gcm, err := cipher.NewGCM(block)
	if err != nil { panic(err) }
	ns := gcm.NonceSize()
	if len(in) < ns { panic("ciphertext too short") }
	nonce, ct := in[:ns], in[ns:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil { panic(err) }
	if err := os.WriteFile(os.Args[3], plain, 0o600); err != nil { panic(err) }
}
GO
  (cd "$work" && go run dec.go "$KEY" "$latest" "$plaintext") || fail "decryption failed"
  archive="$plaintext"
fi

# --- unpack the payload -----------------------------------------------------
tar -xzf "$archive" -C "$work" data manifest.json || fail "could not unpack archive"
payload="$work/data"

# --- restore into the throwaway database ------------------------------------
if head -c5 "$payload" | grep -q PGDMP; then
  pg_restore --clean --if-exists -d "$DRILL_DB_URL" "$payload" \
    || fail "pg_restore failed; check DRILL_DB_URL points at an empty database"
else
  psql "$DRILL_DB_URL" -v ON_ERROR_STOP=1 -f "$payload" \
    || fail "psql restore failed; check DRILL_DB_URL points at an empty database"
fi
echo "drill: restore succeeded"

# --- report row counts for key tables ---------------------------------------
psql "$DRILL_DB_URL" -t -A -c "
SELECT 'users',        COUNT(*) FROM users;
SELECT 'sessions',     COUNT(*) FROM sessions;
SELECT 'resources',    COUNT(*) FROM resources;
SELECT 'registries',   COUNT(*) FROM registries;
SELECT 'tasks',        COUNT(*) FROM tasks;
SELECT 'events',       COUNT(*) FROM events;
SELECT 'oauth_accounts', COUNT(*) FROM oauth_accounts;
" 2>/dev/null | sed 's/^/drill: /' || true

echo "drill: OK — backup $latest is restorable"
