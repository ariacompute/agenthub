package auth

// External login is the one part of auth that talks to the internet, so these
// tests never do: the two HTTP helpers take their endpoint as an argument and
// the OIDC issuer is a URL, which makes both provable against a local server.
// No provider credentials are needed anywhere in this file.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The RFC 7636 appendix B vector: a challenge that does not match this is not
// PKCE, and the provider will reject the exchange.
func TestCodeChallengeMatchesThePkceVector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := CodeChallenge(verifier); got != want {
		t.Fatalf("CodeChallenge = %q, want %q", got, want)
	}
}

func TestNewVerifierIsUsableAsAPkceVerifier(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		verifier, err := NewVerifier()
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if len(verifier) < 43 || len(verifier) > 128 {
			t.Fatalf("verifier is %d characters, want 43-128 (RFC 7636)", len(verifier))
		}
		if seen[verifier] {
			t.Fatalf("NewVerifier repeated %q; a reused verifier links two logins", verifier)
		}
		seen[verifier] = true
		if url.QueryEscape(verifier) != strings.ReplaceAll(verifier, "-", "-") && strings.ContainsAny(verifier, "+/") {
			t.Fatalf("verifier %q is not URL safe", verifier)
		}
	}
}

func TestProvidersOnlyOfferWhatHasCredentials(t *testing.T) {
	full := OAuthConfig{
		Enabled: true,
		Google:  ProviderConfig{ClientID: "g-id", ClientSecret: "g-secret"},
		GitHub:  ProviderConfig{ClientID: "h-id", ClientSecret: "h-secret"},
		OIDC:    ProviderConfig{ClientID: "o-id", ClientSecret: "o-secret"},
	}
	// The generic provider also needs an issuer to discover: credentials alone
	// are not a configuration.
	if got := len(full.Providers()); got != 2 {
		t.Fatalf("Providers = %d, want 2 without an issuer", got)
	}
	full.OIDC.Issuer = "https://issuer.example.com"
	if got := len(full.Providers()); got != 3 {
		t.Fatalf("Providers = %d, want 3 with an issuer", got)
	}

	// A half configured provider is absent, not broken: the console hides the
	// button instead of offering one that answers a 500.
	half := OAuthConfig{Enabled: true, Google: ProviderConfig{ClientID: "g-id"}}
	if len(half.Providers()) != 0 {
		t.Fatal("a provider with no secret must not be offered")
	}
	// Enabled is the master switch: credentials alone must not put a button on
	// the login page.
	if len(OAuthConfig{Google: full.Google}.Providers()) != 0 {
		t.Fatal("a disabled configuration must offer nothing")
	}
}

func TestFindReturnsTheProviderByName(t *testing.T) {
	cfg := OAuthConfig{
		Enabled: true,
		GitHub:  ProviderConfig{ClientID: "h-id", ClientSecret: "h-secret"},
	}
	found, ok := cfg.Find("github")
	if !ok || found.Name() != "github" {
		t.Fatalf("Find(github) = %v, %v", found, ok)
	}
	if _, ok := cfg.Find("google"); ok {
		t.Fatal("Find(google) must not invent a provider that is not configured")
	}
}

func TestScopeStringFallsBackToTheProviderDefault(t *testing.T) {
	if got := scopeString(ProviderConfig{}, "openid email"); got != "openid email" {
		t.Fatalf("scopeString = %q, want the default", got)
	}
	if got := scopeString(ProviderConfig{Scopes: []string{"a", "b"}}, "def"); got != "a b" {
		t.Fatalf("scopeString = %q, want the configured scopes", got)
	}
}

// The authorization URL is the whole security handshake: a missing state or a
// plain challenge is how a login gets hijacked.
func TestAuthURLCarriesStateAndThePkceChallenge(t *testing.T) {
	verifier := "a-fresh-verifier-long-enough-to-be-legal-here"
	cfg := ProviderConfig{ClientID: "client-1", ClientSecret: "secret"}
	cases := []struct {
		name     string
		provider Provider
		wantHost string
	}{
		{"google", &googleProvider{cfg: cfg}, "accounts.google.com"},
		{"github", &githubProvider{cfg: cfg}, "github.com"},
	}
	for _, c := range cases {
		raw := c.provider.AuthURL("state-1", verifier, "https://hub.example.com/oauth/callback")
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: parse %q: %v", c.name, raw, err)
		}
		if parsed.Host != c.wantHost {
			t.Errorf("%s: host = %q, want %q", c.name, parsed.Host, c.wantHost)
		}
		q := parsed.Query()
		if q.Get("state") != "state-1" {
			t.Errorf("%s: state = %q", c.name, q.Get("state"))
		}
		if q.Get("code_challenge") != CodeChallenge(verifier) {
			t.Errorf("%s: challenge = %q, want the S256 transformation", c.name, q.Get("code_challenge"))
		}
		if q.Get("code_challenge_method") != "S256" {
			t.Errorf("%s: method = %q, want S256", c.name, q.Get("code_challenge_method"))
		}
		if q.Get("redirect_uri") != "https://hub.example.com/oauth/callback" {
			t.Errorf("%s: redirect_uri = %q", c.name, q.Get("redirect_uri"))
		}
		if q.Get("client_id") != "client-1" {
			t.Errorf("%s: client_id = %q", c.name, q.Get("client_id"))
		}
		if strings.Contains(raw, "secret") {
			t.Errorf("%s: the URL carries the client secret: %q", c.name, raw)
		}
	}
}

