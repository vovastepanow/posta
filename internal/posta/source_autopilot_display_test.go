package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAutopilotSourceDisplaySeparatesPlatformAndResource(t *testing.T) {
	for _, tc := range []struct {
		platform, id, label, placeholder, want string
	}{
		{PlatformRSS, "https://mastodon.social/@alice.rss", "Mastodon", "Mastodon", "@alice"},
		{PlatformRSS, "https://lemmy.today/feeds/c/marketing.xml", "Lemmy", "Lemmy", "c/marketing"},
		{PlatformRSS, "https://www.reddit.com/r/marketing/.rss", "Reddit", "Reddit", "r/marketing"},
		{PlatformRSS, "https://bsky.app/profile/alice.bsky.social/rss", "Bluesky", "Bluesky", "profile/alice.bsky.social"},
		{PlatformRSS, "https://alice.tumblr.com/rss", "Tumblr", "Tumblr", "alice"},
		{PlatformRSS, "https://habr.com/ru/rss/hubs/marketing/articles/all/", "Хабр", "Хабр", "ru/hubs/marketing"},
		{PlatformTelegram, "example", PlatformTelegram, PlatformTelegram, "@example"},
		{PlatformRSS, "https://example.org/blog/feed", "example.org", "example.org", "blog"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if got := autopilotSourcePlatformLabel(tc.platform, tc.id); got != tc.label {
				t.Errorf("platform = %q, want %q", got, tc.label)
			}
			if got := autopilotDisplayName(tc.platform, tc.id, tc.placeholder); got != tc.want {
				t.Errorf("fallback = %q, want %q", got, tc.want)
			}
			if got := autopilotDisplayName(tc.platform, tc.id, tc.placeholder, "Настоящее название"); got != "Настоящее название" {
				t.Errorf("title = %q", got)
			}
		})
	}
}

func TestAutopilotMastodonPublisherDisplayName(t *testing.T) {
	feed := "https://mastodon.social/@alice.rss"
	data := []byte(`[{"content":"A public post with enough text for parsing.","created_at":"2026-11-01T12:00:00Z","url":"https://mastodon.social/@alice/123","visibility":"public","sensitive":false,"account":{"id":"1","url":"https://mastodon.social/@alice","display_name":"Alice Marketing"}}]`)
	posts, err := autopilotMastodonSafeStatuses(data, feed)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts = %v, err = %v", posts, err)
	}
	info := autopilotSourceReviewInfo(PlatformRSS, feed, feed, FetchResultURL{}, posts)
	if got := jx.Str(autopilotSourceDisplayEvidence(info, PlatformRSS, feed), "source_title"); got != "Alice Marketing" {
		t.Fatalf("source title = %q", got)
	}
}
