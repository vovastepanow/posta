package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestPublishingCatalogCompetitorScope(t *testing.T) {
	a := &App{}
	catalog := a.Networks()
	for _, platform := range []string{"vk_clips", "whatsapp", "likee", "yappy"} {
		if catalog[platform] != nil || contains(networkOrder, platform) {
			t.Errorf("unexpected publishing destination: %s", platform)
		}
	}
	for _, platform := range []string{"telegram", "vk", "ok", "max", "youtube", "rutube", "tiktok", "dzen", "instagram", "facebook", "threads", "x", "pinterest", "tumblr", "linkedin", "viber", "reddit", "discord", "bluesky", "mastodon"} {
		if catalog[platform] == nil || !contains(networkOrder, platform) {
			t.Errorf("missing publishing destination: %s", platform)
		}
	}
}

func TestTelegramPlayableVideoWithoutPoster(t *testing.T) {
	page := `<div data-post="testchannel/123"><a class="tgme_widget_message_video_player"><video src="https://cdn.example.com/video.mp4"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || jx.Str(posts[0], "video") != "https://cdn.example.com/video.mp4" || jx.Bool(posts[0], "video_unavailable") || jx.Bool(posts[0], "text_post") {
		t.Fatalf("playable coverless video lost: %v", posts)
	}
}

func TestTelegramPhotoFirstAlbumWithPlayableVideo(t *testing.T) {
	page := `<div data-post="testchannel/124"><a class="tgme_widget_message_photo_wrap" style="background-image:url(https://cdn.example.com/photo.jpg)"></a><a class="tgme_widget_message_video_player"><video src="https://cdn.example.com/video.mp4"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || jx.Bool(posts[0], "video_unavailable") || !PostItemHasVideo(posts[0]) {
		t.Fatalf("mixed album incorrectly marked unavailable: %v", posts)
	}
	gallery := jx.Maps(posts[0], "gallery")
	if len(gallery) != 2 || jx.Str(gallery[0], "image") != "https://cdn.example.com/photo.jpg" || jx.Str(gallery[1], "video") != "https://cdn.example.com/video.mp4" {
		t.Fatalf("media lost or reordered: %v", gallery)
	}
}
