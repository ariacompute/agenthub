package handlers

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"agenthub/internal/auth"
	"agenthub/internal/authz"
	"agenthub/internal/httputil"
)

// userPayload is the only shape of a user the API ever renders: the password
// hash, and anything else derived from it, never leaves the server.
type userPayload struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	IsOwner     bool       `json:"isOwner"`
	HasPassword bool       `json:"hasPassword"`
	Roles       []string   `json:"roles"`
	Groups      []string   `json:"groups"`
	CreatedAt   time.Time  `json:"createdAt"`
	DisabledAt  *time.Time `json:"disabledAt,omitempty"`
}

func userView(u *auth.User) userPayload {
	roles := append([]string{}, u.Roles...)
	groups := append([]string{}, u.Groups...)
	if roles == nil {
		roles = []string{}
	}
	if groups == nil {
		groups = []string{}
	}
	return userPayload{
		ID:          u.ID,
		Email:       u.Email,
		Name:        u.Name,
		Status:      u.Status,
		IsOwner:     u.IsOwner,
		HasPassword: u.PasswordHash != "",
		Roles:       roles,
		Groups:      groups,
		CreatedAt:   u.CreatedAt,
		DisabledAt:  u.DisabledAt,
	}
}

// sessionPayload is one logged in browser, as shown on "my sessions".
type sessionPayload struct {
	ID         string     `json:"id"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
	UserAgent  string     `json:"userAgent,omitempty"`
	IP         string     `json:"ip,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	Current    bool       `json:"current"`
}

func sessionView(s auth.Session, current bool) sessionPayload {
	return sessionPayload{
		ID:         s.ID,
		CreatedAt:  s.CreatedAt,
		ExpiresAt:  s.ExpiresAt,
		LastSeenAt: s.LastSeenAt,
		UserAgent:  s.UserAgent,
		IP:         s.IP,
		RevokedAt:  s.RevokedAt,
		Current:    current,
	}
}

// handleAuthConfig is what the login screen renders before anybody has typed
// anything: which providers exist and whether email registration is open. A
// provider without credentials is simply absent, so the console never offers a
// button that would 502.
func (d Deps) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	providers := []map[string]string{}
	if d.Auth != nil {
		for _, p := range d.Auth.OAuthProviders() {
			providers = append(providers, map[string]string{"name": p.Name()})
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"providers":        providers,
		"password":         true,
		"registrationOpen": true,
		"configured":       d.Auth != nil,
	})
}

func (d Deps) handleRegister(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	u, err := d.Auth.Register(r.Context(), body.Email, body.Password, body.Name)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	d.Log.Info("handler: user registered", "user", u.ID)
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"user": userView(u)})
}

func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httputil.Decode(w, r, &body, 1<<12); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	u, token, sess, err := d.Auth.Login(r.Context(), body.Email, body.Password, r.UserAgent(), clientIP(r))
	if err != nil {
		// One log line per result, never per credential: the address is enough
		// to investigate a lockout without recording the secret.
		d.Log.Info("handler: login failed", "result", "invalid_credentials")
		writeAuthErr(w, err)
		return
	}
	d.Auth.SetCookie(w, r, token, sess.ExpiresAt)
	d.Log.Info("handler: login", "user", u.ID)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"user": userView(u)})
}

func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	token := d.Auth.SessionTokenFromRequest(r)
	if err := d.Auth.Logout(r.Context(), token); err != nil {
		httputil.WriteErr(w, err)
		return
	}
	d.Auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe answers "who am I": the identity, the permissions it resolves to and
// the group/role membership behind them. The console uses it both to render the
// account menu and to decide which screens to offer.
func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r)
	if p == nil {
		httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeUnauthenticated)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":      p.UserID,
			"email":   p.Email,
			"name":    p.Name,
			"isOwner": p.IsOwner,
			"roles":   stringSlice(p.Roles),
			"groups":  stringSlice(p.Groups),
		},
		"permissions": permissionList(p),
	})
}

