package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

func TestAutopilotYieldRecheckBoundedPostgres(t *testing.T) {
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
	const topic, owner = "Тест повторной проверки отдачи", "autopilot-yield-recheck-owner"
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
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_content_results", "source_autopilot_sources", "source_autopilot_overrides", "source_autopilot_jobs"} {
		exec(`DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
		defer d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	old, future := at.Add(-time.Hour).Unix(), at.Add(time.Hour).Unix()
	exec(`INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,resume_state,reason,policy_version,first_seen_at,checked_at,next_check_at,evidence)
		SELECT 'public',$1,'rejected-'||n,'rejected','','preliminary_topic_mismatch',$2,$3,$3,$4,'{}'::jsonb FROM generate_series(1,30) n`, key, TopicProfileVersion, old, future)
	exec(`INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,resume_state,reason,policy_version,first_seen_at,checked_at,next_check_at,evidence)
		SELECT 'public',$1,id,state,resume,reason,$2,$3,$3,$4,evidence::jsonb FROM (VALUES
		('legacy','active','','retain','{}'),('current','active','','retain','{"source_decision_policy":"yield_v2"}'),
		('uncertain','candidate','','assessment_unavailable','{}'),('sparse','cooldown','candidate','sparse_publisher','{}'),
		('limited','cooldown','candidate','rate_limited','{}'),('excluded','rejected','','preliminary_topic_mismatch','{}')) v(id,state,resume,reason,evidence)`, key, TopicProfileVersion, old, future)
	exec(`INSERT INTO source_autopilot_overrides(scope,topic_key,source_id,owner_id,decision,updated_at)
		VALUES('public',$1,'excluded','','excluded',$2)`, key, old)
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotAutoScope, key, autopilotAutoKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	a := &App{DB: d}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		for i := 0; i < 6; i++ {
			if err := a.autopilotScheduleYieldRecheck(work, job); err != nil {
				return err
			}
			var count int
			if err := d.Pool.QueryRow(work, `SELECT count(*) FROM source_autopilot_decisions WHERE scope='public' AND topic_key=$1 AND reason='yield_recheck_scheduled'`, key).Scan(&count); err != nil {
				return err
			}
			want := min((i+1)*5, 20)
			if count != want {
				t.Fatalf("cycle %d scheduled %d, want %d", i, count, want)
			}
			if i == 0 {
				for _, id := range []string{"legacy", "uncertain", "sparse"} {
					var scheduled bool
					if err := d.Pool.QueryRow(work, `SELECT evidence ? 'yield_recheck_scheduled' FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id=$2`, key, id).Scan(&scheduled); err != nil || !scheduled {
						t.Fatalf("priority source %s not scheduled: %v", id, err)
					}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"excluded", "current", "limited"} {
		var next int64
		if err := d.Pool.QueryRow(ctx, `SELECT next_check_at FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id=$2`, key, id).Scan(&next); err != nil || next != future {
			t.Fatalf("protected source %s rescheduled: %d %v", id, next, err)
		}
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND evidence ? 'yield_recheck_scheduled' AND next_check_at <= $2`, key, time.Now().Unix()).Scan(&count); err != nil || count != 20 {
		t.Fatalf("scheduled unique sources=%d err=%v", count, err)
	}
}
