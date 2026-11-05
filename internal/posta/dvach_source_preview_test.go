package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestDvachPreviewImages(t *testing.T) {
	posts := []jx.M{
		{"media": []jx.M{{"kind": "image", "src": "https://example.org/a.jpg"}, {"kind": "image", "src": "https://example.org/a.jpg"}}},
		{"media": []jx.M{{"kind": "video", "src": "https://example.org/v.mp4", "poster": "https://example.org/b.jpg"}, {"kind": "video", "src": "https://example.org/no-poster.mp4"}}},
	}
	got := dvachPreviewImages(posts)
	if len(got) != 2 || jx.Str(got[1], "image") != "https://example.org/b.jpg" {
		t.Fatalf("unexpected images: %#v", got)
	}
}
