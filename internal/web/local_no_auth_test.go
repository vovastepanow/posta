package web

import (
	"net/http/httptest"
	"testing"
)

func TestLocalNoAuthRequest(t *testing.T) {
	for _, tc := range []struct {
		name, host, peer, forwarded string
		want                        bool
	}{
		{"direct local", "localhost:8090", "127.0.0.1:1234", "", true},
		{"local proxy", "localhost", "127.0.0.1:1234", "::1", true},
		{"remote peer", "localhost", "192.0.2.1:1234", "", false},
		{"remote through proxy", "localhost", "127.0.0.1:1234", "192.0.2.1", false},
		{"spoofed chain", "localhost", "127.0.0.1:1234", "127.0.0.1, 192.0.2.1", false},
		{"public host", "example.com", "127.0.0.1:1234", "127.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://localhost/", nil)
			r.Host, r.RemoteAddr = tc.host, tc.peer
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := localNoAuthRequest(r); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
