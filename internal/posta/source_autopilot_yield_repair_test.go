package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

func TestAutopilotRepairYieldAdmissionPostgres(t *testing.T) {
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
	key := autopilotTopicKey("Тест восстановления допуска источников")
	at := time.Now().Truncate(time.Second)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_overrides"} {
		exec(`DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
		defer d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	exec(`INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,checked_at,next_check_at,evidence)
		SELECT 'public',$1,id,'probation',reason,$2,$3,$3,$3,'{}'::jsonb FROM (VALUES
		('unverified','yield_observing'),('positive','yield_observing'),('mixed','yield_observing'),
		('restored','yield_confirmation_required'),('excluded','yield_observing')) v(id,reason)`, key, TopicProfileVersion, at.Unix())
	exec(`INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
		SELECT 'public',$1,id,old,'probation',reason,evidence::jsonb,$2,$3 FROM (VALUES
		('unverified','candidate','yield_readable_trial','{}'),
		('positive','candidate','yield_readable_trial','{"preliminary_score":95,"threshold":85,"reviewed_samples":5}'),
		('mixed','candidate','yield_readable_trial','{"preliminary_score":65,"threshold":85,"reviewed_samples":5,"topic_relevant":3}'),
		('restored','active','yield_confirmation_required','{}'),
		('excluded','candidate','yield_readable_trial','{}')) v(id,old,reason,evidence)`, key, TopicProfileVersion, at.Unix())
	exec(`INSERT INTO source_autopilot_overrides(scope,topic_key,source_id,owner_id,decision,updated_at)
		VALUES('public',$1,'excluded','','excluded',$2)`, key, at.Unix())
	if count, err := repairAutopilotYieldAdmission(ctx, d.Pool, key, at); err != nil || count != 2 {
		t.Fatalf("repair count=%d err=%v", count, err)
	}
	for id, want := range map[string]string{"unverified": "candidate", "positive": "probation", "mixed": "probation", "restored": "active", "excluded": "probation"} {
		var state string
		if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id=$2`, key, id).Scan(&state); err != nil || state != want {
			t.Fatalf("%s: state=%s want=%s err=%v", id, state, want, err)
		}
	}
	if count, err := repairAutopilotYieldAdmission(ctx, d.Pool, key, at); err != nil || count != 0 {
		t.Fatalf("repeat repair count=%d err=%v", count, err)
	}
}
