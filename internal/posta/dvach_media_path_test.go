package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestDvachPostMediaChecksBoardAndPath(t *testing.T) {
	post := jx.M{"files": []jx.M{
		{"path": "/B/SRC/123/one.JPG"},
		{"path": "/b/src/123/video.mp4", "thumbnail": "/b/thumb/123/cover.png"},
		{"path": "/other/src/123/wrong.png"},
		{"path": "/b/src/123/video.webm", "thumbnail": "/other/thumb/123/wrong.png"},
		{"path": "//example.org/b/src/123/wrong.png"},
		{"path": "/b/src/../wrong.png"},
		{"path": "/b/src/123/one.png/extra"},
		{"path": "/b/src/123/no-cover.mp4"},
	}}
	got := DvachPostMedia("b", post, true)
	if len(got) != 2 || jx.Str(got[0], "image") != "https://2ch.hk/B/SRC/123/one.JPG" ||
		jx.Str(got[1], "video") != "https://2ch.hk/b/src/123/video.mp4" {
		t.Fatalf("unexpected media: %v", got)
	}
	if got := DvachPostMedia("b", post, false); len(got) != 1 {
		t.Fatalf("video disabled: %v", got)
	}
}

func BenchmarkDvachPostMedia(b *testing.B) {
	post := jx.M{"files": []jx.M{
		{"path": "/b/src/123/one.png"},
		{"path": "/b/src/123/video.mp4", "thumbnail": "/b/thumb/123/cover.jpg"},
	}}
	for i := 0; i < b.N; i++ {
		_ = DvachPostMedia("b", post, true)
	}
}
