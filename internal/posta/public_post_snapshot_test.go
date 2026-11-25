package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPublicPostPartialRefreshPostgres(t *testing.T) {
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
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	source := "integration-snapshot-" + time.Now().Format("150405.000000000")
	defer d.Pool.Exec(ctx, "DELETE FROM public_posts WHERE source_key=$1", source)
	post := jx.M{"url": "https://example.com/post/1", "text": "Original complete text", "image": "https://example.com/1.jpg"}
	key := CandidatePostKey(post)
	load := func() jx.M {
		t.Helper()
		var raw []byte
		if err := d.Pool.QueryRow(ctx, "SELECT data FROM public_posts WHERE source_key=$1 AND post_key=$2", source, key).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return jx.Unmarshal(raw)
	}
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{post})
	for _, excerpt := range []string{"short", "short again"} {
		a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{{"url": jx.Str(post, "url"), "text": excerpt, "text_incomplete": true}})
		got := load()
		old := jx.Map(got, "previous_complete_snapshot")
		if jx.Str(got, "text") != excerpt || jx.Str(old, "text") != jx.Str(post, "text") || jx.Str(old, "image") != jx.Str(post, "image") || jx.Has(old, "previous_complete_snapshot") {
			t.Fatalf("incorrect snapshot retention: %v", got)
		}
	}
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{{"url": jx.Str(post, "url"), "text": "New complete version"}})
	if got := load(); jx.Has(got, "previous_complete_snapshot") || jx.Str(got, "text") != "New complete version" {
		t.Fatalf("old version retained after complete update: %v", got)
	}
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{{"url": jx.Str(post, "url"), "text": "Restricted", "source_sensitive": true, "text_incomplete": true}})
	if got := load(); jx.Has(got, "previous_complete_snapshot") {
		t.Fatalf("sensitive refresh retained previous content: %v", got)
	}
}
