package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestCSRFAllowsGet(t *testing.T) {
	h := CSRF(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://agenthub.test/api/x", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET must pass, got %d", rec.Code)
	}
}

func TestCSRFAllowsNoOrigin(t *testing.T) {
	h := CSRF(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://agenthub.test/api/x", nil)
	// No Origin header (curl / scripts).
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-Origin POST must pass, got %d", rec.Code)
	}
}

func TestCSRFAllowsLoopback(t *testing.T) {
	h := CSRF(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://agenthub.test/api/x", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	req.Header.Set("Origin", "http://evil.test")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback client must be exempt, got %d", rec.Code)
	}
}

func TestCSRFRejectsBadOrigin(t *testing.T) {
	h := CSRF(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://agenthub.test/api/x", nil)
	req.RemoteAddr = "203.0.113.9:55555"
	req.Header.Set("Origin", "http://evil.test")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST must be 403, got %d", rec.Code)
	}
}

func TestCSRFAllowsSameOrigin(t *testing.T) {
	h := CSRF(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://agenthub.test/api/x", nil)
	req.RemoteAddr = "203.0.113.9:55555"
	req.Header.Set("Origin", "http://agenthub.test")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin POST must pass, got %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders("prod")(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://agenthub.test/", nil)
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options")
	}
	if rec.Header().Get("Content-Security-Policy-Report-Only") == "" {
		t.Fatal("missing CSP report-only")
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("prod must emit HSTS")
	}
}

func TestSecurityHeadersNoHSTSOnPlaintext(t *testing.T) {
	h := SecurityHeaders("dev")(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://agenthub.test/", nil)
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("dev plaintext must not emit HSTS")
	}
}
