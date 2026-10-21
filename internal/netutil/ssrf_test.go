package netutil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsDangerous(t *testing.T) {
	dangerous := []string{
		"127.0.0.1", "10.0.0.1", "172.16.5.5", "192.168.1.1",
		"169.254.1.1", "::1", "fe80::1", "0.0.0.0",
	}
	for _, s := range dangerous {
		if !isDangerous(net.ParseIP(s)) {
			t.Errorf("%s should be dangerous", s)
		}
	}
	safe := []string{"8.8.8.8", "1.1.1.1", "203.0.113.9", "2606:4700::1"}
	for _, s := range safe {
		if isDangerous(net.ParseIP(s)) {
			t.Errorf("%s should be safe", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	if err := ValidateURL("https://example.com/cb", false); err != nil {
		t.Fatalf("https should pass: %v", err)
	}
	if err := ValidateURL("http://example.com/cb", false); err == nil {
		t.Fatal("http should be rejected when allowHTTP=false")
	}
	if err := ValidateURL("http://example.com/cb", true); err != nil {
		t.Fatalf("http should pass when allowed: %v", err)
	}
	for _, bad := range []string{"ftp://x", "not-a-url", "https://", "//host"} {
		if err := ValidateURL(bad, true); err == nil {
			t.Errorf("expected rejection of %q", bad)
		}
	}
}

func TestSafeClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// httptest serves on 127.0.0.1; a private-disallowing client must refuse.
	c := SafeClient(0, 0, false)
	_, err := c.Get(srv.URL)
	if err == nil {
		t.Fatal("expected SSRF refusal for loopback destination")
	}
}
