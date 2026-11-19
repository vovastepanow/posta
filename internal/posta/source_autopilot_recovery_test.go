package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

func TestAutopilotNextReviewDelay(t *testing.T) {
	at := time.Date(2026, 11, 19, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		earliest int64
		want time.Duration
	}{
		{"no sources", 0, 24 * time.Hour},
		{"due review", at.Add(-time.Hour).Unix(), 2 * time.Minute},
		{"new candidate", at.Add(40*time.Minute).Unix(), 40 * time.Minute},
		{"active 3h review", at.Add(3*time.Hour).Unix(), 3 * time.Hour},
		{"future retry preserved", at.Add(3*24*time.Hour).Unix(), 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autopilotNextReviewDelay(24*time.Hour, at, tc.earliest); got != tc.want {
				t.Fatalf("next review delay = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAutopilotRecoversStaleFailuresWithoutBypassingQuotaPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated Postgres")
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
	const owner = "autopilot-stale-recovery-test"
	const topic = "Тест восстановления зависших ошибок"
	key := autopilotTopicKey(topic)
	_, _ = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2`, autopilotAutoScope, key)
	defer func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2`, autopilotAutoScope, key)
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_demands WHERE user_id=$1`, owner)
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	}()
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3,
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(20 * time.Hour).Unix()
	change := func(enabled bool, reason string) {
		t.Helper()
		_, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_jobs
			SET next_run_at=$4,last_error=$5,enabled=$6,lease_until=0
			WHERE scope=$1 AND topic_key=$2 AND kind=$3`,
			autopilotAutoScope, key, autopilotAutoKind, future, reason, enabled)
		if err != nil {
			t.Fatal(err)
		}
	}
	next := func() int64 {
		t.Helper()
		var at int64
		if err := d.Pool.QueryRow(ctx, `SELECT next_run_at FROM source_autopilot_jobs
			WHERE scope=$1 AND topic_key=$2 AND kind=$3`,
			autopilotAutoScope, key, autopilotAutoKind).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	check := func(wantRecovery bool) {
		t.Helper()
		if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
			t.Fatal(err)
		}
		got := next()
		if wantRecovery && got > time.Now().Add(2*time.Minute).Unix() {
			t.Fatalf("stale job was not awakened: %d", got)
		}
		if !wantRecovery && got != future {
			t.Fatalf("existing backoff was incorrectly bypassed: %d vs %d", got, future)
		}
	}
	change(true, "classifier_unavailable")
	check(true)
	change(true, "search_unavailable")
	check(true)
	change(true, "source autopilot budget exhausted")
	check(false)
	// After the default fetch cap rises from 1000 to 3000, only a job
	// genuinely stopped at the old cap may resume before the UTC reset.
	_, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_budget(scope,resource,day,used)
		VALUES($1,'public_fetch',(now() AT TIME ZONE 'UTC')::date,1000)
		ON CONFLICT(scope,resource,day) DO UPDATE SET used=1000`, autopilotAutoScope+":"+key)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_budget
		WHERE scope=$1 AND resource='public_fetch'`, autopilotAutoScope+":"+key)
	change(true, "source autopilot budget exhausted")
	check(true)
	change(false, "classifier_unavailable")
	check(false)
	change(true, "")
	check(false)

	// A user activating auto mode explicitly should wake a healthy job that
	// would otherwise sleep for a full day after a prior configuration.
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3,
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	change(true, "")
	if err := a.User(ctx, owner, nil).SourceAutopilotDemandSave(topic, "auto", 5, 3); err != nil {
		t.Fatal(err)
	}
	if got := next(); got > time.Now().Add(2*time.Minute).Unix() {
		t.Fatalf("newly active demand left sleeping: %d", got)
	}
}
