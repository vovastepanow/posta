package posta

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// A classifier outage for one candidate must not block review of another
// source in the same job. The failed source stays pending for later retry.
func TestAutopilotClassifierFailureDoesNotBlockNextCandidatePostgres(t *testing.T) {
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
	const topic = "Тест независимости оценки кандидатов"
	const owner = "autopilot-classifier-isolation-owner"
	key := autopilotTopicKey(topic)
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(context.Background(), d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_collection_passes", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotShadowScope, key, autopilotShadowKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	const failedSource = "rss:https://example.org/a-unavailable.xml"
	const healthySource = "rss:https://example.org/b-healthy.xml"
	for _, source := range []string{failedSource, healthySource} {
		if err := registerAutopilotCandidate(ctx, d, job, source, at); err != nil {
			t.Fatal(err)
		}
	}
	var feed strings.Builder
	feed.WriteString("<rss><channel><title>Test</title>")
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&feed, "<item><title>Article %d</title><link>https://example.org/post/%d</link><pubDate>%s</pubDate><description>A useful and detailed public post.</description></item>", i, i, at.Format(time.RFC1123Z))
	}
	feed.WriteString("</channel></rss>")
	calls := 0
	fetch := func(url string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: []byte(feed.String())}, URL: url}
	}
	evaluate := func(_ jx.M, _ []jx.M, _ string) *sourceEvaluation {
		calls++
		if calls == 1 {
			return nil
		}
		return &sourceEvaluation{Score: 95}
	}
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		return a.autopilotReviewShadowCandidatesWith(work, job, 2, 85, fetch, evaluate,
			func(context.Context, string, []jx.M) error { return nil }, policy)
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("classifier calls=%d, want 2", calls)
	}
	for _, tc := range []struct{ source, wantState, wantReason string }{
		{failedSource, "candidate", "assessment_unavailable"},
		{healthySource, "probation", "shadow_preliminary_pass"},
	} {
		var state, reason string
		if err := d.Pool.QueryRow(ctx, `SELECT state,reason FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, tc.source).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if state != tc.wantState || reason != tc.wantReason {
			t.Fatalf("%s state=%s reason=%s; want %s/%s", tc.source, state, reason, tc.wantState, tc.wantReason)
		}
	}
}
