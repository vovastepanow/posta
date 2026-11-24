package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotYieldPolicySeparatesPostsAndSources(t *testing.T) {
	p := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, YieldMinLive: 1, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	y := sourceYieldEvidence{Evidence: sourceEvidence{Evaluated: 20, Relevant: 4, Eligible: 4, FreshUnique: 3, CuratedUnique: 1, SuccessfulPasses: 2, RequireEligible: true}, UsefulUnique: 4, LiveUnique: 1, TrialPasses: 2, TrialStartedAt: 100, CompleteSample: true}
	if state, _ := decideSourceState("probation", y.Evidence, p); state != "rejected" {
		t.Fatalf("strict policy expected to reject mixed source, got %s", state)
	}
	if state, _ := decideSourceYieldState("probation", y, p); state != "active" {
		t.Fatalf("mixed source with proven live utility: %s", state)
	}
	y.LiveUnique = 0
	if state, _ := decideSourceYieldState("probation", y, p); state != "probation" {
		t.Fatalf("archive alone promoted: %s", state)
	}
	y.LiveUnique = 1
	y.TrialPasses = 1
	if state, _ := decideSourceYieldState("probation", y, p); state != "probation" {
		t.Fatalf("single pass promoted: %s", state)
	}
	y.TrialPasses = 2
	y.Evidence.SameSourceDuplicates = 11
	if state, _ := decideSourceYieldState("probation", y, p); state != "rejected" {
		t.Fatalf("self-repeat spam passed: %s", state)
	}
	y.Evidence.SameSourceDuplicates = 0
	y.UsefulUnique, y.LiveUnique, y.Evidence.Eligible = 0, 0, 0
	y.CompleteSample = false
	if state, _ := decideSourceYieldState("probation", y, p); state != "probation" {
		t.Fatalf("unknown context treated as negative: %s", state)
	}
	y.CompleteSample = true
	if state, _ := decideSourceYieldState("probation", y, p); state != "rejected" {
		t.Fatalf("fully observed zero utility retained: %s", state)
	}
	y.Evidence.TechnicalFailure = true
	if state, _ := decideSourceYieldState("active", y, p); state != "active" {
		t.Fatalf("technical failure demoted: %s", state)
	}
}

func TestAutopilotYieldPollInterval(t *testing.T) {
	fallback := 6 * time.Hour
	posts := []jx.M{{"posted_at": 1000}, {"posted_at": 1000 + 86400}, {"posted_at": 1000 + 2*86400}}
	if got := autopilotYieldPollInterval(posts, fallback); got != 36*time.Hour {
		t.Fatalf("daily source interval=%s", got)
	}
	if got := autopilotYieldPollInterval(posts[:1], fallback); got != fallback {
		t.Fatalf("one observation invented frequency: %s", got)
	}
}

func TestAutopilotYieldEvidencePostgres(t *testing.T) {
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
	const key, source = "yield-test", "rss:test"
	at := time.Now().Truncate(time.Second)
	trial := at.Add(-24 * time.Hour).Unix()
	old := trial - 1
	live := trial + 1
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at)
		VALUES ('public',$1,$2,'candidate','probation','trial',1,$3),
		('public',$1,$2,'probation','cooldown','fetch',1,$4),('public',$1,$2,'cooldown','probation','resume',1,$5)`, key, source, trial, live, live+1)
	exec(`INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising)
		VALUES ('public',$1,$2,'a','archive',1,$3,$4,true,true,false),
		('public',$1,$2,'b','live',1,$5,$5,true,true,false),
		('public',$1,$2,'c','live',1,$5,$5,true,true,false),
		('public',$1,$2,'recycled','archive',1,$5,$5,true,true,false),
		('public',$1,$2,'d','obsolete',1,$5,$5,true,true,false),
		('public',$1,$2,'d','obsolete',2,$5,$5,false,false,false),
		('public:shadow',$1,$2,'e','shadow',2,$5,$5,true,true,false)`, key, source, old, trial, live)
	exec(`INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count)
		VALUES ('public',$1,$2,1,$3,4),('public',$1,$2,2,$4,4),('public',$1,$2,3,$5,4)`, key, source, old, trial, live)
	y, err := autopilotYieldEvidenceWindow(ctx, tx, "public", key, source, at.Add(-30*24*time.Hour), at.Add(time.Second))
	if err != nil || y.UsefulUnique != 2 || y.LiveUnique != 1 || y.TrialPasses != 2 || y.TrialStartedAt != trial {
		t.Fatalf("yield=%+v err=%v", y, err)
	}
}
