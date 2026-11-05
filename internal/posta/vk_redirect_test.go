package posta

import (
	"testing"

	"posta/internal/config"
)

func TestVKOAuthRedirectURIUsesPublicPrefix(t *testing.T) {
	cases := []struct{ base, explicit, want string }{
		{"http://localhost/posta", "", "http://localhost/posta/index.php?action=vk_oauth_callback"},
		{"https://example.com/posta/", "", "https://example.com/posta/index.php?action=vk_oauth_callback"},
		{"https://example.com/posta", "https://example.com/custom", "https://example.com/custom"},
		{"", "", ""},
	}
	for _, tc := range cases {
		a := &App{Cfg: config.Server{PublicURL: tc.base, VKRedirectURI: tc.explicit, VKClientID: "123"}}
		if got := a.VKOAuthRedirectURI(); got != tc.want {
			t.Errorf("base %q explicit %q: got %q, want %q", tc.base, tc.explicit, got, tc.want)
		}
		if a.VKOAuthConfigured() != (tc.want != "") {
			t.Errorf("incorrect configured state for %q", tc.base)
		}
	}
}
