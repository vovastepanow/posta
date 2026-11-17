package posta

import (
	"errors"
	"net"
	"testing"
)

func TestPublicDNSFallbackAndAddressValidation(t *testing.T) {
	primaryErr := errors.New("DNS unavailable")
	failed := func(string) ([]net.IP, error) { return nil, primaryErr }
	calls := 0
	fallback := func(host string, server net.IP) ([]net.IP, error) {
		calls++
		if host != "example.test" || !server.Equal(net.ParseIP("192.168.0.1")) {
			t.Fatal("unexpected fallback")
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	for _, invalid := range []string{"", "resolver.example", "0.0.0.0", "224.0.0.1", "::1"} {
		if _, err := lookupPublicHostWith("example.test", invalid, failed, fallback); !errors.Is(err, primaryErr) {
			t.Fatalf("fallback with %q: %v", invalid, err)
		}
	}
	if calls != 0 {
		t.Fatal("fallback used without explicit valid server")
	}
	ips, err := lookupPublicHostWith("example.test", "192.168.0.1", failed, fallback)
	if err != nil || calls != 1 || len(ips) != 1 {
		t.Fatalf("fallback: %v %v", ips, err)
	}
	good := func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("93.184.216.34")}, nil }
	if _, err = lookupPublicHostWith("example.test", "192.168.0.1", good, fallback); err != nil || calls != 1 {
		t.Fatal("fallback used when primary succeeded")
	}
	mixed := func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}, nil
	}
	if _, _, err = publicResolveWithLookup("https://example.test/path", mixed); err == nil {
		t.Fatal("mixed private answer accepted")
	}
	if _, addr, err := publicResolveWithLookup("https://example.test/path", good); err != nil || addr != "93.184.216.34:443" {
		t.Fatalf("public result: %s %v", addr, err)
	}
}
