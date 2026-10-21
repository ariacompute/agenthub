package middleware

import (
	"net/http"
	"strings"
)

// strictCSP is the console's baseline policy. It ships as Report-Only so the
// build can be observed in the wild before enforcement turns on; a violation
// only logs, it never breaks a request.
const strictCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"base-uri 'self'; frame-ancestors 'none'"

// SecurityHeaders attaches the defensive response headers agenthub should send on
// every response. HSTS is only emitted when the instance is configured for
// production or is already being served over TLS (e.g. behind a terminating
// proxy), so a plain-http dev box is never told to refuse cleartext.
func SecurityHeaders(env string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Content-Security-Policy-Report-Only", strictCSP)
			if strings.EqualFold(env, "prod") || isHTTPS(r) {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
