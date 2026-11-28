package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestTelegramAlbumPreviewTracksIndividualVideoPages(t *testing.T) {
	page := `<div data-post="testchannel/10"><a class="tgme_widget_message_photo_wrap" href="https://t.me/testchannel/10?single" style="background-image:url(https://example.com/photo.jpg)"></a><a class="tgme_widget_message_video_player" href="https://t.me/testchannel/11?single"><video poster="https://example.com/11.jpg"></video></a><a class="tgme_widget_message_video_player" href="https://t.me/testchannel/12?single"><video poster="https://example.com/12.jpg"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || !jx.Bool(posts[0], "media_incomplete") {
		t.Fatalf("partial album not retained: %v", posts)
	}
	inventory := jx.Maps(posts[0], "source_attachments")
	if len(inventory) != 3 || jx.Str(inventory[0], "kind") != "photo" || jx.Str(inventory[1], "video_page") != "https://t.me/testchannel/11" || jx.Str(inventory[2], "video_page") != "https://t.me/testchannel/12" {
		t.Fatalf("wrong attachment inventory: %v", inventory)
	}
	var requested []string
	resolve := func(page string) string {
		requested = append(requested, page)
		if strings.HasSuffix(page, "/11") {
			return "/media/11.mp4"
		}
		return ""
	}
	partial := restoreAttachmentInventory(posts[0], resolve, func(s string) string { return s }, func(s string) string { return s + ".jpg" })
	if !jx.Bool(partial, "media_incomplete") || len(requested) != 2 || requested[0] != "https://t.me/testchannel/11" || requested[1] != "https://t.me/testchannel/12" {
		t.Fatalf("partial album accepted or wrong downloads: %v %v", partial, requested)
	}
	ready := restoreAttachmentInventory(posts[0], func(page string) string {
		if strings.HasSuffix(page, "/11") {
			return "/media/11.mp4"
		}
		return "/media/12.mp4"
	}, func(s string) string { return s }, func(s string) string { return s + ".jpg" })
	gallery := ContentGallery(ready)
	if jx.Bool(ready, "media_incomplete") || len(gallery) != 3 || jx.Str(gallery[0], "image") != "https://example.com/photo.jpg" || jx.Str(gallery[1], "video") != "/media/11.mp4" || jx.Str(gallery[2], "video") != "/media/12.mp4" {
		t.Fatalf("incorrect recovered media order: %v", gallery)
	}
}

func TestTelegramInventoryRejectsForeignTileLink(t *testing.T) {
	page := `<div data-post="testchannel/10"><a class="tgme_widget_message_video_player" href="https://t.me/foreignchannel/11?single"></a><a class="tgme_widget_message_photo_wrap" style="background-image:url(https://example.com/photo.jpg)"></a></div>`
	p := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 0)
	if len(p) != 1 || !jx.Bool(p[0], "media_incomplete") {
		t.Fatalf("foreign album video was accepted: %v", p)
	}
	inventory := jx.Maps(p[0], "source_attachments")
	if len(inventory) != 2 || jx.Str(inventory[0], "kind") != "video" || jx.Str(inventory[0], "video_page") != "" || jx.Str(p[0], "video_page") != "" {
		t.Fatalf("unknown tile must not fall back to album ID: %v", inventory)
	}
}

func TestPostDisplayDoesNotTruncateText(t *testing.T) {
	body := strings.Repeat("Long text. ", 400) + "End."
	if got := CleanPostDisplayText(body, jx.M{"text": body}); got != body {
		t.Fatalf("display lost text: %d of %d bytes", len(got), len(body))
	}
}

func TestTelegramPublicWidgetDoesNotCollectServiceEvents(t *testing.T) {
	page := `<script>var fake = '<div data-post="testchannel/999">';</script>
<!-- <div data-post="testchannel/998"> -->
<div class="tgme_widget_message" data-post="testchannel/1"><div class="tgme_widget_message_text">Original content</div></div>
<div class="tgme_widget_message service_message" data-post="testchannel/2"><div class="tgme_widget_message_text">Channel created</div></div>
<div data-post='testchannel/3' class="tgme_widget_message"><div class="tgme_widget_message_text">A post about how a file is pinned</div></div>`
	got := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 20, true, 0)
	if len(got) != 2 || jx.Str(got[0], "post_id") != "3" || jx.Str(got[1], "post_id") != "1" ||
		jx.Str(got[1], "text") != "Original content" {
		t.Fatalf("incorrect message boundaries: %+v", got)
	}
}

func TestTelegramPublicWidgetMarksUnsupportedAttachmentsIncomplete(t *testing.T) {
	page := `<div data-post="testchannel/1"><div class="tgme_widget_message_text">Read the attached guide</div><a class="tgme_widget_message_document_wrap">document</a></div>
<div data-post="testchannel/2"><div class="tgme_widget_message_text">A complete text post</div></div>`
	got := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 20, true, 0)
	if len(got) != 2 || jx.Bool(got[0], "media_incomplete") || !jx.Bool(got[1], "media_incomplete") ||
		!jx.Bool(got[1], "attachment_inventory_incomplete") || jx.Bool(got[1], "text_post") {
		t.Fatalf("incomplete document treated as complete content: %+v", got)
	}
}
