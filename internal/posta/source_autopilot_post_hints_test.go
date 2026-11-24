package posta

import (
	"strconv"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotSafePostHintsRequiresVerifiedPublicAuthor(t *testing.T) {
	at := time.Now().Unix()
	source := "telegram:marketing_digest"
	posts := []jx.M{
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/1", "posted_at": at, "text": "Разбор кампании с измеримыми результатами"},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/1", "posted_at": at, "text": "Дубликат"},
		{"source_url": "https://t.me/another_digest", "url": "https://t.me/another_digest/1", "posted_at": at, "text": "Чужой автор"},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/2?token=secret", "posted_at": at, "text": "Закрытая ссылка"},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/3", "posted_at": at - 4*86400, "text": "Старая публикация"},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/4", "posted_at": at, "text": "Рекламная запись", "advertising": true},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/5", "posted_at": at, "text": "Публикация с изображением", "image": "https://example.org/image.jpg"},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/6", "posted_at": at, "text": "Приватный пост", "nsfw": true},
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/7", "posted_at": at, "text": "Подробная аналитика и цифры"},
	}
	hints := autopilotSafePostHints(source, posts, at)
	if len(hints) != 2 {
		t.Fatalf("accepted %d, expected two safe, nonduplicate text posts: %+v", len(hints), hints)
	}
	for _, post := range hints {
		author := PostSearchAuthor(post)
		if author == nil {
			t.Fatalf("post without verifiable author: %+v", post)
		}
		if id, ok := autopilotCandidateID(*author); !ok || id != source {
			t.Fatalf("source mismatch: %s, %v", id, ok)
		}
	}
}

func TestAutopilotSafePostHintsBounded(t *testing.T) {
	at := time.Now().Unix()
	var posts []jx.M
	for i := 0; i < 20; i++ {
		posts = append(posts, jx.M{
			"source_url": "https://t.me/marketing_digest",
			"url":        "https://t.me/marketing_digest/" + strconv.Itoa(i+1),
			"posted_at":  at,
			"text":       "Полезная публикация",
		})
	}
	if got := len(autopilotSafePostHints("telegram:marketing_digest", posts, at)); got != autopilotPostHintLimit {
		t.Fatalf("staged %d posts, limit %d", got, autopilotPostHintLimit)
	}
	if autopilotPostHintKey("topic-a", "telegram:a") == autopilotPostHintKey("topic-b", "telegram:a") {
		t.Fatal("post hints leaked across topics")
	}
}

func TestAutopilotPostMatchesSourceRejectsImpersonation(t *testing.T) {
	cases := []struct {
		source string
		link   string
		author string
		want   bool
	}{
		{"telegram:marketing_digest", "https://t.me/marketing_digest/1", "https://t.me/marketing_digest", true},
		{"telegram:marketing_digest", "https://evil.example.org/post/1", "https://t.me/marketing_digest", false},
		{"telegram:marketing_digest", "https://t.me/other_channel/1", "https://t.me/marketing_digest", false},
		{"telegram:marketing_digest", "https://t.me/marketing_digest", "https://t.me/marketing_digest", false},
		{"telegram:marketing_digest", "https://t.me/marketing_digest/not_a_post", "https://t.me/marketing_digest", false},
		{"telegram:marketing_digest", "https://t.me/marketing_digest/0", "https://t.me/marketing_digest", false},
		{"telegram:marketing_digest", "https://t.me/s/Marketing_Digest/123", "https://t.me/marketing_digest", true},
		{"vk:club12345", "https://vk.com/wall-12345_8", "https://vk.com/club12345", true},
		{"vk:club12345", "https://vk.com/wall-99999_8", "https://vk.com/club12345", false},
		{"vk:club12345", "https://other.example.org/post", "https://vk.com/club12345", false},
	}
	for _, tc := range cases {
		post := jx.M{"url": tc.link, "source_url": tc.author}
		if got := autopilotPostMatchesSource(tc.source, post); got != tc.want {
			t.Errorf("source=%s post=%s: got %t, want %t", tc.source, tc.link, got, tc.want)
		}
	}
}

func TestAutopilotOsnovaAndMastodonRequireExactPublisher(t *testing.T) {
	source := "rss:https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123"
	post := jx.M{
		"source_author_url": osnovaTimelineURL("vc.ru", 199123),
		"publisher_id":      199123,
		"publisher_uri":     "/life",
		"publisher_post_id": 3168963,
		"url":               "https://vc.ru/life/3168963-article",
	}
	if !autopilotPostMatchesSource(source, post) {
		t.Fatal("valid official API article was rejected")
	}
	for _, raw := range []string{"https://vc.ru/marketing/3168963-article", "https://vc.ru/life/111-other", "https://vc.ru.evil.example/life/3168963-article"} {
		forged := jx.Copy(post)
		forged["url"] = raw
		if autopilotPostMatchesSource(source, forged) {
			t.Fatalf("different publisher or article accepted: %s", raw)
		}
	}
	mastodon := jx.M{"source_key": "mastodon:123", "source_author_url": "https://mastodon.social/@example", "url": "https://mastodon.social/@example/456"}
	if !autopilotPostMatchesSource("rss:https://mastodon.social/@example.rss", mastodon) {
		t.Fatal("valid Mastodon post rejected")
	}
	mastodon["url"] = "https://mastodon.social/@other/456"
	if autopilotPostMatchesSource("rss:https://mastodon.social/@example.rss", mastodon) {
		t.Fatal("same-instance impersonation accepted")
	}
}
