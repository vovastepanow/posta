package posta

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// An older candidate backlog must not exhaust the review budget before a
// probation source can gather evidence and graduate to active.
func TestAutopilotProbationNotStarvedByCandidateBacklogPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN for isolated PostgreSQL")
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

	const topic = "Проверка очереди испытаний без голодания"
	key := autopilotTopicKey(topic)
	const owner = "autopilot-probation-fairness-test"
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(context.Background(), d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	old := time.Now().Add(-48 * time.Hour)
	for _, id := range []string{"rss:https://example.org/old-a", "rss:https://example.org/old-b", "rss:https://example.org/old-c"} {
		if err := registerAutopilotCandidate(ctx, d, job, id, old); err != nil {
			t.Fatal(err)
		}
	}
	const trial = "rss:https://example.org/trial"
	if err := registerAutopilotCandidate(ctx, d, job, trial, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if err := transitionAutopilotSource(ctx, d, job, trial, "candidate", "probation", "test_preliminary_pass", []byte(`{}`), at, at); err != nil {
		t.Fatal(err)
	}
	seen := ""
	err = runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85,
			func(feed string) FetchResultURL {
				seen = feed
				return FetchResultURL{URL: feed, FetchResult: FetchResult{Status: http.StatusNotFound}}
			},
			func(jx.M, []jx.M, string) *sourceEvaluation { t.Fatal("unexpected new candidate evaluation"); return nil },
			func(context.Context, string, []jx.M) error { return nil },
			sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5})
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "https://example.org/trial" {
		t.Fatalf("reviewed %q instead of the due probation source; candidates starved probation", seen)
	}
}
