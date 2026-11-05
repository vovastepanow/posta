package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestLemmyParseEmbeddedVideo(t *testing.T) {
	data := []byte(`{"posts":[{"post":{"name":"Trying to be a good brother", "ap_id":"https://example.org/post/1", "thumbnail_url":"https://example.org/cover.jpg", "body":"Video ![](https://cdn.example.org/clip.mp4?x=1)", "published":"2026-11-01T12:00:00Z", "community_id":1},"community":{"actor_id":"https://example.org/c/test"}}]}`)
	posts, err := LemmyParse(data)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%v error=%v", posts, err)
	}
	if got := jx.Str(posts[0], "video"); got != "https://cdn.example.org/clip.mp4?x=1" {
		t.Fatalf("video=%q", got)
	}
	if got := jx.Str(posts[0], "image"); got != "https://example.org/cover.jpg" {
		t.Fatalf("image=%q", got)
	}
	if got := lemmyBodyVideo("[unrelated](https://example.org/clip.mp4)"); got != "" {
		t.Fatalf("ordinary link treated as attachment: %q", got)
	}
}
