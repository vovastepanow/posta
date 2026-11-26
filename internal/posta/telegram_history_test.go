package posta

import (
	"testing"

	"posta/internal/jx"
	"posta/internal/tgclient"
)

func TestNativeTelegramHistoryPreservesAlbum(t *testing.T) {
	h := tgclient.PublicHistory{Username: "publicchannel", Title: "Channel", Messages: []tgclient.PublicHistoryMessage{
		{ID: 13, GroupID: 8, Kind: "video", Text: "Second caption", Date: 100},
		{ID: 12, GroupID: 8, Kind: "photo", Text: "First caption", Date: 100},
		{ID: 11, Kind: "text", Text: "Full\ntext", Date: 90},
		{ID: 10, Kind: "video", Date: 80},
	}}
	p := normalizeTelegramHistory(h, "", 10)
	if len(p) != 3 || !jx.Bool(p[0], "media_incomplete") || jx.Bool(p[0], "text_post") || jx.Str(p[0], "text") != "First caption\n\nSecond caption" {
		t.Fatalf("album lost: %v", p)
	}
	g := jx.Maps(p[0], "source_attachments")
	if len(g) != 2 || jx.Int(g[0], "message_id") != 12 || jx.Int(g[1], "message_id") != 13 {
		t.Fatalf("album order lost: %v", g)
	}
	if !jx.Bool(p[1], "text_post") || jx.Str(p[1], "text") != "Full\ntext" || jx.Str(p[1], "fetched_via") != "telegram_mtproto" {
		t.Fatalf("text post lost: %v", p[1])
	}
	if jx.Str(p[2], "video_page") != "https://t.me/publicchannel/10" || jx.Str(p[2], "video_via") != "tgu" {
		t.Fatalf("single video not queued for existing preview path: %v", p[2])
	}
}
