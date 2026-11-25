package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPoolUpdatePreservesUnchangedRowsPostgres(t *testing.T) {
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
	first := jx.M{"image": "https://example.org/a.jpg", "topic": "test", "pooled_at": now()}
	second := jx.M{"image": "https://example.org/b.jpg", "topic": "test", "pooled_at": now()}
	third := jx.M{"image": "https://example.org/c.jpg", "topic": "test", "pooled_at": now()}
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{first, second} }); err != nil {
		t.Fatal(err)
	}
	read := func(image string) (id int64, position int32, version string) {
		t.Helper()
		if err := d.Pool.QueryRow(ctx, `SELECT id,position,ctid::text FROM pool_items WHERE user_id=$1 AND image_key=$2`, uid, image).Scan(&id, &position, &version); err != nil {
			t.Fatal(err)
		}
		return
	}
	bID, _, bVersion := read(ContentImageKey(second))
	if _, err := u.PoolUpdate(func(pool []jx.M) []jx.M { return append(pool, third) }); err != nil {
		t.Fatal(err)
	}
	if id, pos, version := read(ContentImageKey(second)); id != bID || pos != 1 || version != bVersion {
		t.Fatal("appending rewrote an unchanged row")
	}
	if _, err := u.PoolUpdate(func(pool []jx.M) []jx.M { return pool[1:] }); err != nil {
		t.Fatal(err)
	}
	if id, pos, _ := read(ContentImageKey(second)); id != bID || pos != 0 {
		t.Fatal("deletion changed the remaining row identity or left incorrect order")
	}
	pool := u.Pool()
	if len(pool) != 2 || ContentImageKey(pool[0]) != ContentImageKey(second) || ContentImageKey(pool[1]) != ContentImageKey(third) {
		t.Fatalf("unexpected pool: %v", pool)
	}
	if _, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
		pool[0]["text"] = "edited"
		return pool
	}); err != nil {
		t.Fatal(err)
	}
	if pool := u.Pool(); len(pool) != 2 || jx.Str(pool[0], "text") != "edited" {
		t.Fatal("in-place callback mutation was not persisted")
	}
}
