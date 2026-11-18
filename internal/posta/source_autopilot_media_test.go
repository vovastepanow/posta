package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestSourcePostsHaveImages(t *testing.T) {
	cases := []struct {
		name  string
		posts []jx.M
		want  bool
	}{
		{name: "empty feed", posts: nil, want: false},
		{name: "text only", posts: []jx.M{{"text": "useful text"}, {"text": "another post", "image": ""}}, want: false},
		{name: "media", posts: []jx.M{{"text": "first"}, {"image": "https://example.org/photo.jpg"}}, want: true},
		{name: "blank media ref", posts: []jx.M{{"image": "  "}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourcePostsHaveImages(tc.posts); got != tc.want {
				t.Fatalf("sourcePostsHaveImages() = %v, want %v", got, tc.want)
			}
		})
	}
}
