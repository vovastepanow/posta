package posta

import (
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotCandidatesFromPosts(t *testing.T) {
	at := time.Now().Unix()
	posts := map[string][]jx.M{
		PlatformTelegram: {
			{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/1", "posted_at": at, "text": "Анализ кампании"},
			{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/2", "posted_at": at, "text": "Ещё разбор"},
			{"source_url": "https://t.me/reklama_digest", "url": "https://t.me/reklama_digest/1", "posted_at": at, "advertising": true},
			{"source_url": "https://t.me/old_digest", "url": "https://t.me/old_digest/1", "posted_at": at - 400*86400},
			{"source_url": "https://t.me/private_digest", "url": "https://t.me/private_digest/1", "posted_at": at, "nsfw": true},
			{"source_url": "https://t.me/future_digest", "url": "https://t.me/future_digest/1", "posted_at": at + 900},
			{"source_url": "https://t.me/growth_digest", "url": "https://t.me/growth_digest/1", "posted_at": at},
			{"source_url": "https://t.me/private_link", "url": "https://t.me/private_link/1?token=secret", "posted_at": at},
			{"source_url": "https://t.me/missing_post", "posted_at": at},
		},
	}
	got := autopilotCandidatesFromPosts(posts, at, 10)
	if len(got) != 2 {
		t.Fatalf("candidates = %d, want two unique safe recent authors: %+v", len(got), got)
	}
	for _, candidate := range got {
		if _, ok := autopilotCandidateID(candidate); !ok {
			t.Fatalf("invalid source bypassed validator: %+v", candidate)
		}
	}
	if len(autopilotCandidatesFromPosts(posts, at, 1)) != 1 {
		t.Fatal("post candidate limit was not enforced")
	}
	if len(autopilotCandidatesFromPosts(posts, at, 0)) != 0 {
		t.Fatal("zero limit must return no sources")
	}
}

func TestAutopilotCandidatesFromPostsBalancesPlatforms(t *testing.T) {
	at := time.Now().Unix()
	posts := map[string][]jx.M{
		"a": {
			{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/1", "posted_at": at},
			{"source_url": "https://t.me/growth_digest", "url": "https://t.me/growth_digest/1", "posted_at": at},
		},
		"b": {
			{"source_url": "https://vk.com/club12345", "url": "https://vk.com/wall-12345_42", "posted_at": at},
		},
	}
	got := autopilotCandidatesFromPosts(posts, at, 2)
	if len(got) != 2 || got[0].Platform == got[1].Platform {
		t.Fatalf("expected two independent source platforms before consuming a second author from one: %+v", got)
	}
}

func TestAutopilotPublicPostQueryPlan(t *testing.T) {
	ru := autopilotPublicPostQueryPlan("маркетинг")
	if len(ru.RU) != 1 || ru.RU[0] != "маркетинг" {
		t.Fatalf("VK and vc/dtf query lost: %+v", ru)
	}
	if len(ru.Hashtags) != 1 || ru.Hashtags[0] != "#маркетинг" {
		t.Fatalf("Mastodon tag missing: %+v", ru)
	}
	if len(ru.EN) != 0 {
		t.Fatalf("Cyrillic-only phrase sent to English Lemmy search: %+v", ru)
	}
	en := autopilotPublicPostQueryPlan("social media marketing")
	if len(en.EN) != 1 || en.EN[0] != "social media marketing" ||
		len(en.Hashtags) != 1 || en.Hashtags[0] != "#socialmediamarketing" {
		t.Fatalf("English term not routed to Lemmy/Mastodon: %+v", en)
	}
}
