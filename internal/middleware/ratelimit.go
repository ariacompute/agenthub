// Package middleware holds the small, dependency free HTTP guards agenthub
// layers in front of its router: security response headers, an in-memory rate
// limiter and a CSRF origin check. They are deliberately standard library only
// so the binary gains no new dependencies for defence in depth.
package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// window is one fixed window counter for a single key.
type window struct {
	count int
	reset time.Time
}

// Limiter is an in-memory fixed-window rate limiter. It is safe for concurrent
// use and cheap: O(1) per request plus an occasional sweep of expired windows.
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	cleanup time.Duration
	lastGC  time.Time
}

// NewLimiter builds an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{
		windows: make(map[string]*window),
		cleanup: time.Minute,
	}
}

// Allow reports whether key is still within its max for the next window
// duration and, if so, consumes one. A fresh or expired key starts a new window.
func (l *Limiter) Allow(key string, max int, dur time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w, ok := l.windows[key]
	if !ok || now.After(w.reset) {
		w = &window{reset: now.Add(dur)}
		l.windows[key] = w
	} else if w.count >= max {
		return false
	}
	w.count++
	if now.Sub(l.lastGC) > l.cleanup {
		for k, v := range l.windows {
			if now.After(v.reset) {
				delete(l.windows, k)
			}
		}
		l.lastGC = now
	}
	return true
}

// clientIP returns the originating IP, trusting the leftmost X-Forwarded-For hop
// when a reverse proxy sits in front, otherwise the connection remote address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// isLoopback reports whether ip is a loopback address (covers 127.0.0.1, ::1 and
// a bare "localhost"), which is how the loopback desktop client (agentbuddy) and
// local dev reach the server.
func isLoopback(ip string) bool {
	host := ip
	if h, _, err := net.SplitHostPort(ip); err == nil {
		host = h
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if parsed := net.ParseIP(host); parsed != nil && parsed.IsLoopback() {
		return true
	}
	return false
}

// RateLimit caps requests per client IP. Paths where isStrict reports true (the
// identity endpoints) get a tight allowance so credential stuffing is throttled;
// everything else under /api or /a2a gets a generous shared ceiling. Paths that
// are neither are left unlimited (static assets, healthz, discovery).
func RateLimit(next http.Handler, limiter *Limiter, isStrict func(path string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") && path != "/a2a" {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		// The loopback desktop client (agentbuddy) and local dev reach the
		// server on 127.0.0.1, so every request from them collapses into a
		// single per-IP bucket. Throttling them by IP would break legitimate
		// logout→login and polling flows, and the credential-stuffing defence
		// this cap provides does not apply to a host-only reachable address.
		// CSRF already trusts loopback for the same reason, so stay consistent.
		if isLoopback(ip) {
			next.ServeHTTP(w, r)
			return
		}
		if isStrict(path) {
			if !limiter.Allow("auth:"+ip, 10, time.Minute) {
				writeRateLimited(w)
				return
			}
		} else if !limiter.Allow("api:"+ip, 200, time.Minute) {
			writeRateLimited(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeRateLimited(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "60")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"code":"rate_limited"}`))
}
