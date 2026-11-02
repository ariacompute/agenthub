package auth

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// the service facade over the provider set
// ---------------------------------------------------------------------------

func TestServiceOAuthProvidersFollowsTheConfiguration(t *testing.T) {
	svc, _, _ := newOAuthTestService(OAuthConfig{
		Enabled: true,
		Google:  ProviderConfig{ClientID: "g-id", ClientSecret: "g-sec"},
		GitHub:  ProviderConfig{ClientID: "gh-id", ClientSecret: "gh-sec"},
	})
	var names []string
	for _, p := range svc.OAuthProviders() {
		names = append(names, p.Name())
	}
	if !contains(names, "google") || !contains(names, "github") {
		t.Fatalf("providers = %v, want google and github", names)
	}

	off, _, _ := newOAuthTestService(OAuthConfig{Enabled: false, Google: ProviderConfig{ClientID: "g-id", ClientSecret: "g-sec"}})
	if got := off.OAuthProviders(); len(got) != 0 {
		t.Fatalf("a disabled service must expose no providers, got %d", len(got))
	}
}

func TestBeginOAuthHandsBackTheStateAndVerifier(t *testing.T) {
	svc, _, _ := newOAuthTestService(OAuthConfig{
		Enabled: true,
		Google:  ProviderConfig{ClientID: "g-id", ClientSecret: "g-sec"},
	})

	authURL, state, verifier, err := svc.BeginOAuth("google", "https://app.example/api/auth/oauth/google/callback")
	if err != nil {
		t.Fatalf("BeginOAuth: %v", err)
	}
	if state == "" || verifier == "" {
		t.Fatalf("state=%q verifier=%q, both must be fresh secrets", state, verifier)
	}
	if len(verifier) < 43 {
		t.Fatalf("verifier too short for PKCE: %q", verifier)
	}
	for _, want := range []string{
		"https://accounts.google.com/o/oauth2/v2/auth",
		"state=" + state,
		"code_challenge=" + CodeChallenge(verifier),
		"code_challenge_method=S256",
		"redirect_uri=https%3A%2F%2Fapp.example",
	} {
		if !strings.Contains(authURL, want) {
			t.Fatalf("auth url %q missing %q", authURL, want)
		}
	}

	// Every call is a fresh attempt: reusing a state would let a replay finish a
	// login that was already abandoned.
	_, secondState, secondVerifier, err := svc.BeginOAuth("google", "https://app.example/cb")
	if err != nil {
		t.Fatalf("second BeginOAuth: %v", err)
	}
	if secondState == state || secondVerifier == verifier {
		t.Fatal("each attempt needs its own state and verifier")
	}
}

func TestBeginOAuthRefusesAnUnconfiguredProvider(t *testing.T) {
	svc, _, _ := newOAuthTestService(OAuthConfig{
		Enabled: true,
		Google:  ProviderConfig{ClientID: "g-id", ClientSecret: "g-sec"},
	})
	if _, _, _, err := svc.BeginOAuth("github", "https://app.example/cb"); err != ErrOAuthNotConfigured {
		t.Fatalf("err = %v, want ErrOAuthNotConfigured", err)
	}
}

func TestBeginOAuthRefusesWhenTheAuthorizationEndpointIsUnknown(t *testing.T) {
	// An OIDC issuer that cannot be reached leaves no authorization endpoint, so
	// the redirect would be a dead link: say "not configured" instead.
	svc, _, _ := newOAuthTestService(OAuthConfig{
		Enabled: true,
		OIDC:    ProviderConfig{ClientID: "o-id", ClientSecret: "o-sec", Issuer: "http://127.0.0.1:0"},
	})
	if _, _, _, err := svc.BeginOAuth("oidc", "https://app.example/cb"); err != ErrOAuthNotConfigured {
		t.Fatalf("err = %v, want ErrOAuthNotConfigured", err)
	}
}

func TestCompleteOAuthRefusesAnUnconfiguredProvider(t *testing.T) {
	svc, _, _ := newOAuthTestService(OAuthConfig{Enabled: true})
	login, err := svc.CompleteOAuth(context.Background(), "github", "code", "vrf", "https://app.example/cb", "ua", "127.0.0.1")
	if err != ErrOAuthNotConfigured {
		t.Fatalf("err = %v, want ErrOAuthNotConfigured", err)
	}
	if login != nil {
		t.Fatalf("login = %+v, want nil", login)
	}
}
