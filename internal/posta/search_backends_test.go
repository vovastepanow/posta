package posta

import (
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

// Разборы ответов новых площадок — на сохранённых ответах из tools/fixtures (снято 2026-10-30).
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../tools/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRutubeParse(t *testing.T) {
	posts, err := RutubeParse(readFixture(t, "search_rutube.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) == 0 {
		t.Fatal("ждали посты Rutube")
	}
	for _, p := range posts {
		if jx.Str(p, "url") == "" || jx.Str(p, "title") == "" || jx.Int(p, "posted_at") == 0 {
			t.Fatalf("пост без ссылки, заголовка или даты: %v", p)
		}
		if !strings.HasPrefix(jx.Str(p, "source_key"), "rutube:") {
			t.Fatalf("источник: %s", jx.Str(p, "source_key"))
		}
	}
}

func TestNineGAGParse(t *testing.T) {
	posts, err := NineGAGParse(readFixture(t, "search_9gag.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) == 0 {
		t.Fatal("ждали посты 9GAG")
	}
	for _, p := range posts {
		if jx.Str(p, "image") == "" && jx.Str(p, "video") == "" {
			t.Fatalf("пост без медиа: %v", p)
		}
	}
}

func TestLemmyParse(t *testing.T) {
	posts, err := LemmyParse(readFixture(t, "search_lemmy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) == 0 {
		t.Fatal("ждали посты Lemmy")
	}
	for _, p := range posts {
		if !strings.HasPrefix(jx.Str(p, "source_key"), "lemmy:") || jx.Int(p, "posted_at") == 0 {
			t.Fatalf("пост Lemmy: %v", p)
		}
	}
}

func TestMastodonParse(t *testing.T) {
	posts, err := MastodonParse(readFixture(t, "tag_mastodon.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range posts {
		if strings.Contains(jx.Str(p, "text"), "<p>") {
			t.Fatalf("HTML не вычищен: %s", jx.Str(p, "text"))
		}
		if jx.Str(p, "image") == "" && jx.Str(p, "video") == "" {
			t.Fatalf("пост без медиа: %v", p)
		}
	}
}

func TestJoyreactorParse(t *testing.T) {
	posts := JoyreactorParse(string(readFixture(t, "tag_joyreactor.html")), time.Unix(1793400000, 0))
	if len(posts) == 0 {
		t.Fatal("ждали карточки Joyreactor")
	}
	for _, p := range posts {
		if !strings.HasPrefix(jx.Str(p, "url"), "https://joyreactor.cc/post/") {
			t.Fatalf("ссылка: %s", jx.Str(p, "url"))
		}
		if strings.Contains(jx.Str(p, "image"), "avatar") {
			t.Fatalf("взята аватарка: %s", jx.Str(p, "image"))
		}
	}
}

func TestJoyAgo(t *testing.T) {
	at := time.Unix(1000000, 0)
	if got := joyAgo(`<span>3<!-- --> <!-- -->ч.<!-- --> </span>`, at); got != at.Add(-3*time.Hour).Unix() {
		t.Fatalf("3 ч: %d", got)
	}
	if got := joyAgo(`<span>2<!-- --> <!-- -->дн.</span>`, at); got != at.Add(-48*time.Hour).Unix() {
		t.Fatalf("2 дн: %d", got)
	}
}

// Pikabu отдаёт страницу поиска без результатов (разметка рисуется JS): парсер не должен находить ложных постов.
func TestPikabuNoServerResults(t *testing.T) {
	page := string(readFixture(t, "search_pikabu.html"))
	if strings.Contains(page, "story__title-link") {
		t.Fatal("разметка Pikabu появилась — пересмотреть, можно включать площадку")
	}
}
