package handlers

// The identity surface has more failure shapes than success shapes: a missing
// service, a store that does not answer, a browser that did not start the OAuth
// flow, and one sentinel per credential rule. Each of them is a code the console
// localises, so "which code did that 500 carry" is the contract this file pins.
// The happy paths live in auth_test.go / api_sessions_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/httputil"
)

// TestWriteAuthErrMapsEverySentinel keeps the whole vocabulary in one table: a
// sentinel that falls through to the default answer tells the console
// "internal_error" for something the user can fix.
func TestWriteAuthErrMapsEverySentinel(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{auth.ErrInvalidCredentials, http.StatusUnauthorized, string(httputil.CodeInvalidCredentials)},
		{auth.ErrEmailTaken, http.StatusConflict, string(httputil.CodeEmailTaken)},
		{auth.ErrWeakPassword, http.StatusBadRequest, string(httputil.CodeWeakPassword)},
		{auth.ErrInvalidEmail, http.StatusBadRequest, string(httputil.CodeInvalidEmail)},
		{auth.ErrUserDisabled, http.StatusForbidden, string(httputil.CodeUserDisabled)},
		{auth.ErrPasswordRequired, http.StatusBadRequest, string(httputil.CodePasswordRequired)},
		{auth.ErrNotFound, http.StatusNotFound, string(httputil.CodeNotFound)},
		{auth.ErrSessionExpired, http.StatusUnauthorized, string(httputil.CodeSessionExpired)},
		{auth.ErrOAuthNotConfigured, http.StatusBadGateway, string(httputil.CodeOAuthNotConfigured)},
		{auth.ErrOAuthStateMismatch, http.StatusBadGateway, string(httputil.CodeOAuthStateMismatch)},
		{auth.ErrOAuthNoEmail, http.StatusBadGateway, string(httputil.CodeOAuthNoEmail)},
		{auth.ErrOAuthFailed, http.StatusBadGateway, string(httputil.CodeOAuthFailed)},
		{errors.New("driver went away"), http.StatusInternalServerError, string(httputil.CodeInternal)},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		writeAuthErr(w, c.err)
		if w.Code != c.status {
			t.Errorf("%v: status = %d, want %d", c.err, w.Code, c.status)
			continue
		}
		if got := decodeErrorCode(t, w); got != c.code {
			t.Errorf("%v: code = %q, want %q", c.err, got, c.code)
		}
	}
}

// TestAuthCodeOfCoversTheOAuthVocabulary: the redirect can only carry a code, so
// every branch here is one less "callback failed" with no explanation.
func TestAuthCodeOfCoversTheOAuthVocabulary(t *testing.T) {
	cases := map[error]string{
		auth.ErrOAuthNotConfigured: string(httputil.CodeOAuthNotConfigured),
		auth.ErrOAuthStateMismatch: string(httputil.CodeOAuthStateMismatch),
		auth.ErrOAuthNoEmail:       string(httputil.CodeOAuthNoEmail),
		auth.ErrUserDisabled:       string(httputil.CodeUserDisabled),
		auth.ErrInvalidCredentials: string(httputil.CodeOAuthFailed),
	}
	for err, want := range cases {
		if got := authCodeOf(err); got != want {
			t.Errorf("authCodeOf(%v) = %q, want %q", err, got, want)
		}
	}
}

// TestUserViewAlwaysRendersArraysAsArrays: the console iterates roles/groups
// without a null guard, so "no memberships" has to be [] and not null.
func TestUserViewAlwaysRendersArraysAsArrays(t *testing.T) {
	got := userView(&auth.User{ID: "u-1", Email: "u@example.com"})
	if got.Roles == nil || got.Groups == nil {
		t.Fatalf("roles/groups = %v / %v, want empty slices", got.Roles, got.Groups)
	}
	if len(got.Roles) != 0 || len(got.Groups) != 0 {
		t.Fatalf("memberships = %v / %v, want none", got.Roles, got.Groups)
	}

	// The view must copy: the caller keeps the user after the response is built,
	// and a handler that later assigns roles must not mutate this payload.
	u := &auth.User{ID: "u-2", Roles: []string{"viewer"}, Groups: []string{"ops"}}
	payload := userView(u)
	payload.Roles[0] = "owner"
	payload.Groups[0] = "admins"
	if u.Roles[0] != "viewer" || u.Groups[0] != "ops" {
		t.Fatalf("the view shares its slices with the user: %v / %v", u.Roles, u.Groups)
	}
}

// TestClientIPReadsTheOriginItCanSee covers the three shapes a deployment
// produces: one forwarded hop, a chain, and no proxy at all.
func TestClientIPReadsTheOriginItCanSee(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the leftmost hop", got)
	}

	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the single hop", got)
	}

	r.Header.Del("X-Forwarded-For")
	r.RemoteAddr = "192.0.2.5:4711"
	if got := clientIP(r); got != "192.0.2.5" {
		t.Fatalf("clientIP = %q, want the socket address", got)
	}

	// A unix socket has no host:port to split, and the session row must not
	// carry a nonsense value for it.
	r.RemoteAddr = "unix-socket"
	if got := clientIP(r); got != "" {
		t.Fatalf("clientIP = %q, want empty for an address without a port", got)
	}
}

