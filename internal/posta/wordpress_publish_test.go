package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestWordPressSiteAndBody(t *testing.T) {
	if got, err := wordpressSite("https://EXAMPLE.com/blog/"); err != nil || got != "https://example.com/blog" {
		t.Fatalf("site=%q error=%v", got, err)
	}
	for _, raw := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/?token=secret", "https://example.com:8443"} {
		if _, err := wordpressSite(raw); err == nil {
			t.Errorf("unsafe site accepted: %s", raw)
		}
	}
	text := "Заголовок\n" + strings.Repeat("длинный текст ", 300) + "<script>"
	p := wordpressPostBody(text)
	if jx.Str(p, "title") != "Заголовок" || !strings.Contains(jx.Str(p, "content"), "&lt;script&gt;") || !strings.Contains(jx.Str(p, "content"), strings.Repeat("длинный текст ", 300)) {
		t.Fatal("body truncated or not escaped")
	}
}
