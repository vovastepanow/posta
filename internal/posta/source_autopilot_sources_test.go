package posta

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

func TestAutopilotCandidateOverridesPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_overrides", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='test-source'`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"source_autopilot_decisions", "source_autopilot_overrides", "source_autopilot_sources", "source_autopilot_jobs"} {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='test-source'`)
		}
	})
	at := time.Now()
	if err = enqueueAutopilotJob(ctx, d.Pool, "test-source", "topic", "discovery", "topic", "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJob(ctx, d.Pool, at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if err = setAutopilotSourceOverride(ctx, d, "test-source", "person-a", "topic", "rss:one", "excluded", at); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:one", at); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:one", at); err != nil {
		t.Fatal(err)
	}
	if err = setAutopilotSourceOverride(ctx, d, "test-source", "", "topic", "rss:two", "excluded", at); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:two", at); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope='test-source'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	evidence := json.RawMessage(`{"evaluated":10}`)
	if err = transitionAutopilotSource(ctx, d, job, "rss:one", "candidate", "probation", "preliminary_pass", evidence, at, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err = transitionAutopilotSource(ctx, d, job, "rss:one", "probation", "active", "evidence", evidence, at, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err = setAutopilotSourceOverride(ctx, d, "test-source", "", "topic", "rss:one", "pinned", at); err != nil {
		t.Fatal(err)
	}
	if err = transitionAutopilotSource(ctx, d, job, "rss:one", "active", "probation", "drift", evidence, at, time.Time{}); !errors.Is(err, errAutopilotSourceOverride) {
		t.Fatalf("pinned source demoted: %v", err)
	}
	if err = transitionAutopilotSource(ctx, d, job, "rss:one", "active", "cooldown", "timeout", evidence, at, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = transitionAutopilotSource(ctx, d, job, "rss:one", "cooldown", "rejected", "timeout", evidence, at, time.Time{}); !errors.Is(err, errAutopilotSourceChanged) {
		t.Fatalf("technical failure rejected source: %v", err)
	}
	if err = transitionAutopilotSource(ctx, d, job, "rss:one", "cooldown", "active", "recovered", evidence, at, time.Time{}); err != nil {
		t.Fatal(err)
	}
	totals, err := readAutopilotSourceTotals(ctx, d.Pool, "test-source", "topic")
	if err != nil || totals != (autopilotSourceTotals{Active: 1, Checked: 1}) {
		t.Fatalf("shared source totals: %+v, %v", totals, err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions WHERE scope='test-source'`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("decision count=%d err=%v", count, err)
	}
	if _, err = setAutopilotJobEnabled(ctx, d.Pool, "test-source", "topic", "discovery", false); err != nil {
		t.Fatal(err)
	}
	if err = registerAutopilotCandidate(ctx, d, job, "rss:three", at); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale worker: %v", err)
	}
}
