package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAttachmentRecoveryUsesEachVideoAndPreservesOrder(t *testing.T) {
	post := jx.M{"media_incomplete": true, "video_unavailable": true, "video_page": "https://t.me/channel/1", "source_attachments": []jx.M{
		{"kind": "photo", "image": "https://example.org/photo.jpg"},
		{"kind": "video", "video_page": "https://t.me/channel/2"},
		{"kind": "video", "video_page": "https://t.me/channel/3"},
	}}
	existing := func(s string) string { return s }
	poster := func(s string) string { return s+".jpg" }
	resolve := func(s string) string {
		switch s {
		case "https://t.me/channel/2": return "/media/two.mp4"
		case "https://t.me/channel/3": return "/media/three.mp4"
		}
		return ""
	}
	ready := restoreAttachmentInventory(post, resolve, existing, poster)
	g := ContentGallery(ready)
	if jx.Bool(ready,"media_incomplete") || len(g) != 3 || jx.Str(g[0],"image") != "https://example.org/photo.jpg" ||
		jx.Str(g[1],"video") != "/media/two.mp4" || jx.Str(g[2],"video") != "/media/three.mp4" ||
		!PostItemHasVideo(ready) || !jx.Bool(ready,"attachment_recovered") {
		t.Fatalf("invalid restored album: %v",ready)
	}
	if !jx.Bool(post,"media_incomplete") || jx.Str(jx.Maps(post,"source_attachments")[1],"video") != "" {
		t.Fatalf("shared observation mutated: %v",post)
	}
}

func TestAttachmentRecoveryDoesNotInventMissingMedia(t *testing.T) {
	for _, post := range []jx.M{
		{"media_incomplete": true,"attachment_inventory_incomplete":true,"source_attachments":[]jx.M{{"kind":"video","video_page":"https://t.me/channel/2"}}},
		{"media_incomplete": true,"source_attachments":[]jx.M{{"kind":"video","video_page":"https://t.me/channel/2"},{"kind":"photo"}}},
		{"media_incomplete": true,"source_attachments":[]jx.M{{"kind":"video","image":"https://example.org/cover.jpg"}}},
	} {
		ready := restoreAttachmentInventory(post,func(string)string{return ""},func(s string)string{return s},func(string)string{return ""})
		if !jx.Bool(ready,"media_incomplete") {
			t.Fatal("incomplete record accepted",ready)
		}
	}
}
