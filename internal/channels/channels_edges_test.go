package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"agenthub/internal/httputil"
)

// A timestamp that is not a number is a malformed signature, not a replay: the
// caller has to be told which of the two it is.
func TestVerifySlackRejectsATimestampThatIsNotANumber(t *testing.T) {
	t.Setenv("CH_S", "shh")
	body := []byte(`{"event_id":"e1"}`)
	req := postWith(body, "", "")
	req.Header.Set("X-Slack-Request-Timestamp", "yesterday")
	req.Header.Set("X-Slack-Signature", "v0=abc")

	if err := Verify(KindSlack, body, req, "CH_S"); err == nil ||
		httputil.AsError(err).Code != httputil.CodeChannelSignatureInvalid {
		t.Fatalf("expected signature_invalid, got %v", err)
	}
}

// The generic HMAC scheme fails closed on anything that is not a full-length
// digest: a truncated or non-hex value is a broken caller, not a valid MAC.
func TestVerifyHMACRejectsMalformedDigests(t *testing.T) {
	t.Setenv("CH_T", "shh")
	body := []byte(`{"id":"m1"}`)

	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(body)
	sum := mac.Sum(nil)

	wrong := hmac.New(sha256.New, []byte("another-secret"))
	wrong.Write(body)

	cases := map[string]string{
		"not hex":      "HMAC zzz",
		"truncated":    "HMAC " + hex.EncodeToString(sum[:8]),
		"wrong secret": "HMAC " + hex.EncodeToString(wrong.Sum(nil)),
		"empty digest": "HMAC ",
	}
	for name, header := range cases {
		req := postWith(body, "Authorization", header)
		if err := Verify(KindTeams, body, req, "CH_T"); err == nil ||
			httputil.AsError(err).Code != httputil.CodeChannelSignatureInvalid {
			t.Fatalf("%s: expected signature_invalid, got %v", name, err)
		}
	}
}

// An absent Telegram token is missing, not invalid: the console shows different
// guidance for a misconfigured webhook than for a forged one.
func TestVerifyTelegramWithoutATokenIsMissing(t *testing.T) {
	t.Setenv("CH_G", "tok")
	req := postWith(nil, "", "")
	if err := Verify(KindTelegram, nil, req, "CH_G"); err == nil ||
		httputil.AsError(err).Code != httputil.CodeChannelSignatureMissing {
		t.Fatalf("expected signature_missing, got %v", err)
	}
}

// A nil context cannot build a request. The domain reports that instead of
// dereferencing a request that was never created.
func TestDeliverWithoutAContextIsAnError(t *testing.T) {
	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": "https://provider.example.test/hook"}}
	if _, err := Deliver(nil, ch, "body"); err == nil {
		t.Fatal("a nil context must be reported as an error")
	}
}

// The success path: the provider's status is echoed and a body longer than the
// cap is truncated with a marker rather than buffered whole.
func TestDeliverEchoesTheProviderResponse(t *testing.T) {
	long := strings.Repeat("x", maxOutboundBody+64)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"text":"hi"}` {
			t.Errorf("body = %q", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, long)
	}))
	defer srv.Close()

	// The SSRF guard refuses loopback by design, so the test supplies the
	// server's own client: what is under test here is the reading of the reply.
	restore := deliverClient
	t.Cleanup(func() { deliverClient = restore })
	deliverClient = func() *http.Client { return srv.Client() }

	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": srv.URL}}
	res, err := Deliver(context.Background(), ch, `{"text":"hi"}`)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if res.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.Status)
	}
	want := long[:maxOutboundBody] + "…"
	if res.Raw != want {
		t.Fatalf("raw = %q (len %d), want %d bytes plus the marker", res.Raw, len(res.Raw), maxOutboundBody)
	}
}

// A provider that answers with nothing useful still produces a result: an empty
// body is not an error, it is an empty string the caller records as-is.
func TestDeliverAcceptsAnEmptyResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	restore := deliverClient
	t.Cleanup(func() { deliverClient = restore })
	deliverClient = func() *http.Client { return srv.Client() }

	ch := &Channel{ID: "c1", Config: map[string]any{"target_url": srv.URL}}
	res, err := Deliver(context.Background(), ch, "{}")
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if res.Status != http.StatusNoContent || res.Raw != "" {
		t.Fatalf("result = %+v, want 204 with an empty body", res)
	}
}

// The Slack timestamp window is two-sided: a signature dated in the future is a
// replay attempt just as an old one is.
func TestVerifySlackRejectsAFutureTimestamp(t *testing.T) {
	t.Setenv("CH_S", "shh")
	body := []byte(`{"event_id":"e1"}`)
	future := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	req := postWith(body, "", "")
	req.Header.Set("X-Slack-Request-Timestamp", future)
	req.Header.Set("X-Slack-Signature", slackSig("shh", future, body))

	if err := Verify(KindSlack, body, req, "CH_S"); err == nil ||
		httputil.AsError(err).Code != httputil.CodeChannelTimestampExpired {
		t.Fatalf("expected timestamp_expired, got %v", err)
	}
}
