package posta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestAutopilotDeliveryRespectsRequiredMedia(t *testing.T) {
	u := taxonomyTestUser()
	topic := "Неизвестная тема с обязательным медиа"
	if autopilotFormatAllowsItem(u, topic, jx.M{"text": "Текст"}) {
		t.Fatal("text bypassed required media")
	}
	if !autopilotFormatAllowsItem(u, topic, jx.M{"image": "/media/test.jpg"}) {
		t.Fatal("media rejected by format check")
	}
}

func TestAutopilotDeliversContentPerOwnerPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
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
	const topic, feed = "Проверка текстовой доставки", "https://www.reddit.com/r/postaautopilottest/.rss"
	key, source := autopilotTopicKey(topic), "rss:"+feed
	for _, table := range []string{"source_autopilot_content_results", "source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", ""))
	a := &App{DB: d, LLM: router, Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
		var keep []jx.M
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") != feed {
				keep = append(keep, entry)
			}
		}
		c.RSS = keep
		return true
	})
	owners := []string{"autopilot-delivery-a", "autopilot-delivery-b", "autopilot-delivery-c"}
	for _, owner := range owners {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
		u := a.User(ctx, owner, nil)
		if err = u.SaveSettings(jx.M{"topic": "Другая тема", "active_sources_only": false, "topic_formats": jx.M{TopicNorm(topic): jx.M{"media": "optional"}}}); err != nil {
			t.Fatal(err)
		}
		if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 3, HorizonDays: 2}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	second := a.User(ctx, owners[1], nil)
	if err = second.SaveSettings(jx.M{"topic": topic}); err != nil {
		t.Fatal(err)
	}
	if err = second.SourceOverrideSet(source, false); err != nil {
		t.Fatal(err)
	}
	if err = second.SaveSettings(jx.M{"topic": "Другая тема"}); err != nil {
		t.Fatal(err)
	}
	if err = a.User(ctx, owners[2], nil).SourceAutopilotPause(topic, true); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v %v", job, err)
	}
	job.MaxProbation = 3
	at := time.Now()
	var post jx.M
	analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test", "description": "Содержательный текст", "why_relevant": "Материал соответствует теме", "tags": []any{}}
	if err = a.autopilotShadowSearchWith(ctx, job, func(context.Context) ([]DiscoveryCandidate, error) {
		return []DiscoveryCandidate{{Platform: PlatformRSS, ID: feed}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var items []string
	for i := 0; i < 3; i++ {
		items = append(items, fmt.Sprintf(`{"data":{"title":"Post %d","selftext":%q,"permalink":"/r/postaautopilottest/comments/%d/post/","subreddit":"postaautopilottest","created_utc":%d,"over_18":false}}`, i, strings.Repeat("Public post body with enough text. ", 5), i, at.Unix()))
	}
	items = append(items, fmt.Sprintf(`{"data":{"title":"Sensitive","selftext":%q,"permalink":"/r/postaautopilottest/comments/hidden/post/","subreddit":"postaautopilottest","created_utc":%d,"over_18":true}}`, strings.Repeat("Private safety test. ", 10), at.Unix()))
	feedBytes := []byte(`{"data":{"children":[` + strings.Join(items, ",") + `]}}`)
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	if err = a.autopilotReviewShadowCandidatesWith(ctx, job, 3, 85, func(string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: feedBytes}, URL: "https://www.reddit.com/r/postaautopilottest/new.json?limit=25"}
	}, func(jx.M, []jx.M, string) *sourceEvaluation { return &sourceEvaluation{Score: 90} }, func(ctx context.Context, gotSource string, samples []jx.M) error {
		if gotSource != source || len(samples) != 3 {
			t.Fatalf("unexpected assessment input: %s %d", gotSource, len(samples))
		}
		post = samples[0]
		return a.saveAutopilotContentResult(ctx, job, gotSource, post, analysis)
	}, policy); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotProjections(ctx, job); err != nil {
		t.Fatal(err)
	}
	excludedCtx := withAutopilotDelivery(contextWithAutopilotLease(ctx, job), owners[1], source, topic, post, analysis)
	if _, err = a.User(excludedCtx, owners[1], nil).poolUpdate(func(_ pgx.Tx, pool []jx.M) ([]jx.M, error) {
		t.Error("excluded owner's pool callback ran")
		return pool, nil
	}); !errors.Is(err, errAutopilotSourceOverride) {
		t.Fatalf("excluded owner bypassed transaction guard: %v", err)
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	for i, owner := range owners {
		var count int
		if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1`, owner).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 0 {
			want = 1
		}
		if count != want {
			t.Fatalf("owner %s received %d posts, want %d", owner, count, want)
		}
		var audit jx.M
		found := a.CacheGet(ctx, autopilotDeliveryAuditCacheKey(owner, key, false), &audit)
		if i == 2 && found {
			t.Fatal("paused owner unexpectedly has a delivery attempt")
		}
		if i == 0 && (!found || jx.Int(jx.Map(audit, "counters"), "pool_not_added") == 0) {
			t.Fatalf("repeat delivery not diagnosed: %v", audit)
		}
		if i == 1 && (!found || jx.Int(jx.Map(audit, "counters"), "source_excluded") == 0) {
			t.Fatalf("personal exclusion not diagnosed: %v", audit)
		}
		available, supplyErr := a.User(ctx, owner, nil).AutopilotAvailableSupply(topic)
		if supplyErr != nil || available != want {
			t.Fatalf("owner %s supply=%d want=%d err=%v", owner, available, want, supplyErr)
		}
		if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_admissions WHERE user_id=$1 AND topic_key=$2 AND accepted`, owner, key).Scan(&count); err != nil || count != want {
			t.Fatalf("owner %s admission count=%d want=%d err=%v", owner, count, want, err)
		}
	}
	if err = transitionAutopilotSource(ctx, d, job, source, "probation", "active", "test", []byte(`{}`), at, at); err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err = png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pngBytes.Bytes())
	verifiedLocal, err := a.cacheAutopilotMedia(ctx, job, source, hex.EncodeToString(sum[:]), pngBytes.Bytes(), false)
	if err != nil || a.MediaExisting(verifiedLocal) == "" {
		t.Fatalf("verified image cache: %q %v", verifiedLocal, err)
	}
	if _, err = a.cacheAutopilotMedia(ctx, job, source, "wrong", pngBytes.Bytes(), false); err == nil {
		t.Fatal("incorrect media hash accepted")
	}
	verifiedPost := jx.M{"url": "https://example.org/news/cached", "image": "https://example.org/cached.png", "image_local": verifiedLocal, "repeat_file_hashes": []any{hex.EncodeToString(sum[:])}, "posted_at": at.Unix()}
	if err = a.saveAutopilotContentResult(ctx, job, source, verifiedPost, analysis); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `UPDATE source_autopilot_content_results SET assessed_at=$4 WHERE scope=$1 AND topic_key=$2 AND post_hash=$3`, job.Scope, key, repeatDigest(CandidatePostKey(verifiedPost)), at.Add(-48*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if handled, err := a.refreshAutopilotMedia(contextWithAutopilotLease(ctx, job), job, source, repeatDigest(CandidatePostKey(verifiedPost)), verifiedPost, false); err != nil || !handled {
		t.Fatalf("existing immutable cache was not reused: %v %v", handled, err)
	}
	if _, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_content_results WHERE scope=$1 AND topic_key=$2 AND post_hash=$3`, job.Scope, key, repeatDigest(CandidatePostKey(verifiedPost))); err != nil {
		t.Fatal(err)
	}
	if err = a.projectAutopilotSource(ctx, job, source, "active"); err != nil {
		t.Fatal(err)
	}
	imageLocal, err := a.MediaWrite(MediaCacheName("test-image", "jpg"), []byte("verified image placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	imagePost := jx.M{"url": "https://example.org/news/delivery-image", "image": "https://example.org/image.jpg", "image_local": imageLocal, "text": "Подпись к проверенному изображению", "posted_at": at.Unix()}
	if err = a.saveAutopilotContentResult(ctx, job, source, imagePost, analysis); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	for i, owner := range owners {
		var count int
		if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1`, owner).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 0 {
			want = 2
		}
		if count != want {
			t.Fatalf("owner %s received %d posts after image, want %d", owner, count, want)
		}
	}
	const videoOwner = "autopilot-delivery-video"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, videoOwner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, videoOwner)
	if err = a.User(ctx, videoOwner, nil).SaveSettings(jx.M{"topic": "Другая тема", "active_sources_only": false, "video_only": true}); err != nil {
		t.Fatal(err)
	}
	if err = saveAutopilotDemand(ctx, d.Pool, videoOwner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 3, HorizonDays: 2}, at.Unix()); err != nil {
		t.Fatal(err)
	}
	videoLocal, err := a.MediaWrite(MediaCacheName("test-video", "mp4"), []byte("verified video placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	videoPost := jx.M{"url": "https://example.org/news/delivery-video", "image": imageLocal, "image_local": imageLocal, "video": "https://example.org/clip.mp4", "video_local": videoLocal, "text": "Подпись к проверенному ролику", "posted_at": at.Unix()}
	if err = a.saveAutopilotContentResult(ctx, job, source, videoPost, analysis); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	var videoCount int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1 AND data->>'video'=$2`, videoOwner, videoLocal).Scan(&videoCount); err != nil || videoCount != 1 {
		t.Fatalf("video-only subscriber did not receive verified video: %d %v", videoCount, err)
	}
	staleVideo := jx.M{"url": "https://example.org/news/delivery-stale", "video": "https://example.org/stale.mp4", "posted_at": at.Unix()}
	if err = a.saveAutopilotContentResult(ctx, job, source, staleVideo, analysis); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `UPDATE source_autopilot_content_results SET assessed_at=$4 WHERE scope=$1 AND topic_key=$2 AND post_hash=$3`, job.Scope, key, repeatDigest(CandidatePostKey(staleVideo)), at.Add(-48*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1`, videoOwner).Scan(&videoCount); err != nil || videoCount != 1 {
		t.Fatalf("uncached video delivered: %d %v", videoCount, err)
	}
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		var keep []jx.M
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") != feed {
				keep = append(keep, entry)
			}
		}
		c.RSS = keep
		return true
	}); err != nil {
		t.Fatal(err)
	}
	removedCtx := withAutopilotDelivery(contextWithAutopilotLease(ctx, job), owners[0], source, topic, post, analysis)
	if _, err = a.User(removedCtx, owners[0], nil).poolUpdate(func(_ pgx.Tx, pool []jx.M) ([]jx.M, error) {
		t.Error("removed source pool callback ran")
		return pool, nil
	}); !errors.Is(err, errAutopilotSourceOverride) {
		t.Fatalf("removed source bypassed transaction guard: %v", err)
	}
	if err = a.projectAutopilotSource(ctx, job, source, "active"); err != nil {
		t.Fatal(err)
	}
	if err = a.User(ctx, owners[0], nil).SourceAutopilotPause(topic, true); err != nil {
		t.Fatal(err)
	}
	pausedCtx := withAutopilotDelivery(contextWithAutopilotLease(ctx, job), owners[0], source, topic, post, analysis)
	if _, err = a.User(pausedCtx, owners[0], nil).poolUpdate(func(_ pgx.Tx, pool []jx.M) ([]jx.M, error) {
		t.Error("paused owner's pool callback ran")
		return pool, nil
	}); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("paused owner bypassed transaction guard: %v", err)
	}
	post["url"] = "https://example.org/news/delivery-2"
	if err = a.saveAutopilotContentResult(ctx, job, source, post, analysis); err != nil {
		t.Fatal(err)
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1`, owners[0]).Scan(&count); err != nil || count != 2 {
		t.Fatalf("paused owner received more posts: %d %v", count, err)
	}
	if _, err = setAutopilotJobEnabled(ctx, d.Pool, autopilotAutoScope, key, autopilotAutoKind, false); err != nil {
		t.Fatal(err)
	}
	if _, err = a.cacheAutopilotMedia(ctx, job, source, hex.EncodeToString(sum[:]), pngBytes.Bytes(), false); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("revoked worker wrote media: %v", err)
	}
}
