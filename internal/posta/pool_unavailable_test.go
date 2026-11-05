package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestUnavailablePoolVideo(t *testing.T) {
	tests := []struct {
		item jx.M
		want bool
	}{
		{jx.M{"video_via": "ytdlp", "video_unavailable": true}, true},
		{jx.M{"video_via": "ytdlp", "video_unavailable": false}, false},
		{jx.M{"video_via": "tgu", "video_unavailable": true}, false},
		{jx.M{"image": "photo"}, false},
	}
	for _, tc := range tests {
		if got := hasUnavailablePoolVideo([]jx.M{tc.item}); got != tc.want {
			t.Errorf("item %v: got %v, want %v", tc.item, got, tc.want)
		}
	}
}
