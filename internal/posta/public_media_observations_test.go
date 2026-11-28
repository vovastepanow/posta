package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPublicMediaObservationSearchPostgres(t *testing.T) {
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
	a := &App{DB: d}
	source := "media-observation-" + randHex(8)
	defer d.Pool.Exec(ctx, "DELETE FROM public_posts WHERE source_key=$1", source)
	post := jx.M{"url": "https://example.com/image-only", "image": "https://example.com/picture.jpg"}
	key := CandidatePostKey(post)
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{post})
	if _, err := d.Pool.Exec(ctx, publicMediaObservationSaveSQL, source, key, "Кот спит на клавиатуре", now()); err != nil {
		t.Fatal(err)
	}
	find := func() int {
		t.Helper()
		rows, err := d.Pool.Query(ctx, publicMediaSearchSQL, "кот клавиатура", []string{source}, 120)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := find(); n != 1 {
		t.Fatalf("image-only result count=%d, want 1", n)
	}
	post["image"] = "https://example.com/replacement.jpg"
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{post})
	if n := find(); n != 0 {
		t.Fatalf("stale media observation count=%d", n)
	}
}
