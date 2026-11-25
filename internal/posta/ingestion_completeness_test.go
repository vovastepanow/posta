package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestOKRetainsLongTextOnlyPost(t *testing.T) {
	body := strings.Repeat("Длинный текст. ", 150) + "Последний абзац"
	page := `<li class="item it feed-card" data-json="topicId&quot;:&quot;123"><div class="topic-text_content">Первый абзац<br>` + body + `<div class="widget-list"><span class="tstmp feed_descr">вчера 12:00</span></li>`
	posts := OKParseFeed(page, "1234567890123", 10, "")
	if len(posts) != 1 || !jx.Bool(posts[0], "text_post") || !strings.Contains(jx.Str(posts[0], "text"), strings.TrimSpace(body)) || !strings.Contains(jx.Str(posts[0], "text"), "Первый абзац\n") {
		t.Fatalf("text-only post lost or truncated: %v", posts)
	}
}

func TestRSSRetainsVideoWithoutImage(t *testing.T) {
	data := []byte(`<rss><channel><item><title>Video</title><link>https://example.com/post</link><enclosure url="https://example.com/video.mp4" type="video/mp4"/></item></channel></rss>`)
	posts, err := RSSParse(data, 10, "", "https://example.com/feed", false, false, 120)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "video") != "https://example.com/video.mp4" || jx.Bool(posts[0], "text_post") {
		t.Fatalf("RSS video lost: %v %v", posts, err)
	}
}

func TestTelegramRetainsPendingVideoWithoutImage(t *testing.T) {
	page := `<div data-post="testchannel/123"><a class="tgme_widget_message_video_player"><video></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || !jx.Bool(posts[0], "video_unavailable") || jx.Bool(posts[0], "text_post") {
		t.Fatalf("pending video lost: %v", posts)
	}
}

func TestThreadsMixedCarousel(t *testing.T) {
	data := []byte(`{"data":[{"id":"1","username":"test","media_type":"CAROUSEL_ALBUM","permalink":"https://www.threads.com/@test/post/A","timestamp":"2026-11-01T12:00:00Z","children":{"data":[{"media_type":"IMAGE","media_url":"https://cdn.example.com/1.jpg"},{"media_type":"VIDEO","media_url":"https://cdn.example.com/2.mp4"},{"media_type":"IMAGE","media_url":"https://cdn.example.com/3.jpg"}]}}]}`)
	posts, err := threadsProfilePosts(data, "test", 10, false)
	if err != nil || len(posts) != 1 {
		t.Fatalf("carousel lost: %v %v", posts, err)
	}
	gallery := jx.Maps(posts[0], "gallery")
	if len(gallery) != 3 || jx.Str(gallery[0], "image") != "https://cdn.example.com/1.jpg" || jx.Str(gallery[1], "video") != "https://cdn.example.com/2.mp4" || jx.Str(gallery[2], "image") != "https://cdn.example.com/3.jpg" || jx.Bool(posts[0], "text_post") {
		t.Fatalf("carousel lost or reordered: %v", gallery)
	}
}
