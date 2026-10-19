package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// External login is the part of auth that reaches other people's servers, so the
// offline rules around it matter most: which providers exist at all, what the
// redirect carries, and how a callback that does not match is handled. No case
// here touches the network.

func TestOAuthProvidersOnlyOfferConfiguredOnes(t *testing.T) {
	cfg := OAuthConfig{Enabled: true}
	if got := cfg.Providers(); len(got) != 0 {
		t.Fatalf("providers = %d, want none without credentials", len(got))
	}
	// Disabled is a hard switch: credentials alone must not enable it.
	withCreds := OAuthConfig{
		Google:  ProviderConfig{ClientID: "id", ClientSecret: "secret"},
		GitHub:  ProviderConfig{ClientID: "id", ClientSecret: "secret"},
		OIDC:    ProviderConfig{ClientID: "id", ClientSecret: "secret", Issuer: "https://issuer.test"},
		Enabled: false,
	}
	if got := withCreds.Providers(); len(got) != 0 {
		t.Fatalf("providers = %d, want none while OAuth is off", len(got))
	}

	withCreds.Enabled = true
	got := withCreds.Providers()
	if len(got) != 3 {
		t.Fatalf("providers = %d, want google, github and oidc", len(got))
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name()] = true
		if !p.Configured() {
			t.Fatalf("%s reports itself unconfigured", p.Name())
		}
	}
	for _, want := range []string{"google", "github", "oidc"} {
		if !names[want] {
			t.Fatalf("provider %q missing: %v", want, names)
		}
	}
	// An OIDC provider without an issuer cannot be used, so it is not offered.
	noIssuer := withCreds
	noIssuer.OIDC.Issuer = ""
	if got := noIssuer.Providers(); len(got) != 2 {
		t.Fatalf("providers = %d, want the two without an issuer requirement", len(got))
	}
	if _, ok := withCreds.Find("google"); !ok {
		t.Fatal("google must be findable")
	}
	if _, ok := withCreds.Find("twitter"); ok {
		t.Fatal("an unknown provider must not be findable")
	}
}

// The redirect has to carry PKCE and the state the callback is checked against,
// and it must never carry the secret.
func TestBeginOAuthBuildsACompleteRedirect(t *testing.T) {
	svc := New(nil, nil, nil, nil, Options{
		OAuth: OAuthConfig{
			Enabled: true,
			Google:  ProviderConfig{ClientID: "gid", ClientSecret: "gsecret"},
			GitHub:  ProviderConfig{ClientID: "hid", ClientSecret: "hsecret"},
		},
	})
	if got := svc.OAuthProviders(); len(got) != 2 {
		t.Fatalf("providers = %d, want 2", len(got))
	}

	for _, name := range []string{"google", "github"} {
		authURL, state, verifier, err := svc.BeginOAuth(name, "https://hub.test/api/auth/oauth/callback")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if state == "" || verifier == "" {
			t.Fatalf("%s: state/verifier = %q / %q", name, state, verifier)
		}
		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatalf("%s: url = %v", name, err)
		}
		q := parsed.Query()
		if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("%s: PKCE missing: %v", name, q)
		}
		if q.Get("state") != state {
			t.Fatalf("%s: state mismatch", name)
		}
		if q.Get("client_id") == "" || q.Get("redirect_uri") == "" {
			t.Fatalf("%s: incomplete request: %v", name, q)
		}
		if strings.Contains(authURL, "secret") {
			t.Fatalf("%s: the redirect leaks a credential: %s", name, authURL)
		}
	}

	// A provider nobody configured is a clear error, not an empty URL.
	if _, _, _, err := svc.BeginOAuth("twitter", "https://hub.test/cb"); err != ErrOAuthNotConfigured {
		t.Fatalf("unknown provider = %v, want ErrOAuthNotConfigured", err)
	}
}

// A callback for a provider that is not configured must fail before anything is
// exchanged or written.
func TestCompleteOAuthRefusesUnconfiguredProviders(t *testing.T) {
	svc := New(newMemUserStore(), newMemSessionStore(), nil, nil, Options{})
	if _, _, _, err := svc.CompleteOAuth(context.Background(), "google", "code", "verifier", "https://hub.test/cb", "ua", "1.2.3.4"); err != ErrOAuthNotConfigured {
		t.Fatalf("err = %v, want ErrOAuthNotConfigured", err)
	}
	if got := svc.OAuthProviders(); len(got) != 0 {
		t.Fatalf("providers = %d, want none", len(got))
	}
}

