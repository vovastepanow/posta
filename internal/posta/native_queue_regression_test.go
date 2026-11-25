package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

func TestNativeSettleAndSlotConflictPostgres(t *testing.T) {
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
	u := (&App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{DataDir: t.TempDir()}}).User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	native := jx.M{"id": randHex(6), "status": "telegram", "publish_at": now()-360, "channel": "test", "text": "Native", "record": "native-key"}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{native} }); err != nil {
		t.Fatal(err)
	}
	constraint := "test_native_" + uid
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE published ADD CONSTRAINT `+constraint+` CHECK (user_id <> '`+uid+`')`); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(ctx, `ALTER TABLE published DROP CONSTRAINT IF EXISTS `+constraint)
	u.TelegramSettleNativePosts()
	if len(u.Outbox()) != 1 {
		t.Fatal("native row lost after history failure")
	}
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE published DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	u.TelegramSettleNativePosts()
	u.TelegramSettleNativePosts()
	if len(u.Outbox()) != 0 {
		t.Fatal("elapsed native row was not settled")
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM published WHERE user_id=$1 AND data->>'record'='native-key' AND data->>'source_unconfirmed'='true'`, uid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("native journal count=%d err=%v", count, err)
	}
	at := now()+3600
	ch := &Channel{ID: "test", Platform: "telegram"}
	if _, err := u.OutboxQueuePost(ch, "First", nil, at, &at, "", nil, nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := u.OutboxQueuePost(ch, "Second", nil, at+15, &at, "", nil, nil, "", "", nil); err == nil {
		t.Fatal("duplicate slot was accepted")
	}
	q := u.Outbox()
	if len(q) != 1 {
		t.Fatal("duplicate slot changed queue")
	}
	if u.TelegramUnqueuePost(jx.Str(q[0], "id")) {
		t.Fatal("native cancellation removed a local queued post")
	}
}
