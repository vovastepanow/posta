package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestCalendarMediaPreservesPostType(t *testing.T) {
	a := &App{}
	cases := []struct {
		name  string
		item  jx.M
		kinds []string
		fail  bool
	}{
		{"text", jx.M{"text_post": true, "text": "A text publication"}, nil, false},
		{"video", jx.M{"image": "https://example.org/poster.jpg", "video": "https://example.org/movie.mp4"}, []string{"video"}, false},
		{"pending video", jx.M{"image": "https://example.org/poster.jpg", "video_via": "tgu", "video_page": "https://t.me/test/1"}, nil, true},
		{"album", jx.M{"gallery": []jx.M{{"image": "https://example.org/1.jpg"}, {"image": "https://example.org/2.jpg", "video": "https://example.org/2.mp4"}}}, []string{"image", "video"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			media, err := a.calendarRecordMedia(tc.item)
			if (err != nil) != tc.fail || len(media) != len(tc.kinds) {
				t.Fatalf("media=%v err=%v", media, err)
			}
			for i, kind := range tc.kinds {
				if media[i].Kind != kind {
					t.Fatalf("media type changed: %v", media)
				}
			}
		})
	}
}

func TestCalendarFindTextRecord(t *testing.T) {
	first := jx.M{"text_post": true, "text": "SMM"}
	second := jx.M{"text_post": true, "text": "SEO"}
	h := History{"liked": []jx.M{first, second}}
	if got := calendarFillRecord(ContentItemKey(second), nil, h); jx.Str(got, "text") != "SEO" {
		t.Fatal("could not address text proposal")
	}
	if calendarFillRecord("", nil, h) != nil {
		t.Fatal("empty key selected an arbitrary text record")
	}
}
