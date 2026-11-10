package posta

import (
	"posta/internal/config"
	"posta/internal/jx"
	"testing"
	"time"
)

func TestVKRegistrySamplePreservesAlbum(t *testing.T) {
	post := jx.M{"image": "https://example.com/1.jpg", "images": []string{"https://example.com/1.jpg", "https://example.com/2.jpg"}, "text": "Пост"}
	sample := registrySample([]jx.M{post})
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	preview := u.pendingPreviewPosts(sample)
	if len(preview) != 1 || len(jx.Maps(preview[0], "media")) != 2 {
		t.Fatalf("album lost in saved preview: %#v", preview)
	}
}

func TestPendingSourcesExcludeDecidedVariants(t *testing.T) {
	videos := "rss:https://www.youtube.com/feeds/videos.xml?channel_id=UC1234567890123456789012"
	shorts := "rss:https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH1234567890123456789012"
	candidates := []jx.M{{"source_id": shorts}, {"source_id": "telegram:Example"}, {"source_id": "telegram:other"}}
	decided := map[string]bool{recommendationSourceKey(videos): true, recommendationSourceKey("telegram:example"): true}
	got := excludeDecidedSourceCandidates(candidates, decided)
	if len(got) != 1 || jx.Str(got[0], "source_id") != "telegram:other" {
		t.Fatalf("decided sources returned: %#v", got)
	}
}

