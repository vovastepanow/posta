package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
)

func TestAutopilotPublicFetchPostgres(t *testing.T) {
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
	for _, id := range []string{"autopilot-fetch-a", "autopilot-fetch-b"} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		if err = saveAutopilotDemand(ctx, d.Pool, id, autopilotDemandSettings{Topic: "Тест выборки", Mode: "shadow", PostsPerDay: 1, HorizonDays: 1}, 1); err != nil {
			t.Fatal(err)
		}
	}
	key := autopilotTopicKey("Тест выборки")
	if _, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_collection_passes WHERE topic_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_collection_passes WHERE topic_key=$1`, key)
	})
	for i := 0; i < 2; i++ {
		if err = recordAutopilotPublicFetch(ctx, d.Pool, "Тест выборки", "rss:test", 12000, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err = recordAutopilotPublicFetch(ctx, d.Pool, "Другая тема", "rss:test", 12000, 3); err != nil {
		t.Fatal(err)
	}
	if err = recordAutopilotPublicFetch(ctx, d.Pool, "Тест выборки", "rss:test", 13200, 3); err != nil {
		t.Fatal(err)
	}
	var count, fetched int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(fetched_count),0) FROM source_autopilot_collection_passes WHERE source_id='rss:test'`).Scan(&count, &fetched); err != nil || count != 2 || fetched != 3 {
		t.Fatalf("count=%d fetched=%d err=%v", count, fetched, err)
	}
	stats, err := autopilotPublicFetchWindow(ctx, d.Pool, "Тест выборки", "rss:test", 12000, 13200)
	if err != nil || stats.SuccessfulPasses != 1 || stats.FetchedItems != 0 || stats.LastSuccess != 12000 {
		t.Fatalf("window=%+v err=%v", stats, err)
	}
}
