package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestRSSRejectsTruncatedDocuments(t *testing.T) {
	for _, raw := range []string{
		`<rss><channel><item><title>One</title></item>`,
		`<feed><entry><id>one</id></entry><entry`,
		`<rss><channel/></rss><rss><channel/></rss>`,
	} {
		if rssIsFeed(rssLoad([]byte(raw))) {
			t.Fatalf("invalid feed accepted: %s", raw)
		}
	}
	if !rssIsFeed(rssLoad([]byte(`<rss><channel/></rss>`))) {
		t.Fatal("valid empty feed rejected")
	}
}

func TestRSSPreservesMixedInlineContent(t *testing.T) {
	raw := []byte(`<rss><channel><item><title>Title</title><description>Before <b>important</b> after.</description><link>https://example.com/1</link></item></channel></rss>`)
	posts, err := RSSParse(raw, 10, "", "https://example.com/feed", true, true, 0)
	if err != nil || len(posts) != 1 || !strings.Contains(jx.Str(posts[0], "text"), "Before important after.") {
		t.Fatalf("inline body lost: %v %v", posts, err)
	}
}

func TestIncompletePublicPostsNotClassified(t *testing.T) {
	for _, key := range []string{"text_incomplete", "media_incomplete", "video_unavailable"} {
		if _, _, ok := publicClassificationInput(jx.M{"text": "Enough text to classify", key: true}); ok {
			t.Errorf("incomplete post passed: %s", key)
		}
	}
}
