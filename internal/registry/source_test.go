package registry

import (
	"testing"

	"agenthub/internal/httputil"
)

func TestValidateSource(t *testing.T) {
	valid := func() *RegistrySource {
		return &RegistrySource{
			Name:       "upstream",
			Kind:       SourceKindMCP,
			URL:        "https://registry.example.test/sync",
			AuthScheme: AuthSchemeNone,
			Namespace:  "team",
		}
	}
	if err := ValidateSource(valid()); err != nil {
		t.Fatalf("valid source: %v", err)
	}

	cases := []struct {
		name  string
		mut   func(*RegistrySource)
		code  httputil.Code
		field string
	}{
		{"missing name", func(s *RegistrySource) { s.Name = "" }, CodeInvalidSource, "name"},
		{"bad kind", func(s *RegistrySource) { s.Kind = "bogus" }, CodeInvalidSource, "kind"},
		{"bad url", func(s *RegistrySource) { s.URL = "not-a-url" }, CodeInvalidSource, "url"},
		{"bad auth scheme", func(s *RegistrySource) { s.AuthScheme = "basic" }, CodeInvalidSource, "auth_scheme"},
		{"bearer without env", func(s *RegistrySource) {
			s.AuthScheme = AuthSchemeBearer
			s.AuthTokenEnv = ""
		}, CodeInvalidSource, "auth_token_env"},
		{"bad env name", func(s *RegistrySource) {
			s.AuthScheme = AuthSchemeBearer
			s.AuthTokenEnv = "1BAD_NAME"
		}, CodeInvalidSource, "auth_token_env"},
		{"bad namespace", func(s *RegistrySource) { s.Namespace = "team_a" }, CodeInvalidNamespace, ""},
	}
	for _, c := range cases {
		s := valid()
		c.mut(s)
		err := ValidateSource(s)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if httputil.AsError(err).Code != c.code {
			t.Fatalf("%s: code = %v, want %v", c.name, err, c.code)
		}
		if c.field != "" && FieldOf(err) != c.field {
			t.Fatalf("%s: field = %q, want %q", c.name, FieldOf(err), c.field)
		}
	}

	// Bearer with a valid env name is accepted.
	ok := valid()
	ok.AuthScheme = AuthSchemeBearer
	ok.AuthTokenEnv = "UPSTREAM_TOKEN"
	if err := ValidateSource(ok); err != nil {
		t.Fatalf("bearer with env: %v", err)
	}
}

func TestValidateSourceNil(t *testing.T) {
	if err := ValidateSource(nil); httputil.AsError(err).Code != CodeInvalidSource {
		t.Fatalf("nil source code = %v", err)
	}
}

func TestRegistrySourceEffectiveNamespace(t *testing.T) {
	var nilSrc *RegistrySource
	if nilSrc.EffectiveNamespace() != DefaultNamespace {
		t.Fatal("nil source must fall back to default namespace")
	}
	if (&RegistrySource{}).EffectiveNamespace() != DefaultNamespace {
		t.Fatal("empty namespace must fall back to default")
	}
	if (&RegistrySource{Namespace: "  Team-A  "}).EffectiveNamespace() != "team-a" {
		t.Fatal("namespace must be trimmed and lower-cased")
	}
}

func TestSourceKindsAndSchemesConstants(t *testing.T) {
	if len(SourceKinds) != 2 || SourceKinds[0] != SourceKindMCP || SourceKinds[1] != SourceKindSkill {
		t.Fatalf("SourceKinds = %v", SourceKinds)
	}
	if len(AuthSchemesForSources) != 2 {
		t.Fatalf("AuthSchemesForSources = %v", AuthSchemesForSources)
	}
}
