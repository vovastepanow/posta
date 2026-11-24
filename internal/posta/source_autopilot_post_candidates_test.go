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

func TestAutopilotDirectCandidatesReserveSlotsForAssessablePosts(t *testing.T) {
	at := time.Now().Unix()
	posts := map[string][]jx.M{
		PlatformTelegram: {
			{"source_url": "https://t.me/video_digest", "url": "https://t.me/video_digest/1", "posted_at": at, "video": "https://example.org/1.mp4", "text": "Video caption"},
			{"source_url": "https://t.me/image_digest", "url": "https://t.me/image_digest/1", "posted_at": at, "image": "https://example.org/1.jpg", "text": "Image caption"},
			{"source_url": "https://t.me/old_digest", "url": "https://t.me/old_digest/1", "posted_at": at - 3*86400, "text": "Older post"},
			{"source_url": "https://t.me/empty_digest", "url": "https://t.me/empty_digest/1", "posted_at": at},
			{"source_url": "https://t.me/first_digest", "url": "https://t.me/first_digest/1", "posted_at": at, "text": "Detailed comparison of camera lenses"},
			{"source_url": "https://t.me/first_digest", "url": "https://t.me/first_digest/1", "posted_at": at, "text": "Detailed comparison of camera lenses"},
			{"source_url": "https://t.me/first_digest", "url": "https://t.me/first_digest/2", "posted_at": at, "text": "Another tutorial about exposure"},
			{"source_url": "https://t.me/second_digest", "url": "https://t.me/second_digest/1", "posted_at": at, "text": "Guide to landscape photography"},
			{"source_url": "https://t.me/third_digest", "url": "https://t.me/third_digest/1", "posted_at": at, "text": "Practice with street photography"},
		},
	}
	got, bySource := autopilotAssessablePostCandidates(posts, at, 2, 0)
	if len(got) != 2 || got[0].ID != "first_digest" || got[1].ID != "second_digest" {
		t.Fatalf("unassessable posts consumed candidate slots: %+v", got)
	}
	if len(bySource["telegram:first_digest"]) != 2 {
		t.Fatalf("duplicate or missing staged hints: %+v", bySource)
	}
	got, _ = autopilotAssessablePostCandidates(posts, at, 2, 1)
	if len(got) != 2 || got[0].ID != "second_digest" || got[1].ID != "third_digest" {
		t.Fatalf("later authors lost to unchanged search order: %+v", got)
	}
}

func TestAutopilotPostSearchNominationsDoNotBypassDirectAssessment(t *testing.T) {
	at := time.Now().Unix()
	posts := map[string][]jx.M{
		PlatformTelegram: {
			{"source_url": "https://t.me/media_digest", "url": "https://t.me/media_digest/1", "posted_at": at, "image": "https://example.org/1.jpg", "text": "Social media marketing campaign"},
			{"source_url": "https://t.me/other_digest", "url": "https://t.me/other_digest/1", "posted_at": at, "image": "https://example.org/2.jpg", "text": "Travel photography"},
			{"source_url": "https://t.me/old_digest", "url": "https://t.me/old_digest/1", "posted_at": at - 400*86400, "text": "Social media marketing campaign"},
			{"source_url": "https://t.me/text_digest", "url": "https://t.me/text_digest/1", "posted_at": at, "text": "Social media marketing campaign"},
			{"source_url": "https://t.me/foreign_digest", "url": "https://t.me/media_digest/2", "posted_at": at, "image": "https://example.org/3.jpg", "text": "Social media marketing campaign"},
			{"source_url": "https://t.me/ad_digest", "url": "https://t.me/ad_digest/1", "posted_at": at, "advertising": true, "text": "Social media marketing campaign"},
		},
	}
	_, direct := autopilotAssessablePostCandidates(posts, at, 10, 0)
	got := autopilotPostSearchNominations(posts, "social media marketing", at, 8, direct)
	if len(got) != 1 || got[0].ID != "media_digest" {
		t.Fatalf("unexpected media nominations: %+v", got)
	}
	if len(autopilotPostSearchNominations(posts, "social media marketing", at, 0, direct)) != 0 {
		t.Fatal("nomination limit ignored")
	}
}
