package channels

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

// slackSig builds the X-Slack-Signature value: hex(hmac-sha256("v0:ts:body")).
func slackSig(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// hmacHex builds the generic "HMAC <hex>" authorization value.
func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "HMAC " + hex.EncodeToString(mac.Sum(nil))
}

func postWith(body []byte, header, value string) *http.Request {
	req, _ := http.NewRequest("POST", "/x", bytes.NewReader(body))
	if header != "" {
		req.Header.Set(header, value)
	}
	return req
}

func TestVerifySlack(t *testing.T) {
	t.Setenv("CH_S", "shh")
	body := []byte(`{"text":"hi","event_id":"e1"}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	// Valid signature passes.
	req := postWith(body, "", "")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSig("shh", ts, body))
	if err := Verify(KindSlack, body, req, "CH_S"); err != nil {
		t.Fatalf("valid slack signature rejected: %v", err)
	}

	// Wrong secret is rejected as an invalid signature.
	req.Header.Set("X-Slack-Signature", slackSig("wrong", ts, body))
	if err := Verify(KindSlack, body, req, "CH_S"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureInvalid {
		t.Fatalf("expected signature_invalid, got %v", err)
	}

	// Missing headers are reported as missing, not invalid.
	req2 := postWith(body, "", "")
	if err := Verify(KindSlack, body, req2, "CH_S"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureMissing {
		t.Fatalf("expected signature_missing, got %v", err)
	}

	// An old timestamp is a replay and is rejected.
	old := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	req3 := postWith(body, "", "")
	req3.Header.Set("X-Slack-Request-Timestamp", old)
	req3.Header.Set("X-Slack-Signature", slackSig("shh", old, body))
	if err := Verify(KindSlack, body, req3, "CH_S"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelTimestampExpired {
		t.Fatalf("expected timestamp_expired, got %v", err)
	}
}

func TestVerifyTeamsHMAC(t *testing.T) {
	t.Setenv("CH_T", "shh")
	body := []byte(`{"text":"hi","id":"m1"}`)

	// Valid HMAC scheme passes.
	req := postWith(body, "Authorization", hmacHex("shh", body))
	if err := Verify(KindTeams, body, req, "CH_T"); err != nil {
		t.Fatalf("valid teams signature rejected: %v", err)
	}

	// A non-HMAC scheme is invalid.
	req = postWith(body, "Authorization", "Bearer abc")
	if err := Verify(KindTeams, body, req, "CH_T"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureInvalid {
		t.Fatalf("expected signature_invalid, got %v", err)
	}

	// Missing authorization header is missing, not invalid.
	req = postWith(body, "", "")
	if err := Verify(KindTeams, body, req, "CH_T"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureMissing {
		t.Fatalf("expected signature_missing, got %v", err)
	}
}

func TestVerifyTelegram(t *testing.T) {
	t.Setenv("CH_G", "tok")

	// Matching secret token passes.
	req := postWith(nil, "X-Telegram-Bot-Api-Secret-Token", "tok")
	if err := Verify(KindTelegram, nil, req, "CH_G"); err != nil {
		t.Fatalf("valid telegram token rejected: %v", err)
	}

	// Mismatched token is invalid.
	req = postWith(nil, "X-Telegram-Bot-Api-Secret-Token", "nope")
	if err := Verify(KindTelegram, nil, req, "CH_G"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureInvalid {
		t.Fatalf("expected signature_invalid, got %v", err)
	}
}

func TestVerifySMSIsOutboundOnly(t *testing.T) {
	t.Setenv("CH_SMS", "shh")
	// SMS inbound is a no-op that always reports a missing signature; it never
	// trusts a body, so there is nothing to verify.
	if err := Verify(KindSMS, []byte("x"), postWith([]byte("x"), "", ""), "CH_SMS"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureMissing {
		t.Fatalf("expected signature_missing for SMS, got %v", err)
	}
}

func TestVerifyUnknownKind(t *testing.T) {
	if err := Verify(Kind("carrier-pigeon"), nil, postWith(nil, "", ""), "X"); err == nil || httputil.AsError(err).Code != httputil.CodeChannelSignatureInvalid {
		t.Fatalf("expected signature_invalid for unknown kind, got %v", err)
	}
}
