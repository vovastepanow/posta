package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestVideoJobFollowsLikedRecordPostgres(t *testing.T) {
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
	a := &App{DB: d, Mem: NewMemCache(100)}
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	item := jx.M{"image": "https://example.org/cover.jpg", "video_via": "tgu", "video_page": "https://t.me/test/12", "url": "https://t.me/test/12", "pooled_at": now()}
	key := ContentItemKey(item)
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{item} }); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := u.VotePool(key, "like", nil); err != nil {
		t.Fatal(err)
	}
	if len(u.Pool()) != 0 {
		t.Fatal("vote did not move item")
	}
	job := VideoJob{UID: uid, Key: key, Page: jx.Str(item, "video_page"), Via: "tgu"}
	if err := a.videoAttach(ctx, job, "media/test.mp4"); err != nil {
		t.Fatal(err)
	}
	h := u.History()["liked"]
	if len(h) != 1 || jx.Str(h[0], "video_local") != "media/test.mp4" || jx.Bool(h[0], "video_unavailable") {
		t.Fatalf("download lost after vote: %v", h)
	}
}
