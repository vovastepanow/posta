package posta

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

// Explicit opt-in; this reaches one public search endpoint and never calls a
// model or changes the production catalog.
func TestAutopilotLivePublicRSS(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" || os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1 and an isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner, topic = "autopilot-live-smoke", "Технологии"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 1, HorizonDays: 1}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	err = runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 1, 1, 3, 3, func(work context.Context) error {
		result := a.User(work, "", nil).RSSGet("https://www.bing.com/news/search?q=technology&format=rss", rssMaxBytes, 15*time.Second, HTTPUA)
		if !result.OK || !rssIsFeed(rssLoad(result.Bytes)) {
			return fmt.Errorf("RSS unavailable or unrecognized: status=%d error=%s", result.Status, result.Error)
		}
		t.Logf("public RSS HTTP %d; items=%d", result.Status, len(rssItems(rssLoad(result.Bytes))))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
