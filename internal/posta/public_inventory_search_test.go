package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPublicInventorySearchPostgres(t *testing.T) {
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
	source := "integration-inventory-" + randHex(8)
	blocked := source + "-blocked"
	defer d.Pool.Exec(ctx, "DELETE FROM public_posts WHERE source_key=ANY($1::text[])", []string{source, blocked})
	matching := "https://example.com/inventory/matching"
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{
		{"url": matching, "text": "Продвижение социальных сетей"},
		{"url": "https://example.com/inventory/english", "title": "Running campaigns"},
		{"url": "https://example.com/inventory/irrelevant", "text": "Вязание крючком"},
		{"url": "https://example.com/inventory/incomplete", "text": "Продвижение социальных сетей", "text_incomplete": true},
		{"url": "https://example.com/inventory/sensitive", "text": "Продвижение социальных сетей", "source_sensitive": true},
	})
	a.storePublicPosts(ctx, PlatformRSS, blocked, []jx.M{
		{"url": "https://example.com/inventory/blocked", "text": "Продвижение социальных сетей"},
	})
	find := func(query string) []string {
		t.Helper()
		rows, err := d.Pool.Query(ctx, publicInventorySearchSQL, query, []string{source}, 240)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var urls []string
		for rows.Next() {
			var platform string
			var raw []byte
			if err := rows.Scan(&platform, &raw); err != nil {
				t.Fatal(err)
			}
			urls = append(urls, jx.Str(jx.Unmarshal(raw), "url"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return urls
	}
	// The relevant post has no topic label at all. Inflection must not hide it.
	if got := find("социальные сети"); len(got) != 1 || got[0] != matching {
		t.Fatalf("unexpected Russian candidates: %v", got)
	}
	if got := find("run campaign"); len(got) != 1 || got[0] != "https://example.com/inventory/english" {
		t.Fatalf("unexpected English candidates: %v", got)
	}
	// A content refresh must update the index without an AI classification.
	a.storePublicPosts(ctx, PlatformRSS, source, []jx.M{{"url": matching, "text": "История Древнего Рима"}})
	if got := find("социальные сети"); len(got) != 0 {
		t.Fatalf("stale indexed content: %v", got)
	}
	if got := find("Древний Рим"); len(got) != 1 || got[0] != matching {
		t.Fatalf("updated post missing: %v", got)
	}
}
