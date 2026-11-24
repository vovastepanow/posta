package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestAutopilotRSSBodyCanUseTextAdmissionWithoutCover(t *testing.T) {
	body := strings.Repeat("An article explains a practical social media reporting method. ", 30)
	raw := `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><title>Title</title><link>https://example.com/story</link><content:encoded><![CDATA[<p>` + body + `</p><img src="https://example.com/cover.jpg">]]></content:encoded></item></channel></rss>`
	posts, err := RSSParse([]byte(raw), 1, "", "https://example.com/feed", true, true, 0)
	if err != nil || len(posts) != 1 {
		t.Fatalf("parse: %v %+v", err, posts)
	}
	post := posts[0]
	if !jx.Bool(post, "rss_article_body") || ContentImageKey(post) == "" {
		t.Fatalf("missing body provenance or cover: %+v", post)
	}
	alternative := autopilotTextArticleAlternative("rss:https://example.com/feed", post, true)
	if alternative == nil || PostItemHasMedia(alternative) || !jx.Bool(alternative, "text_post") ||
		CandidatePostKey(alternative) != CandidatePostKey(post) || CandidateTextKey(alternative) == "" ||
		AnalysisText(alternative) != AnalysisText(post) || ContentImageKey(post) == "" {
		t.Fatalf("text alternative changed identity, leaked cover, or mutated input: %+v", alternative)
	}
	for _, tc := range []struct {
		source string
		post   jx.M
		allow  bool
	}{
		{"rss:https://example.com/feed", post, false},
		{"rss:https://wrong.example/feed", post, true},
		{"telegram:example", post, true},
		{"rss:https://example.com/feed", jx.Merge(post, jx.M{"rss_article_body": false}), true},
		{"rss:https://example.com/feed", jx.Merge(post, jx.M{"video": "https://example.com/video.mp4"}), true},
		{"rss:https://example.com/feed", jx.Merge(post, jx.M{"source_sensitive": true}), true},
	} {
		if got := autopilotTextArticleAlternative(tc.source, tc.post, tc.allow); got != nil {
			t.Fatalf("unsafe alternative: %+v", got)
		}
	}
}
