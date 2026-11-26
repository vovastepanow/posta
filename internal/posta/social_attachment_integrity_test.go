package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestTelegramMixedAlbumMissingSecondVideo(t *testing.T) {
	page := `<div data-post="testchannel/123"><a class="tgme_widget_message_photo_wrap" style="background-image:url(https://example.com/photo.jpg)"></a><a class="tgme_widget_message_video_player"><video src="https://example.com/video.mp4"></video></a><a class="tgme_widget_message_video_player"><video poster="https://example.com/missing.jpg"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || !jx.Bool(posts[0], "media_incomplete") || jx.Int(posts[0], "expected_video_count") != 2 {
		t.Fatalf("partial album not identified: %v", posts)
	}
	if _, _, ok := publicClassificationInput(posts[0]); ok {
		t.Fatal("partial album admitted for public classification")
	}
}

func TestVKOrderedAttachmentInventoryAndPlainText(t *testing.T) {
	video := func(id int) jx.M {
		return jx.M{"type": "video", "video": jx.M{"owner_id": -10, "id": id, "image": []jx.M{{"url": "https://example.com/cover.jpg", "width": 100, "height": 100}}}}
	}
	raw := jx.M{"id": 55, "owner_id": -10, "text": "First line\n2 < 3 > 1", "attachments": []jx.M{
		{"type": "photo", "photo": jx.M{"sizes": []jx.M{{"url": "https://example.com/photo.jpg", "width": 100, "height": 100}}}}, video(12), video(13),
	}, "copy_history": []jx.M{{"owner_id": -20, "id": 4, "text": "Original"}}}
	post := VKWallPost(raw, "VK", "club10", true, 0)
	attachments := jx.Maps(post, "source_attachments")
	if len(attachments) != 3 || jx.Str(attachments[0], "kind") != "photo" || jx.Str(attachments[1], "video_page") != "https://vk.com/video-10_12" || jx.Str(attachments[2], "video_page") != "https://vk.com/video-10_13" {
		t.Fatalf("attachment inventory lost: %v", attachments)
	}
	if jx.Str(post, "text") != "First line\n2 < 3 > 1" || !jx.Bool(post, "media_incomplete") || len(jx.Maps(post, "repost_context")) != 1 {
		t.Fatalf("incorrect normalized post: %v", post)
	}
}
