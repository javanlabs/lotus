// Package netguard builds an http.Client that refuses to fetch anything
// task 2.2 calls "internal": private, loopback, link-local, and cloud
// metadata addresses. It exists because Connection.BaseURL (task 2.2) is
// attacker-reachable input that this server itself fetches while carrying
// a credential — without this, anyone who can edit a connection's base_url
// can repoint it at an address of their choosing and harvest the
// credential on the next scheduled fetch.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ErrBlockedAddress is returned (possibly wrapped) whenever a scheme, host,
// or resolved address is rejected.
var ErrBlockedAddress = errors.New("netguard: address is not allowed")

// Config controls what a Client permits.
type Config struct {
	// AllowHTTP permits the http:// scheme in addition to https://. Only
	// ever true under LOTUS_DEV=1 — production credentials must never be
	// sent in the clear.
	AllowHTTP bool

	// Resolver looks up host's addresses. Defaults to
	// net.DefaultResolver.LookupIP when nil. Overridable so tests can
	// exercise "a public domain that resolves to 127.0.0.1" (one of task
	// 2.2's explicit acceptance criteria) without depending on real,
	// network-reachable DNS.
	Resolver func(ctx context.Context, host string) ([]net.IP, error)

	// IsBlocked reports whether an already-resolved IP is disallowed.
	// Defaults to IsBlockedIP when nil. The only reason to override this
	// in production would be to weaken it, so the only real caller of this
	// field is this package's own tests, which need to point NewClient at
	// a real loopback-bound httptest.Server to prove the "allowed" path
	// actually completes a round trip — something that can't be
	// demonstrated by pointing at a real public IP in a sandboxed test
	// environment with no outbound network access.
	IsBlocked func(net.IP) bool
}

func (c Config) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if c.Resolver != nil {
		return c.Resolver(ctx, host)
	}
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func (c Config) isBlocked(ip net.IP) bool {
	if c.IsBlocked != nil {
		return c.IsBlocked(ip)
	}
	return IsBlockedIP(ip)
}

// NewClient builds an http.Client whose every connection — including every
// redirect hop — is validated against cfg. The validation happens in
// DialContext, at the moment a TCP connection is actually opened, against
// the specific IP about to be dialed: checking only at the URL-parsing
// stage (see ValidateBaseURL) and then trusting DNS to resolve the same
// way a moment later is exactly what a DNS-rebinding attack defeats.
func NewClient(cfg Config) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip, err := firstAllowedIP(ctx, cfg, host)
			if err != nil {
				return nil, err
			}
			// Dial the validated IP directly, not the original hostname —
			// the whole point of validating at dial time is defeated if
			// the dial itself re-resolves and can get a different answer.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("%w: too many redirects", ErrBlockedAddress)
			}
			return checkScheme(req.URL.Scheme, cfg.AllowHTTP)
			// The redirect target's host is re-validated automatically:
			// following it opens a new connection through the same
			// DialContext above.
		},
	}
}

// ValidateBaseURL performs the same scheme + resolved-address check
// NewClient's DialContext does, for use at connection-save time (task
// 2.2) so a bad base_url is rejected immediately with a clear message
// instead of only failing on the next scheduled sync. This is a UX
// convenience, not the actual security boundary — DNS can answer
// differently a moment later, which is exactly why NewClient re-validates
// at dial time regardless of whether this passed.
func ValidateBaseURL(ctx context.Context, cfg Config, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if err := checkScheme(u.Scheme, cfg.AllowHTTP); err != nil {
		return err
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: no host in URL", ErrBlockedAddress)
	}
	_, err = firstAllowedIP(ctx, cfg, u.Hostname())
	return err
}

func firstAllowedIP(ctx context.Context, cfg Config, host string) (net.IP, error) {
	ips, err := cfg.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !cfg.isBlocked(ip) {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("%w: %s resolves only to blocked addresses", ErrBlockedAddress, host)
}

func checkScheme(scheme string, allowHTTP bool) error {
	switch scheme {
	case "https":
		return nil
	case "http":
		if allowHTTP {
			return nil
		}
	}
	return fmt.Errorf("%w: scheme %q is not allowed", ErrBlockedAddress, scheme)
}

// IsBlockedIP reports whether ip falls in a private, loopback, link-local
// (which includes the 169.254.169.254 cloud metadata address), unique
// local (fc00::/7), or multicast/unspecified range. net.IP's own
// classifiers already implement the exact ranges task 2.2 lists by name:
// IsPrivate covers 10/8, 172.16/12, 192.168/16, and fc00::/7; IsLoopback
// covers 127.0.0.0/8 and ::1; IsLinkLocalUnicast covers 169.254.0.0/16
// (including .169.254.254) and fe80::/10.
func IsBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
