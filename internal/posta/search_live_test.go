package posta

import (
	"os"
	"testing"

	"posta/internal/jx"
)

// Живая проверка: настоящие запросы к площадкам. Включается POSTA_LIVE_SEARCH=1, в обычном прогоне пропускается.
func TestSearchLiveBackends(t *testing.T) {
	if os.Getenv("POSTA_LIVE_SEARCH") != "1" {
		t.Skip("живой поиск выключен: POSTA_LIVE_SEARCH=1")
	}
	u := &U{App: &App{}}
	for name, f := range map[string]func(string, int) ([]jx.M, bool, error){
		"Rutube":     func(q string, p int) ([]jx.M, bool, error) { return u.SearchRutube(q, p) },
		"9GAG":       func(q string, p int) ([]jx.M, bool, error) { return u.SearchNineGAG(q, p) },
		"Lemmy":      func(q string, p int) ([]jx.M, bool, error) { return u.SearchLemmy(q, p) },
		"Mastodon":   func(q string, p int) ([]jx.M, bool, error) { return u.SearchMastodon(q, p) },
		"Joyreactor": func(q string, p int) ([]jx.M, bool, error) { return u.SearchJoyreactor(q, p) },
	} {
		posts, _, err := f("кот", 1)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		t.Logf("%s: %d постов", name, len(posts))
	}
}
