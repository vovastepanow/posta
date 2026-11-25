package posta

import (
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
