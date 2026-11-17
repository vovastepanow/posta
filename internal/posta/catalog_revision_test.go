package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
)

func TestCatalogRejectsStaleProcessPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
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
	a, b := &App{DB: d}, &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err = b.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	start, result := make(chan struct{}), make(chan error, 2)
	go func() {
		<-start
		_, err := b.UpdateCatalog(ctx, func(c *SourceDB) bool { c.Discovery["revision_test_second"] = "second"; return true })
		result <- err
	}()
	go func() {
		<-start
		_, err := a.UpdateCatalog(ctx, func(c *SourceDB) bool { c.Discovery["revision_test"] = "first"; return true })
		result <- err
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	var value string
	if err = d.Pool.QueryRow(ctx, `SELECT value->>'revision_test' FROM kv WHERE key='discovery'`).Scan(&value); err != nil || value != "first" {
		t.Fatalf("catalog value %q: %v", value, err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT value->>'revision_test_second' FROM kv WHERE key='discovery'`).Scan(&value); err != nil || value != "second" {
		t.Fatalf("second value %q: %v", value, err)
	}
	if err = a.refreshCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Catalog().Discovery["revision_test"] != "first" || a.Catalog().Discovery["revision_test_second"] != "second" {
		t.Fatal("reader did not refresh both committed changes")
	}
}
