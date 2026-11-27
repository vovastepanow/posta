package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestCalendarMediaPreservesPostType(t *testing.T) {
	a := &App{}
	cases := []struct {
		name  string
		item  jx.M
		kinds []string
		fail  bool
	}{
		{"text", jx.M{"text_post": true, "text": "A text publication"}, nil, false},
		{"video", jx.M{"image": "https://example.org/poster.jpg", "video": "https://example.org/movie.mp4"}, []string{"video"}, false},
		{"pending video", jx.M{"image": "https://example.org/poster.jpg", "video_via": "tgu", "video_page": "https://t.me/test/1"}, nil, true},
		{"album", jx.M{"gallery": []jx.M{{"image": "https://example.org/1.jpg"}, {"image": "https://example.org/2.jpg", "video": "https://example.org/2.mp4"}}}, []string{"image", "video"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			media, err := a.calendarRecordMedia(tc.item)
			if (err != nil) != tc.fail || len(media) != len(tc.kinds) {
				t.Fatalf("media=%v err=%v", media, err)
			}
			for i, kind := range tc.kinds {
				if media[i].Kind != kind {
					t.Fatalf("media type changed: %v", media)
				}
			}
		})
	}
}

func TestCalendarFindTextRecord(t *testing.T) {
	first := jx.M{"text_post": true, "text": "SMM"}
	second := jx.M{"text_post": true, "text": "SEO"}
	h := History{"liked": []jx.M{first, second}}
	if got := calendarFillRecord(ContentItemKey(second), nil, h); jx.Str(got, "text") != "SEO" {
		t.Fatal("could not address text proposal")
	}
	if calendarFillRecord("", nil, h) != nil {
		t.Fatal("empty key selected an arbitrary text record")
	}
}

func TestCalendarRichOriginal(t *testing.T) {
	raw := `<b>Bold <a href="https://example.org/a">link</a></b><br><i>Italic</i><script>hidden</script>`
	if got := calendarHTMLText(raw, true); got != "**Bold** [link](https://example.org/a)\n__Italic__" {
		t.Fatalf("formatted original: %q", got)
	}
	if got := calendarHTMLText(raw, false); got != "Bold link (https://example.org/a)\nItalic" {
		t.Fatalf("plain original: %q", got)
	}
	article := jx.M{"title": "Title", "blocks": []jx.M{
		{"type": "p", "text": "Full first paragraph", "links": []any{[]any{"first", "https://example.org"}}},
		{"type": "image", "src": "https://example.org/photo.jpg"},
		{"type": "p", "text": "Last paragraph"},
	}}
	text, media, err := calendarStructuredArticle(article, "", nil, true)
	if err != nil || len(media) != 1 || text != "**Title**\n\nFull [first](https://example.org) paragraph\n\nLast paragraph" {
		t.Fatalf("article: %q media=%v err=%v", text, media, err)
	}
	article["blocks"] = []jx.M{{"type": "video", "url": "https://example.org/video"}}
	if _, _, err := calendarStructuredArticle(article, "", nil, true); err == nil {
		t.Fatal("unsupported article silently scheduled")
	}
	if calendarRequiresEditor(jx.M{"text_html": raw}) || !calendarRequiresEditor(jx.M{"article_source": true, "url": "https://example.org/news"}) ||
		calendarRequiresEditor(jx.M{"article_source": true, "url": "https://vc.ru/123456"}) {
		t.Fatal("incorrect rich post routing")
	}
}

func TestCalendarArticleIdentityDoesNotUseCover(t *testing.T) {
	cover := "https://example.org/shared-cover.jpg"
	first := jx.M{"article_source": true, "image": cover, "url": "https://vc.ru/123456", "text": "First article"}
	second := jx.M{"article_source": true, "image": cover, "url": "https://vc.ru/123457", "text": "Second article"}
	if ContentItemKey(first) == ContentItemKey(second) || ContentItemKey(first) == cover {
		t.Fatal("articles with shared covers have the same identity")
	}
	h := History{"liked": []jx.M{first, second}}
	if got := calendarFillRecord(ContentItemKey(second), nil, h); jx.Str(got, "text") != "Second article" {
		t.Fatal("wrong article selected")
	}
	if calendarFillRecord(cover, nil, h) != nil || historyCanonicalKey(h, cover) != "" {
		t.Fatal("ambiguous legacy cover resolved to arbitrary article")
	}
	if got := calendarFillRecord(cover, []jx.M{first}, nil); ContentItemKey(got) != ContentItemKey(first) {
		t.Fatal("unambiguous legacy cover lost compatibility")
	}
	history := (&App{}).ContentHistoryRecord(first, nil)
	if ContentItemKey(history) != ContentItemKey(first) {
		t.Fatal("article identity changed after saving history")
	}
}
