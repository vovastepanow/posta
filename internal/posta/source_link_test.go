package posta

import (
	"strings"
	"testing"
)

func TestRSSWebURLIsReadableSourcePage(t *testing.T) {
	tests := []struct {
		feed, want string
	}{
		{"https://lemmy.today/feeds/c/womensmma.xml", "https://lemmy.today/c/womensmma"},
		{"https://lemmy.world/feeds/u/alice.xml", "https://lemmy.world/u/alice"},
		{"https://bsky.app/profile/did:plc:oky5czdrnfjpqslsw2a5iclo/rss", "https://bsky.app/profile/did:plc:oky5czdrnfjpqslsw2a5iclo"},
		{"https://mastodon.social/@cats.rss", "https://mastodon.social/@cats"},
		{"https://www.reddit.com/r/cats/.rss", "https://www.reddit.com/r/cats/"},
		{"https://www.reddit.com/user/alice/.rss", "https://www.reddit.com/user/alice/"},
		{"https://cats.tumblr.com/rss", "https://cats.tumblr.com/"},
		{"https://cats.substack.com/feed", "https://cats.substack.com/"},
		{"https://habr.com/ru/rss/hubs/python/articles/all/?fl=ru", "https://habr.com/ru/hubs/python/"},
		{"https://www.pinterest.com/alice/feed.rss", "https://www.pinterest.com/alice/"},
		{"https://www.pinterest.com/alice/paintings.rss", "https://www.pinterest.com/alice/paintings/"},
		{"https://medium.com/feed/@alice", "https://medium.com/@alice"},
		{"https://example.org/blog/feed.xml", "https://example.org/blog/"},
		{"https://example.org/unusual-rss?token=something", "https://example.org/"},
		{"https://www.youtube.com/feeds/videos.xml?channel_id=UC1234567890123456789012", "https://www.youtube.com/channel/UC1234567890123456789012/videos"},
		{"https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH1234567890123456789012", "https://www.youtube.com/channel/UC1234567890123456789012/shorts"},
	}
	for _, tt := range tests {
		t.Run(tt.feed, func(t *testing.T) {
			got := RSSWebURL(tt.feed)
			if got != tt.want {
				t.Fatalf("RSSWebURL(%s) = %q, want %q", tt.feed, got, tt.want)
			}
			if strings.Contains(got, "/feeds/c/") || strings.Contains(got, ".rss") ||
				strings.HasSuffix(got, "/rss") || strings.HasSuffix(got, ".xml") {
				t.Fatalf("technical feed was displayed as a clickable source: %s", got)
			}
			if linked := SourceWebURL(PlatformRSS, tt.feed); linked != tt.want {
				t.Fatalf("source card link = %q, want %q", linked, tt.want)
			}
		})
	}
}

func TestRSSWebURLRejectsUntrustedAndInvalidFeedURLs(t *testing.T) {
	for _, url := range []string{
		"javascript:alert(1)", "file:///etc/passwd",
		"https://user:secret@lemmy.today/feeds/c/cats.xml",
		"https://", "", "/feeds/c/cats.xml",
	} {
		if got := RSSWebURL(url); got != "" {
			t.Errorf("unsafe feed %q produced clickable URL %q", url, got)
		}
	}
}

func TestSourceWebURLPreservesOtherSocialLinks(t *testing.T) {
	if got := SourceWebURL(PlatformTelegram, "sample_channel"); got != "https://t.me/sample_channel" {
		t.Fatalf("Telegram channel lost: %q", got)
	}
	if got := SourceWebURL(PlatformOK, "1234567890"); got != "https://ok.ru/group/1234567890" {
		t.Fatalf("OK group lost: %q", got)
	}
}
