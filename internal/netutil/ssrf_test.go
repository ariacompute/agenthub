package netutil

import (
	"context"
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

func TestSafeClientReachesATrustedPrivateDestination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("internal"))
	}))
	defer srv.Close()

	// allowPrivate is for destinations an operator configured themselves, such as
	// an upstream registry on the local network.
	c := SafeClient(0, 0, true)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("an explicitly trusted private destination must work: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestSafeClientDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect target must never be reached")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := SafeClient(0, 0, true)
	resp, err := c.Get(redirector.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	// The 3xx is surfaced instead: a redirect is the easiest SSRF bypass.
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the 302 itself", resp.StatusCode)
	}
}

func TestSafeClientDefaultsAndDialGuards(t *testing.T) {
	// Zero values must not produce a client that hangs forever or reads unbounded.
	c := SafeClient(0, 0, false)
	if c.Timeout != DefaultSafeTimeout {
		t.Fatalf("timeout = %v, want %v", c.Timeout, DefaultSafeTimeout)
	}
	transport, ok := c.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatal("the client must dial through the guarding transport")
	}

	// A non-TCP network (unix sockets, packet sockets) is refused before any
	// address resolution happens.
	if _, err := transport.DialContext(context.Background(), "unix", "/tmp/sock"); err == nil {
		t.Fatal("unsupported networks must be refused")
	}
	// An address without a port is a programming error, not a dial.
	if _, err := transport.DialContext(context.Background(), "tcp", "example.com"); err == nil {
		t.Fatal("an address without a port must be refused")
	}
	// An unresolvable host fails at resolution, never at connect.
	if _, err := transport.DialContext(context.Background(), "tcp", "no-such-host.invalid:80"); err == nil {
		t.Fatal("an unresolvable host must fail")
	}
}

func TestSafeClientWithAllowlistAllowsListedLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := SafeClientWithAllowlist(0, 0, []string{"127.0.0.1"})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("a loopback destination in the allowlist must be reachable: %v", err)
	}
	defer resp.Body.Close()
}

func TestSafeClientWithAllowlistBlocksUnlistedPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A private destination NOT in the allowlist stays blocked even when another
	// entry is listed: the list narrows the guard instead of disabling it.
	c := SafeClientWithAllowlist(0, 0, []string{"10.0.0.5"})
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("an unlisted loopback destination must be refused")
	}
}

func TestSafeClientWithAllowlistBlocksWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := SafeClientWithAllowlist(0, 0, nil)
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("an empty allowlist must keep the strict default")
	}
}

func TestAllowMatchHelpers(t *testing.T) {
	if !allowContainsHost([]string{"localhost"}, "LOCALHOST") {
		t.Fatal("host match must be case-insensitive")
	}
	if allowContainsHost([]string{"example.com"}, "localhost") {
		t.Fatal("host must not match an unrelated entry")
	}
	if !allowContainsIP([]string{"10.0.0.0/8"}, net.ParseIP("10.1.2.3")) {
		t.Fatal("CIDR must contain the address")
	}
	if allowContainsIP([]string{"10.0.0.0/8"}, net.ParseIP("192.168.0.1")) {
		t.Fatal("CIDR must not contain an outside address")
	}
	if !allowContainsIP([]string{"127.0.0.1"}, net.ParseIP("127.0.0.1")) {
		t.Fatal("exact IP must match")
	}
	if !allowContainsIP([]string{"[::1]"}, net.ParseIP("::1")) {
		t.Fatal("bracketed IPv6 literal must match")
	}
}
