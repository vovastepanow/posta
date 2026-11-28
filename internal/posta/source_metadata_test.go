package posta

import (
	"posta/internal/jx"
	"testing"
	"time"
)

func TestVKRegistrySamplePreservesAlbum(t *testing.T) {
	post := jx.M{"image": "https://example.com/1.jpg", "images": []string{"https://example.com/1.jpg", "https://example.com/2.jpg"}, "text": "Пост"}
	sample := registrySample([]jx.M{post})
	if len(sample) != 1 {
		t.Fatalf("album lost in registry sample: %#v", sample)
	}
	if images := jx.Strs(sample[0], "images"); len(images) != 2 || images[0] != "https://example.com/1.jpg" || images[1] != "https://example.com/2.jpg" {
		t.Fatalf("album lost in saved registry sample: %#v", sample)
	}
}

func TestLegacyDecisionsExcludeVariants(t *testing.T) {
	videos := "rss:https://www.youtube.com/feeds/videos.xml?channel_id=UC1234567890123456789012"
	shorts := "rss:https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH1234567890123456789012"
	decided := map[string]bool{recommendationSourceKey(videos): true, recommendationSourceKey("telegram:example"): true}
	if !recommendationExcluded(decided, shorts) || !recommendationExcluded(decided, "telegram:Example") || recommendationExcluded(decided, "telegram:other") {
		t.Fatal("legacy recommendation identity lost")
	}
}

func TestLegacyDecisionsRespectRelatedTopics(t *testing.T) {
	u := taxonomyTestUser()
	votes := []jx.M{
		{"source_id": "telegram:Approved", "topic": "предпринимательство", "vote": "approve"},
		{"source_id": "telegram:Other", "topic": "спорт", "vote": "approve"},
		{"source_id": "telegram:Legacy", "vote": "approve"},
	}
	decided := u.decidedSourceKeys(votes, "Бизнес")
	if !recommendationExcluded(decided, "telegram:approved") || !recommendationExcluded(decided, "telegram:legacy") || recommendationExcluded(decided, "telegram:other") {
		t.Fatalf("unexpected decisions: %#v", decided)
	}
}

func TestCanonicalYouTubeSamplePage(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://www.youtube.com/shorts/abcdefghijk", "https://www.youtube.com/shorts/abcdefghijk"},
		{"https://youtu.be/abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk"},
		{"https://evil.example/shorts/abcdefghijk", ""},
	} {
		if got := canonicalYouTubeSamplePage(tc.raw); got != tc.want {
			t.Fatalf("sample page %q = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestYouTubeGalleryPosterRemainsVideo(t *testing.T) {
	post := jx.M{"video_page": "https://www.youtube.com/shorts/abcdefghijk", "video_via": "ytdlp", "gallery": []jx.M{{"image": "https://example.com/poster.jpg"}}}
	if !PostItemHasVideo(post) || !PostItemHasMedia(post) {
		t.Fatalf("pending video was treated as a photo: %#v", post)
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

func TestTelegramCachedPageReadsPosts(t *testing.T) {
	page := `<div class="tgme_channel_info_header_title">Канал</div><div data-post="humor_channel/123"><a class="tgme_widget_message_photo_wrap" style="background-image:url('https://example.com/photo.jpg')"></a><div class="tgme_widget_message_text">Пост</div></div>`
	a := &App{Mem: NewMemCache(10)}
	a.Mem.Set("tgpage:humor_channel|0", page, time.Minute)
	posts := TelegramPostsFromHTML(a.TelegramChannelPageCached("humor_channel", 0), jx.M{"id": "humor_channel"}, 1, true, 0)
	if len(posts) != 1 {
		t.Fatalf("Telegram cached page failed to read post: %#v", posts)
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
