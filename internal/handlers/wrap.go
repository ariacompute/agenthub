package handlers

import (
	"net/http"
	"runtime/debug"

	"agenthub/internal/config"
	"agenthub/internal/middleware"
)

// Wrap layers the security middleware over the router. It is applied only at
// process startup (see cmd/agenthub-server), not inside Register, so unit tests
// that call Register directly stay fast and deterministic and are not throttled
// or rejected by the guards.
//
// Order, outermost first: Recover → SecurityHeaders → CSRF → RateLimit → routes.
func Wrap(h http.Handler, cfg *config.Config) http.Handler {
	limiter := middleware.NewLimiter()
	chain := middleware.SecurityHeaders(cfg.Env)(h)
	chain = middleware.CSRF(chain)
	chain = middleware.RateLimit(chain, limiter, isAuthPath)
	chain = recoverPanic(chain)
	return chain
}

// isAuthPath reports whether path is one of the identity endpoints, which get a
// tight per-IP rate ceiling (see middleware.RateLimit).
func isAuthPath(path string) bool {
	const authPrefix = "/api/auth/"
	return len(path) >= len(authPrefix) && path[:len(authPrefix)] == authPrefix
}

// recoverPanic converts an unexpected panic into a clean JSON error instead of
// letting the net/http default handler dump a stack trace to the client.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				debug.PrintStack()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"internal_error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
