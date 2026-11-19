package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotWorkerHeartbeat(t *testing.T) {
	a := &App{}
	if autopilotWorkerAlive(a.autopilotWorkerHealth("auto"), time.Now().Unix()) {
		t.Fatal("worker with no heartbeat must be reported stopped")
	}
	a.recordAutopilotWorkerTick("auto", nil)
	a.recordAutopilotWorkerTick("shadow", context.DeadlineExceeded)
	a.recordAutopilotWorkerPanic("auto")
	auto := a.autopilotWorkerHealth("auto")
	shadow := a.autopilotWorkerHealth("shadow")
	if !autopilotWorkerAlive(auto, time.Now().Unix()) || auto.ReconcileError || auto.Panics != 1 {
		t.Fatalf("auto heartbeat incorrect: %+v", auto)
	}
	if !shadow.ReconcileError || !autopilotWorkerAlive(shadow, time.Now().Unix()) {
		t.Fatalf("shadow scheduler failure not captured: %+v", shadow)
	}
	if autopilotWorkerAlive(auto, auto.LastTick+181) {
		t.Fatal("stale worker heartbeat counted alive")
	}
}

func TestMalformedCandidateDoesNotStarveAutopilotReviewPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
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
	const owner, topic = "autopilot-invalid-candidate-test", "Тест очереди с неверным источником"
	key := autopilotTopicKey(topic)
	defer func() {
		for _, table := range []string{"source_autopilot_decisions", "source_autopilot_collection_passes", "source_autopilot_sources", "source_autopilot_jobs"} {
			_, _ = d.Pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE scope=$1 AND topic_key=$2", autopilotShadowScope, key)
		}
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_demands WHERE user_id=$1`, owner)
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	}()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 2, HorizonDays: 2}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err := a.reconcileAutopilotMode(ctx, true, "shadow", autopilotShadowScope, autopilotShadowKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("could not claim test job: %+v %v", job, err)
	}
	job.MaxProbation = 3
	const invalid = "invalid:not-a-source"
	const valid = "rss:https://example.org/legitimate-feed.xml"
	if _, _, _, ok := autopilotSourceParts(valid); !ok {
		t.Fatal("test RSS identity unexpectedly unsupported")
	}
	for i, source := range []string{invalid, valid} {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources
			(scope,topic_key,source_id,state,first_seen_at,policy_version)
			VALUES($1,$2,$3,'candidate',$4,$5)`, autopilotShadowScope, key, source, i+1, TopicProfileVersion); err != nil {
			t.Fatal(err)
		}
	}
	fetches := 0
	feed := []byte(`<rss><channel><title>Empty</title></channel></rss>`)
	fetch := func(url string) FetchResultURL {
		fetches++
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: feed}, URL: url}
	}
	eval := func(_ jx.M, _ []jx.M, _ string) *sourceEvaluation {
		t.Fatal("empty feed must never reach AI review")
		return nil
	}
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 10, 10, 10, 10,
		func(work context.Context) error {
			if err := a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, eval, func(context.Context, string, []jx.M) error { return nil }, policy); err != nil {
				return err
			}
			return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, eval, func(context.Context, string, []jx.M) error { return nil }, policy)
		}); err != nil {
		t.Fatal(err)
	}
	var invalidState, invalidReason, validState string
	if err := d.Pool.QueryRow(ctx, `SELECT state,reason FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
		autopilotShadowScope, key, invalid).Scan(&invalidState, &invalidReason); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
		autopilotShadowScope, key, valid).Scan(&validState); err != nil {
		t.Fatal(err)
	}
	if invalidState != "rejected" || invalidReason != "unsupported_source_format" || validState != "cooldown" || fetches != 1 {
		t.Fatalf("head-of-line block remains: invalid=%s (%s), valid=%s, fetches=%d", invalidState, invalidReason, validState, fetches)
	}
}
