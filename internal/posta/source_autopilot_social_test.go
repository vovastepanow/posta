package posta

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestAutopilotSocialIdentitiesAndPages(t *testing.T) {
	for _, tc := range []struct{ platform, id, want string }{
		{PlatformTelegram, "ExampleChannel", "telegram:examplechannel"},
		{PlatformVK, "PublicExample", "vk:publicexample"},
		{PlatformOK, "1234567890123", "ok:1234567890123"},
	} {
		got, ok := autopilotCandidateID(DiscoveryCandidate{Platform: tc.platform, ID: tc.id})
		if !ok || got != tc.want {
			t.Fatalf("candidate=%q %v", got, ok)
		}
		platform, id, _, ok := autopilotSourceParts(got)
		if !ok || platform != tc.platform || SourceIDFor(platform, id) != got {
			t.Fatalf("source parts: %s %s %v", platform, id, ok)
		}
	}
	if _, ok := autopilotCandidateID(DiscoveryCandidate{Platform: PlatformTelegram, ID: "privatebot"}); ok {
		t.Fatal("bot accepted")
	}
	if _, err := autopilotPublicSocialPosts(PlatformTelegram, "examplechannel", FetchResultURL{URL: "https://evil.test/s/examplechannel", FetchResult: FetchResult{Bytes: []byte("tgme_widget_message")}}); err == nil {
		t.Fatal("foreign redirect accepted")
	}
	if _, err := autopilotPublicSocialPosts(PlatformVK, "publicexample", FetchResultURL{URL: "https://vk.com/login", FetchResult: FetchResult{Bytes: []byte("wall_post")}}); err == nil {
		t.Fatal("login redirect accepted")
	}
	date := time.Now().UTC().Format(time.RFC3339)
	tg := fmt.Sprintf(`<div class="tgme_widget_message" data-post="examplechannel/123"><div class="tgme_widget_message_text">Публичный материал для проверки.</div><time datetime="%s"></time></div>`, date)
	posts, err := autopilotPublicSocialPosts(PlatformTelegram, "examplechannel", FetchResultURL{URL: "https://t.me/s/examplechannel", FetchResult: FetchResult{Bytes: []byte(tg)}})
	if err != nil || len(posts) != 1 || jx.Int(posts[0], "posted_at") == 0 {
		t.Fatalf("Telegram parse: %v %v", posts, err)
	}
	vk := fmt.Sprintf(`<div id="post-12_34"><div class="wall_post_text">Публичный материал для проверки.</div><time datetime="%s"></time></div>`, date)
	posts, err = autopilotPublicSocialPosts(PlatformVK, "publicexample", FetchResultURL{URL: "https://vk.com/publicexample", FetchResult: FetchResult{Bytes: []byte(vk)}})
	if err != nil || len(posts) != 1 || jx.Int(posts[0], "posted_at") == 0 {
		t.Fatalf("VK parse: %v %v", posts, err)
	}
	okPage := `<li class="item it feed-card" data-json="topicId&quot;:&quot;123"><div class="topic-text_content">Текст материала<div class="widget-list"><span class="tstmp feed_descr">вчера 12:00</span><img class="image_inner_img" src="https://i.okcdn.ru/test.jpg"></li>`
	posts, err = autopilotPublicSocialPosts(PlatformOK, "1234567890123", FetchResultURL{URL: okGroupURL("1234567890123"), FetchResult: FetchResult{Bytes: []byte(okPage)}})
	if err != nil || len(posts) != 1 || jx.Int(posts[0], "posted_at") == 0 {
		t.Fatalf("OK parse: %v %v", posts, err)
	}
	if _, err = autopilotPublicSocialPosts(PlatformOK, "1234567890123", FetchResultURL{URL: okGroupURL("1234567890124"), FetchResult: FetchResult{Bytes: []byte(okPage)}}); err == nil {
		t.Fatal("wrong OK group accepted")
	}
}

