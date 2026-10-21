package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterAllow(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 10; i++ {
		if !l.Allow("k", 10, time.Minute) {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if l.Allow("k", 10, time.Minute) {
		t.Fatal("11th request should be denied")
	}
	// A different key is independent.
	if !l.Allow("other", 10, time.Minute) {
		t.Fatal("independent key must be allowed")
	}
}

func TestLimiterWindowExpiry(t *testing.T) {
	l := NewLimiter()
	if !l.Allow("k", 1, 10*time.Millisecond) {
		t.Fatal("first should pass")
	}
	if l.Allow("k", 1, 10*time.Millisecond) {
		t.Fatal("second should be denied")
	}
	time.Sleep(15 * time.Millisecond)
	if !l.Allow("k", 1, 10*time.Millisecond) {
		t.Fatal("after window expiry should pass again")
	}
}

func TestRateLimitHandler(t *testing.T) {
	hit := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit++ })
	h := RateLimit(next, NewLimiter(), func(string) bool { return false })

	// 200 allowed, 201st denied, for a non-strict path.
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/x", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: want 200 got %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/x", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if hit != 200 {
		t.Fatalf("next called %d times, want 200", hit)
	}
}

func TestRateLimitAuthStrict(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	isAuth := func(p string) bool { return len(p) >= 10 && p[:10] == "/api/auth/" }
	h := RateLimit(next, NewLimiter(), isAuth)
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("auth request %d: want 200 got %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("auth 11th: want 429 got %d", rec.Code)
	}
}

func TestRateLimitSkipsStatic(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := RateLimit(next, NewLimiter(), func(string) bool { return false })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !called {
		t.Fatal("non /api path must reach next")
	}
}
