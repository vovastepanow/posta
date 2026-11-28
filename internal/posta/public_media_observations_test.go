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
	vector := make([]float32, 512)
	vector[0] = 1
	var revision int64
	if err := d.Pool.QueryRow(ctx, "SELECT media_observation_revision FROM public_posts WHERE source_key=$1 AND post_key=$2", source, key).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	save := func(rev int64) int64 {
		t.Helper()
		tag, err := d.Pool.Exec(ctx, publicEmbeddingSaveWithObservationSQL, source, key, jx.Marshal(post), "test-media-v2", vector, now(), rev)
		if err != nil {
			t.Fatal(err)
		}
		return tag.RowsAffected()
	}
	if revision == 0 || save(revision) != 1 {
		t.Fatal("fresh media revision could not be indexed")
	}
	if _, err := d.Pool.Exec(ctx, publicMediaObservationSaveSQL, source, key, "Кошка лежит на клавиатуре", now()); err != nil {
		t.Fatal(err)
	}
	if save(revision) != 0 {
		t.Fatal("stale embedding was written after new media evidence")
	}
	var count int
	if err := d.Pool.QueryRow(ctx, "SELECT count(*) FROM public_post_embeddings WHERE source_key=$1 AND post_key=$2", source, key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old vector was not invalidated: count=%d err=%v", count, err)
	}
	post["image"] = "https://example.com/replacement.jpg"
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{post})
	if n := find(); n != 0 {
		t.Fatalf("stale media observation count=%d", n)
	}
}
