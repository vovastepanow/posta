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

func TestRSSArticleLinkSelection(t *testing.T) {
	feed := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Title</title><link rel="self" href="https://example.com/feed"/><link rel="alternate" type="application/rss+xml" href="/other-feed"/><link rel="alternate" type="text/html" href="/story"/><summary>Short</summary></entry></feed>`
	posts, err := RSSParse([]byte(feed), 1, "", "https://example.com/rss", true, true, 1)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "url") != "https://example.com/story" {
		t.Fatalf("wrong article link: %#v, %v", posts, err)
	}
}

func TestArticleApplyDataKeepsFeedFallbackAndFullText(t *testing.T) {
	post := jx.M{"text": "Заголовок\nАнонс", "article_source": true}
	body := strings.Repeat("Полный текст статьи. ", 100) + "Конец."
	got := articleApplyData(post, &articleData{ArticleText: body})
	if !strings.Contains(jx.Str(got, "text"), "Конец.") || jx.Str(got, "article_text") != body {
		t.Fatalf("full text lost: %#v", got)
	}
	fallback := articleApplyData(post, &articleData{})
	if jx.Str(fallback, "text") != jx.Str(post, "text") {
		t.Fatalf("feed text lost: %#v", fallback)
	}
}

func TestArticleSemanticExcludesRelatedWidgets(t *testing.T) {
	page := `<article><ul class="ssrcss-MetadataStrip"><li>Published today</li></ul><p>Первый абзац.</p><p>Последний абзац.</p><div data-testid="topic-list"><h2>Related topics</h2><ul><li>Тема</li></ul></div><div data-block="promoList"><h2>More on this story</h2><p>Другая статья</p></div><div data-print-layout="hide"><ul><li>Рубрика</li></ul></div></article>`
	got := articleSemanticText(page)
	if !strings.Contains(got, "Последний абзац.") || strings.Contains(got, "Published") || strings.Contains(got, "Related topics") || strings.Contains(got, "Другая статья") || strings.Contains(got, "Рубрика") {
		t.Fatalf("unexpected article text: %q", got)
	}
}

func TestArticleKommersantBodyWinsOverRelatedArticles(t *testing.T) {
	page := `<article><h3>Несвязанная новость про спорт</h3></article><div class="article_text_wrapper js-article"><div class="doc-content__text-block"><div class="doc-text-block__paragraph">Первый абзац новости.</div><div class="doc-text-block__paragraph">Последний абзац новости.</div></div></div>`
	got := articleParse(page).ArticleText
	if !strings.Contains(got, "Первый абзац") || !strings.Contains(got, "Последний абзац") || strings.Contains(got, "спорт") {
		t.Fatalf("wrong article: %q", got)
	}
}

func TestArticleRestrictedDoesNotExposePreview(t *testing.T) {
	for _, page := range []string{`<article><p>Preview text</p></article><script type="application/ld+json">{"isAccessibleForFree":false}</script>`, `<article><p>You have a preview view of this article while we are checking your access.</p></article>`, `<article><p>Здесь только начало статьи…</p></article>`} {
		if got := articleParse(page).ArticleText; got != "" {
			t.Fatalf("preview accepted: %q", got)
		}
	}
}

func TestGoogleNewsResolutionResponse(t *testing.T) {
	page := `<div data-n-a-id="token" data-n-a-ts="123" data-n-a-sg="sig"></div>`
	id, ts, sig := googleNewsAttributes(page)
	if id != "token" || ts != "123" || sig != "sig" {
		t.Fatal(id, ts, sig)
	}
	response := ")]}'\n\n[[\"wrb.fr\",\"Fbv4je\",\"[\\\"garturlres\\\",\\\"https://example.com/story\\\",1]\"]]"
	if got := googleNewsResponse([]byte(response)); got != "https://example.com/story" {
		t.Fatalf("bad url %q", got)
	}
	if googleNewsURL("https://news.google.com.evil.test/rss/articles/x") {
		t.Fatal("unsafe host accepted")
	}
}

func TestArticleRemovesPromotionsFromHTMLAndJSONLD(t *testing.T) {
	tech := `<article><p>Первый абзац.</p><p>Последний абзац статьи.</p><p class="affiliate-disclaimer-text">When you purchase through links in our articles, we may earn a small commission. This doesn’t affect our editorial independence.</p></article>`
	wired := `<script type="application/ld+json">{"@type":"NewsArticle","articleBody":"Первый абзац.\n\nПоследний абзац статьи.\n\nPower up with unlimited access to WIRED. Get best-in-class reporting and exclusive subscriber content that's too important to ignore. Subscribe Today."}</script>`
	for _, page := range []string{tech, wired} {
		got := articleParse(page).ArticleText
		if !strings.Contains(got, "Последний абзац статьи.") || strings.Contains(got, "commission") || strings.Contains(got, "Subscribe Today") {
			t.Fatalf("unexpected text: %q", got)
		}
	}
	ordinary := "The reporter said readers should subscribe to the public archive to access historical documents."
	if got := articleCleanPromotions(ordinary); got != ordinary {
		t.Fatalf("ordinary paragraph removed: %q", got)
	}
}
