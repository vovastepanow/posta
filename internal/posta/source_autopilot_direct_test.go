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

func TestAutopilotInterleaveDirectPosts(t *testing.T) {
	candidates := []DiscoveryCandidate{
		{Platform: PlatformTelegram, ID: "marketing_digest"},
		{Platform: PlatformTelegram, ID: "growth_digest"},
		{Platform: PlatformTelegram, ID: "marketing_digest"},
	}
	pending := func(source string) []jx.M {
		if source == "telegram:marketing_digest" {
			return []jx.M{
				{"url": "https://t.me/marketing_digest/1"},
				{"url": "https://t.me/marketing_digest/2"},
				{"url": "https://t.me/marketing_digest/3"},
			}
		}
		return []jx.M{{"url": "https://t.me/growth_digest/1"}}
	}
	out := autopilotInterleaveDirectPosts(candidates, pending, 3)
	if len(out) != 3 {
		t.Fatalf("got %d candidates: %+v", len(out), out)
	}
	if out[0].source != "telegram:marketing_digest" ||
		out[1].source != "telegram:growth_digest" ||
		out[2].source != "telegram:marketing_digest" {
		t.Fatalf("prolific first source starved the second: %+v", out)
	}
	if len(autopilotInterleaveDirectPosts(candidates, pending, 0)) != 0 {
		t.Fatal("zero model budget should select no posts")
	}
}

