package posta

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"posta/internal/config"
	"posta/internal/db"
)

func TestMediaCleanupKeepsFilesOnMissingSnapshot(t *testing.T) {
	a := &App{Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	u := a.User(context.Background(), "test", nil)
	name := MediaCacheName("https://example.org/photo.jpg", "jpg")
	file := filepath.Join(a.Cfg.MediaDir, name)
	if err := os.WriteFile(file, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if n := u.MediaCleanup(nil, true, true); n != 0 || !fileExists(file) {
		t.Fatal("cleanup deleted a file without a database snapshot")
	}
}

func TestMediaRemoveKeepsFilesOnDatabaseFailurePostgres(t *testing.T) {
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
	a := &App{DB: d, Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	name := MediaCacheName("https://example.org/photo.jpg", "jpg")
	file := filepath.Join(a.Cfg.MediaDir, name)
	if err := os.WriteFile(file, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if items := a.AllUsersPoolItems(cancelled); items != nil {
		t.Fatal("failed snapshot reported success")
	}
	a.MediaRemove(cancelled, a.Cfg.MediaURL+"/"+name)
	if !fileExists(file) {
		t.Fatal("database failure deleted a file")
	}
}
