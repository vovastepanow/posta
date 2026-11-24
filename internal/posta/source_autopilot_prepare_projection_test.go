package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

// An unsupported legacy suggestion is intentionally persisted as rejected.
// Replaying rejected projections must never abort the auto pipeline before
// the pending candidate review phase.
func TestAutopilotPrepareSkipsInvalidRejectedProjectionsPostgres(t *testing.T) {
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
	const owner = "autopilot-invalid-projection-owner"
	const topic = "Проверка некорректных отклонённых источников"
	key := autopilotTopicKey(topic)
	defer func() {
		for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
			_, _ = d.Pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE scope=$1 AND topic_key=$2", autopilotAutoScope, key)
		}
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
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	job.MaxProbation = 10
	const invalidRejected = "unsupported:old-suggestion"
	const invalidTrial = "unsupported:invalid-trial"
	const legitimateCandidate = "rss:https://example.org/valid.xml"
	const oldVC = "rss:https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123"
	const oldHabr = "rss:https://habr.com/ru/rss/hubs/business_models/articles/all/?fl=ru"
	const fixedHabr = "rss:https://habr.com/ru/rss/hubs/business_models/articles/all/"
	const oldPikabu = "rss:https://pikabu.ru/community/lnk"
	const oldYouTube = "rss:https://www.youtube.com/feeds/videos.xml?channel_id=UCabcdefghijklmnopqrstuv"
	const brokenYouTube = "rss:https://www.youtube.com/feeds/videos.xml?channel_id=ucabcdefghijklmnopqrstuv"
	for _, candidate := range []struct{ source, state, reason string }{
		{invalidRejected, "rejected", "legacy_pending_import"},
		{invalidTrial, "probation", "legacy_pending_import"},
		{legitimateCandidate, "candidate", "legacy_pending_import"},
		{oldVC, "rejected", "unsupported_source_format"},
		{oldHabr, "rejected", "unsupported_source_format"},
		{oldPikabu, "rejected", "unsupported_source_format"},
		{oldYouTube, "rejected", "invalid_source_identity"},
		{brokenYouTube, "rejected", "invalid_source_identity"},
	} {
		_, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources
			(scope,topic_key,source_id,state,reason,first_seen_at,policy_version)
			VALUES($1,$2,$3,$4,$5,$6,$7)`,
			autopilotAutoScope, key, candidate.source, candidate.state, candidate.reason, time.Now().Unix(), TopicProfileVersion)
		if err != nil {
			t.Fatal(err)
		}
	}
	for iteration := 0; iteration < 2; iteration++ {
		if err := a.reconcileAutopilotProjections(ctx, job); err != nil {
			t.Fatalf("reconciliation iteration %d stopped before candidate review: %v", iteration, err)
		}
	}
	for _, check := range []struct{ source, state string }{
		{invalidRejected, "rejected"},
		{invalidTrial, "cooldown"},
		{legitimateCandidate, "candidate"},
		{oldVC, "candidate"},
		{oldHabr, "rejected"},
		{fixedHabr, "candidate"},
		{oldPikabu, "candidate"},
		{oldYouTube, "candidate"},
		{brokenYouTube, "rejected"},
	} {
		var got string
		if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, autopilotAutoScope, key, check.source).Scan(&got); err != nil || got != check.state {
			t.Fatalf("source=%q state=%q want=%q err=%v", check.source, got, check.state, err)
		}
	}
	var repairedReason, replacement string
	if err := d.Pool.QueryRow(ctx, `SELECT reason, COALESCE(evidence->>'replacement_source_id','')
		FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
		autopilotAutoScope, key, oldHabr).Scan(&repairedReason, &replacement); err != nil ||
		repairedReason != "source_identity_recovered" || replacement != fixedHabr {
		t.Fatalf("old Habr identity still treated as unsupported: %s %s %v", repairedReason, replacement, err)
	}
	// A lost lowercase YouTube ID is never guessed or accidentally
	// activated. Technical source rechecks are exactly once per old key.
	var marked bool
	if err := d.Pool.QueryRow(ctx, `SELECT COALESCE((evidence->>'technical_reaudit_v1')::boolean,false)
		FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
		autopilotAutoScope, key, brokenYouTube).Scan(&marked); err != nil || !marked {
		t.Fatalf("unrecoverable YouTube ID not marked for audit: %v / %v", marked, err)
	}
	var auditCount int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3
		AND new_state='cooldown' AND reason='unsupported_source_format'`,
		autopilotAutoScope, key, invalidTrial).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("invalid trial audit count=%d err=%v", auditCount, err)
	}
	if err := a.autopilotMarkPhase(ctx, job, "review"); err != nil {
		t.Fatalf("pipeline cannot progress to review: %v", err)
	}
}
