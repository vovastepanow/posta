package posta

import (
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
			"url":        "https://t.me/marketing_digest/" + string(rune('a'+i)),
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

func TestAutopilotMergePostHintsPrioritizesFreshIndependentPosts(t *testing.T) {
	at := time.Now().Unix()
	shared := jx.M{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/10", "posted_at": at, "text": "Общий пост"}
	original := []jx.M{
		{"url": "https://t.me/marketing_digest/20", "posted_at": at, "text": "Лента один"},
		{"url": "https://t.me/marketing_digest/21", "posted_at": at, "text": "Лента два"},
		shared,
	}
	hinted := []jx.M{
		{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/30", "posted_at": at, "text": "Новый самостоятельный пост"},
		shared,
	}
	merged := autopilotMergePostHints("telegram:marketing_digest", original, hinted, at)
	if len(merged) != 4 {
		t.Fatalf("merged %d posts, expected four unique items: %+v", len(merged), merged)
	}
	if jx.Str(merged[0], "url") != "https://t.me/marketing_digest/30" {
		t.Fatalf("staged post did not reach the front: %+v", merged)
	}
	if jx.Str(merged[2], "url") != "https://t.me/marketing_digest/20" {
		t.Fatalf("original feed order changed: %+v", merged)
	}
}
