package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestVisiblePoolQuotaKeepsHiddenFreshRecords(t *testing.T) {
	same := func(it jx.M) bool { return jx.Str(it, "topic") == "SMM" }
	visible := func(it jx.M) bool { return !jx.Bool(it, "hidden") }
	fresh := func(it jx.M) bool { return !jx.Bool(it, "expired") }
	hidden := jx.M{"topic": "SMM", "hidden": true, "pooled_at": 10}
	shown := jx.M{"topic": "SMM", "pooled_at": 20}
	other := jx.M{"topic": "Other", "pooled_at": 1}
	pool := []jx.M{hidden, hidden, other}
	if remove, ok := poolQuotaInsertion(pool, 2, same, visible, fresh); !ok || remove != -1 {
		t.Fatal("hidden records blocked a usable candidate or were deleted")
	}
	pool = append(pool, shown, shown)
	if _, ok := poolQuotaInsertion(pool, 2, same, visible, fresh); ok {
		t.Fatal("visible target was exceeded")
	}
	if _, ok := poolQuotaInsertion([]jx.M{hidden, hidden, hidden, hidden, other}, 2, same, visible, fresh); ok {
		t.Fatal("storage reserve was unbounded")
	}
	expired := jx.Merge(hidden, jx.M{"expired": true, "pooled_at": 2})
	if remove, ok := poolQuotaInsertion([]jx.M{hidden, other, expired, shown}, 2, same, visible, fresh); !ok || remove != 2 {
		t.Fatalf("oldest expired same-profile record was not replaced: %d %t", remove, ok)
	}
}

func TestVisiblePoolQuotaPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set; requires disposable database")
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
	a := &App{DB: d, Mem: NewMemCache(100)}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	uid := randHex(16)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	if err := u.SaveSettings(jx.M{"topic": "Мемы", "video_only": true, "fresh_only": false, "media_only": false, "media_no_text": false, "active_sources_only": false}); err != nil {
		t.Fatal(err)
	}
	photo := jx.M{"image": "https://example.org/p.jpg", "topic": "Мемы", "posted_at": now(), "pooled_at": now()}
	video := jx.M{"image": "https://example.org/v.jpg", "video_via": "ytdlp", "video_page": "https://www.youtube.com/watch?v=abcdefghijk", "topic": "Мемы", "posted_at": now(), "pooled_at": now()}
	other := jx.M{"image": "https://example.org/q.jpg", "topic": "Мемы ? коты", "posted_at": now(), "pooled_at": now()}
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{photo, video, other} }); err != nil {
		t.Fatal(err)
	}
	if count := u.PoolTopicCount(u.SearchTopicKey()); count != 1 {
		t.Fatalf("video-only quota = %d, want 1", count)
	}
	if count, allowed, err := u.poolRefillState(u.SearchTopicKey(), 2); err != nil || count != 1 || !allowed {
		t.Fatalf("video-only refill = %d %t %v", count, allowed, err)
	}
	if shown := u.PoolForCurrentTopic(); len(shown) != 1 || !PostItemHasVideo(shown[0]) {
		t.Fatalf("video-only view = %v", shown)
	}
	if err := u.SaveSettings(jx.M{"video_only": false}); err != nil {
		t.Fatal(err)
	}
	if count := u.PoolTopicCount(u.SearchTopicKey()); count != 2 {
		t.Fatalf("restored quota = %d, want 2", count)
	}
	if shown := u.PoolForCurrentTopic(); len(shown) != 2 {
		t.Fatalf("restored view = %v", shown)
	}
	if len(u.Pool()) != 3 {
		t.Fatal("filtering removed stored records")
	}
}
