package posta

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestAutopilotDirectPostRejectsImpersonationAndMedia(t *testing.T) {
	u := taxonomyTestUser()
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", ""))
	now := time.Now().Unix()
	analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test", "description": "Полезный разбор", "why_relevant": "Тема", "tags": []any{}}
	base := jx.M{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/100", "posted_at": now, "text": "Практический подробный анализ результатов маркетингового эксперимента."}
	if len(autopilotSafePostHints("telegram:marketing_digest", []jx.M{base}, now)) != 1 {
		t.Fatal("safe public text could not enter independent candidate staging")
	}
	for name, patch := range map[string]jx.M{
		"forged author": {"source_url": "https://t.me/other_digest"},
		"forged url":    {"url": "https://evil.example.org/post"},
		"private url":   {"url": "https://t.me/marketing_digest/100?token=secret"},
		"stale post":    {"posted_at": now - 4*86400},
		"image":         {"image": "https://example.org/image.jpg"},
		"video":         {"video": "https://example.org/video.mp4"},
		"nsfw":          {"nsfw": true},
	} {
		t.Run(name, func(t *testing.T) {
			post := jx.Merge(base, patch)
			if autopilotDirectPostEligible(u, "telegram:marketing_digest", "Маркетинг и реклама", post, analysis, now) {
				t.Fatalf("unsafe item passed: %v", post)
			}
		})
	}
	bad := jx.Merge(analysis, jx.M{"advertising": true})
	if autopilotDirectPostEligible(u, "telegram:marketing_digest", "Маркетинг и реклама", base, bad, now) {
		t.Fatal("advertising analysis passed")
	}
}

func TestAutopilotDirectDeliversWithoutApprovedSourcePostgres(t *testing.T) {
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
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const topic = "Проверка прямой доставки публикаций"
	const source = "telegram:marketing_digest"
	key := autopilotTopicKey(topic)
	owners := []string{"autopilot-direct-accepted", "autopilot-direct-blocked", "autopilot-direct-paused"}
	for _, table := range []string{"source_autopilot_direct_results", "source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	defer func() {
		for _, owner := range owners {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_demands WHERE user_id=$1`, owner)
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
		}
	}()
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", ""))
	a := &App{DB: d, LLM: router, Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, owner := range owners {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
			t.Fatal(err)
		}
		u := a.User(ctx, owner, nil)
		if err := u.SaveSettings(jx.M{"topic": "Другая тема", "active_sources_only": false, "topic_formats": jx.M{TopicNorm(topic): jx.M{"media": "optional"}}}); err != nil {
			t.Fatal(err)
		}
		if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 3, HorizonDays: 2}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	blocked := a.User(ctx, owners[1], nil)
	if err := blocked.SaveSettings(jx.M{"topic": topic}); err != nil {
		t.Fatal(err)
	}
	if err := blocked.SourceOverrideSet(source, false); err != nil {
		t.Fatal(err)
	}
	if err := blocked.SaveSettings(jx.M{"topic": "Другая тема"}); err != nil {
		t.Fatal(err)
	}
	if err := a.User(ctx, owners[2], nil).SourceAutopilotPause(topic, true); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	post := jx.M{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/1337", "posted_at": time.Now().Unix(), "text": "Подробное объяснение решения, конкретные результаты эксперимента и этапы работы."}
	analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test", "description": "Содержательная публикация", "why_relevant": "Соответствует теме", "tags": []any{}}
	if err := a.saveAutopilotDirectResult(ctx, job, source, post, analysis); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_direct_results WHERE topic_key=$1`, key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("direct assessment missing: count=%d err=%v", count, err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE topic_key=$1`, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("direct assessment unexpectedly approved source: count=%d err=%v", count, err)
	}
	if err := a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	for i, owner := range owners {
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1`, owner).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 0 {
			want = 1
		}
		if count != want {
			t.Fatalf("owner %s got %d direct posts, want %d", owner, count, want)
		}
	}
	// A stored assessment cannot be used after the worker lease is revoked.
	if _, err := setAutopilotJobEnabled(ctx, d.Pool, autopilotAutoScope, key, autopilotAutoKind, false); err != nil {
		t.Fatal(err)
	}
	guarded := withAutopilotDirectDelivery(contextWithAutopilotLease(ctx, job), owners[0], source, topic, post, analysis)
	if _, err := a.User(guarded, owners[0], nil).poolUpdate(func(_ pgx.Tx, pool []jx.M) ([]jx.M, error) {
		t.Error("revoked direct worker was allowed to mutate pool")
		return pool, nil
	}); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("revoked direct delivery bypassed fencing: %v", err)
	}
}
