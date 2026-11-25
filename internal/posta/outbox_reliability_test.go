package posta

import (
	"context"
	"os"
	"slices"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestOutboxEditAndClaimPostgres(t *testing.T) {
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
	uid := randHex(16)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	at := now()
	ready, retry, sending := randHex(12), randHex(12), randHex(12)
	items := []jx.M{
		{"id": ready, "status": "queued", "publish_at": at - 5},
		{"id": retry, "status": "queued", "publish_at": at - 100, "next_try_at": at + 1000},
		{"id": sending, "status": "sending", "publish_at": at - 10, "sending_at": at},
	}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return items }); err != nil {
		t.Fatal(err)
	}
	version := func(id string) string {
		t.Helper()
		var v string
		if err := d.Pool.QueryRow(ctx, `SELECT ctid::text FROM outbox WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := version(sending)
	ids, err := a.outboxDueIDs(ctx, at)
	if err != nil || !slices.Contains(ids, ready) || slices.Contains(ids, retry) || slices.Contains(ids, sending) {
		t.Fatalf("incorrect due list: %v (%v)", ids, err)
	}
	if _, err := u.OutboxUpdate(func(q []jx.M) []jx.M {
		for _, p := range q {
			if jx.Str(p, "id") == ready {
				p["publish_at"] = at + 3600
			}
		}
		return q
	}); err != nil {
		t.Fatal(err)
	}
	if version(sending) != before {
		t.Fatal("unrelated edit rewrote in-flight publication")
	}
	if _, _, err := a.outboxClaimDue(ctx, ready, at); err == nil {
		t.Fatal("rescheduled post was claimed too early")
	}
	if _, _, err := a.outboxClaimDue(ctx, retry, at); err == nil {
		t.Fatal("post in backoff was claimed too early")
	}
	if _, post, err := a.outboxClaimDue(ctx, ready, at+3600); err != nil || jx.Str(post, "status") != "sending" {
		t.Fatalf("due post was not claimed: %v", err)
	}
	if _, _, err := a.outboxClaimDue(ctx, ready, at+3600); err == nil {
		t.Fatal("claimed post was claimed a second time")
	}
}
