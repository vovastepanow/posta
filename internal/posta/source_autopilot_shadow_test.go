package posta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotShadowQueuePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const topic = "Уникальная теневая тема"
	key := autopilotTopicKey(topic)
	for _, id := range []string{"autopilot-shadow-a", "autopilot-shadow-b"} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		if err = saveAutopilotDemand(ctx, d.Pool, id, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_query_sources", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
		})
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_jobs WHERE scope='public:shadow' AND topic_key=$1 AND kind=$2`, key, autopilotShadowKind).Scan(&count); err != nil || count != 1 {
		t.Fatalf("jobs=%d err=%v", count, err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	job.SearchQuery = "пример запроса"
	var before int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM catalog_sources`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	find := func(context.Context) ([]DiscoveryCandidate, error) {
		return []DiscoveryCandidate{
			{Platform: PlatformRSS, ID: "https://example.org/feed"}, {Platform: PlatformRSS, ID: "https://example.org/feed"},
			{Platform: PlatformRSS, ID: "https://example.org/empty"},
			{Platform: PlatformRSS, ID: "https://example.org/feed?token=private"},
		}, nil
	}
	date := time.Now().UTC().Format(time.RFC1123Z)
	item := func(n int) string {
		return fmt.Sprintf(`<item><title>Test %d</title><link>https://example.org/post/%d</link><pubDate>%s</pubDate><description>Test post body.</description></item>`, n, n, date)
	}
	feed := []byte(`<rss><channel><title>Test</title>` + item(1) + item(2) + item(3) + `</channel></rss>`)
	fetch := func(url string) FetchResultURL {
		body := feed
		if strings.HasSuffix(url, "/empty") {
			body = []byte(`<rss><channel><title>Empty</title></channel></rss>`)
		}
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: body}, URL: url}
	}
	evaluations := 0
	evaluate := func(_ jx.M, posts []jx.M, _ string) *sourceEvaluation {
		evaluations++
		if len(posts) != 3 {
			t.Errorf("samples=%d", len(posts))
		}
		return &sourceEvaluation{Score: 90}
	}
	err = runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 10, 10, 10, 10, func(work context.Context) error {
		if err := a.autopilotShadowSearchWith(work, job, find); err != nil {
			return err
		}
		return a.autopilotReviewShadowCandidatesWith(work, job, 3, 85, fetch, evaluate, func(context.Context, string, []jx.M) error { return nil }, sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope='public:shadow' AND topic_key=$1`, key).Scan(&count); err != nil || count != 2 {
		t.Fatalf("candidates=%d err=%v", count, err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_query_sources WHERE scope='public:shadow' AND topic_key=$1 AND query=$2`, key, job.SearchQuery).Scan(&count); err != nil || count != 2 {
		t.Fatalf("query attribution=%d err=%v", count, err)
	}
	partialErr := a.autopilotShadowSearchWith(ctx, job, func(context.Context) ([]DiscoveryCandidate, error) {
		return []DiscoveryCandidate{{Platform: PlatformRSS, ID: "https://example.org/partial"}}, errAutopilotBudgetExhausted
	})
	if !errors.Is(partialErr, errAutopilotBudgetExhausted) {
		t.Fatalf("partial search error lost: %v", partialErr)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope='public:shadow' AND topic_key=$1 AND source_id='rss:https://example.org/partial'`, key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial search candidates lost: %d %v", count, err)
	}
	var state, reason string
	if err = d.Pool.QueryRow(ctx, `SELECT state,reason FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, autopilotShadowScope, key, "rss:https://example.org/feed").Scan(&state, &reason); err != nil || state != "probation" || evaluations != 1 {
		t.Fatalf("state=%s reason=%s evaluations=%d err=%v", state, reason, evaluations, err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT state,reason FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, autopilotShadowScope, key, "rss:https://example.org/empty").Scan(&state, &reason); err != nil || state != "candidate" || reason != "insufficient_fresh_samples" {
		t.Fatalf("empty state=%s reason=%s err=%v", state, reason, err)
	}
	var after int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM catalog_sources`).Scan(&after); err != nil || before != after {
		t.Fatalf("shadow changed catalog: %d -> %d err=%v", before, after, err)
	}
	if err = saveAutopilotDemand(ctx, d.Pool, "autopilot-shadow-a", autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:still-live", time.Now()); err != nil {
		t.Fatalf("other subscriber should retain lease: %v", err)
	}
	if err = saveAutopilotDemand(ctx, d.Pool, "autopilot-shadow-b", autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 3); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:no-demand", time.Now()); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("job wrote after last demand was disabled: %v", err)
	}
	if err = a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:stale", time.Now()); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("job not revoked: %v", err)
	}
	if _, err = setAutopilotJobEnabled(ctx, d.Pool, autopilotShadowScope, key, autopilotShadowKind, false); err != nil {
		t.Fatal(err)
	}
	if err = saveAutopilotDemand(ctx, d.Pool, "autopilot-shadow-a", autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 4); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	var enabled, eligible bool
	if err = d.Pool.QueryRow(ctx, `SELECT enabled,demand_active FROM source_autopilot_jobs WHERE scope='public:shadow' AND topic_key=$1 AND kind=$2`, key, autopilotShadowKind).Scan(&enabled, &eligible); err != nil || enabled || !eligible {
		t.Fatalf("pause lost: enabled=%v eligible=%v err=%v", enabled, eligible, err)
	}
}

func TestAutopilotRetryAfter(t *testing.T) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, header := range []string{"172800", at.Add(48 * time.Hour).Format(http.TimeFormat)} {
		next := autopilotRetryAt(at, "rss:https://example.org/feed", header)
		if next.Before(at.Add(48*time.Hour)) || next.After(at.Add(49*time.Hour)) {
			t.Fatalf("retry-after %q: %v", header, next)
		}
	}
	if next := autopilotRetryAt(at, "rss:https://example.org/feed", "invalid"); next.Before(at.Add(24 * time.Hour)) {
		t.Fatalf("retry earlier than default: %v", next)
	}
}

func TestAutopilotRestrictedTopicStopsBeforeExternalSearch(t *testing.T) {
	a := taxonomyTestUser().App
	topic := "порно"
	job := &autopilotJob{Scope: autopilotAutoScope, Kind: autopilotAutoKind, Topic: topic, TopicKey: autopilotTopicKey(topic)}
	if err := a.autopilotShadowSearch(context.Background(), job); err == nil || err.Error() != "restricted_profile" {
		t.Fatalf("restricted topic was not stopped: %v", err)
	}
}

func TestAutopilotShadowEvidencePromotionPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const topic = "Тест подтверждения источника"
	key := autopilotTopicKey(topic)
	const owner = "autopilot-promotion-test"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner) })
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_post_evidence", "source_autopilot_collection_passes", "source_autopilot_jobs"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, autopilotShadowScope, key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, autopilotShadowScope, key)
		})
	}
	at := time.Now().Truncate(time.Second)
	if err = enqueueAutopilotJob(ctx, d.Pool, autopilotShadowScope, key, autopilotShadowKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	const source = "rss:https://example.org/promotion"
	if err = registerAutopilotCandidate(ctx, d, job, source, at); err != nil {
		t.Fatal(err)
	}
	if err = transitionAutopilotSource(ctx, d, job, source, "candidate", "probation", "preliminary", []byte(`{}`), at, at); err != nil {
		t.Fatal(err)
	}
	for _, when := range []time.Time{at.Add(-24 * time.Hour), at} {
		if err = recordAutopilotShadowFetch(ctx, d, job, source, when.Unix(), 5); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		e := autopilotPostEvidence{PostHash: fmt.Sprintf("post-%d", i), ContentHash: fmt.Sprintf("content-%d", i), PublishedAt: at.Add(-time.Hour).Unix(), ObservedAt: at.Unix(), Relevant: i < 9, Eligible: i < 9, Advertising: i == 9}
		if err = recordAutopilotPostEvidence(ctx, d, job, source, e); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := autopilotEvidenceWindow(ctx, d.Pool, job.Scope, key, source, at.Add(-30*24*time.Hour), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	next, reason := decideSourceState("probation", sourceEvidence{Evaluated: stats.Evaluated, Relevant: stats.Relevant, FreshUnique: stats.FreshUnique, SuccessfulPasses: stats.SuccessfulPasses, Advertising: stats.Advertising, Duplicates: stats.Duplicates}, policy)
	if next != "active" {
		t.Fatalf("state=%s reason=%s stats=%+v", next, reason, stats)
	}
	if err = transitionAutopilotSource(ctx, d, job, source, "probation", next, reason, []byte(`{}`), at, at); err != nil {
		t.Fatal(err)
	}
}
