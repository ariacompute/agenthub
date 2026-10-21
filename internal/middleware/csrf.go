package middleware

import (
	"errors"
	"net/http"
	"strings"
)

// errBadOrigin marks a malformed or non-absolute Origin value.
var errBadOrigin = errors.New("bad origin")

// CSRF defends the session-cookie control plane against cross-site request
// forgery. State-changing requests must either carry no Origin (non-browser
// clients, scripts, the loopback desktop proxy) or an Origin whose host matches
// the request Host. A mismatched Origin — the hallmark of a browser cross-site
// request — is rejected. Bearer-authenticated API keys are unaffected: they come
// without cookies and, with no CORS allow list configured, cross-site bearer
// calls are already blocked by the browser preflight.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		// The desktop proxy and local dev arrive on loopback; treat them as
		// trusted same-origin so the console inside Tauri keeps working.
		if isLoopback(clientIP(r)) {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin means a non-browser or same-origin request; allow it.
			next.ServeHTTP(w, r)
			return
		}
		oh, err := hostOf(origin)
		if err != nil || !strings.EqualFold(oh, r.Host) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"csrf_origin_mismatch"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostOf extracts the host portion of an absolute origin URL.
func hostOf(origin string) (string, error) {
	// Strip a scheme:// prefix; an origin is always absolute.
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	// Drop any path/userinfo/port-irrelevant trailing content after the host.
	if i := strings.IndexAny(origin, "/"); i >= 0 {
		origin = origin[:i]
	}
	if origin == "" {
		return "", errBadOrigin
	}
	return origin, nil
}