func TestAutopilotDirectSeenKeyIsScoped(t *testing.T) {
	first := autopilotDirectSeenKey("business", "telegram:marketing_digest", "hash-1")
	if first != autopilotDirectSeenKey("business", "telegram:marketing_digest", "hash-1") {
		t.Fatal("assessment cache key unstable")
	}
	for _, variant := range []string{
		autopilotDirectSeenKey("marketing", "telegram:marketing_digest", "hash-1"),
		autopilotDirectSeenKey("business", "telegram:other_digest", "hash-1"),
		autopilotDirectSeenKey("business", "telegram:marketing_digest", "hash-2"),
	} {
		if first == variant {
			t.Fatal("independent assessments share a cache key")
		}
	}
}

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
	post := jx.M{"source_url": "https://t.me/marketing_digest", "url": "https://t.me/marketing_digest/1337", "posted_at": time.Now().Unix(), "text": "Подробное объяснение решения, конкретные результаты эксперимента и этапы работы. https://t.me/citation_digest/42 https://t.me/citation_blocked https://t.me/citation_digest"}
	defer d.Pool.Exec(context.Background(), `DELETE FROM public_posts WHERE source_key=$1 AND post_key=$2`, source, CandidatePostKey(post))
	analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test", "description": "Содержательная публикация", "why_relevant": "Соответствует теме", "tags": []any{}}
	defer a.KVDelete(context.Background(), autopilotPostHintKey(key, source))
	defer a.KVDelete(context.Background(), autopilotDirectSeenKey(key, source, repeatDigest(CandidatePostKey(post))))
	if err := a.autopilotStagePublicPosts(ctx, job, source, []jx.M{post}); err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	assess := func(texts []string) ([]Analysis, error) {
		modelCalls++
		if len(texts) != 1 || texts[0] != AnalysisText(post) {
			t.Fatalf("unexpected model batch: %v", texts)
		}
		return []Analysis{analysis}, nil
	}
	candidate := DiscoveryCandidate{Platform: PlatformTelegram, ID: "marketing_digest"}
	if err := a.autopilotAssessDirectPostsWith(ctx, job, []DiscoveryCandidate{candidate}, assess); err != nil {
		t.Fatal(err)
	}
	var indexed []byte
	if err := d.Pool.QueryRow(ctx, `SELECT data FROM public_posts WHERE source_key=$1 AND post_key=$2`, source, CandidatePostKey(post)).Scan(&indexed); err != nil {
		t.Fatalf("assessed public post missing from inventory: %v", err)
	}
	if raw := jx.Unmarshal(indexed); jx.Str(raw, "text") != jx.Str(post, "text") || raw["relevance"] != nil || raw["tags"] != nil {
		t.Fatalf("inventory contains profile verdict instead of raw post: %s", indexed)
	}
	// A repeated assessment cannot resurrect an older public observation.
	if _, err := d.Pool.Exec(ctx, `UPDATE public_posts SET data=data || '{"text_incomplete":true}'::jsonb WHERE source_key=$1 AND post_key=$2`, source, CandidatePostKey(post)); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = storeAssessedPublicPost(ctx, tx, source, post); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var incomplete bool
	if err := d.Pool.QueryRow(ctx, `SELECT data @> '{"text_incomplete":true}'::jsonb FROM public_posts WHERE source_key=$1 AND post_key=$2`, source, CandidatePostKey(post)).Scan(&incomplete); err != nil || !incomplete {
		t.Fatalf("newer observation overwritten: %v %v", incomplete, err)
	}

	if err := a.autopilotAssessDirectPostsWith(ctx, job, []DiscoveryCandidate{candidate}, assess); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 {
		t.Fatalf("already analyzed public post used model %d times", modelCalls)
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_direct_results WHERE topic_key=$1`, key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("direct assessment missing: count=%d err=%v", count, err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE topic_key=$1`, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("direct assessment unexpectedly approved source: count=%d err=%v", count, err)
	}
	verified, err := a.autopilotVerifiedDirectSources(ctx, job, []DiscoveryCandidate{
		{Platform: PlatformTelegram, ID: "other_digest"},
		candidate,
		candidate,
	})
	if err != nil || len(verified) != 1 || verified[0].ID != candidate.ID {
		t.Fatalf("only independently admitted authors should be nominated: %+v %v", verified, err)
	}
	status, err := a.User(ctx, owners[0], nil).SourceAutopilotStatus()
	if err != nil {
		t.Fatal(err)
	}
	foundStatus := false
	for _, row := range status["topics"].([]jx.M) {
		if jx.Str(row, "topic") != topic {
			continue
		}
		foundStatus = true
		if jx.Int(row, "direct_qualified_recent") != 1 ||
			jx.Int(row, "direct_accepted_total") != 1 ||
			jx.Int(row, "last_success") < jx.Int(post, "posted_at") ||
			jx.Str(row, "diagnosis") == "" {
			t.Fatalf("public direct status missing quality metrics: %v", row)
		}
	}
	if !foundStatus {
		t.Fatalf("auto topic missing from status: %v", status)
	}
	if err := a.deliverAutopilotDirectTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	// The later combined source + post delivery must remain idempotent.
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
	// The transaction must verify the entire saved post and model response:
	// a caller cannot substitute a different body or relevance score while
	// retaining the same URL and model name.
	for _, tc := range []struct {
		name     string
		post     jx.M
		analysis jx.M
	}{
		{"tampered text", jx.Merge(post, jx.M{"text": "Подменённая публикация"}), analysis},
		{"tampered relevance", post, jx.Merge(analysis, jx.M{"relevance": 100})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guarded := withAutopilotDirectDelivery(contextWithAutopilotLease(ctx, job), owners[0], source, topic, tc.post, tc.analysis)
			_, err := a.User(guarded, owners[0], nil).poolUpdate(func(_ pgx.Tx, pool []jx.M) ([]jx.M, error) {
				t.Error("unverified direct result reached the pool transaction")
				return pool, nil
			})
			if !errors.Is(err, errAutopilotSourceChanged) {
				t.Fatalf("tampered direct result was not rejected: %v", err)
			}
		})
	}
	// A verified author must get a review slot before an older unverified
	// directory hit, without granting either source probation or approval.
	rawSource := "telegram:aaa_raw_digest"
	if err := registerAutopilotCandidate(ctx, d, job, rawSource, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := registerAutopilotCandidate(ctx, d, job, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	var reviewed []string
	err = a.autopilotReviewShadowCandidatesWith(ctx, job, 2, 85, func(feed string) FetchResultURL {
		reviewed = append(reviewed, feed)
		return FetchResultURL{FetchResult: FetchResult{Status: 429}}
	}, func(jx.M, []jx.M, string) *sourceEvaluation {
		t.Error("unavailable feed must not receive a verdict")
		return nil
	}, func(context.Context, string, []jx.M) error {
		t.Error("unavailable feed must not receive a post assessment")
		return nil
	}, sourcePolicy{})
	if err != nil || len(reviewed) != 2 || reviewed[0] != source || reviewed[1] != rawSource {
		t.Fatalf("verified author priority or raw exploration lost: %v %v", reviewed, err)
	}
	// Snowball discovery can use an admitted post from an otherwise unapproved
	// author, but references are merely candidates and global exclusions win.
	if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_overrides
		(scope,topic_key,source_id,owner_id,decision,updated_at)
		VALUES($1,$2,'telegram:citation_blocked','','excluded',$3)`, job.Scope, key, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_overrides WHERE scope=$1 AND topic_key=$2 AND source_id='telegram:citation_blocked' AND owner_id=''`, job.Scope, key)
	for i := 0; i < 2; i++ {
		if err := a.autopilotConsumeCitations(contextWithAutopilotLease(ctx, job), job); err != nil {
			t.Fatal(err)
		}
	}
	var citationState, origin string
	if err := d.Pool.QueryRow(ctx, `SELECT state,evidence->>'discovery_origin' FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id='telegram:citation_digest'`, job.Scope, key).Scan(&citationState, &origin); err != nil || citationState != "candidate" || origin != "citation" {
		t.Fatalf("citation was not staged as an unverified candidate: %q %q %v", citationState, origin, err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id='telegram:citation_blocked'`, job.Scope, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("global exclusion bypassed: %d %v", count, err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2 AND source_id='telegram:citation_digest'`, job.Scope, key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeated citations duplicated registration: %d %v", count, err)
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
