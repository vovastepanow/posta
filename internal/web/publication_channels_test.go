package web

import (
	"testing"

	"posta/internal/jx"
	"posta/internal/posta"
)

func TestPublicationChannelSnapshot(t *testing.T) {
	snapshot := newPublicationChannels([]*posta.Channel{
		{ID: "vk", Platform: "vk", ChatTitle: "VK"},
		nil,
		{ID: "tg1", Platform: "telegram", ChatTitle: "Main", Token: "token"},
		{ID: "tg2", Platform: "telegram", ChatTitle: "Other"},
	})
	if !snapshot.connected() || len(snapshot.telegram) != 2 {
		t.Fatal("wrong Telegram channel list")
	}
	for id, want := range map[string]string{"": "tg1", "vk": "vk", "tg2": "tg2", "missing": ""} {
		if got := jx.Str(snapshot.fields(id), "channel"); got != want {
			t.Fatalf("channel %q: got %q want %q", id, got, want)
		}
	}
	if newPublicationChannels(nil).connected() {
		t.Fatal("empty channel list reports connected")
	}
}
