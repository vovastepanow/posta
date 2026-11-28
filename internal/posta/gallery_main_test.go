package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestGalleryMainIndexSkipsOnlyOneEntry(t *testing.T) {
	g := []jx.M{
		{"image": "other"},
		{"image": "shared", "video": "one"},
		{"image": "shared", "video": "two"},
	}
	if got := galleryMainIndex(g, "shared"); got != 1 {
		t.Fatalf("main index = %d, want 1", got)
	}
	if got := galleryMainIndex(g, ""); got != 0 {
		t.Fatalf("empty main index = %d, want 0", got)
	}
	if got := galleryMainIndex(g, "missing"); got != -1 {
		t.Fatalf("missing main index = %d, want -1", got)
	}
}

func TestGalleryPresenceMatchesNormalizedGallery(t *testing.T) {
	for _, raw := range []any{
		nil,
		[]jx.M{},
		[]jx.M{{"video": "single.mp4"}},
		[]jx.M{{"image": "first.jpg"}, {"video": "second.mp4"}},
		[]jx.M{{"video": "first.mp4"}, {"image": "second.jpg"}},
		[]jx.M{{"image": "first.jpg"}, {"image": "second.jpg"}},
		[]jx.M{{"video": " "}, {"image": "second.jpg"}},
		[]any{"invalid", jx.M{"video": "single.mp4"}, jx.M{}},
		[]any{jx.M{"image": "first.jpg"}, "invalid", jx.M{"video": "second.mp4"}},
	} {
		item := jx.M{"gallery": raw}
		gallery := ContentGallery(item)
		wantVideo := false
		for _, f := range gallery {
			wantVideo = wantVideo || strings.TrimSpace(jx.Str(f, "video")) != ""
		}
		if PostItemHasMedia(item) != (len(gallery) > 0) || PostItemHasVideo(item) != wantVideo {
			t.Fatalf("media detection differs from normalized gallery: %v", raw)
		}
	}
}
