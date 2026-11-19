package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

// An active shadow backlog must not block an independently scheduled auto
// worker. The global concurrency cap still allows only two active jobs.
func TestAutopilotModeClaimsDoNotStarvePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated autopilot PostgreSQL")
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

	const scope = "test-cross-mode-claim"
	if _, err := d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope=$1`, scope); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope=$1`, scope)

	at := time.Now().Truncate(time.Second)
	if err := enqueueAutopilotJob(ctx, d.Pool, scope, "older-shadow", autopilotShadowKind,
		"Shadow", "test", 1, at.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := enqueueAutopilotJob(ctx, d.Pool, scope, "younger-auto", autopilotAutoKind,
		"Auto", "test", 1, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_jobs SET demand_active=true
		WHERE scope=$1`, scope); err != nil {
		t.Fatal(err)
	}

	// Prior to this regression fix, an older due shadow job prevented
	// the auto-kind worker from claiming its own eligible job.
	auto, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotAutoKind)
	if err != nil || auto == nil || auto.TopicKey != "younger-auto" {
		t.Fatalf("auto starved by shadow backlog: job=%+v err=%v", auto, err)
	}
	shadow, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotShadowKind)
	if err != nil || shadow == nil || shadow.TopicKey != "older-shadow" {
		t.Fatalf("shadow claim failed: job=%+v err=%v", shadow, err)
	}
	third, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotAutoKind)
	if err != nil || third != nil {
		t.Fatalf("unexpected extra claim: job=%+v err=%v", third, err)
	}
}
