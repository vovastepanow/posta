package posta

import (
	"testing"
	"time"
)

func TestAutopilotTelegramIndexCandidates(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":[
		{"type":"channel","username":"MarketingTips","telegram_url":"https://t.me/MarketingTips","title":"Marketing tips"},
		{"type":"channel","username":"marketingtips","telegram_url":"https://t.me/marketingtips"},
		{"type":"group","username":"SpamGroup","telegram_url":"https://t.me/SpamGroup"},
		{"type":"channel","username":"OtherChannel","telegram_url":"https://t.me/DifferentChannel"},
		{"type":"channel","username":"ForeignChannel","telegram_url":"https://t.me.example/ForeignChannel"},
		{"type":"channel","username":"MarketingBot","telegram_url":"https://t.me/MarketingBot"},
		{"type":"channel","username":"PrivateChannel","telegram_url":"https://t.me/+PrivateChannel"}
	]}}}`)
	got, err := autopilotTelegramIndexCandidates(data)
	if err != nil || len(got) != 1 || got[0].ID != "marketingtips" {
		t.Fatalf("unexpected candidates: %+v, %v", got, err)
	}
	if _, err := autopilotTelegramIndexCandidates([]byte(`{"jsonrpc":"2.0","result":{"isError":true,"structuredContent":{"results":[]}}}`)); err == nil {
		t.Fatal("provider error was interpreted as empty results")
	}
	if _, err := autopilotTelegramIndexCandidates([]byte(`{"jsonrpc":"2.0","result":{"content":[]}}`)); err == nil {
		t.Fatal("unknown response was interpreted as empty results")
	}
}


func TestAutopilotTelegramIndexRanksActiveBeforeStale(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":[
		{"type":"channel","username":"stale_channel","telegram_url":"https://t.me/stale_channel","title":"Astronomy","last_post_at":"2022-01-01T00:00:00","posts_30d":0},
		{"type":"channel","username":"unknown_channel","telegram_url":"https://t.me/unknown_channel","title":"Astronomy"},
		{"type":"channel","username":"active_channel","telegram_url":"https://t.me/active_channel","title":"Astronomy","last_post_at":"2026-11-22T10:00:00","posts_30d":5}
	]}}}`)
	got,err:=autopilotTelegramIndexRankedCandidates(data,"astronomy",time.Date(2026,11,24,0,0,0,0,time.UTC))
	if err!=nil || len(got)!=3 || got[0].ID!="active_channel" || got[1].ID!="unknown_channel" || got[2].ID!="stale_channel" {
		t.Fatalf("unexpected ranking: %+v %v",got,err)
	}
}
