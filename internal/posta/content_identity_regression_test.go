package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAnalysisCacheUsesActualContent(t *testing.T) {
	const topic = "SMM"
	cases := []struct {
		name string
		a, b jx.M
	}{
		{"short text", jx.M{"text": "SEO"}, jx.M{"text": "SMM"}},
		{"video without poster", jx.M{"video": "first.mp4"}, jx.M{"video": "second.mp4"}},
		{"shared poster", jx.M{"image": "same.jpg", "video": "first.mp4"}, jx.M{"image": "same.jpg", "video": "second.mp4"}},
		{"edited caption", jx.M{"image": "same.jpg", "text": "cats"}, jx.M{"image": "same.jpg", "text": "marketing"}},
		{"edited article", jx.M{"image": "same.jpg", "article": jx.M{"title": "cats"}}, jx.M{"image": "same.jpg", "article": jx.M{"title": "marketing"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if AnalysisCacheKey(tc.a, topic) == AnalysisCacheKey(tc.b, topic) {
				t.Fatal("distinct content shared its analysis")
			}
			if AnalysisCacheKey(tc.a, topic) != AnalysisCacheKey(jx.Clone(tc.a), topic) {
				t.Fatal("JSON round trip changed the key")
			}
		})
	}
}

func TestQuerySearchKeepsDistinctVideosAndShortPosts(t *testing.T) {
	items := []jx.M{
		{"source_key": "telegram:a", "image": "same.jpg", "video": "first.mp4", "text": "SMM", "posted_at": now()},
		{"source_key": "telegram:b", "image": "same.jpg", "video": "second.mp4", "text": "SMM", "posted_at": now()},
		{"source_key": "telegram:c", "text": "SMM tips", "text_post": true, "posted_at": now()},
		{"source_key": "telegram:d", "text": "SMM tools", "text_post": true, "posted_at": now()},
	}
	out := rankQuerySearch(map[string][]jx.M{PlatformTelegram: items}, QueryPlan{Must: []string{"smm"}}, 30, noSkip)
	if len(out[PlatformTelegram]) != len(items) {
		t.Fatalf("distinct records collapsed: %v", out)
	}
}

func TestQueryScoresRejectMalformedBatch(t *testing.T) {
	if !validQueryScores([]any{float64(0), float64(100)}, 2) {
		t.Fatal("valid scores rejected")
	}
	for _, scores := range [][]any{
		{float64(10)}, {nil, float64(90)}, {"n/a", float64(90)}, {true, float64(90)}, {float64(-1), float64(90)}, {float64(101), float64(90)},
	} {
		if validQueryScores(scores, 2) {
			t.Fatalf("invalid batch accepted: %v", scores)
		}
	}
}
