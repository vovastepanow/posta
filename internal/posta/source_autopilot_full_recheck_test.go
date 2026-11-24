package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotFullRecheckPostgres(t *testing.T) {
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
	const topic, owner = "Тест полного перезапуска", "autopilot-full-recheck-owner"
	key := autopilotTopicKey(topic)
	at := time.Now().Truncate(time.Second)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_post_evidence", "source_autopilot_collection_passes", "source_autopilot_sources", "source_autopilot_overrides", "source_autopilot_jobs"} {
		exec(`DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
		defer d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
	}
	old := at.Add(-time.Hour).Unix()
	future := at.Add(time.Hour).Unix()
	exec(`INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,resume_state,reason,policy_version,first_seen_at,checked_at,next_check_at,evidence)
		SELECT 'public:shadow',$1,id,state,resume,reason,$2,$3,$3,$4,'{"review_policy_v3_checked":true}'::jsonb FROM (VALUES
		('active','active','','retain'),('trial','probation','','retain'),('rejected','rejected','','preliminary_topic_mismatch'),
		('candidate','candidate','','discovered'),('cooldown','cooldown','active','feed_unavailable'),
		('limited','cooldown','active','rate_limited'),('pinned','active','','retain'),('excluded','rejected','','preliminary_topic_mismatch'),
		('alias','rejected','','source_identity_recovered')) v(id,state,resume,reason)`, key, TopicProfileVersion, old, future)
	exec(`INSERT INTO source_autopilot_overrides(scope,topic_key,source_id,owner_id,decision,updated_at)
		VALUES('public:shadow',$1,'pinned','','pinned',$2),('public:shadow',$1,'excluded','','excluded',$2)`, key, old)
	exec(`INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising)
		VALUES('public:shadow',$1,'active','p','h',$2,$3,$3,true,true,false)`, key, TopicProfileVersion, old)
	exec(`INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count)
		VALUES('public:shadow',$1,'active',1,$2,10)`, key, old)
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotShadowScope, key, autopilotShadowKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	// Schedule while the job is asleep with a recorded error. This must
	// not require a claim, a model, a budget, or a successful supply check.
	exec(`UPDATE source_autopilot_jobs SET next_run_at=$2,last_error='supply_unavailable'
		WHERE scope='public:shadow' AND topic_key=$1`, key, future)
	a := &App{DB: d}
	if err := a.reconcileAutopilotMode(ctx, true, "shadow", autopilotShadowScope, autopilotShadowKind); err != nil {
		t.Fatal(err)
	}
	var scheduledState string
	if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope='public:shadow' AND topic_key=$1 AND source_id='active'`, key).Scan(&scheduledState); err != nil || scheduledState != "candidate" {
		t.Fatalf("idle scheduler did not reset source: %q %v", scheduledState, err)
	}
	var scheduledNext int64
	if err := d.Pool.QueryRow(ctx, `SELECT next_run_at FROM source_autopilot_jobs WHERE scope='public:shadow' AND topic_key=$1`, key).Scan(&scheduledNext); err != nil || scheduledNext != future {
		t.Fatalf("scheduler discarded failure backoff: %d %v", scheduledNext, err)
	}
	exec(`UPDATE source_autopilot_jobs SET next_run_at=0,last_error='' WHERE scope='public:shadow' AND topic_key=$1`, key)
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		if err := a.autopilotQueueFullRecheck(work, job); err != nil {
			return err
		}
		stats, err := autopilotEvidenceWindow(work, d.Pool, job.Scope, key, "active", at.Add(-30*24*time.Hour), time.Now().Add(time.Minute))
		if err != nil || stats.Evaluated != 0 || stats.SuccessfulPasses != 0 {
			t.Fatalf("old evidence carried into restart: %+v %v", stats, err)
		}
		if err := recordAutopilotPostEvidence(work, d, job, "active", autopilotPostEvidence{PostHash: "p", ContentHash: "h", PublishedAt: old, ObservedAt: time.Now().Unix()}); err != nil {
			return err
		}
		stats, err = autopilotEvidenceWindow(work, d.Pool, job.Scope, key, "active", at.Add(-30*24*time.Hour), time.Now().Add(time.Minute))
		if err != nil || stats.Evaluated != 1 || stats.Eligible != 0 {
			t.Fatalf("old verdict was not replaced by fresh verdict: %+v %v", stats, err)
		}
		if !jx.Bool(jx.Unmarshal(job.Checkpoint), "full_recheck_v4_started") {
			t.Fatal("restart checkpoint missing")
		}
		// Even if the in-memory checkpoint is lost, the persisted source
		// generation prevents another mass reset.
		job.Checkpoint = jx.Marshal(jx.M{})
		return a.autopilotQueueFullRecheck(work, job)
	}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"active": "candidate", "trial": "candidate", "rejected": "candidate", "candidate": "candidate", "cooldown": "candidate", "limited": "cooldown", "pinned": "active", "excluded": "rejected", "alias": "rejected"} {
		var state string
		var next int64
		if err := d.Pool.QueryRow(ctx, `SELECT state,next_check_at FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, id).Scan(&state, &next); err != nil || state != want || (id == "limited" && next != future) {
			t.Fatalf("source %s: state=%s next=%d err=%v", id, state, next, err)
		}
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2 AND reason='full_recheck_v4_scheduled'`, job.Scope, key).Scan(&count); err != nil || count != 7 {
		t.Fatalf("restart not idempotent: count=%d err=%v", count, err)
	}
	funnel, err := AutopilotAuditFunnel(ctx, d.Pool, job.Scope, key, time.Now())
	if err != nil || jx.Int(jx.Map(funnel, "full_recheck"), "scheduled") != 7 {
		t.Fatalf("restart progress missing: %+v %v", funnel, err)
	}
}
