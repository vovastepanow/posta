package web

import (
	"testing"

	"posta/internal/jx"
)

func TestTelegramPoolVideoAutoEligible(t *testing.T) {
	base := jx.M{"image": "https://example.org/poster.jpg", "source_url": "https://t.me/example/123", "video_unavailable": true}
	if !telegramPoolVideoAutoEligible(base, true) {
		t.Fatal("connected account must automatically start legacy Telegram video")
	}
	if telegramPoolVideoAutoEligible(base, false) {
		t.Fatal("disconnected account must not start a download")
	}
	for _, change := range []jx.M{
		{"video_unavailable": false},
		{"video": "/media/done.mp4"},
		{"video_error": "failed"},
		{"video_auto_started": true},
		{"source_url": "https://example.org/video"},
	} {
		item := jx.Copy(base)
		for k, v := range change {
			item[k] = v
		}
		if telegramPoolVideoAutoEligible(item, true) {
			t.Fatalf("unexpected automatic retry: %v", change)
		}
	}
}
