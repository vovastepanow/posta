package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotAuditFunnelPostgres(t *testing.T) {
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
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	at := time.Now().Truncate(time.Second)
	const key = "audit-funnel-test"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,checked_at)
		SELECT 'public',$1,s,state,reason,1,$2,checked FROM (VALUES
		('a','candidate','',0::bigint),('b','rejected','preliminary_topic_mismatch',$2),
		('c','probation','insufficient_evidence',$2),('d','probation','retain',$2),
		('e','active','observed_useful',$2),('f','rejected','invalid_source_identity',$2)) AS v(s,state,reason,checked)`, key, at.Unix())
	exec(`INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count)
		VALUES ('public',$1,'b',1,$2,10),('public',$1,'c',1,$2,10),('public',$1,'d',1,$2,10),('public',$1,'e',1,$2,10)`, key, at.Unix())
	exec(`INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising)
		VALUES ('public',$1,'c','p','h',1,$2,$2,true,true,false),
		('public',$1,'c','p','h',2,$2,$2,false,false,false),
		('public',$1,'d','p','h',2,$2,$2,true,true,false),
		('public',$1,'e','p','h',2,$2,$2,true,true,false),
		('public:shadow',$1,'c','q','h2',2,$2,$2,true,true,false)`, key, at.Unix())
	exec(`INSERT INTO source_autopilot_content_results(scope,topic_key,source_id,post_hash,post,analysis,published_at,assessed_at,policy_version)
		VALUES ('public',$1,'e','p','{}','{}',$2,$2,2),
		('public:shadow',$1,'d','p','{}','{}',$2,$2,2)`, key, at.Unix())
	exec(`INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at)
		VALUES ('public',$1,'b','','candidate','discovered',1,$2),
		('public',$1,'b','candidate','candidate','retry',1,$2),
		('public',$1,'b','candidate','rejected','preliminary_topic_mismatch',1,$2)`, key, at.Unix())
	got, err := AutopilotAuditFunnel(ctx, tx, "public", key, at)
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]int64{
		"sources": 6, "sources_with_fetch": 4, "sources_with_post_verdict": 3,
		"sources_with_eligible": 2, "sources_with_saved_result": 1,
		"distinct_source_post_verdicts": 3, "eligible_without_matching_saved_version": 1,
		"decision_records": 3, "actual_state_changes": 1, "fetched_items_including_repeats": 40,
		"evaluated_posts_published_30d": 3, "useful_unique_per_source_30d": 2,
	} {
		if jx.Int(got, field) != want {
			t.Errorf("%s = %v, want %d", field, got[field], want)
		}
	}
	stages := jx.Map(got, "stages")
	for _, stage := range []string{"saved_result", "eligible_no_saved_result", "assessed_no_eligible", "fetched_no_post_verdict", "checked_no_recorded_fetch_or_verdict", "unchecked_no_recorded_evidence"} {
		if jx.Int(stages, stage) != 1 {
			t.Errorf("stage %s = %v, want 1", stage, stages[stage])
		}
	}
}
