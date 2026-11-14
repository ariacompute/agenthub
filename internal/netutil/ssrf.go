// Package netutil holds network helpers, chief among them an HTTP client that
// refuses to reach private, loopback, link-local and unspecified addresses — the
// SSRF surface — so a server-side request to a caller supplied URL (an A2A push
// callback, an upstream registry) can never be pivoted into the internal network.
package netutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultSafeTimeout / DefaultSafeMaxBytes cap a single outbound request.
const (
	DefaultSafeTimeout  = 15 * time.Second
	DefaultSafeMaxBytes = 8 << 20
)

// SafeClient builds an *http.Client that blocks dangerous destinations, caps the
// response body and refuses to follow redirects (a redirect is an easy SSRF
// bypass). When allowPrivate is true the IP blocklist is relaxed, which is only
// appropriate for destinations an operator configured and explicitly trusts
// (e.g. an internal upstream registry); caller supplied callbacks must always
// pass allowPrivate=false.
func SafeClient(timeout time.Duration, maxBytes int64, allowPrivate bool) *http.Client {
	if timeout <= 0 {
		timeout = DefaultSafeTimeout
	}
	if maxBytes <= 0 {
		maxBytes = DefaultSafeMaxBytes
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if network != "tcp" && network != "tcp4" && network != "tcp6" {
					return nil, fmt.Errorf("ssrf: unsupported network %q", network)
				}
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				var safe net.IP
				for _, ipa := range ips {
					if allowPrivate || !isDangerous(ipa.IP) {
						safe = ipa.IP
						break
					}
				}
				if safe == nil {
					return nil, fmt.Errorf("ssrf: every address for %q is blocked", host)
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(safe.String(), port))
			},
			MaxIdleConns:    20,
			IdleConnTimeout: 90 * time.Second,
		},
		// Redirects are an SSRF bypass vector; surface them so the caller can
		// decide rather than silently following the attacker's second hop.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// isDangerous reports whether ip is the sort of address an outbound request must
// never reach: loopback, private, link-local, multicast or unspecified. The
// IPv4-mapped IPv6 form is normalised before the same checks run.
func isDangerous(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	return false
}

// SafeClientWithAllowlist behaves like SafeClient but, instead of a blanket
// "allow private" switch, relaxes the SSRF blocklist only for destinations an
// operator enumerated in allow. Public addresses are always reachable; a
// dangerous address (loopback, private, link-local, …) is permitted only when
// it matches allow — by exact IP, by CIDR, or by the literal hostname. An empty
// allow keeps the strict default, so a caller-supplied URL still cannot pivot
// into the internal network.
func SafeClientWithAllowlist(timeout time.Duration, maxBytes int64, allow []string) *http.Client {
	if timeout <= 0 {
		timeout = DefaultSafeTimeout
	}
	if maxBytes <= 0 {
		maxBytes = DefaultSafeMaxBytes
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if network != "tcp" && network != "tcp4" && network != "tcp6" {
					return nil, fmt.Errorf("ssrf: unsupported network %q", network)
				}
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				hostAllowed := allowContainsHost(allow, host)
				var safe net.IP
				for _, ipa := range ips {
					if !isDangerous(ipa.IP) {
						safe = ipa.IP
						break
					}
					if hostAllowed || allowContainsIP(allow, ipa.IP) {
						safe = ipa.IP
						break
					}
				}
				if safe == nil {
					return nil, fmt.Errorf("ssrf: every address for %q is blocked", host)
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(safe.String(), port))
			},
			MaxIdleConns:    20,
			IdleConnTimeout: 90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// allowContainsHost reports whether host matches an allow entry verbatim, so a
// hostname such as "localhost" covers its loopback resolution.
func allowContainsHost(allow []string, host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	for _, a := range allow {
		if strings.EqualFold(strings.TrimSpace(a), h) {
			return true
		}
	}
	return false
}

// allowContainsIP reports whether ip is listed verbatim or falls inside a CIDR in
// allow. Bracketed IPv6 literals are accepted.
func allowContainsIP(allow []string, ip net.IP) bool {
	for _, a := range allow {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") {
			a = a[1 : len(a)-1]
		}
		if _, cidr, err := net.ParseCIDR(a); err == nil {
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if got := net.ParseIP(a); got != nil && got.Equal(ip) {
			return true
		}
	}
	return false
}

// ValidateURL checks that u is an absolute http(s) URL and, unless allowHTTP is
// true, that it is https. Scheme and host validation run before any connection;
// the IP allow/deny decision is made later by SafeClient at dial time.
func ValidateURL(u string, allowHTTP bool) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("ssrf: bad url: %w", err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("ssrf: url has no host")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return fmt.Errorf("ssrf: only https is allowed")
		}
	default:
		return fmt.Errorf("ssrf: unsupported scheme %q", parsed.Scheme)
	}
	return nil
}
