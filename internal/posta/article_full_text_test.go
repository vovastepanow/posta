package posta

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"posta/internal/jx"
)

func TestArticleParsePreservesFullSemanticArticle(t *testing.T) {
	long := strings.Repeat("Длинный текст статьи. ", 400)
	page := "<article><h2>Подзаголовок</h2><p>Коротко.</p><p>" + long + "</p><ul><li>Последний пункт</li></ul></article>"
	got := articleParse(page).ArticleText
	for _, want := range []string{"Подзаголовок", "Коротко.", "Последний пункт"} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет фрагмента %q", want)
		}
	}
	if !strings.Contains(got, strings.TrimSpace(long)) {
		t.Fatal("статья обрезана")
	}
}

func TestArticleParseRIAAndWindows1251(t *testing.T) {
	page := `<meta charset="windows-1251"><div class="article__body"><div class="article__text">Начало статьи.</div><div class="banner">Реклама</div><div class="article__text">Последний абзац.</div></div>`
	encoded, err := charmap.Windows1251.NewEncoder().String(page)
	if err != nil {
		t.Fatal(err)
	}
	body := articleParse(articleDecode([]byte(encoded), "text/html; charset=windows-1251")).ArticleText
	if !strings.Contains(body, "Начало статьи.") || !strings.Contains(body, "Последний абзац.") || strings.Contains(body, "Реклама") {
		t.Fatalf("неверный текст статьи: %q", body)
	}
}

func TestRSSParseKeepsFullFeedText(t *testing.T) {
	body := strings.Repeat("Текст публикации. ", 200) + "Конец публикации"
	feed := `<rss><channel><item><title>Заголовок</title><link>https://example.com/story</link><description>` + body + `</description></item></channel></rss>`
	posts, err := RSSParse([]byte(feed), 1, "", "https://example.com/rss", true, true, 1)
	if err != nil || len(posts) != 1 || !strings.Contains(jx.Str(posts[0], "text"), "Конец публикации") || !jx.Bool(posts[0], "article_source") {
		t.Fatalf("статья из RSS потеряна: posts=%d err=%v", len(posts), err)
	}
}
