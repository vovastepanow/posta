package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestVotePoolAtomicPostgres(t *testing.T) {
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
	uid := randHex(16)
	u := (&App{DB: d, Mem: NewMemCache(100)}).User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	item := jx.M{"image": "https://example.org/" + uid + ".jpg", "topic": "test", "pooled_at": now()}
	key := ContentItemKey(item)
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{item} }); err != nil {
		t.Fatal(err)
	}

	// Force only this synthetic user's history write to fail; no other
	// user's data is affected. The constraint exists only in the test DB.
	constraint := "test_vote_" + uid
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE history_items ADD CONSTRAINT `+constraint+` CHECK (user_id <> '`+uid+`')`); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(ctx, `ALTER TABLE history_items DROP CONSTRAINT IF EXISTS `+constraint)
	if _, _, _, _, err := u.VotePool(key, "like", nil); err == nil {
		t.Fatal("expected history insert to fail")
	}
	if PoolFindItem(u.Pool(), key) == nil || len(u.History()["liked"]) != 0 {
		t.Fatal("failed vote must leave the pool and history unchanged")
	}
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE history_items DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	if _, _, found, record, err := u.VotePool(key, "like", nil); err != nil || found == nil || record == nil {
		t.Fatalf("vote failed: %v", err)
	}
	if PoolFindItem(u.Pool(), key) != nil || len(u.History()["liked"]) != 1 {
		t.Fatal("successful vote must move the post exactly once")
	}
	if _, _, found, _, err := u.VotePool(key, "like", nil); err != nil || found != nil || len(u.History()["liked"]) != 1 {
		t.Fatalf("repeated vote was not a no-op: %v", err)
	}
}
