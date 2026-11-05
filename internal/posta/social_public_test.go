package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestVKPublicFeedParsesPostAndAttachments(t *testing.T) {
	page := `<div id="post-12_34"><div class="wall_post_text">Привет <b>мир</b></div><time datetime="2026-11-01T12:00:00Z"></time><img src="https://cdn.example/one.jpg"><img data-src="https://cdn.example/two.jpg"><a href="/video-12_4">video</a></div>`
	posts := vkPublicParse([]byte(page), "example", "VK", 10, false, 120)
	if len(posts) != 1 || jx.Str(posts[0], "url") != "https://vk.com/wall-12_34" || jx.Int(posts[0], "posted_at") == 0 || jx.Str(posts[0], "video_page") != "https://vk.com/video-12_4" {
		t.Fatalf("unexpected posts: %#v", posts)
	}
	if len(uniqueStrings(posts[0]["images"].([]string))) != 2 {
		t.Fatalf("attachments: %#v", posts[0])
	}
}

func TestInstagramPublicFeedEmbeddedJSON(t *testing.T) {
	page := `<script type="application/json">{"other":{"items":[{"shortcode":"ABC123","taken_at_timestamp":1793534400,"display_url":"https://cdn.example/cover.jpg","owner":{"username":"cats"},"edge_media_to_caption":{"edges":[{"node":{"text":"hello"}}]},"edge_sidecar_to_children":{"edges":[{"node":{"display_url":"https://cdn.example/a.jpg"}},{"node":{"display_url":"https://cdn.example/b.jpg","video_url":"https://cdn.example/b.mp4"}}]}},{"shortcode":"OTHER1","taken_at_timestamp":1793534400,"display_url":"https://cdn.example/x.jpg","owner":{"username":"other"}}]}}</script>`
	posts := instagramPublicParse([]byte(page), "cats", 10, false)
	if len(posts) != 1 || jx.Str(posts[0], "text") != "hello" || jx.Str(posts[0], "url") != "https://www.instagram.com/p/ABC123/" {
		t.Fatalf("unexpected posts: %#v", posts)
	}
	if len(jx.Maps(posts[0], "gallery")) != 2 {
		t.Fatalf("album lost: %#v", posts[0])
	}
}

func TestVKPublicIgnoresAvatarsAndReadsRenderedText(t *testing.T) {
	page := `<div id="post1_23" data-post-id="1_23"><img class="AvatarRich__img" src="https://cdn.example/avatar.jpg"><div class="wall_post_text_wrapper"><div class="wall_post_text">Actual text</div></div><a data-testid="post_date_block_preview">3 мая 2018</a><img data-testid="primary-attachment-image-content" src="https://cdn.example/photo.jpg"></div><div id="post1_24"><img class="AvatarRich__img" src="https://cdn.example/avatar.jpg"></div>`
	posts := vkPublicParse([]byte(page), "test", "VK", 10, true, 1)
	if len(posts) != 1 || jx.Str(posts[0], "image") != "https://cdn.example/photo.jpg" || jx.Str(posts[0], "text") != "Actual text" || jx.Int(posts[0], "posted_at") == 0 {
		t.Fatalf("unexpected posts: %#v", posts)
	}
}

func TestInstagramRenderedGrid(t *testing.T) {
	page := `<a href="/cats/reel/ABC123/"><img src="https://cdn.example/reel.jpg"></a><a href="/cats/p/DEF456/"><img src="https://cdn.example/photo.jpg"></a><a href="/other/p/XYZ789/"><img src="https://cdn.example/other.jpg"></a>`
	posts := instagramPublicDOMPosts([]byte(page), "cats", 10)
	if len(posts) != 2 || jx.Str(posts[0], "video_page") != "https://www.instagram.com/reel/ABC123/" || jx.Str(posts[1], "image") != "https://cdn.example/photo.jpg" || jx.Int(posts[0], "posted_at") != 0 {
		t.Fatalf("unexpected posts: %#v", posts)
	}
}

func TestInstagramPostDetailsRequireMatchingCanonical(t *testing.T) {
	post := jx.M{"url": "https://www.instagram.com/reel/ABC123/", "text": EmptyPostText}
	page := `<meta property="og:url" content="https://www.instagram.com/cats/reel/ABC123/"><meta property="og:description" content='10 likes - cats: "Real caption"'><meta property="og:image" content="https://cdn.example/image.jpg"><time datetime="2026-11-02T19:25:51.000Z"></time>`
	if !instagramPublicEnrichPost([]byte(page), post) || jx.Str(post, "text") != "Real caption" || jx.Int(post, "posted_at") == 0 || jx.Str(post, "image") != "https://cdn.example/image.jpg" {
		t.Fatalf("details: %#v", post)
	}
	other := jx.M{"url": "https://www.instagram.com/p/OTHER1/"}
	if instagramPublicEnrichPost([]byte(page), other) {
		t.Fatal("accepted another post")
	}
}
