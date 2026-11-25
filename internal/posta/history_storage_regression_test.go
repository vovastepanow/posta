package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestHistoryDeltaAndCancelGuardPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
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
	uid := randHex(16)
	u := (&App{DB: d, Mem: NewMemCache(100)}).User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	first := jx.M{"image": "https://example.org/a.jpg", "timestamp": now()}
	second := jx.M{"image": "https://example.org/b.jpg", "timestamp": now()}
	if err := u.HistoryAppend("liked", first); err != nil {
		t.Fatal(err)
	}
	if err := u.HistoryAppend("liked", second); err != nil {
		t.Fatal(err)
	}
	read := func() (int64, string) {
		t.Helper()
		var id int64
		var version string
		if err := d.Pool.QueryRow(ctx, `SELECT id,ctid::text FROM history_items WHERE user_id=$1 AND image_key=$2`, uid, ContentImageKey(second)).Scan(&id, &version); err != nil {
			t.Fatal(err)
		}
		return id, version
	}
	id, version := read()
	key := ContentItemKey(first)
	if u.ContentMarkPosted(key) != "liked" {
		t.Fatal("could not mark post")
	}
	if gotID, gotVersion := read(); gotID != id || gotVersion != version {
		t.Fatal("mark rewrote unrelated history row")
	}
	q := jx.M{"id": randHex(6), "status": "queued", "publish_at": now()+3600, "record": key}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{q} }); err != nil {
		t.Fatal(err)
	}
	if u.ContentUnmarkPosted(key) {
		t.Fatal("restored post still in queue")
	}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{} }); err != nil {
		t.Fatal(err)
	}
	if !u.ContentUnmarkPosted(key) {
		t.Fatal("post was not restored after cancellation")
	}
	if u.ContentMarkPosted(key) != "liked" {
		t.Fatal("could not mark again")
	}
	if err := u.PublishedAdd(jx.M{"record": key, "status": "ok", "at": now()}); err != nil {
		t.Fatal(err)
	}
	if u.ContentUnmarkPosted(key) {
		t.Fatal("restored already sent post")
	}
}
