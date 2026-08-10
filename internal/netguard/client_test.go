package netguard_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/uyab/lotus/internal/netguard"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.5.5.5", // loopback
		"10.0.0.5", "172.16.0.5", "192.168.1.1", // private
		"169.254.169.254", "169.254.1.1", // link-local + cloud metadata
		"::1",     // IPv6 loopback
		"fc00::1", // unique local
		"fe80::1", // IPv6 link-local
	}
	for _, s := range blocked {
		if !netguard.IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = false, want true", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"}
	for _, s := range allowed {
		if netguard.IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = true, want false", s)
		}
	}
}

func fakeResolver(mapping map[string][]net.IP) func(context.Context, string) ([]net.IP, error) {
	return func(_ context.Context, host string) ([]net.IP, error) {
		ips, ok := mapping[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return ips, nil
	}
}

func TestClient_CloudMetadataAddressRejected(t *testing.T) {
	client := netguard.NewClient(netguard.Config{
		Resolver: fakeResolver(map[string][]net.IP{"metadata.internal": {net.ParseIP("169.254.169.254")}}),
	})
	_, err := client.Get("https://metadata.internal/latest/meta-data/")
	if err == nil {
		t.Fatal("expected the cloud metadata address to be rejected")
	}
}

func TestClient_LocalhostRejected(t *testing.T) {
	client := netguard.NewClient(netguard.Config{
		AllowHTTP: true,
		Resolver:  fakeResolver(map[string][]net.IP{"localhost": {net.ParseIP("127.0.0.1")}}),
	})
	_, err := client.Get("http://localhost:8080")
	if err == nil {
		t.Fatal("expected localhost to be rejected")
	}
}

// TestClient_PublicDomainResolvingToLoopbackRejected is task 2.2's third
// explicit SSRF test case: "domain publik yang resolve ke 127.0.0.1". A
// fake resolver stands in for DNS so this doesn't depend on any real,
// network-reachable domain actually being configured to answer that way.
func TestClient_PublicDomainResolvingToLoopbackRejected(t *testing.T) {
	client := netguard.NewClient(netguard.Config{
		Resolver: fakeResolver(map[string][]net.IP{"evil-but-public-looking.example.com": {net.ParseIP("127.0.0.1")}}),
	})
	_, err := client.Get("https://evil-but-public-looking.example.com/")
	if err == nil {
		t.Fatal("expected a public-looking domain resolving to loopback to be rejected")
	}
}

// TestClient_AllowedAddressSucceeds proves the "allowed" path actually
// completes a real HTTP round trip through DialContext, not just that
// blocking works. httptest.Server always binds loopback, which
// IsBlockedIP correctly rejects by default, so this test supplies its own
// IsBlocked (see Config's doc comment) that allows loopback — a real
// public IP isn't reachable from a sandboxed test environment. Production
// code never overrides IsBlocked, so this doesn't loosen the real default.
func TestClient_AllowedAddressSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	srvIP, srvPort, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}

	client := netguard.NewClient(netguard.Config{
		AllowHTTP: true,
		Resolver:  fakeResolver(map[string][]net.IP{"looks-public.example.com": {net.ParseIP(srvIP)}}),
		IsBlocked: func(net.IP) bool { return false },
	})
	resp, err := client.Get("http://looks-public.example.com:" + srvPort)
	if err != nil {
		t.Fatalf("expected the allowed address to succeed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestValidateBaseURL_RejectsHTTPWithoutDevFlag(t *testing.T) {
	err := netguard.ValidateBaseURL(context.Background(), netguard.Config{
		Resolver: fakeResolver(map[string][]net.IP{"gitlab.example.com": {net.ParseIP("8.8.8.8")}}),
	}, "http://gitlab.example.com")
	if err == nil {
		t.Fatal("expected http:// to be rejected without AllowHTTP")
	}
}

func TestValidateBaseURL_AllowsHTTPSPublicAddress(t *testing.T) {
	err := netguard.ValidateBaseURL(context.Background(), netguard.Config{
		Resolver: fakeResolver(map[string][]net.IP{"gitlab.example.com": {net.ParseIP("8.8.8.8")}}),
	}, "https://gitlab.example.com")
	if err != nil {
		t.Fatalf("expected a public https address to be allowed: %v", err)
	}
}

func TestValidateBaseURL_RejectsPrivateAddress(t *testing.T) {
	err := netguard.ValidateBaseURL(context.Background(), netguard.Config{
		Resolver: fakeResolver(map[string][]net.IP{"internal.example.com": {net.ParseIP("10.0.0.5")}}),
	}, "https://internal.example.com")
	if err == nil {
		t.Fatal("expected a private address to be rejected")
	}
}
