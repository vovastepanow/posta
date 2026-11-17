package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotSupplyCountsUniqueQueueAndVideos(t *testing.T) {
	first := jx.M{"url": "https://example.org/posts/1", "image": "https://example.org/shared.jpg", "video": "https://example.org/1.mp4"}
	second := jx.M{"url": "https://example.org/posts/2", "image": "https://example.org/shared.jpg", "video": "https://example.org/2.mp4"}
	queued := jx.M{"repeat_identity": jx.M{"keys": repeatKeys(first)}}
	count := autopilotSupplyCount([]jx.M{first, second}, []jx.M{queued}, nil, func(jx.M) bool { return true }, func(jx.M) bool { return true })
	if count != 2 {
		t.Fatalf("count=%d, want queued first video and distinct second video", count)
	}
	count = autopilotSupplyCount([]jx.M{first, second}, nil, map[string]bool{repeatKeys(first)[0]: true}, func(jx.M) bool { return true }, func(jx.M) bool { return true })
	if count != 1 {
		t.Fatalf("remembered post counted again: %d", count)
	}
}

func TestAutopilotAvailableSupplyPostgres(t *testing.T) {
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
	const owner = "autopilot-supply-test"
	const topic = "Проверка запаса"
	if _, err = d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner) })
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	u := a.User(ctx, owner, nil)
	if err = u.SaveSettings(jx.M{"topic": topic, "active_sources_only": false}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Unix()
	first := jx.M{"url": "https://example.org/posts/1", "image": "https://example.org/shared.jpg", "video": "https://example.org/1.mp4", "source_key": "rss:https://example.org/one", "topic": topic, "posted_at": at}
	second := jx.M{"url": "https://example.org/posts/2", "image": "https://example.org/shared.jpg", "video": "https://example.org/2.mp4", "source_key": "rss:https://example.org/two", "topic": topic, "posted_at": at}
	for i, item := range []jx.M{first, second} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO pool_items(user_id,position,image_key,topic,data) VALUES($1,$2,$3,$4,$5)`, owner, i, ContentItemKey(item), topic, db.JSON(item)); err != nil {
			t.Fatal(err)
		}
	}
	queued := jx.M{"topic": topic, "source_key": "rss:https://example.org/one", "publish_at": at + 3600, "repeat_identity": jx.M{"keys": repeatKeys(first)}}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO outbox(id,user_id,publish_at,status,data) VALUES('autopilot-supply-post',$1,$2,'queued',$3)`, owner, at+3600, db.JSON(queued)); err != nil {
		t.Fatal(err)
	}
	count, err := u.AutopilotAvailableSupply(topic)
	if err != nil || count != 2 {
		t.Fatalf("supply=%d err=%v", count, err)
	}
	if err = u.SourceOverrideSet("rss:https://example.org/one", false); err != nil {
		t.Fatal(err)
	}
	count, err = u.AutopilotAvailableSupply(topic)
	if err != nil || count != 1 {
		t.Fatalf("excluded supply=%d err=%v", count, err)
	}
}