func (d Deps) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	p := PrincipalFrom(r)
	all, err := d.Auth.Sessions(r.Context(), p.UserID)
	if err != nil {
		httputil.WriteErr(w, err)
		return
	}
	current := d.currentSessionID(r)
	out := make([]sessionPayload, 0, len(all))
	for _, s := range all {
		out = append(out, sessionView(s, s.ID == current))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleRevokeOwnSession lets a user drop one of its own sessions ("log this
// device out"). Revoking somebody else's is handleRevokeUserSessions, which is
// guarded by sessions:manage.
func (d Deps) handleRevokeOwnSession(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	p := PrincipalFrom(r)
	if err := d.Auth.RevokeSession(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		writeAuthErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// OAuth
// ---------------------------------------------------------------------------

// oauth state and PKCE verifier travel in short lived cookies: the callback has
// to prove the browser that started the flow is the one that came back.
const (
	oauthStateCookie    = "agenthub_oauth_state"
	oauthVerifierCookie = "agenthub_oauth_verifier"
)

func (d Deps) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	provider := r.PathValue("provider")
	url, state, verifier, err := d.Auth.BeginOAuth(provider, d.oauthRedirect(r, provider))
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	secure := d.Auth.Options().Secure
	for name, value := range map[string]string{oauthStateCookie: state, oauthVerifierCookie: verifier} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    value,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// handleOAuthCallback finishes the flow, opens a session and hands the browser
// back to the console. A failure is reported as a code on the console URL — the
// provider's own error text stays in the log, never in a redirect a user can
// forwarded to somebody else.
func (d Deps) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if d.Auth == nil {
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
		return
	}
	provider := r.PathValue("provider")
	stateCookie, err := r.Cookie(oauthStateCookie)
	expected := ""
	if err == nil {
		expected = stateCookie.Value
	}
	if expected == "" || r.URL.Query().Get("state") != expected {
		redirectWithCode(w, r, string(httputil.CodeOAuthStateMismatch))
		return
	}
	verifierCookie, err := r.Cookie(oauthVerifierCookie)
	if err != nil || verifierCookie.Value == "" {
		redirectWithCode(w, r, string(httputil.CodeOAuthStateMismatch))
		return
	}
	d.clearOAuthCookies(w)

	code := r.URL.Query().Get("code")
	if code == "" {
		// The provider refused (or the user cancelled): it says so in `error`.
		redirectWithCode(w, r, string(httputil.CodeOAuthFailed))
		return
	}
	u, token, sess, err := d.Auth.CompleteOAuth(r.Context(), provider, code, verifierCookie.Value,
		d.oauthRedirect(r, provider), r.UserAgent(), clientIP(r))
	if err != nil {
		d.Log.Error("handler: oauth callback", "provider", provider, "error", err)
		redirectWithCode(w, r, authCodeOf(err))
		return
	}
	d.Auth.SetCookie(w, r, token, sess.ExpiresAt)
	d.Log.Info("handler: oauth login", "user", u.ID, "provider", provider)
	http.Redirect(w, r, "/", http.StatusFound)
}

// oauthRedirect builds the callback URL a provider must send the browser back
// to. AGENTHUB_PUBLIC_BASE_URL wins over the request host so a deployment
// behind a proxy registers the origin users actually reach.
func (d Deps) oauthRedirect(r *http.Request, provider string) string {
	base := strings.TrimSuffix(d.Cfg.PublicBaseURL, "/")
	if base == "" {
		base = baseURL(r)
	}
	return base + "/api/auth/oauth/" + provider + "/callback"
}

func (d Deps) clearOAuthCookies(w http.ResponseWriter) {
	secure := false
	if d.Auth != nil {
		secure = d.Auth.Options().Secure
	}
	for _, name := range []string{oauthStateCookie, oauthVerifierCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	}
}

// redirectWithCode sends the browser back to the login screen with a machine
// readable code, which the console localises.
func redirectWithCode(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+code, http.StatusFound)
}

// currentSessionID resolves the session behind this request, so "my sessions"
// can mark the browser the user is sitting in front of.
func (d Deps) currentSessionID(r *http.Request) string {
	if d.Auth == nil {
		return ""
	}
	_, sess, err := d.Auth.Principal(r.Context(), r)
	if err != nil || sess == nil {
		return ""
	}
	return sess.ID
}

// writeAuthErr maps the identity layer onto machine readable codes. Nothing
// about the credential itself survives into the response.
func writeAuthErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeInvalidCredentials)
	case errors.Is(err, auth.ErrEmailTaken):
		httputil.WriteError(w, http.StatusConflict, httputil.CodeEmailTaken)
	case errors.Is(err, auth.ErrWeakPassword):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeWeakPassword)
	case errors.Is(err, auth.ErrInvalidEmail):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodeInvalidEmail)
	case errors.Is(err, auth.ErrUserDisabled):
		httputil.WriteError(w, http.StatusForbidden, httputil.CodeUserDisabled)
	case errors.Is(err, auth.ErrPasswordRequired):
		httputil.WriteError(w, http.StatusBadRequest, httputil.CodePasswordRequired)
	case errors.Is(err, auth.ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, httputil.CodeNotFound)
	case errors.Is(err, auth.ErrSessionExpired):
		httputil.WriteError(w, http.StatusUnauthorized, httputil.CodeSessionExpired)
	case errors.Is(err, auth.ErrOAuthNotConfigured):
		httputil.WriteError(w, http.StatusBadGateway, httputil.CodeOAuthNotConfigured)
	case errors.Is(err, auth.ErrOAuthStateMismatch):
		httputil.WriteError(w, http.StatusBadGateway, httputil.CodeOAuthStateMismatch)
	case errors.Is(err, auth.ErrOAuthNoEmail):
		httputil.WriteError(w, http.StatusBadGateway, httputil.CodeOAuthNoEmail)
	case errors.Is(err, auth.ErrOAuthFailed):
		httputil.WriteError(w, http.StatusBadGateway, httputil.CodeOAuthFailed)
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.CodeInternal)
	}
}

// authCodeOf is writeAuthErr's vocabulary for the OAuth redirect, which can
// only carry a code.
func authCodeOf(err error) string {
	switch {
	case errors.Is(err, auth.ErrOAuthNotConfigured):
		return string(httputil.CodeOAuthNotConfigured)
	case errors.Is(err, auth.ErrOAuthStateMismatch):
		return string(httputil.CodeOAuthStateMismatch)
	case errors.Is(err, auth.ErrOAuthNoEmail):
		return string(httputil.CodeOAuthNoEmail)
	case errors.Is(err, auth.ErrUserDisabled):
		return string(httputil.CodeUserDisabled)
	default:
		return string(httputil.CodeOAuthFailed)
	}
}

func permissionList(p *authz.Principal) []string {
	out := make([]string, 0, len(authz.AllPermissions))
	if p.IsOwner {
		return authz.AllPermissions
	}
	for _, code := range authz.AllPermissions {
		if p.Can(code) {
			out = append(out, code)
		}
	}
	return out
}

func stringSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// clientIP is the best effort origin of a request: the forwarded header when
// the deployment sits behind a proxy, the socket address otherwise. It is only
// ever shown next to a session, never used to authorize anything.
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}
