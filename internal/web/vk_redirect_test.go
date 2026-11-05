package web

import (
	"net/http/httptest"
	"testing"

	"posta/internal/config"
	"posta/internal/posta"
)

func TestVKRedirectPreservesApplicationPrefix(t *testing.T) {
	for _, tc := range []struct{ public, base, result, want string }{
		{"http://localhost/posta", "", "error", "http://localhost/posta/?vk=error#account"},
		{"", "/posta", "misconfigured", "/posta/?vk=misconfigured#account"},
		{"", "", "unexpected", "/?vk=error#account"},
	} {
		w := httptest.NewRecorder()
		c := &Ctx{W: w, R: httptest.NewRequest("GET", "http://backend/index.php", nil), A: &posta.App{Cfg: config.Server{PublicURL: tc.public, BasePath: tc.base}}}
		if _, err := vkRedirect(c, tc.result); err != nil {
			t.Fatal(err)
		}
		if got := w.Header().Get("Location"); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
