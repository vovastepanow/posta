package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestProposalAlbumRefreshRejectsMissingOrReorderedMembers(t *testing.T) {
	old := jx.M{"image": "a", "gallery": []jx.M{{"image": "a"}, {"image": "b", "video": "v"}, {"image": "c"}}}
	short := jx.M{"gallery": []jx.M{{"image": "a"}, {"image": "b", "video": "v"}}}
	reordered := jx.M{"gallery": []jx.M{{"image": "a"}, {"image": "c"}, {"image": "b", "video": "v"}}}
	if proposalGalleryCompatible(old, short) || proposalGalleryCompatible(old, reordered) {
		t.Fatal("incomplete or reordered album accepted")
	}
	if got := proposalApply(old, short); len(ContentGallery(got)) != 3 {
		t.Fatal("saved album was shortened")
	}
	if !proposalGalleryCompatible(old, old) {
		t.Fatal("same album rejected")
	}
}