// TestAuthRoutesWithoutAnIdentityService pins the in-memory posture: with no
// auth service every identity route answers 404 rather than pretending a
// session, and /api/auth/config reports itself unconfigured.
func TestAuthRoutesWithoutAnIdentityService(t *testing.T) {
	d := Deps{Cfg: openConfig(""), Log: testLogger()}

	w := httptest.NewRecorder()
	d.handleAuthConfig(w, httptest.NewRequest(http.MethodGet, "/api/auth/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("config: status = %d", w.Code)
	}
	var cfg struct {
		Providers  []map[string]string `json:"providers"`
		Configured bool                `json:"configured"`
	}
	decodeJSON(t, w, &cfg)
	if cfg.Configured || len(cfg.Providers) != 0 {
		t.Fatalf("config = %+v, want an unconfigured instance with no providers", cfg)
	}

	for _, c := range []struct {
		name   string
		handle http.HandlerFunc
		method string
		target string
	}{
		{"register", d.handleRegister, http.MethodPost, "/api/auth/register"},
		{"login", d.handleLogin, http.MethodPost, "/api/auth/login"},
		{"logout", d.handleLogout, http.MethodPost, "/api/auth/logout"},
		{"sessions", d.handleListSessions, http.MethodGet, "/api/auth/sessions"},
		{"revoke", d.handleRevokeOwnSession, http.MethodDelete, "/api/auth/sessions/s-1"},
		{"oauth start", d.handleOAuthStart, http.MethodGet, "/api/auth/oauth/github"},
		{"oauth callback", d.handleOAuthCallback, http.MethodGet, "/api/auth/oauth/github/callback"},
	} {
		w := httptest.NewRecorder()
		c.handle(w, httptest.NewRequest(c.method, c.target, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s without auth: status = %d, want 404", c.name, w.Code)
			continue
		}
		if got := decodeErrorCode(t, w); got != string(httputil.CodeNotFound) {
			t.Errorf("%s without auth: code = %q", c.name, got)
		}
	}

	// handleMe is wrapped by requireAuth in the mux, so the guard inside it is
	// only reachable directly: a request without a principal is a 401, never a
	// 200 with an empty user.
	w = httptest.NewRecorder()
	d.handleMe(w, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if w.Code != http.StatusUnauthorized || decodeErrorCode(t, w) != string(httputil.CodeUnauthenticated) {
		t.Fatalf("me without a principal: status = %d, body = %s", w.Code, w.Body.String())
	}

	// A request with no session cannot name the browser it is in.
	if got := d.currentSessionID(httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)); got != "" {
		t.Fatalf("currentSessionID without auth = %q, want empty", got)
	}
}

// TestAuthBodiesMustDecode keeps a truncated JSON body a 400 with the decoding
// code, not a 500 from the service when it is handed empty strings.
func TestAuthBodiesMustDecode(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)

	for _, path := range []string{"/api/auth/register", "/api/auth/login"} {
		w := do(t, h, http.MethodPost, path, `{"email":`, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body = %s", path, w.Code, w.Body.String())
		}
	}
}

// TestOAuthStartHandsTheBrowserToTheProvider covers the success side of the
// start route: state and PKCE verifier travel in short lived cookies, and the
// browser is redirected to the provider.
func TestOAuthStartHandsTheBrowserToTheProvider(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"userinfo_endpoint":%q}`,
			issuer.URL+"/authorize", issuer.URL+"/token", issuer.URL+"/userinfo")
	}))
	defer issuer.Close()

	d, _ := authTestDepsWith(t, "", func(o *auth.Options) {
		o.OAuth = auth.OAuthConfig{Enabled: true, OIDC: auth.ProviderConfig{
			ClientID: "id", ClientSecret: "secret", Issuer: issuer.URL,
		}}
	})
	h := Register(d)

	w := do(t, h, http.MethodGet, "/api/auth/oauth/oidc", "", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, issuer.URL+"/authorize") {
		t.Fatalf("location = %q, want the provider's authorize endpoint", loc)
	}
	cookies := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		cookies[c.Name] = c
	}
	for _, name := range []string{oauthStateCookie, oauthVerifierCookie} {
		c, ok := cookies[name]
		if !ok || c.Value == "" {
			t.Fatalf("%s was not set: %v", name, cookies)
		}
		if !c.HttpOnly || c.MaxAge != 600 {
			t.Fatalf("%s = %+v, want an HttpOnly 10 minute cookie", name, c)
		}
	}
}

// TestOAuthCallbackRejectsABrowserThatDidNotStartTheFlow covers the three guards
// before the provider is ever called. Each one redirects to the login screen
// with a code, because a JSON error in a browser tab is not actionable.
func TestOAuthCallbackRejectsABrowserThatDidNotStartTheFlow(t *testing.T) {
	d, _ := authTestDeps(t, "")
	h := Register(d)

	redirectCode := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != http.StatusFound {
			t.Fatalf("status = %d, want a redirect (body %s)", w.Code, w.Body.String())
		}
		loc := w.Header().Get("Location")
		_, value, ok := strings.Cut(loc, "error=")
		if !ok {
			t.Fatalf("location = %q, want an error code", loc)
		}
		return value
	}

	// No state cookie at all.
	w := do(t, h, http.MethodGet, "/api/auth/oauth/oidc/callback?state=st&code=cd", "", nil)
	if got := redirectCode(w); got != string(httputil.CodeOAuthStateMismatch) {
		t.Fatalf("without a state cookie: code = %q", got)
	}

	// A state cookie that does not match the query.
	w = do(t, h, http.MethodGet, "/api/auth/oauth/oidc/callback?state=other&code=cd", "",
		map[string]string{"Cookie": oauthStateCookie + "=st"})
	if got := redirectCode(w); got != string(httputil.CodeOAuthStateMismatch) {
		t.Fatalf("state mismatch: code = %q", got)
	}

	// State is right but the PKCE verifier is gone: the code cannot be redeemed.
	w = do(t, h, http.MethodGet, "/api/auth/oauth/oidc/callback?state=st&code=cd", "",
		map[string]string{"Cookie": oauthStateCookie + "=st"})
	if got := redirectCode(w); got != string(httputil.CodeOAuthStateMismatch) {
		t.Fatalf("without a verifier: code = %q", got)
	}

	// Both cookies present, but the provider sent the browser back without a
	// code: the user cancelled, which is a failure and not a state problem.
	w = do(t, h, http.MethodGet, "/api/auth/oauth/oidc/callback?state=st", "",
		map[string]string{"Cookie": oauthStateCookie + "=st; " + oauthVerifierCookie + "=vf"})
	if got := redirectCode(w); got != string(httputil.CodeOAuthFailed) {
		t.Fatalf("without a code: code = %q", got)
	}
}

// failingSessions answers the two reads the session routes make and can be told
// to fail, which is how a dropped database shows up on the console.
type failingSessions struct {
	*memSessions
	listErr   error
	revokeErr error
}

func (s *failingSessions) ListForUser(ctx context.Context, userID string) ([]auth.Session, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.memSessions.ListForUser(ctx, userID)
}

func (s *failingSessions) Revoke(ctx context.Context, id string) error {
	if s.revokeErr != nil {
		return s.revokeErr
	}
	return s.memSessions.Revoke(ctx, id)
}

// RevokeForUser is the "sign this user out everywhere" path the admin screen
// uses; keeping it failing is what makes the logout-everywhere route testable.
func (s *failingSessions) RevokeForUser(ctx context.Context, userID string) error {
	if s.revokeErr != nil {
		return s.revokeErr
	}
	return s.memSessions.RevokeForUser(ctx, userID)
}

// TestSessionRoutesReportAStoreFailure: the console separates "log in again"
// from "the server is broken" by the code, so a store failure must reach it as
// internal_error and never as an empty list.
func TestSessionRoutesReportAStoreFailure(t *testing.T) {
	d, _ := authTestDeps(t, "")
	sessions := &failingSessions{memSessions: newMemSessions()}
	d.Auth = auth.New(newMemUsers(), sessions, &memOAuthBindings{}, nil, d.Authz,
		auth.Options{CookieName: "agenthub_session", TTL: time.Hour})
	h := Register(d)

	if w := do(t, h, http.MethodPost, "/api/auth/register",
		`{"email":"owner@example.com","password":"correct horse","name":"Owner"}`, nil); w.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, body = %s", w.Code, w.Body.String())
	}
	owner := sessionOf(t, h, "owner@example.com", "correct horse")

	sessions.listErr = errors.New("sessions store is down")
	w := do(t, h, http.MethodGet, "/api/auth/sessions", "", authHeader(owner))
	if w.Code != http.StatusInternalServerError || decodeErrorCode(t, w) != string(httputil.CodeInternal) {
		t.Fatalf("list sessions: status = %d, body = %s", w.Code, w.Body.String())
	}
	// Revoking a session checks ownership first, so the same read failure is
	// also what a revoke reports.
	w = do(t, h, http.MethodDelete, "/api/auth/sessions/some-session", "", authHeader(owner))
	if w.Code != http.StatusInternalServerError || decodeErrorCode(t, w) != string(httputil.CodeInternal) {
		t.Fatalf("revoke session: status = %d, body = %s", w.Code, w.Body.String())
	}

	sessions.listErr = nil
	sessions.revokeErr = errors.New("revoke failed")
	w = do(t, h, http.MethodPost, "/api/auth/logout", "", authHeader(owner))
	if w.Code != http.StatusInternalServerError || decodeErrorCode(t, w) != string(httputil.CodeInternal) {
		t.Fatalf("logout: status = %d, body = %s", w.Code, w.Body.String())
	}
}
