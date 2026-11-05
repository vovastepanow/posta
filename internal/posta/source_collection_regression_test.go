package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestRutubeSearchResultsArePlayableVideoCandidates(t *testing.T) {
	posts, err := RutubeParse(readFixture(t, "search_rutube.json"))
	if err != nil || len(posts) == 0 {
		t.Fatalf("parse: %v, %d posts", err, len(posts))
	}
	for _, p := range posts {
		if jx.Str(p, "video_via") != "ytdlp" || VideoPageVia(jx.Str(p, "video_page")) != "ytdlp" || !PostItemHasVideo(p) {
			t.Fatalf("Rutube video not connected to worker: %v", p)
		}
	}
}

func TestVKVideoAttachmentsAndPreviews(t *testing.T) {
	video := jx.M{"id": int64(42), "owner_id": int64(-123), "image": []jx.M{
		{"url": "https://sun9.userapi.com/small.jpg", "width": 100, "height": 80},
		{"url": "https://sun9.userapi.com/large.jpg", "width": 800, "height": 600},
	}}
	post := jx.M{"id": 55, "owner_id": -123, "text": "Публичный ролик", "attachments": []jx.M{{"type": "video", "video": video}}}
	p := VKWallPost(post, "VK", "club123", false, 120)
	if p == nil || jx.Str(p, "image") != "https://sun9.userapi.com/large.jpg" || jx.Str(p, "video_page") != "https://vk.com/video-123_42" || !PostItemHasVideo(p) {
		t.Fatalf("video-only post lost: %v", p)
	}
	if VideoPageVia(jx.Str(p, "video_page")) != "ytdlp" {
		t.Fatalf("VK page is not accepted by worker: %v", p)
	}
	delete(video, "image")
	p = VKWallPost(post, "VK", "club123", false, 120)
	if p == nil || jx.Str(p, "video_page") == "" || jx.Str(p, "image") != "" {
		t.Fatalf("missing cover should be eligible for metadata lookup: %v", p)
	}
	video["is_private"] = 1
	if p := VKWallPost(post, "VK", "club123", false, 120); p != nil {
		t.Fatalf("private video must not be treated as public: %v", p)
	}
}

func TestVideoPageViaOnlyAcceptsKnownVideoURLs(t *testing.T) {
	for _, page := range []string{"https://rutube.ru/video/4ceb9757e7eec396856b7c7b08b7565a/", "https://vk.com/video-123_42", "https://vkvideo.ru/video123_456"} {
		if VideoPageVia(page) != "ytdlp" {
			t.Errorf("valid video rejected: %s", page)
		}
	}
	for _, page := range []string{"https://vk.com.evil.example/video-123_42", "https://rutube.ru/video/not-a-video/", "http://vk.com/video-123_42", "https://vk.com/video-123_42?x=1"} {
		if VideoPageVia(page) != "" {
			t.Errorf("unexpected video page accepted: %s", page)
		}
	}
}

func TestImageOnlyQueryResultsHaveBoundedFallback(t *testing.T) {
	var posts []jx.M
	for i := 0; i < 30; i++ {
		posts = append(posts, jx.M{"source_key": "src:" + string(rune('A'+i)), "image": "https://example.com/" + string(rune('A'+i)) + ".jpg", "url": "https://example.com/post/" + string(rune('A'+i)), "posted_at": now()})
	}
	posts = append(posts, jx.M{"source_key": "src:irrelevant", "text": "Новости Томска", "image": "irrelevant.jpg", "url": "https://example.com/unrelated", "posted_at": now()})
	got := rankQuerySearch(map[string][]jx.M{PlatformTelegram: posts}, queryPlanFallback("Тюмень"), 30, noSkip)[PlatformTelegram]
	if len(got) != 20 {
		t.Fatalf("unverified image fallback must be bounded, got %d", len(got))
	}
	for _, p := range got {
		if jx.Str(p, "source_key") == "src:irrelevant" {
			t.Fatal("irrelevant text should not become image-only fallback")
		}
	}
}