func TestPendingSourcesExcludeDecisionsFromRelatedTopics(t *testing.T) {
	u := taxonomyTestUser()
	votes := []jx.M{
		{"source_id": "telegram:Approved", "topic": "предпринимательство", "vote": "approve"},
		{"source_id": "telegram:Other", "topic": "спорт", "vote": "approve"},
		{"source_id": "telegram:Legacy", "vote": "approve"},
	}
	decided := u.decidedSourceKeys(votes, "Бизнес")
	got := excludeDecidedSourceCandidates([]jx.M{{"source_id": "telegram:approved"}, {"source_id": "telegram:other"}, {"source_id": "telegram:legacy"}}, decided)
	if len(got) != 1 || jx.Str(got[0], "source_id") != "telegram:other" {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestSortPendingSources(t *testing.T) {
	for _, tc := range []struct{ order, first, last string }{
		{"rank", "b", "c"}, {"newest", "a", "c"}, {"oldest", "b", "c"},
		{"score_high", "b", "c"}, {"score_low", "a", "c"}, {"name", "a", "c"},
	} {
		list := []jx.M{{"source_id": "b", "name": "Б", "timestamp": 10, "score": 80}, {"source_id": "a", "name": "А", "timestamp": 20, "score": 60}, {"source_id": "c", "name": "В"}}
		if err := sortPendingSources(list, tc.order); err != nil || jx.Str(list[0], "source_id") != tc.first || jx.Str(list[2], "source_id") != tc.last {
			t.Errorf("order %s: %v, %v", tc.order, list, err)
		}
	}
	if sortPendingSources(nil, "invalid") == nil {
		t.Error("invalid sort accepted")
	}
}

func TestSourceMatchesKind(t *testing.T) {
	for _, tc := range []struct {
		id, kind string
		want     bool
	}{
		{"https://dzen.ru/id/123", "Дзен", true},
		{"https://zen.yandex.ru/rss/channel/123", "Дзен", true},
		{"https://dzen.ru.evil.example/feed", "Дзен", false},
		{"https://reddit.com/r/example/.rss", "Reddit", true},
		{"https://youtube.com/feeds/videos.xml?channel_id=abc", "YouTube Shorts", false},
	} {
		c := jx.M{"platform": PlatformRSS, "id": tc.id, "kind": RSSFeedKind(tc.id)}
		if got := sourceMatchesKind(c, tc.kind); got != tc.want {
			t.Errorf("sourceMatchesKind(%q, %q) = %v, want %v", tc.id, tc.kind, got, tc.want)
		}
	}
}

func TestPendingPreviewPostsRankAndLimit(t *testing.T) {
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	posts := []jx.M{}
	for i := 1; i <= 9; i++ {
		posts = append(posts, jx.M{"text": "пример", "url": "https://example.com/post", "views": i})
	}
	out := u.pendingPreviewPosts(posts)
	if len(out) != 6 {
		t.Fatalf("expected six examples, got %d", len(out))
	}
	if jx.Int(out[0], "views") != 9 || jx.Int(out[5], "views") != 4 {
		t.Fatalf("posts not ranked by views: %v", out)
	}
}

func TestPendingPreviewPostsKeepsSampleOrderWithoutMetrics(t *testing.T) {
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	out := u.pendingPreviewPosts([]jx.M{{"text": "первый"}, {"text": "второй"}, {"text": ""}})
	if len(out) != 2 || jx.Str(out[0], "text") != "первый" || jx.Str(out[1], "text") != "второй" {
		t.Fatalf("unexpected examples: %v", out)
	}
}

func TestRestoreSavedYouTubeVideo(t *testing.T) {
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	input := []jx.M{{"image": "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg", "url": "https://www.youtube.com/shorts/abcdefghijk", "text": "short"}}
	repaired := restoreYouTubeSamples(input)
	if jx.Str(input[0], "video_page") != "" {
		t.Fatal("changed the saved sample")
	}
	posts := u.pendingPreviewPosts(repaired)
	if len(posts) != 1 {
		t.Fatalf("missing post: %#v", posts)
	}
	media := jx.Maps(posts[0], "media")
	if len(media) != 1 || jx.Str(media[0], "kind") != "video" || jx.Str(media[0], "page") != "https://www.youtube.com/shorts/abcdefghijk" {
		t.Fatalf("short was not restored as video: %#v", media)
	}
	if got := canonicalYouTubeSamplePage("https://evil.example/shorts/abcdefghijk"); got != "" {
		t.Fatalf("untrusted URL accepted: %s", got)
	}
}

func TestYouTubeGalleryPosterRemainsVideo(t *testing.T) {
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	media := u.sourcePreviewMedia(jx.M{"video_page": "https://www.youtube.com/shorts/abcdefghijk", "gallery": []jx.M{{"image": "https://example.com/poster.jpg"}}})
	if len(media) != 1 || jx.Str(media[0], "kind") != "video" || jx.Str(media[0], "poster") == "" {
		t.Fatalf("gallery poster lost its video: %#v", media)
	}
}

func TestPendingRSSName(t *testing.T) {
	feed := "https://www.youtube.com/feeds/videos.xml?channel_id=UC1234567890123456789012"
	if name, missing := pendingRSSName("RSS |", "Автор канала", feed); name != "Автор канала" || missing {
		t.Fatalf("registry name lost: %q %v", name, missing)
	}
	if name, missing := pendingRSSName("RSS |", "", feed); name == "" || name == "RSS" || !missing {
		t.Fatalf("fallback missing: %q %v", name, missing)
	}
	if name, missing := pendingRSSName("RSS |", "", "https://example.com/feed"); name != "example.com" || !missing {
		t.Fatalf("site fallback: %q %v", name, missing)
	}
}

func TestTelegramSourcePreviewReadsPosts(t *testing.T) {
	page := `<div class="tgme_channel_info_header_title">Канал</div><div data-post="humor_channel/123"><a class="tgme_widget_message_photo_wrap" style="background-image:url('https://example.com/photo.jpg')"></a><div class="tgme_widget_message_text">Пост</div></div>`
	a := &App{Mem: NewMemCache(10)}
	a.Mem.Set("tgpage:humor_channel|0", page, time.Minute)
	feed := a.telegramSourcePreviewFeed("humor_channel", 1)
	if posts := jx.Maps(feed, "posts"); len(posts) != 1 {
		t.Fatalf("Telegram preview failed to read post: %#v", feed)
	}
}

func TestRSSPlatformLabel(t *testing.T) {
	tests := []struct{ feed, want string }{
		{RSSYouTubeShortsFeed("UC1234567890123456789012"), "YouTube Shorts"},
		{YouTubeChannelFeedURL("UC1234567890123456789012"), "YouTube"},
		{"https://www.reddit.com/r/cats/.rss", "Reddit"},
		{"https://habr.com/ru/rss/all/", "Хабр"},
		{"https://dzen.ru/rss/example", "Дзен"},
		{"https://cats.example.com/feed", "Сайт · cats.example.com"},
		{"https://reddit.com.evil.example/feed", "Сайт · reddit.com.evil.example"},
		{"https://evil.example/feed?playlist_id=UUSHfoo", "Сайт · evil.example"},
	}
	for _, tt := range tests {
		if got := RSSPlatformLabel(tt.feed); got != tt.want {
			t.Errorf("%s: got %q want %q", tt.feed, got, tt.want)
		}
	}
}

func TestRSSNameGenericAndDoublePrefix(t *testing.T) {
	feed := YouTubeChannelFeedURL("UC1234567890123456789012")
	if got, missing := pendingRSSName("RSS | RSS | Кошки", "", feed); got != "Кошки" || missing {
		t.Fatalf("unexpected real title: %q %v", got, missing)
	}
	for _, name := range []string{"RSS |", "RSS | www.youtube.com", "YouTube · UC1234567890123456789012"} {
		if _, missing := pendingRSSName(name, "", feed); !missing {
			t.Errorf("%q should be resolved", name)
		}
	}
}