func TestTokenRequestReadsTheAccessToken(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-1","token_type":"bearer"}`))
	}))
	defer server.Close()

	token, err := tokenRequest(context.Background(), server.URL, url.Values{"code": {"c1"}})
	if err != nil {
		t.Fatalf("tokenRequest: %v", err)
	}
	if token != "tok-1" {
		t.Fatalf("token = %q, want tok-1", token)
	}
	if gotForm.Get("code") != "c1" {
		t.Fatalf("the form did not carry the code: %v", gotForm)
	}
}

func TestTokenRequestRefusesAnythingThatIsNotAToken(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rejected by the provider", http.StatusBadRequest, `{"error":"invalid_grant"}`},
		{"no access token", http.StatusOK, `{"token_type":"bearer"}`},
		{"not json", http.StatusOK, `not json at all`},
	}
	for _, c := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		if _, err := tokenRequest(context.Background(), server.URL, url.Values{}); err == nil {
			t.Errorf("%s: tokenRequest accepted the response", c.name)
		}
		server.Close()
	}
}

func TestUserinfoRequestSendsTheTokenAndDecodesTheProfile(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"sub":"user-1","email":"u@example.com"}`))
	}))
	defer server.Close()

	var profile struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := userinfoRequest(context.Background(), server.URL, "tok-1", &profile); err != nil {
		t.Fatalf("userinfoRequest: %v", err)
	}
	if profile.Sub != "user-1" || profile.Email != "u@example.com" {
		t.Fatalf("profile = %+v", profile)
	}
	if authorization != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", authorization)
	}
}

func TestUserinfoRequestRefusesAFailedCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	var dst map[string]any
	if err := userinfoRequest(context.Background(), server.URL, "tok", &dst); err == nil {
		t.Fatal("userinfoRequest accepted a 401")
	}
}

// Discovery is best effort: an issuer that cannot be reached must leave the
// provider unusable rather than panic or hang the login page.
func TestOidcDiscoveryReadsTheDocumentAndSurvivesTheRest(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(oidcEndpoints{
			Authorization: "https://issuer.test/authorize",
			Token:         "https://issuer.test/token",
			Userinfo:      "https://issuer.test/userinfo",
		})
	}))
	defer good.Close()

	provider := &oidcProvider{cfg: ProviderConfig{Issuer: good.URL}}
	end := provider.discover()
	if end.Authorization != "https://issuer.test/authorize" {
		t.Fatalf("discover = %+v, want the authorization endpoint", end)
	}
	// The second call is served from memory: discovery happens once.
	if again := provider.discover(); again.Token != "https://issuer.test/token" {
		t.Fatalf("discover = %+v, want the cached endpoints", again)
	}
	raw := provider.AuthURL("state", "verifier", "https://hub.test/cb")
	if !strings.HasPrefix(raw, "https://issuer.test/authorize?") {
		t.Fatalf("AuthURL = %q, want it aimed at the discovered endpoint", raw)
	}

	// An issuer that answers 404 leaves no endpoint, and AuthURL then returns
	// nothing at all rather than a URL that goes nowhere.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer broken.Close()
	missing := &oidcProvider{cfg: ProviderConfig{Issuer: broken.URL}}
	if missing.AuthURL("state", "verifier", "https://hub.test/cb") != "" {
		t.Fatal("AuthURL must stay empty when discovery finds no endpoint")
	}
	if _, err := missing.Exchange(context.Background(), "code", "verifier", "https://hub.test/cb"); err == nil {
		t.Fatal("Exchange must refuse to run without a token endpoint")
	}
}

func TestFirstNonEmptySkipsBlanks(t *testing.T) {
	if got := firstNonEmpty("  ", "", "name", "login"); got != "name" {
		t.Fatalf("firstNonEmpty = %q, want name", got)
	}
	if got := firstNonEmpty("", "   "); got != "" {
		t.Fatalf("firstNonEmpty = %q, want empty", got)
	}
}
