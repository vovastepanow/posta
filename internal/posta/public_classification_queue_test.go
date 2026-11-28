package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPublicClassificationQueuePostgres(t *testing.T) {
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
	source := "classification-queue-" + randHex(8)
	defer d.Pool.Exec(ctx, "DELETE FROM public_posts WHERE source_key=$1", source)
	image := jx.M{"url": "https://example.org/queue/image", "image": "https://example.org/image.jpg"}
	body := jx.M{"url": "https://example.org/queue/text", "text": "Text with existing labels"}
	private := jx.M{"url": "https://example.org/queue/private", "image": "https://example.org/private.jpg", "fetched_via": "telegram_mtproto"}
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{image, body, private})
	for _, p := range []jx.M{image, body, private} {
		if _, err := d.Pool.Exec(ctx, "INSERT INTO public_post_topics(source_key,post_key,topic_key) VALUES($1,$2,'test')", source, CandidatePostKey(p)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.Pool.Query(ctx, publicClassificationBackfillSQL, now()-86400, now()-3600, []string{source})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for rows.Next() {
		var sid, key string
		var raw []byte
		if err := rows.Scan(&sid, &key, &raw); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(keys) != 1 || keys[0] != CandidatePostKey(image) {
		t.Fatalf("unexpected media backfill: %v %v", keys, err)
	}
	var labels bool
	if err := d.Pool.QueryRow(ctx, publicClassificationClaimSQL, source, CandidatePostKey(image), now(), jx.Marshal(image)).Scan(&labels); err != nil || labels {
		t.Fatalf("tagged image must only be described: %v %v", labels, err)
	}
	if err := d.Pool.QueryRow(ctx, publicClassificationClaimSQL, source, CandidatePostKey(image), now(), jx.Marshal(image)).Scan(&labels); err == nil {
		t.Fatal("duplicate claim")
	}
	if _, err := d.Pool.Exec(ctx, publicMediaObservationSaveSQL, source, CandidatePostKey(image), "A cat on a keyboard", now()); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, publicClassificationClaimSQL, source, CandidatePostKey(image), now()+7200, jx.Marshal(image)).Scan(&labels); err == nil {
		t.Fatal("already described and tagged image was claimed again")
	}
}
