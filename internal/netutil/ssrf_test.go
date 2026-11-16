package netutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubResolver replaces the package resolver for one test. Tests using it must
// not run in parallel: the seam is process wide.
func stubResolver(t *testing.T, fn func(ctx context.Context, host string) ([]net.IPAddr, error)) {
	t.Helper()
	previous := lookupIPAddr
	lookupIPAddr = fn
	t.Cleanup(func() { lookupIPAddr = previous })
}

// requireReservedUnreachable guards the two dial-guard cases below. They assert
// that the guard *attempts* a public but reserved address, and that assertion
// only means something while that address is genuinely unreachable. A
// transparent outbound proxy — corporate networks, some container runtimes —
// accepts the connection, so the premise is false and a hard failure would
// report a product bug that does not exist. Skipping says that plainly.
//
// The probe dials directly, never through the client under test: the point is
// to learn what this machine's network does, not what the guard does.
func requireReservedUnreachable(t *testing.T) {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: 400 * time.Millisecond}).Dial("tcp", "203.0.113.7:80")
	if err == nil {
		_ = conn.Close()
		t.Skip("outbound TCP reaches 203.0.113.7 (a reserved address) from this machine; " +
			"a transparent proxy makes the dial-guard assertion unprovable here")
	}
}

func dialerOf(t *testing.T, c *http.Client) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	transport, ok := c.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatal("client must dial through the guarding transport")
	}
	return transport.DialContext
}

func TestIsDangerous(t *testing.T) {
	dangerous := []string{
		"127.0.0.1", "10.0.0.1", "172.16.5.5", "192.168.1.1",
		"169.254.1.1", "::1", "fe80::1", "0.0.0.0",
		// IPv4-mapped IPv6 must be normalised before the check.
		"::ffff:127.0.0.1", "::ffff:10.1.2.3",
	}
	for _, s := range dangerous {
		if !isDangerous(net.ParseIP(s)) {
			t.Errorf("%s should be dangerous", s)
		}
	}
	safe := []string{"8.8.8.8", "1.1.1.1", "203.0.113.9", "2606:4700::1", "::ffff:8.8.8.8"}
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
	// An unparsable URL must fail inside url.Parse, before the host check.
	if err := ValidateURL("http://[::1", true); err == nil {
		t.Fatal("an unparsable URL must be rejected")
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
	dial := dialerOf(t, c)

	// A non-TCP network (unix sockets, packet sockets) is refused before any
	// address resolution happens.
	if _, err := dial(context.Background(), "unix", "/tmp/sock"); err == nil {
		t.Fatal("unsupported networks must be refused")
	}
	// An address without a port is a programming error, not a dial.
	if _, err := dial(context.Background(), "tcp", "example.com"); err == nil {
		t.Fatal("an address without a port must be refused")
	}
	// An unresolvable host fails at resolution, never at connect.
	if _, err := dial(context.Background(), "tcp", "no-such-host.invalid:80"); err == nil {
		t.Fatal("an unresolvable host must fail")
	}
}

func TestSafeClientSkipsDangerousAddressesUntilOneIsUsable(t *testing.T) {
	requireReservedUnreachable(t)
	// The first answer is loopback (blocked) and only the second one is public:
	// the guard must skip the first instead of giving up on the hostname.
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("127.0.0.1")},
			{IP: net.ParseIP("203.0.113.7")},
		}, nil
	})
	c := SafeClient(0, 0, false)
	dial := dialerOf(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := dial(ctx, "tcp", "public.example.test:80")
	if err == nil {
		t.Fatal("a reserved address cannot be reached from a test, but the branch must be dialled")
	}
	if strings.Contains(err.Error(), "blocked") {
		t.Fatalf("the second address was allowed, so the dial must be attempted: %v", err)
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

func TestSafeClientWithAllowlistAllowsACIDR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := SafeClientWithAllowlist(0, 0, []string{"127.0.0.0/8"})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("a private address inside an allowed CIDR must be reachable: %v", err)
	}
	defer resp.Body.Close()
}

func TestSafeClientWithAllowlistAllowsAHostname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Pin the answer to the family httptest listens on: a real "localhost" may
	// resolve to ::1 first, which is not where this server is bound.
	stubResolver(t, func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "localhost" {
			return nil, errors.New("unexpected host")
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})

	// The literal hostname covers whatever it resolves to, which is what lets an
	// operator trust "localhost" without pinning an address that may change.
	c := SafeClientWithAllowlist(0, 0, []string{"localhost"})
	resp, err := c.Get(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
	if err != nil {
		t.Fatalf("an allowed hostname must be reachable: %v", err)
	}
	defer resp.Body.Close()
}

func TestSafeClientWithAllowlistDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect target must never be reached")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := SafeClientWithAllowlist(0, 0, []string{"127.0.0.1"})
	resp, err := c.Get(redirector.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the 302 itself", resp.StatusCode)
	}
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

func TestSafeClientWithAllowlistDialGuards(t *testing.T) {
	c := SafeClientWithAllowlist(0, 0, nil)
	if c.Timeout != DefaultSafeTimeout {
		t.Fatalf("timeout = %v, want %v", c.Timeout, DefaultSafeTimeout)
	}
	dial := dialerOf(t, c)

	if _, err := dial(context.Background(), "unix", "/tmp/sock"); err == nil {
		t.Fatal("unsupported networks must be refused")
	}
	if _, err := dial(context.Background(), "tcp", "example.com"); err == nil {
		t.Fatal("an address without a port must be refused")
	}
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("resolver unavailable")
	})
	if _, err := dial(context.Background(), "tcp", "private.example.test:443"); err == nil {
		t.Fatal("a failed resolution must surface as an error")
	}
}

func TestSafeClientWithAllowlistDialsAPublicAddress(t *testing.T) {
	requireReservedUnreachable(t)
	stubResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.7")}}, nil
	})
	c := SafeClientWithAllowlist(0, 0, nil)
	dial := dialerOf(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := dial(ctx, "tcp", "public.example.test:80")
	if err == nil {
		t.Fatal("a reserved address cannot be reached, but the branch must be dialled")
	}
	if strings.Contains(err.Error(), "blocked") {
		t.Fatalf("a public address must not be blocked: %v", err)
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
	if allowContainsIP([]string{"", "   "}, net.ParseIP("127.0.0.1")) {
		t.Fatal("empty allow entries must be ignored")
	}
	if allowContainsIP([]string{"not-an-ip"}, net.ParseIP("127.0.0.1")) {
		t.Fatal("an unparsable entry must not match")
	}
}
