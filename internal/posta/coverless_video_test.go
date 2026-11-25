package posta

import (
	"context"
	"os"
	"strings"
	"testing"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

func TestCoverlessVideoIdentity(t *testing.T) {
	page := "https://www.youtube.com/shorts/abcdefghijk"
	item := jx.M{"video_via": "ytdlp", "video_page": page}
	key := ContentItemKey(item)
	job := VideoJob{Key: key, Page: page, Via: "ytdlp"}
	if key == "" || !videoJobMatchesPoolItem(item, job) {
		t.Fatal("coverless video is not addressable")
	}
	item["video"] = "media/cached.mp4"
	if ContentItemKey(item) != key {
		t.Fatal("download changed the stable identity")
	}
	other := jx.M{"video_via": "ytdlp", "video_page": page + "2"}
	if videoJobMatchesPoolItem(other, job) {
		t.Fatal("download matched another video")
	}
	item["image"], other["image"] = "same-cover", "same-cover"
	job.Key = "same-cover"
	if !videoJobMatchesPoolItem(item, job) || videoJobMatchesPoolItem(other, job) {
		t.Fatal("shared cover caused incorrect video attachment")
	}
	if PoolFindItem([]jx.M{{"text": "short"}}, "") != nil {
		t.Fatal("empty key matched an unrelated record")
	}
}

func TestLostCoverlessVideoKeepsTextPostPostgres(t *testing.T) {
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
	u := (&App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "media"}}).User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	video := jx.M{"video": "https://example.org/v.mp4", "video_local": "media/" + strings.Repeat("a", 32) + ".mp4", "source_url": "https://example.org/video", "topic": "test"}
	text := jx.M{"text": "A perfectly usable text publication.", "text_post": true, "source_url": "https://example.org/text", "topic": "test"}
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{video, text} }); err != nil {
		t.Fatal(err)
	}
	shown := u.PoolWithoutLostVideo(u.Pool())
	stored := u.Pool()
	if len(shown) != 1 || len(stored) != 1 || ContentItemKey(stored[0]) != ContentItemKey(text) || ContentItemKey(shown[0]) != ContentItemKey(text) {
		t.Fatalf("missing video removed unrelated text: shown=%v stored=%v", shown, stored)
	}
}
