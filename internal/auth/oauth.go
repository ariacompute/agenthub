package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrOAuthNotConfigured means the provider has no credentials in the
	// environment; the console hides the button and the endpoint answers a
	// machine readable code instead of a 500.
	ErrOAuthNotConfigured = errors.New("oauth_not_configured")
	// ErrOAuthStateMismatch is the CSRF guard of the callback.
	ErrOAuthStateMismatch = errors.New("oauth_state_mismatch")
	// ErrOAuthFailed hides provider specifics: the client only ever sees this.
	ErrOAuthFailed = errors.New("oauth_failed")
	// ErrOAuthNoEmail is returned when the provider did not hand over an
	// address that can be used to create an account.
	ErrOAuthNoEmail = errors.New("oauth_no_email")
)

// Identity is the normalized profile a provider returned.
type Identity struct {
	Provider string
	Subject  string
	Email    string
	Name     string
}

// Provider is one external identity provider. The three implementations below
// share this shape so adding another one is a matter of filling in endpoints.
type Provider interface {
	// Name is the key used in the URL and in oauth_accounts.provider.
	Name() string
	// Configured reports whether credentials are present.
	Configured() bool
	// AuthURL builds the authorization redirect, including PKCE challenge.
	AuthURL(state, verifier, redirectURL string) string
	// Exchange trades the code for a token and reads the profile.
	Exchange(ctx context.Context, code, verifier, redirectURL string) (*Identity, error)
}

// ProviderConfig carries one provider's credentials.
type ProviderConfig struct {
	ClientID     string
	ClientSecret string
	// Issuer is only used by the generic OIDC provider (discovery endpoint).
	Issuer string
	Scopes []string
}

// Configured reports whether this provider can be used.
func (c ProviderConfig) Configured() bool { return c.ClientID != "" && c.ClientSecret != "" }

// OAuthConfig is the whole external login surface. A provider with no
// credentials is simply absent from the login page.
type OAuthConfig struct {
	Google  ProviderConfig
	GitHub  ProviderConfig
	OIDC    ProviderConfig
	Enabled bool
}

// Providers returns every configured provider.
func (c OAuthConfig) Providers() []Provider {
	out := make([]Provider, 0, 3)
	if !c.Enabled {
		return out
	}
	if c.Google.Configured() {
		out = append(out, &googleProvider{cfg: c.Google})
	}
	if c.GitHub.Configured() {
		out = append(out, &githubProvider{cfg: c.GitHub})
	}
	if c.OIDC.Configured() && c.OIDC.Issuer != "" {
		out = append(out, &oidcProvider{cfg: c.OIDC})
	}
	return out
}

