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

func TestHistoryPhotoSizeBudget(t *testing.T) {
	p := &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 1000},
		&tg.PhotoSizeProgressive{Type: "m", W: 800, H: 600, Sizes: []int{4000, 8000}},
		&tg.PhotoSize{Type: "x", W: 1600, H: 1200, Size: 90000},
	}}
	if got := bestHistoryPhotoSize(p, 10000); got.kind != "m" || got.size != 8000 {
		t.Fatalf("size = %+v", got)
	}
	b := &historyPhotoBuffer{limit: 3}
	if _, err := b.Write([]byte("abcd")); err == nil || b.Len() != 0 {
		t.Fatal("oversized file accepted")
	}
}
