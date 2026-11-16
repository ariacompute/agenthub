package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The sweep is what keeps the map from growing with every address that ever
// called: an expired window must be removed on the first Allow after it lapsed.
func TestLimiterSweepsExpiredWindows(t *testing.T) {
	l := NewLimiter()
	if !l.Allow("stale", 1, 10*time.Millisecond) {
		t.Fatal("first request must pass")
	}
	time.Sleep(20 * time.Millisecond)

	// The sweep runs at most once a minute; age the marker rather than waiting
	// for it, so the window above is genuinely lapsed when the sweep looks.
	l.lastGC = time.Now().Add(-2 * time.Minute)

	// Any later Allow runs the sweep; the lapsed key is what it drops.
	if !l.Allow("fresh", 1, time.Minute) {
		t.Fatal("a new key must pass")
	}
	if _, ok := l.windows["stale"]; ok {
		t.Fatal("an expired window must be swept")
	}
	if _, ok := l.windows["fresh"]; !ok {
		t.Fatal("the live window must survive the sweep")
	}
}

// The rate limiter keys on the originating address: the leftmost forwarded hop
// when a proxy is in front, otherwise the socket.
func TestClientIPPrefersTheForwardedHop(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("socket ip = %q", got)
	}

	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if got := clientIP(req); got != "198.51.100.7" {
		t.Fatalf("forwarded ip = %q, want the leftmost hop", got)
	}

	// A RemoteAddr without a port (a test client, a unix socket) is used verbatim.
	req.Header.Del("X-Forwarded-For")
	req.RemoteAddr = "unix-socket"
	if got := clientIP(req); got != "unix-socket" {
		t.Fatalf("portless address = %q", got)
	}
}

// isLoopback decides the exemption for both the limiter and the CSRF guard, so it
// has to recognise every spelling a loopback client arrives with.
func TestIsLoopbackSpellings(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":       true,
		"127.0.0.1:8080":  true,
		"[::1]:8080":      true,
		"localhost":       true,
		"localhost:3500":  true,
		"127.0.0.5":       true, // any loopback address, not just the first one
		"10.0.0.1":        false,
		"203.0.113.9:443": false,
		"not-an-ip":       false,
	}
	for ip, want := range cases {
		if got := isLoopback(ip); got != want {
			t.Fatalf("isLoopback(%q) = %v, want %v", ip, got, want)
		}
	}
}

// hostOf is the CSRF comparison: a scheme, a path and userinfo are all noise,
// and an origin with no host at all is a browser bug worth refusing.
func TestHostOfStripsEverythingButTheHost(t *testing.T) {
	cases := map[string]string{
		"https://hub.example.com":            "hub.example.com",
		"https://hub.example.com/console":    "hub.example.com",
		"http://localhost:3500/":             "localhost:3500",
		"https://user:pw@hub.example.com/x/": "user:pw@hub.example.com",
	}
	for origin, want := range cases {
		got, err := hostOf(origin)
		if err != nil {
			t.Fatalf("hostOf(%q): %v", origin, err)
		}
		if got != want {
			t.Fatalf("hostOf(%q) = %q, want %q", origin, got, want)
		}
	}

	// An origin that is only a scheme separator has no host to compare.
	for _, origin := range []string{"", "https://"} {
		if got, err := hostOf(origin); err == nil {
			t.Fatalf("hostOf(%q) = %q, want an error", origin, got)
		}
	}
}
