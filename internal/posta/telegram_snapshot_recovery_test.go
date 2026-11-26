package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestTelegramHistoryRepairsOnlyMatchingOrderedSnapshot(t *testing.T) {
	web := jx.M{"url":"https://t.me/channel/1","media_incomplete":true,"text":"Old caption","source_attachments":[]jx.M{
		{"kind":"photo","url":"https://t.me/channel/1"},
		{"kind":"video","url":"https://t.me/channel/2"},
	}}
	native := jx.M{"url":"https://t.me/channel/1","fetched_via":"telegram_mtproto","text":"New caption","media_incomplete":true,"source_attachments":[]jx.M{
		{"kind":"photo","url":"https://t.me/channel/1","image_local":"/media/photo.jpg"},
		{"kind":"video","url":"https://t.me/channel/2","video_page":"https://t.me/channel/2"},
	}}
	if telegramRepairChannel(web)!="channel" {t.Fatal("missing photo not recoverable")}
	repaired:=repairTelegramSnapshot(web,[]jx.M{native})
	if jx.Str(repaired,"text")!="New caption" || jx.Str(repaired,"fetched_via")!="telegram_mtproto" {t.Fatalf("native snapshot not used: %v",repaired)}
	jx.Maps(native,"source_attachments")[1]["url"]="https://t.me/channel/3"
	if jx.Str(repairTelegramSnapshot(web,[]jx.M{native}),"text")!="Old caption" {t.Fatal("different album was substituted")}
}
