package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newWechatTestService() (*Service, *memUserStore, *memSessionStore) {
	users, sessions := newMemUserStore(), newMemSessionStore()
	cfg := OAuthConfig{
		Enabled: true,
		WeChat:  ProviderConfig{ClientID: "wx-app-id", ClientSecret: "wx-app-secret"},
	}
	return New(users, sessions, &memOAuthStore{}, nil, nil, Options{OAuth: cfg, TTL: time.Hour}), users, sessions
}

func TestWeChatProviderAuthURL(t *testing.T) {
	p := &wechatProvider{cfg: ProviderConfig{ClientID: "wx-app-id", ClientSecret: "wx-app-secret"}}
	got := p.AuthURL("the-state", "the-verifier", "https://hub.example.com/api/auth/oauth/wechat/callback")

	if !strings.Contains(got, "open.weixin.qq.com/connect/qrconnect") {
		t.Fatalf("auth url = %q, want the WeChat QR authorize host", got)
	}
	if !strings.Contains(got, "scope=snsapi_login") {
		t.Fatalf("auth url = %q, want scope=snsapi_login", got)
	}
	if !strings.Contains(got, "state=the-state") {
		t.Fatalf("auth url = %q, want the state echoed back", got)
	}
	if !strings.HasSuffix(got, "#wechat_redirect") {
		t.Fatalf("auth url = %q, want the #wechat_redirect anchor", got)
	}
	// WeChat's web login does not support PKCE, so the challenge must be absent.
	if strings.Contains(got, "code_challenge") {
		t.Fatalf("auth url = %q, wechat must not use PKCE", got)
	}
}

func TestWeChatProviderConfigured(t *testing.T) {
	if (&wechatProvider{cfg: ProviderConfig{}}).Configured() {
		t.Fatal("an unconfigured WeChat provider must report not configured")
	}
	if !(&wechatProvider{cfg: ProviderConfig{ClientID: "a", ClientSecret: "b"}}).Configured() {
		t.Fatal("a WeChat provider with both credentials must be configured")
	}
}

func TestWeChatProviderNotListedWhenUnconfigured(t *testing.T) {
	cfg := OAuthConfig{Enabled: true} // no WeChat credentials
	if _, ok := cfg.Find("wechat"); ok {
		t.Fatal("an unconfigured WeChat provider must not be discoverable")
	}
}

// withWechatServer points the WeChat API at an httptest server for the duration
// of the caller, returning it so the caller can assert on requests.
func withWechatServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	old := wechatAPIBase
	wechatAPIBase = srv.URL
	t.Cleanup(func() {
		wechatAPIBase = old
		srv.Close()
	})
	return srv
}

func TestWeChatExchange(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/sns/oauth2/access_token"):
			w.Write([]byte(`{"access_token":"AT","openid":"OPENID","unionid":"UNIONID","expires_in":7200}`))
		case strings.Contains(r.URL.Path, "/sns/userinfo"):
			w.Write([]byte(`{"openid":"OPENID","nickname":"微信用户","unionid":"UNIONID"}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	})

	p := &wechatProvider{cfg: ProviderConfig{ClientID: "wx-app-id", ClientSecret: "wx-app-secret"}}
	id, err := p.Exchange(context.Background(), "the-code", "ignored-verifier", "https://hub.example.com/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Provider != "wechat" {
		t.Fatalf("provider = %q, want wechat", id.Provider)
	}
	if id.Subject != "UNIONID" {
		t.Fatalf("subject = %q, want the unionid", id.Subject)
	}
	if id.Email != "" {
		t.Fatalf("email = %q, wechat must not return an email", id.Email)
	}
	if id.Name != "微信用户" {
		t.Fatalf("name = %q, want the WeChat nickname", id.Name)
	}
}

func TestWeChatExchangeFallsBackToOpenID(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/sns/oauth2/access_token"):
			w.Write([]byte(`{"access_token":"AT","openid":"OPENID-ONLY","expires_in":7200}`))
		case strings.Contains(r.URL.Path, "/sns/userinfo"):
			w.Write([]byte(`{"openid":"OPENID-ONLY","nickname":"n"}`))
		}
	})

	p := &wechatProvider{cfg: ProviderConfig{ClientID: "wx-app-id", ClientSecret: "wx-app-secret"}}
	id, err := p.Exchange(context.Background(), "code", "", "")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Subject != "OPENID-ONLY" {
		t.Fatalf("subject = %q, want the openid fallback", id.Subject)
	}
}

func TestWeChatExchangeError(t *testing.T) {
	withWechatServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"errcode":40029,"errmsg":"invalid code"}`))
	})
	p := &wechatProvider{cfg: ProviderConfig{ClientID: "wx-app-id", ClientSecret: "wx-app-secret"}}
	if _, err := p.Exchange(context.Background(), "bad", "", ""); err != ErrOAuthFailed {
		t.Fatalf("exchange error = %v, want ErrOAuthFailed", err)
	}
}

func TestCompleteOAuthWeChatCreatesEmailLessUser(t *testing.T) {
	svc, _, _ := newWechatTestService()
	withWechatServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/sns/oauth2/access_token"):
			w.Write([]byte(`{"access_token":"AT","openid":"OPENID","unionid":"UNIONID","expires_in":7200}`))
		case strings.Contains(r.URL.Path, "/sns/userinfo"):
			w.Write([]byte(`{"openid":"OPENID","nickname":"微信用户","unionid":"UNIONID"}`))
		}
	})

	user, token, sess, err := svc.CompleteOAuth(context.Background(), "wechat", "code", "verifier", "https://hub.example.com/callback", "curl", "10.0.0.1")
	if err != nil {
		t.Fatalf("complete oauth: %v", err)
	}
	if user.Email != "" {
		t.Fatalf("wechat user email = %q, want empty", user.Email)
	}
	if user.Name != "微信用户" {
		t.Fatalf("wechat user name = %q, want the nickname", user.Name)
	}
	if token == "" || sess == nil {
		t.Fatal("a session must be opened for the new wechat user")
	}

	// A second login with the same WeChat identity must resolve the existing
	// account instead of creating another one.
	again, _, _, err := svc.CompleteOAuth(context.Background(), "wechat", "code", "verifier", "https://hub.example.com/callback", "curl", "10.0.0.1")
	if err != nil {
		t.Fatalf("complete oauth (repeat): %v", err)
	}
	if again.ID != user.ID {
		t.Fatal("repeating the wechat login must return the same user")
	}
}
