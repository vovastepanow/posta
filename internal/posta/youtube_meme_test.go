package posta

import "testing"

func TestMemeYouTubeAllowed(t *testing.T) {
	for _, tc := range []struct {
		duration, title string
		want            bool
	}{
		{"15", "Смешной кот", true}, {"20", "мем", true}, {"20.1", "мем", false},
		{"0", "мем", false}, {"NA", "мем", false}, {"12", "Подборка мемов", false},
		{"10", "Meme compilation", false}, {"10", "приколы", true},
	} {
		if got := memeYouTubeAllowed(tc.duration, tc.title); got != tc.want {
			t.Errorf("%q %q: got %v", tc.duration, tc.title, got)
		}
	}
}
