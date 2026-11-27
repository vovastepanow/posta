package posta

import (
	"context"
	"os"
	"strings"
	"testing"

	"posta/internal/db"
)

func TestPrivateMediaAccessPostgres(t *testing.T) {
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
	u := a.User(ctx, "test-owner", nil)
	name := MediaCacheName(randHex(16), "jpg")
	defer d.Pool.Exec(ctx, "DELETE FROM private_media WHERE name=$1", name)
	if !a.CachedMediaAllowed(ctx, "test-other", name) {
		t.Fatal("shared media not accessible")
	}
	if err := u.registerPrivateMedia(name); err != nil {
		t.Fatal(err)
	}
	if !a.CachedMediaAllowed(ctx, u.ID, name) || a.CachedMediaAllowed(ctx, "test-other", name) || a.CachedMediaAllowed(ctx, "", name) {
		t.Fatal("private cache access is not owner-scoped")
	}
	if a.CachedMediaAllowed(ctx, u.ID, strings.Repeat("../", 3)) {
		t.Fatal("invalid cache path")
	}
	bad, cancel := context.WithCancel(ctx)
	cancel()
	if a.CachedMediaAllowed(bad, u.ID, name) {
		t.Fatal("failed authorization must be denied")
	}
}
