package posta

import (
	"strings"
	"testing"

	"posta/internal/config"
	"posta/internal/jx"
)

func TestTelegramAlbumPreviewTracksIndividualVideoPages(t *testing.T) {
	page := `<div data-post="testchannel/10"><a class="tgme_widget_message_photo_wrap" href="https://t.me/testchannel/10?single" style="background-image:url(https://example.com/photo.jpg)"></a><a class="tgme_widget_message_video_player" href="https://t.me/testchannel/11?single"><video poster="https://example.com/11.jpg"></video></a><a class="tgme_widget_message_video_player" href="https://t.me/testchannel/12?single"><video poster="https://example.com/12.jpg"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || !jx.Bool(posts[0], "media_incomplete") {
		t.Fatalf("partial album not retained: %v", posts)
	}
	targets := videoPreviewTargets(posts)
	if len(targets) != 2 || jx.Str(targets[0], "video_page") != "https://t.me/testchannel/11" || jx.Str(targets[1], "video_page") != "https://t.me/testchannel/12" {
		t.Fatalf("wrong download targets: %v", targets)
	}
	applyVideoPreviewTargets(posts, func(page string) jx.M {
		if strings.HasSuffix(page, "/11") {
			return jx.M{"status": "done", "local": "https://example.com/11.mp4"}
		}
		return jx.M{"status": "running"}
	})
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	media := u.sourcePreviewMedia(posts[0])
	if len(media) != 3 || jx.Str(media[0], "kind") != "image" || jx.Str(media[1], "src") != "https://example.com/11.mp4" || jx.Str(media[2], "page") != "https://t.me/testchannel/12" || !jx.Bool(media[2], "pending") {
		t.Fatalf("incorrect media order or state: %v", media)
	}
}

func TestTelegramInventoryRejectsForeignTileLink(t *testing.T) {
	page := `<div data-post="testchannel/10"><a class="tgme_widget_message_video_player" href="https://t.me/foreignchannel/11?single"></a><a class="tgme_widget_message_photo_wrap" style="background-image:url(https://example.com/photo.jpg)"></a></div>`
	p := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 0)
	if len(p) != 1 || len(videoPreviewTargets(p)) != 0 {
		t.Fatalf("foreign album video was accepted: %v", p)
	}
}

func TestSourcePreviewDoesNotTruncateText(t *testing.T) {
	u := &U{App: &App{Cfg: config.Server{MediaURL: "/media"}}}
	body := strings.Repeat("Long text. ", 400) + "End."
	p := u.SourcePreviewPost(jx.M{"text": body})
	if jx.Str(p, "text") != body {
		t.Fatalf("preview lost text: %d of %d bytes", len(jx.Str(p, "text")), len(body))
	}
}
