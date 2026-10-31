package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// overrideVars swaps a set of package-level endpoint vars for the duration of a
// test and restores them afterwards.
func overrideVars(t *testing.T, patch func()) {
	t.Helper()
	orig := map[string]string{
		"googleTokenURL":    googleTokenURL,
		"googleUserinfoURL": googleUserinfoURL,
		"githubTokenURL":    githubTokenURL,
		"githubUserURL":     githubUserURL,
		"githubEmailsURL":   githubEmailsURL,
	}
	patch()
	t.Cleanup(func() {
		googleTokenURL = orig["googleTokenURL"]
		googleUserinfoURL = orig["googleUserinfoURL"]
		githubTokenURL = orig["githubTokenURL"]
		githubUserURL = orig["githubUserURL"]
		githubEmailsURL = orig["githubEmailsURL"]
	})
}

// ---------------------------------------------------------------------------
// scopeString / CodeChallenge
// ---------------------------------------------------------------------------

func TestScopeStringDefault(t *testing.T) {
	cfg := ProviderConfig{}
	if got := scopeString(cfg, "openid email"); got != "openid email" {
		t.Fatalf("scopeString default = %q", got)
	}
}

func TestScopeStringOverride(t *testing.T) {
	cfg := ProviderConfig{Scopes: []string{"a", "b"}}
	if got := scopeString(cfg, "openid email"); got != "a b" {
		t.Fatalf("scopeString override = %q", got)
	}
}

func TestCodeChallengeStable(t *testing.T) {
	a := CodeChallenge("verifier")
	b := CodeChallenge("verifier")
	if a == "" || a != b {
		t.Fatalf("CodeChallenge must be deterministic and non-empty, got %q", a)
	}
	if CodeChallenge("other") == a {
		t.Fatal("different verifiers must produce different challenges")
	}
}

// ---------------------------------------------------------------------------
// tokenRequest / userinfoRequest
// ---------------------------------------------------------------------------

func TestTokenRequestOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"TOK"}`))
	}))
	defer srv.Close()
	tok, err := tokenRequest(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("tokenRequest: %v", err)
	}
	if tok != "TOK" {
		t.Fatalf("token = %q, want TOK", tok)
	}
}

func TestTokenRequestErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	if _, err := tokenRequest(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("expected error for non-200 token response")
	}
}

func TestTokenRequestNoAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"scope":"x"}`))
	}))
	defer srv.Close()
	if _, err := tokenRequest(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("expected error when access_token is missing")
	}
}

func TestUserinfoRequestOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing bearer auth, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"sub":"S"}`))
	}))
	defer srv.Close()
	var dst struct {
		Sub string `json:"sub"`
	}
	if err := userinfoRequest(context.Background(), srv.URL, "TOK", &dst); err != nil {
		t.Fatalf("userinfoRequest: %v", err)
	}
	if dst.Sub != "S" {
		t.Fatalf("sub = %q, want S", dst.Sub)
	}
}

func TestUserinfoRequestErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if err := userinfoRequest(context.Background(), srv.URL, "TOK", &struct{}{}); err == nil {
		t.Fatal("expected error for non-200 userinfo response")
	}
}

// ---------------------------------------------------------------------------
// WeChat edge cases
// ---------------------------------------------------------------------------

func TestWeChatExchangeTokenHTTPError(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	p := &wechatProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "", ""); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

func TestWeChatExchangeTokenBadJSON(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not-json"))
	})
	p := &wechatProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "", ""); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

func TestWeChatExchangeUserInfoHTTPError(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sns/oauth2/access_token") {
			w.Write([]byte(`{"access_token":"AT","openid":"O"}`))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	p := &wechatProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "", ""); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

func TestWeChatExchangeUserInfoError(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sns/oauth2/access_token") {
			w.Write([]byte(`{"access_token":"AT","openid":"O"}`))
		} else {
			w.Write([]byte(`{"errcode":40003,"errmsg":"invalid openid"}`))
		}
	})
	p := &wechatProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "", ""); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// google
// ---------------------------------------------------------------------------

func TestGoogleProviderAuthURL(t *testing.T) {
	p := &googleProvider{cfg: ProviderConfig{ClientID: "cid", ClientSecret: "csec"}}
	got := p.AuthURL("st", "vrf", "https://cb")
	for _, want := range []string{
		"accounts.google.com/o/oauth2/v2/auth",
		"scope=openid+email+profile",
		"code_challenge=",
		"code_challenge_method=S256",
		"state=st",
		"access_type=online",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("auth url = %q, missing %q", got, want)
		}
	}
}

func TestGoogleProviderConfigured(t *testing.T) {
	if (&googleProvider{cfg: ProviderConfig{ClientID: "a"}}).Configured() {
		t.Fatal("google requires both credentials")
	}
	if !(&googleProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}).Configured() {
		t.Fatal("google with both credentials must be configured")
	}
}

func TestGoogleExchange(t *testing.T) {
	var tokenURL, userinfoURL string
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/token"):
				w.Write([]byte(`{"access_token":"T"}`))
			case strings.HasSuffix(r.URL.Path, "/userinfo"):
				w.Write([]byte(`{"sub":"SUB","email":"u@g.com","name":"G User"}`))
			default:
				http.Error(w, "no", http.StatusNotFound)
			}
		}))
		tokenURL, userinfoURL = srv.URL+"/token", srv.URL+"/userinfo"
		googleTokenURL, googleUserinfoURL = tokenURL, userinfoURL
	})
	p := &googleProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	id, err := p.Exchange(context.Background(), "code", "vrf", "https://cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Provider != "google" || id.Subject != "SUB" || id.Email != "u@g.com" || id.Name != "G User" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestGoogleExchangeTokenError(t *testing.T) {
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
		}))
		googleTokenURL = srv.URL
	})
	p := &googleProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "vrf", "https://cb"); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

func TestGoogleExchangeNoSubject(t *testing.T) {
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/token"):
				w.Write([]byte(`{"access_token":"T"}`))
			case strings.HasSuffix(r.URL.Path, "/userinfo"):
				w.Write([]byte(`{"email":"u@g.com"}`))
			}
		}))
		googleTokenURL, googleUserinfoURL = srv.URL+"/token", srv.URL+"/userinfo"
	})
	p := &googleProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "vrf", "https://cb"); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// github
// ---------------------------------------------------------------------------

func TestGithubProviderAuthURL(t *testing.T) {
	p := &githubProvider{cfg: ProviderConfig{ClientID: "cid", ClientSecret: "csec"}}
	got := p.AuthURL("st", "vrf", "https://cb")
	for _, want := range []string{
		"github.com/login/oauth/authorize",
		"scope=read%3Auser+user%3Aemail",
		"code_challenge=",
		"code_challenge_method=S256",
		"state=st",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("auth url = %q, missing %q", got, want)
		}
	}
}

func TestGithubProviderConfigured(t *testing.T) {
	if (&githubProvider{cfg: ProviderConfig{ClientSecret: "b"}}).Configured() {
		t.Fatal("github requires both credentials")
	}
	if !(&githubProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}).Configured() {
		t.Fatal("github with both credentials must be configured")
	}
}

func TestGithubExchange(t *testing.T) {
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/access_token"):
				w.Write([]byte(`{"access_token":"T"}`))
			case strings.HasSuffix(r.URL.Path, "/user"):
				w.Write([]byte(`{"id":123,"login":"octocat","name":"OC"}`))
			case strings.HasSuffix(r.URL.Path, "/emails"):
				w.Write([]byte(`[]`))
			default:
				http.Error(w, "no", http.StatusNotFound)
			}
		}))
		githubTokenURL, githubUserURL, githubEmailsURL = srv.URL+"/access_token", srv.URL+"/user", srv.URL+"/emails"
	})
	p := &githubProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	id, err := p.Exchange(context.Background(), "code", "vrf", "https://cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Provider != "github" || id.Subject != "123" || id.Name != "OC" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestGithubExchangeEmailFromList(t *testing.T) {
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/access_token"):
				w.Write([]byte(`{"access_token":"T"}`))
			case strings.HasSuffix(r.URL.Path, "/user"):
				w.Write([]byte(`{"id":7,"login":"u","name":"U"}`))
			case strings.HasSuffix(r.URL.Path, "/emails"):
				w.Write([]byte(`[{"email":"other@x.com","primary":false,"verified":false},{"email":"primary@x.com","primary":true,"verified":true}]`))
			}
		}))
		githubTokenURL, githubUserURL, githubEmailsURL = srv.URL+"/access_token", srv.URL+"/user", srv.URL+"/emails"
	})
	p := &githubProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	id, err := p.Exchange(context.Background(), "code", "vrf", "https://cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Email != "primary@x.com" {
		t.Fatalf("email = %q, want the primary verified address", id.Email)
	}
}

func TestGithubExchangeNoID(t *testing.T) {
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/access_token"):
				w.Write([]byte(`{"access_token":"T"}`))
			case strings.HasSuffix(r.URL.Path, "/user"):
				w.Write([]byte(`{"login":"u"}`))
			case strings.HasSuffix(r.URL.Path, "/emails"):
				w.Write([]byte(`[]`))
			}
		}))
		githubTokenURL, githubUserURL, githubEmailsURL = srv.URL+"/access_token", srv.URL+"/user", srv.URL+"/emails"
	})
	p := &githubProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}
	if _, err := p.Exchange(context.Background(), "code", "vrf", "https://cb"); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// generic OIDC
// ---------------------------------------------------------------------------

func TestOIDCProviderConfigured(t *testing.T) {
	if (&oidcProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}).Configured() {
		t.Fatal("oidc requires an issuer")
	}
	if !(&oidcProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b", Issuer: "https://iss"}}).Configured() {
		t.Fatal("oidc with issuer and credentials must be configured")
	}
}

func TestOIDCProviderAuthURLFromDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(oidcEndpoints{
				Authorization: "https://op.example/authorize",
				Token:         "https://op.example/token",
				Userinfo:      "https://op.example/userinfo",
			})
			return
		}
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer srv.Close()
	p := &oidcProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b", Issuer: srv.URL}}
	got := p.AuthURL("st", "vrf", "https://cb")
	for _, want := range []string{
		"https://op.example/authorize",
		"scope=openid+email+profile",
		"code_challenge=",
		"state=st",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("auth url = %q, missing %q", got, want)
		}
	}
}

func TestOIDCProviderAuthURLEmptyEndpoint(t *testing.T) {
	p := &oidcProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b", Issuer: "http://127.0.0.1:0"}}
	// discover() cannot reach the issuer, so it returns no authorization endpoint
	// and AuthURL must yield an empty string rather than a half-built URL.
	if got := p.AuthURL("st", "vrf", "https://cb"); got != "" {
		t.Fatalf("auth url = %q, want empty when discovery fails", got)
	}
}

func TestOIDCExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Write([]byte(`{"access_token":"T"}`))
		case r.URL.Path == "/userinfo":
			w.Write([]byte(`{"sub":"SUB","email":"u@x.com","name":"OIDC User"}`))
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := &oidcProvider{
		cfg:      ProviderConfig{ClientID: "a", ClientSecret: "b", Issuer: "https://iss"},
		endpoint: oidcEndpoints{Token: srv.URL + "/token", Userinfo: srv.URL + "/userinfo"},
	}
	id, err := p.Exchange(context.Background(), "code", "vrf", "https://cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Provider != "oidc" || id.Subject != "SUB" || id.Email != "u@x.com" || id.Name != "OIDC User" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestOIDCExchangeNoSubject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Write([]byte(`{"access_token":"T"}`))
		case r.URL.Path == "/userinfo":
			w.Write([]byte(`{"email":"u@x.com"}`))
		}
	}))
	defer srv.Close()
	p := &oidcProvider{
		cfg:      ProviderConfig{ClientID: "a", ClientSecret: "b"},
		endpoint: oidcEndpoints{Token: srv.URL + "/token", Userinfo: srv.URL + "/userinfo"},
	}
	if _, err := p.Exchange(context.Background(), "code", "vrf", "https://cb"); err != ErrOAuthFailed {
		t.Fatalf("want ErrOAuthFailed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OAuthConfig.Providers / Find
// ---------------------------------------------------------------------------

func TestOAuthConfigIncludesWeChatWhenConfigured(t *testing.T) {
	cfg := OAuthConfig{
		Enabled: true,
		WeChat:  ProviderConfig{ClientID: "app", ClientSecret: "sec"},
	}
	var names []string
	for _, p := range cfg.Providers() {
		names = append(names, p.Name())
	}
	if !contains(names, "wechat") {
		t.Fatalf("wechat not in providers: %v", names)
	}
	found, ok := cfg.Find("wechat")
	if !ok || found.Name() != "wechat" {
		t.Fatalf("Find(wechat) = %v, %v", found, ok)
	}
}

func TestOAuthConfigSuppressedWhenDisabled(t *testing.T) {
	cfg := OAuthConfig{
		Enabled: false,
		WeChat:  ProviderConfig{ClientID: "app", ClientSecret: "sec"},
	}
	if len(cfg.Providers()) != 0 {
		t.Fatalf("disabled config must expose no providers, got %d", len(cfg.Providers()))
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// provider names / verifier
// ---------------------------------------------------------------------------

func TestProviderNames(t *testing.T) {
	cases := []struct {
		p    Provider
		want string
	}{
		{&googleProvider{}, "google"},
		{&githubProvider{}, "github"},
		{&wechatProvider{}, "wechat"},
		{&oidcProvider{}, "oidc"},
	}
	for _, c := range cases {
		if c.p.Name() != c.want {
			t.Fatalf("%T.Name() = %q, want %q", c.p, c.p.Name(), c.want)
		}
	}
}

func TestNewVerifier(t *testing.T) {
	v, err := NewVerifier()
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if len(v) < 43 {
		t.Fatalf("verifier too short: %q", v)
	}
}

func TestOAuthConfigIncludesOIDCWhenConfigured(t *testing.T) {
	cfg := OAuthConfig{
		Enabled: true,
		OIDC:    ProviderConfig{ClientID: "a", ClientSecret: "b", Issuer: "https://issuer.example"},
	}
	var names []string
	for _, p := range cfg.Providers() {
		names = append(names, p.Name())
	}
	if !contains(names, "oidc") {
		t.Fatalf("oidc not in providers: %v", names)
	}
}

// ---------------------------------------------------------------------------
// CompleteOAuth with an email-carrying provider (end-to-end via mocked Google)
// ---------------------------------------------------------------------------

func newOAuthTestService(cfg OAuthConfig) (*Service, *memUserStore, *memSessionStore) {
	users, sessions := newMemUserStore(), newMemSessionStore()
	return New(users, sessions, &memOAuthStore{}, nil, nil, Options{OAuth: cfg, TTL: time.Hour}), users, sessions
}

func TestCompleteOAuthGoogleCreatesUser(t *testing.T) {
	overrideVars(t, func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/token"):
				w.Write([]byte(`{"access_token":"T"}`))
			case strings.HasSuffix(r.URL.Path, "/userinfo"):
				w.Write([]byte(`{"sub":"GSUB","email":"googleuser@example.com","name":"G User"}`))
			}
		}))
		googleTokenURL, googleUserinfoURL = srv.URL+"/token", srv.URL+"/userinfo"
	})
	svc, _, _ := newOAuthTestService(OAuthConfig{Enabled: true, Google: ProviderConfig{ClientID: "a", ClientSecret: "b"}})

	user, token, sess, err := svc.CompleteOAuth(context.Background(), "google", "code", "vrf", "https://cb", "curl", "10.0.0.1")
	if err != nil {
		t.Fatalf("complete oauth: %v", err)
	}
	if user.Email != "googleuser@example.com" {
		t.Fatalf("email = %q, want the Google email", user.Email)
	}
	if user.Name != "G User" || token == "" || sess == nil {
		t.Fatalf("unexpected result: name=%q token=%q sess=%v", user.Name, token, sess)
	}

	// A repeat login with the same Google subject must resolve the existing user.
	again, _, _, err := svc.CompleteOAuth(context.Background(), "google", "code", "vrf", "https://cb", "curl", "10.0.0.1")
	if err != nil {
		t.Fatalf("complete oauth (repeat): %v", err)
	}
	if again.ID != user.ID {
		t.Fatal("repeating the login must return the same user")
	}
}

func TestCompleteOAuthUnconfiguredProvider(t *testing.T) {
	svc, _, _ := newOAuthTestService(OAuthConfig{Enabled: true})
	if _, _, _, err := svc.CompleteOAuth(context.Background(), "wechat", "code", "vrf", "https://cb", "curl", "10.0.0.1"); err != ErrOAuthNotConfigured {
		t.Fatalf("want ErrOAuthNotConfigured, got %v", err)
	}
}
