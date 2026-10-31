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

// The loopback desktop client (agentbuddy) and local dev arrive on 127.0.0.1,
// which collapses every request into one shared per-IP bucket. Rate limiting
// must not throttle them, or a normal logout→login flow trips 429. CSRF already
// trusts loopback for the same reason.
func TestRateLimitExemptsLoopback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	isAuth := func(p string) bool { return len(p) >= 10 && p[:10] == "/api/auth/" }
	h := RateLimit(next, NewLimiter(), isAuth)
	for _, ip := range []string{"127.0.0.1", "[::1]", "localhost"} {
		for i := 0; i < 50; i++ {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			req.RemoteAddr = ip + ":54321"
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s auth request %d: want 200 got %d", ip, i, rec.Code)
			}
		}
	}
	// And the shared /api bucket is likewise unbounded for loopback.
	for i := 0; i < 300; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback /api request %d: want 200 got %d", i, rec.Code)
		}
	}
}
