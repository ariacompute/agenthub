package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"agenthub/internal/httputil"
)

// The console never persists a signing secret: it stores only the name of the
// environment variable that holds it at call time. These two errors capture the
// guardrails a public webhook enforces before any message is accepted.
var (
	errSigMissing = &httputil.Error{Code: httputil.CodeChannelSignatureMissing}
	errSigInvalid = &httputil.Error{Code: httputil.CodeChannelSignatureInvalid}
	errTsExpired  = &httputil.Error{Code: httputil.CodeChannelTimestampExpired}
)

// maxClockSkew bounds how old a signed timestamp may be; anything older is treated
// as a replay and refused.
const maxClockSkew = 5 * time.Minute

// Verify validates an inbound webhook signature for the given channel kind. The
// secret lives in the environment variable named by signingKeyEnv; it is read here,
// at verification time, and never cached. Replay windows and header schemes are
// per-kind.
func Verify(kind Kind, body []byte, r *http.Request, signingKeyEnv string) error {
	secret := os.Getenv(signingKeyEnv)
	switch kind {
	case KindSlack:
		return verifySlack(body, r, secret)
	case KindTeams:
		return verifyHMAC(r.Header.Get("Authorization"), body, secret)
	case KindTelegram:
		return verifyTelegram(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"), secret)
	case KindSMS:
		// SMS is an outbound-only transport in this build; inbound is a no-op.
		return errSigMissing
	default:
		return errSigInvalid
	}
}

// verifySlack checks the X-Slack-Signature header, which is
// "v0=hex(hmac-sha256("v0:" + timestamp + ":" + body))" with the timestamp carried
// in X-Slack-Request-Timestamp.
func verifySlack(body []byte, r *http.Request, secret string) error {
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if ts == "" || sig == "" {
		return errSigMissing
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errSigInvalid
	}
	if age := time.Since(time.Unix(secs, 0)); age > maxClockSkew || age < -maxClockSkew {
		return errTsExpired
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return errSigInvalid
	}
	return nil
}

// verifyHMAC supports the generic "HMAC <hex>" authorization scheme used by Teams
// and other providers that sign the raw body.
func verifyHMAC(header string, body []byte, secret string) error {
	if header == "" {
		return errSigMissing
	}
	if !strings.HasPrefix(header, "HMAC ") {
		return errSigInvalid
	}
	got, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, "HMAC ")))
	if err != nil || len(got) != sha256.Size {
		return errSigInvalid
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return errSigInvalid
	}
	return nil
}

// verifyTelegram compares the X-Telegram-Bot-Api-Secret-Token header against the
// secret in constant time.
func verifyTelegram(token string, secret string) error {
	if token == "" {
		return errSigMissing
	}
	if !hmac.Equal([]byte(token), []byte(secret)) {
		return errSigInvalid
	}
	return nil
}