// The pure helpers behind the flow: scopes fall back to a provider default, and
// the first usable value wins.
func TestOAuthHelpers(t *testing.T) {
	if got := (ProviderConfig{}).scopes("openid email"); got != "openid email" {
		t.Fatalf("scopes = %q, want the default", got)
	}
	if got := (ProviderConfig{Scopes: []string{"a", "b"}}).scopes("default"); got != "a b" {
		t.Fatalf("scopes = %q", got)
	}
	if got := scopeString(ProviderConfig{}, "read:user"); got != "read:user" {
		t.Fatalf("scopeString = %q", got)
	}
	if got := firstNonEmpty("", " ", "third", "fourth"); got != "third" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", "  "); got != "" {
		t.Fatalf("firstNonEmpty = %q, want empty", got)
	}
	// A verifier is long and random, and two of them differ.
	a, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	b, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if len(a) < 32 || a == b {
		t.Fatalf("verifiers = %q / %q", a, b)
	}
	// The challenge is derived from the verifier, deterministically.
	if CodeChallenge(a) != CodeChallenge(a) || CodeChallenge(a) == a {
		t.Fatal("the challenge must be a stable one way derivation")
	}
}

// The cookie is the only thing a browser holds, so its flags are part of the
// contract: HttpOnly, SameSite=Lax, and Secure only when configured for it.
func TestSessionCookieFlags(t *testing.T) {
	insecure := New(nil, nil, nil, nil, Options{CookieName: "agenthub_session", Secure: false})
	rec := httptest.NewRecorder()
	expires := time.Now().UTC().Add(time.Hour)
	insecure.SetCookie(rec, "token", expires)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	got := cookies[0]
	if !got.HttpOnly || got.Secure || got.SameSite != http.SameSiteLaxMode || got.Path != "/" {
		t.Fatalf("cookie = %+v", got)
	}
	if got.Name != "agenthub_session" || got.Value != "token" || got.MaxAge <= 0 {
		t.Fatalf("cookie = %+v", got)
	}

	// Clearing writes the same cookie with an expiry in the past.
	rec = httptest.NewRecorder()
	insecure.ClearCookie(rec)
	cleared := rec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 || cleared[0].Value != "" {
		t.Fatalf("cleared cookie = %+v", cleared)
	}

	// A secure deployment marks the cookie, and the defaults fill in.
	secure := New(nil, nil, nil, nil, Options{Secure: true})
	if secure.Options().CookieName != "agenthub_session" {
		t.Fatalf("cookie name = %q, want the default", secure.Options().CookieName)
	}
	if secure.Options().TTL <= 0 {
		t.Fatalf("ttl = %v, want a default", secure.Options().TTL)
	}
	rec = httptest.NewRecorder()
	secure.SetCookie(rec, "token", expires)
	if !rec.Result().Cookies()[0].Secure {
		t.Fatal("a secure deployment must mark the cookie")
	}
}

// The accessors and the inert paths are what let an instance run without an
// identity stack at all.
func TestServiceWithoutStoresStaysInert(t *testing.T) {
	svc := New(nil, nil, nil, nil, Options{})
	if svc.Users() != nil || svc.Authz() != nil {
		t.Fatal("a service without stores must expose nil ports")
	}
	if got, err := svc.Sessions(context.Background(), "user-1"); got != nil || err != nil {
		t.Fatalf("sessions = %v, %v", got, err)
	}
	// No session store means no identity: the request is simply anonymous.
	req := httptest.NewRequest(http.MethodGet, "/api/resources", nil)
	p, sess, err := svc.Principal(context.Background(), req)
	if p != nil || sess != nil || err != nil {
		t.Fatalf("principal = %v, %v, %v", p, sess, err)
	}
	// Logging out without a session is a no-op, not an error.
	if err := svc.Logout(context.Background(), ""); err != nil {
		t.Fatalf("logout = %v", err)
	}
	if err := svc.RevokeSession(context.Background(), "user-1", "ses-1"); err != ErrNotFound {
		t.Fatalf("revoke = %v, want ErrNotFound", err)
	}
	// An unknown token never authenticates.
	if _, _, err := svc.Authenticate(context.Background(), "not-a-token"); err != ErrSessionExpired {
		t.Fatalf("authenticate = %v, want ErrSessionExpired", err)
	}
}