func TestAutopilotSocialProjectionAndPersonalControlPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const topic, owner = "Социальная тестовая тема", "autopilot-social-test"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", ""))
	a := &App{DB: d, LLM: router, Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	u := a.User(ctx, owner, nil)
	if err = u.SaveSettings(jx.M{"topic": topic, "active_sources_only": false, "topic_formats": jx.M{TopicNorm(topic): jx.M{"media": "optional"}}}); err != nil {
		t.Fatal(err)
	}
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	key := autopilotTopicKey(topic)
	for _, table := range []string{"source_autopilot_content_results", "source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	defer a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
		for _, kind := range []string{"telegram_channels", "vk_publics", "ok_groups", "tiktok_accounts"} {
			list := c.List(kind)
			var keep []jx.M
			for _, e := range *list {
				if !strings.HasPrefix(jx.Str(e, "id"), "autopilottest") && jx.Str(e, "id") != "1234567890123" {
					keep = append(keep, e)
				}
			}
			*list = keep
		}
		return true
	})
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%v %v", job, err)
	}
	job.MaxProbation = 4
	for _, tc := range []struct{ platform, id string }{{PlatformTelegram, "autopilottesttg"}, {PlatformVK, "autopilottestvk"}, {PlatformOK, "1234567890123"}, {PlatformTikTok, "autopilottesttt"}} {
		source, ok := autopilotCandidateID(DiscoveryCandidate{Platform: tc.platform, ID: tc.id})
		if !ok {
			t.Fatal(tc)
		}
		if err = a.autopilotShadowSearchWith(ctx, job, func(context.Context) ([]DiscoveryCandidate, error) {
			return []DiscoveryCandidate{{Platform: tc.platform, ID: tc.id}}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if tc.platform == PlatformTelegram {
			hint := jx.M{
				"source_url": "https://t.me/" + tc.id,
				"url": "https://t.me/" + tc.id + "/999",
				"posted_at": time.Now().Unix(),
				"text": "Самостоятельная публикация, найденная по тематическому поисковому запросу.",
			}
			if err := a.autopilotStagePublicPosts(ctx, job, source, []jx.M{hint}); err != nil {
				t.Fatal(err)
			}
			defer a.KVDelete(context.Background(), autopilotPostHintKey(key, source))
			if len(a.autopilotPendingPublicPosts(ctx, job, source)) != 1 {
				t.Fatal("independent search hint was not staged")
			}
		}
		text := strings.Repeat("Публичные новости про котов и уход за домашними животными. ", 4)
		if tc.platform == PlatformVK {
			text = strings.Repeat("Научные исследования орбит космических аппаратов и планет. ", 4)
		} else if tc.platform == PlatformOK {
			text = strings.Repeat("Архитектура старинных городов и история строительства зданий. ", 4)
		}
		analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test", "description": "Текст", "why_relevant": "По теме", "tags": []any{}}
		var page strings.Builder
		date := time.Now().UTC().Format(time.RFC3339)
		for i := 1; i <= 3; i++ {
			if tc.platform == PlatformTelegram {
				fmt.Fprintf(&page, `<div class="tgme_widget_message" data-post="%s/%d"><div class="tgme_widget_message_text">%s %d</div><time datetime="%s"></time></div>`, tc.id, i, text, i, date)
			} else if tc.platform == PlatformVK {
				fmt.Fprintf(&page, `<div id="post-12_%d"><div class="wall_post_text">%s %d</div><time datetime="%s"></time></div>`, i, text, i, date)
			} else if tc.platform == PlatformOK {
				fmt.Fprintf(&page, `<li class="item it feed-card" data-json="topicId&quot;:&quot;%d"><div class="topic-text_content">%s %d<div class="widget-list"><span class="tstmp feed_descr">вчера 12:00</span><img class="image_inner_img" src="https://i.okcdn.ru/test%d.jpg"></li>`, i, text, i, i)
			}
		}
		if tc.platform == PlatformTikTok {
			var posts []jx.M
			for i := 0; i < 3; i++ {
				id := fmt.Sprintf("671833539084509517%d", i)
				data := strings.ReplaceAll(string(tiktokTestPage("https://v16-webapp.tiktok.com/video?token=test")), "6718335390845095173", id)
				data = strings.ReplaceAll(data, "scout2015", tc.id)
				post, _, err := parseAutopilotTikTokPage([]byte(data), tc.id, id)
				if err != nil {
					t.Fatal(err)
				}
				post["posted_at"] = time.Now().Unix()
				posts = append(posts, post)
			}
			page.Write(jx.Marshal(posts))
		}
		pageURL := "https://t.me/s/" + tc.id
		if tc.platform == PlatformVK {
			pageURL = "https://vk.com/" + tc.id
		} else if tc.platform == PlatformOK {
			pageURL = okGroupURL(tc.id)
		} else if tc.platform == PlatformTikTok {
			pageURL = "https://www.tiktok.com/@" + tc.id
		}
		policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
		if err = a.autopilotReviewShadowCandidatesWith(ctx, job, 3, 85, func(string) FetchResultURL {
			return FetchResultURL{URL: pageURL, FetchResult: FetchResult{OK: true, Bytes: []byte(page.String())}}
		}, func(jx.M, []jx.M, string) *sourceEvaluation { return &sourceEvaluation{Score: 90} }, func(ctx context.Context, got string, posts []jx.M) error {
			if got != source || len(posts) != 3 {
				t.Fatalf("assessment source=%s posts=%d", got, len(posts))
			}
			if tc.platform == PlatformOK {
				local, err := a.MediaWrite(MediaCacheName("ok-test-image", "jpg"), []byte("test image"))
				if err != nil {
					return err
				}
				posts[0]["image_local"] = local
			} else if tc.platform == PlatformTikTok {
				image, err := a.MediaWrite(MediaCacheName("tiktok-test-image", "jpg"), []byte("test image"))
				if err != nil {
					return err
				}
				video, err := a.MediaWrite(MediaCacheName("tiktok-test-video", "mp4"), []byte("test video"))
				if err != nil {
					return err
				}
				posts[0]["image"], posts[0]["image_local"], posts[0]["video"], posts[0]["video_local"] = image, image, video, video
			}
			return a.saveAutopilotContentResult(ctx, job, source, posts[0], analysis)
		}, policy); err != nil {
			t.Fatal(err)
		}
		if tc.platform == PlatformTelegram && len(a.autopilotPendingPublicPosts(ctx, job, source)) != 1 {
			t.Fatal("feed assessment consumed independent search hints")
		}
		if err = a.reconcileAutopilotProjections(ctx, job); err != nil {
			t.Fatal(err)
		}
		if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
			t.Fatal(err)
		}
		var count int
		if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1 AND data->>'source_key'=$2`, owner, source).Scan(&count); err != nil || count != 1 {
			t.Fatalf("delivery %s: %d %v", source, count, err)
		}
		if err = u.SourcePersonalPin(source, true); err != nil {
			t.Fatalf("pin %s: %v", source, err)
		}
		if err = u.SourceVote("reject", jx.M{"platform": tc.platform, "id": tc.id, "source_id": source, "name": tc.id}, "other"); err != nil {
			t.Fatal(err)
		}
		if !sourceExcludedByOverride(u.sourceOverridesForTopic(topic), source) {
			t.Fatalf("personal rejection missing: %s", source)
		}
		if err = u.SourcePersonalPin(source, false); err != nil {
			t.Fatal(err)
		}
		if err = transitionAutopilotSource(ctx, d, job, source, "probation", "active", "test", []byte(`{}`), time.Now(), time.Now().Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err = a.projectAutopilotSource(ctx, job, source, "active"); err != nil {
			t.Fatal(err)
		}
	}
}


func TestAutopilotTelegramPageDoesNotAttributeForeignPosts(t *testing.T) {
	date := time.Now().UTC().Format(time.RFC3339)
	page := fmt.Sprintf(`<div class="tgme_widget_message" data-post="ForeignChannel/123"><div class="tgme_widget_message_text">Foreign text</div><time datetime="%s"></time></div><div class="tgme_widget_message" data-post="ExampleChannel/123"><div class="tgme_widget_message_text">Own text</div><time datetime="%s"></time></div><div class="tgme_widget_message" data-post="ForeignChannel/456"><div class="tgme_widget_message_text">Another foreign text</div><time datetime="%s"></time></div>`,date,date,date)
	posts,err := autopilotPublicSocialPosts(PlatformTelegram,"examplechannel",FetchResultURL{URL:"https://t.me/s/examplechannel",FetchResult:FetchResult{Bytes:[]byte(page)}})
	if err!=nil || len(posts)!=1 || jx.Str(posts[0],"text")!="Own text" || jx.Str(posts[0],"url")!="https://t.me/examplechannel/123" {
		t.Fatalf("foreign or incorrectly attributed posts: %+v %v",posts,err)
	}
}

func TestAutopilotPublicSocialSearchRejectsUnknownTikTokPage(t *testing.T) {
	for _, tc := range []struct {
		url, body, want string
		code int
	}{
		{"https://search.brave.com/search", "<html>Consent required</html>", "unavailable", 200},
		{"https://search.brave.com/search", "<html>No results found for your query</html>", "empty", 200},
		{"https://www.bing.com/search", "https://www.tiktok.com/@cats", "ok", 200},
		{"https://www.bing.com/search", "https://www.tiktok.com/@cats", "unavailable", 206},
		{"https://search.brave.com/settings", "<a href=\"https://www.tiktok.com/@cats\">cats</a>", "unavailable", 200},
		{"https://www.bing.com/search", "<rss><channel><item><link>https://example.org/cats</link></item></channel></rss>", "unavailable", 200},
	} {
		r := FetchResultURL{URL: tc.url, FetchResult: FetchResult{OK: true, Status: tc.code, Bytes: []byte(tc.body)}}
		got, status := autopilotPublicSocialSearchCandidates(r, "cats", PlatformTikTok)
		if status != tc.want || (len(got) > 0) != (tc.want == "ok") {
			t.Fatalf("status=%s want=%s candidates=%+v body=%s", status, tc.want, got, tc.body)
		}
	}
}
