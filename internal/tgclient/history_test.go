package tgclient

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestPublicHistoryRejectsForeignAndProtectedMessages(t *testing.T) {
	p := &tg.PeerChannel{ChannelID: 123}
	msgs := []tg.MessageClass{
		&tg.Message{ID: 1, PeerID: p, Message: "text"},
		&tg.Message{ID: 2, PeerID: p, Noforwards: true, Message: "protected"},
		&tg.Message{ID: 3, PeerID: &tg.PeerChannel{ChannelID: 999}, Message: "foreign"},
		&tg.Message{ID: 4, PeerID: p, Media: &tg.MessageMediaPhoto{}, GroupedID: 99},
	}
	got := publicHistoryMessages(msgs, 123)
	if len(got) != 2 || got[0].ID != 4 || got[0].GroupID != 99 || got[0].Kind != "photo" || got[1].Text != "text" {
		t.Fatalf("unexpected history: %+v", got)
	}
}
