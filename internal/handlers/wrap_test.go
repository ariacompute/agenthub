package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agenthub/internal/config"
)

func TestIsAuthPathOnlyCoversTheIdentityEndpoints(t *testing.T) {
	cases := map[string]bool{
		"/api/auth/login":        true,
		"/api/auth/register":     true,
		"/api/auth/":             true,
		"/api/auth":              false, // the bare prefix is not an identity call
		"/api/tasks":             false,
		"/api/authz/roles":       false, // shares the letters, not the prefix
		"/a2a":                   false,
		"/api/auth/login/extra/": true,
		"":                       false,
	}
	for path, want := range cases {
		if got := isAuthPath(path); got != want {
			t.Errorf("isAuthPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestWrapTurnsAPanicIntoAJSONError(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("adapter exploded")
	})
	srv := Wrap(next, &config.Config{Env: "dev"})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("a panic must answer 500, got %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("the error must be JSON, got %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	// The stack trace goes to the log, not to the caller.
	if body["code"] != "internal_error" {
		t.Fatalf("want the internal_error code, got %+v", body)
	}
	if w.Body.Len() > 64 {
		t.Fatalf("the response must not leak the stack, got %q", w.Body.String())
	}
}

func TestWrapKeepsTheSecurityHeadersOnSuccess(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := Wrap(next, &config.Config{Env: "dev"})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	for _, header := range []string{
		"X-Content-Type-Options",
		"Referrer-Policy",
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Resource-Policy",
		"Content-Security-Policy-Report-Only",
	} {
		if w.Header().Get(header) == "" {
			t.Errorf("%s is missing from the response", header)
		}
	}
	// HSTS is only sent where it is meaningful: HTTPS, or an explicitly prod env.
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("dev over plain HTTP must not pin HSTS, got %q", got)
	}
}

func TestWrapPinsHSTSInProd(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	srv := Wrap(next, &config.Config{Env: "prod"})

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("prod must send HSTS")
	}
}