func TestThematicRSSSeedIsAdditiveAndRespectsRegistry(t *testing.T) {
	first := thematicRSSSeeds[0]
	existing := []jx.M{{"id": first.URL, "name": "Мой источник", "topics": []string{"Моя тема"}}}
	blocked := map[string]bool{SourceIDFor(PlatformRSS, thematicRSSSeeds[1].URL): true}
	merged, added := mergeThematicRSS(existing, blocked)
	if jx.Str(merged[0], "name") != "Мой источник" || len(jx.Strs(merged[0], "topics")) != 1 || jx.Strs(merged[0], "topics")[0] != "Моя тема" {
		t.Fatal("existing source overwritten")
	}
	if len(added) != len(thematicRSSSeeds)-2 {
		t.Fatalf("expected existing and blocked sources skipped, got %v", added)
	}
	again, addedAgain := mergeThematicRSS(merged, blocked)
	if len(addedAgain) != 0 || len(again) != len(merged) {
		t.Fatal("seed must be idempotent")
	}
}

func TestFederatedSearchKeepsTextPostsAndArticleThumbnails(t *testing.T) {
	lemmy := []byte(`{"posts":[{"post":{"name":"Новости о криптовалютах", "ap_id":"https://lemmy.world/post/1", "url":"https://example.com/article", "thumbnail_url":"https://example.com/cover.jpg?size=small", "body":"Подробности о событии и рынке", "published":"2026-11-01T12:00:00Z", "community_id":1},"community":{"actor_id":"https://lemmy.world/c/news"}},{"post":{"name":"Текстовая заметка о бизнесе", "ap_id":"https://lemmy.world/post/2", "published":"2026-11-01T12:00:00Z", "community_id":2},"community":{"actor_id":"https://lemmy.world/c/business"}}]}`)
	posts, err := LemmyParse(lemmy)
	if err != nil || len(posts) != 2 {
		t.Fatalf("Lemmy posts lost: %v %v", posts, err)
	}
	if jx.Str(posts[0], "image") != "https://example.com/cover.jpg?size=small" || !jx.Bool(posts[1], "text_post") {
		t.Fatalf("wrong Lemmy media or text: %v", posts)
	}
	mastodon := []byte(`[{"content":"<p>Содержательный текстовый пост о бизнесе и новых технологиях</p>","created_at":"2026-11-01T12:00:00Z","url":"https://mastodon.social/@user/1","account":{"id":"1"},"media_attachments":[]}]`)
	mp, err := MastodonParse(mastodon)
	if err != nil || len(mp) != 1 || !jx.Bool(mp[0], "text_post") {
		t.Fatalf("Mastodon text lost: %v %v", mp, err)
	}
}

func TestSourcePostURLForNewPlatforms(t *testing.T) {
	cases := []struct{ key, source, post string }{
		{"rutube:1", "https://rutube.ru/video/4ceb9757e7eec396856b7c7b08b7565a/", ""},
		{"9gag:1", "https://9gag.com/gag/example", ""},
		{"joyreactor:user", "https://joyreactor.cc/post/123", ""},
		{"mastodon:1", "https://instance.example/@user/123", ""},
		{"lemmy:1", "https://instance.example/post/123", ""},
		{"vk:club123", "https://vk.com/club123", "https://vk.com/wall-123_456"},
	}
	for _, tc := range cases {
		item := jx.M{"source_key": tc.key, "source_url": tc.source, "url": tc.post}
		want := tc.source
		if tc.post != "" {
			want = tc.post
		}
		if got := SourcePostURL(item); got != want {
			t.Errorf("%s: got %q want %q", tc.key, got, want)
		}
	}
	for _, link := range []string{"http://instance.example/post", "https://127.0.0.1/post", "https://device.local/post", "https://user:pass@instance.example/post"} {
		if got := SourcePostURL(jx.M{"source_key": "mastodon:1", "source_url": link}); got != "" {
			t.Errorf("unsafe federated URL accepted: %s", link)
		}
	}
}