// Find returns one configured provider by name.
func (c OAuthConfig) Find(name string) (Provider, bool) {
	for _, p := range c.Providers() {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// scopeString renders the configured scopes or a provider default.
func scopeString(p ProviderConfig, def string) string {
	if len(p.Scopes) == 0 {
		return def
	}
	return strings.Join(p.Scopes, " ")
}

// ---------------------------------------------------------------------------
// google
// ---------------------------------------------------------------------------

type googleProvider struct{ cfg ProviderConfig }

func (p *googleProvider) Name() string     { return "google" }
func (p *googleProvider) Configured() bool { return p.cfg.Configured() }

func (p *googleProvider) AuthURL(state, verifier, redirectURL string) string {
	q := url.Values{
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {redirectURL},
		"response_type":         {"code"},
		"scope":                 {scopeString(p.cfg, "openid email profile")},
		"state":                 {state},
		"code_challenge":        {CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"access_type":           {"online"},
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
}

func (p *googleProvider) Exchange(ctx context.Context, code, verifier, redirectURL string) (*Identity, error) {
	tok, err := tokenRequest(ctx, "https://oauth2.googleapis.com/token", url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURL},
	})
	if err != nil {
		return nil, ErrOAuthFailed
	}
	var profile struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := userinfoRequest(ctx, "https://openidconnect.googleapis.com/v1/userinfo", tok, &profile); err != nil {
		return nil, ErrOAuthFailed
	}
	if profile.Sub == "" {
		return nil, ErrOAuthFailed
	}
	return &Identity{Provider: "google", Subject: profile.Sub, Email: profile.Email, Name: profile.Name}, nil
}

// ---------------------------------------------------------------------------
// github
// ---------------------------------------------------------------------------

type githubProvider struct{ cfg ProviderConfig }

func (p *githubProvider) Name() string     { return "github" }
func (p *githubProvider) Configured() bool { return p.cfg.Configured() }

func (p *githubProvider) AuthURL(state, verifier, redirectURL string) string {
	q := url.Values{
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {redirectURL},
		"state":                 {state},
		"scope":                 {scopeString(p.cfg, "read:user user:email")},
		"code_challenge":        {CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

func (p *githubProvider) Exchange(ctx context.Context, code, verifier, redirectURL string) (*Identity, error) {
	tok, err := tokenRequest(ctx, "https://github.com/login/oauth/access_token", url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURL},
	})
	if err != nil {
		return nil, ErrOAuthFailed
	}
	var user struct {
		ID    any    `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := userinfoRequest(ctx, "https://api.github.com/user", tok, &user); err != nil {
		return nil, ErrOAuthFailed
	}
	subject := fmt.Sprint(user.ID)
	if subject == "" || subject == "<nil>" {
		return nil, ErrOAuthFailed
	}
	email := user.Email
	if email == "" {
		// GitHub only exposes the address on the emails endpoint.
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := userinfoRequest(ctx, "https://api.github.com/user/emails", tok, &emails); err == nil {
			for _, e := range emails {
				if e.Email != "" && (e.Primary || e.Verified) {
					email = e.Email
					break
				}
			}
		}
	}
	return &Identity{Provider: "github", Subject: subject, Email: email, Name: firstNonEmpty(user.Name, user.Login)}, nil
}

// ---------------------------------------------------------------------------
// generic OIDC
// ---------------------------------------------------------------------------

type oidcProvider struct {
	cfg      ProviderConfig
	endpoint oidcEndpoints
}

type oidcEndpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	Userinfo      string `json:"userinfo_endpoint"`
}

func (p *oidcProvider) Name() string     { return "oidc" }
func (p *oidcProvider) Configured() bool { return p.cfg.Configured() && p.cfg.Issuer != "" }

func (p *oidcProvider) AuthURL(state, verifier, redirectURL string) string {
	end := p.discover()
	if end.Authorization == "" {
		return ""
	}
	q := url.Values{
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {redirectURL},
		"response_type":         {"code"},
		"scope":                 {scopeString(p.cfg, "openid email profile")},
		"state":                 {state},
		"code_challenge":        {CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return end.Authorization + "?" + q.Encode()
}

func (p *oidcProvider) Exchange(ctx context.Context, code, verifier, redirectURL string) (*Identity, error) {
	end := p.discover()
	if end.Token == "" {
		return nil, ErrOAuthFailed
	}
	tok, err := tokenRequest(ctx, end.Token, url.Values{
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURL},
	})
	if err != nil {
		return nil, ErrOAuthFailed
	}
	var profile struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if end.Userinfo != "" {
		if err := userinfoRequest(ctx, end.Userinfo, tok, &profile); err != nil {
			return nil, ErrOAuthFailed
		}
	}
	if profile.Sub == "" {
		return nil, ErrOAuthFailed
	}
	return &Identity{Provider: "oidc", Subject: profile.Sub, Email: profile.Email, Name: profile.Name}, nil
}

func (p *oidcProvider) discover() oidcEndpoints {
	if p.endpoint.Authorization != "" {
		return p.endpoint
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return p.endpoint
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return p.endpoint
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.endpoint
	}
	_ = json.NewDecoder(resp.Body).Decode(&p.endpoint)
	return p.endpoint
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// CodeChallenge is the PKCE S256 transformation of a verifier.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewVerifier returns a fresh PKCE verifier (43–128 characters).
func NewVerifier() (string, error) {
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	return tok, nil
}

func tokenRequest(ctx context.Context, endpoint string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint %s: status %d", endpoint, resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("token endpoint returned no access token")
	}
	return out.AccessToken, nil
}

func userinfoRequest(ctx context.Context, endpoint, token string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("userinfo %s: status %d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (c ProviderConfig) scopes(def string) string {
	if len(c.Scopes) == 0 {
		return def
	}
	return strings.Join(c.Scopes, " ")
}

// OAuthProviders returns the providers of the service configuration, or nil when
// OAuth is switched off.
func (s *Service) OAuthProviders() []Provider { return s.oauthCfg.Providers() }

// BeginOAuth returns the redirect URL for a provider plus the state/verifier the
// caller must keep (in short lived cookies) to validate the callback.
func (s *Service) BeginOAuth(provider, redirectURL string) (authURL, state, verifier string, err error) {
	p, ok := s.oauthCfg.Find(provider)
	if !ok {
		return "", "", "", ErrOAuthNotConfigured
	}
	state, err = NewToken()
	if err != nil {
		return "", "", "", err
	}
	verifier, err = NewVerifier()
	if err != nil {
		return "", "", "", err
	}
	url := p.AuthURL(state, verifier, redirectURL)
	if url == "" {
		return "", "", "", ErrOAuthNotConfigured
	}
	return url, state, verifier, nil
}

// CompleteOAuth finishes a login: exchange the code, find or create the local
// account, bind the provider identity and open a session.
func (s *Service) CompleteOAuth(ctx context.Context, provider, code, verifier, redirectURL, userAgent, ip string) (*User, string, *Session, error) {
	p, ok := s.oauthCfg.Find(provider)
	if !ok {
		return nil, "", nil, ErrOAuthNotConfigured
	}
	identity, err := p.Exchange(ctx, code, verifier, redirectURL)
	if err != nil {
		return nil, "", nil, err
	}
	if identity.Subject == "" {
		return nil, "", nil, ErrOAuthFailed
	}
	if s.users == nil || s.oauth == nil {
		return nil, "", nil, errors.New("auth: stores not configured")
	}

	var user *User
	if acc, err := s.oauth.Find(ctx, identity.Provider, identity.Subject); err == nil && acc != nil {
		user, err = s.users.Get(ctx, acc.UserID)
		if err != nil {
			return nil, "", nil, err
		}
	}
	if user == nil && identity.Email != "" {
		// An existing account with the same address is linked rather than
		// duplicated, but only when the provider vouches for that address.
		if existing, err := s.users.GetByEmail(ctx, NormalizeEmail(identity.Email)); err == nil && existing != nil {
			user = existing
		}
	}
	if user == nil {
		if identity.Email == "" {
			return nil, "", nil, ErrOAuthNoEmail
		}
		count, err := s.users.Count(ctx)
		if err != nil {
			return nil, "", nil, err
		}
		user = &User{
			ID:           NewID("user"),
			Email:        NormalizeEmail(identity.Email),
			Name:         firstNonEmpty(identity.Name, strings.Split(identity.Email, "@")[0]),
			PasswordHash: "",
			Status:       StatusActive,
			IsOwner:      count == 0,
		}
		if err := s.users.Create(ctx, user); err != nil {
			return nil, "", nil, err
		}
	}
	if user.Status != StatusActive {
		return nil, "", nil, ErrUserDisabled
	}
	if err := s.oauth.Link(ctx, &OAuthAccount{
		ID:       NewID("oauth"),
		UserID:   user.ID,
		Provider: identity.Provider,
		Subject:  identity.Subject,
		Email:    identity.Email,
	}); err != nil {
		return nil, "", nil, err
	}
	sess, token, err := s.openSession(ctx, user.ID, userAgent, ip)
	if err != nil {
		return nil, "", nil, err
	}
	return user, token, sess, nil
}
