package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationDumpConnection(t *testing.T) {
	for _, dsn := range []string{
		"postgres://posta:s%40cret@localhost:5432/posta?sslmode=disable",
		"postgresql://posta@localhost/posta?password=s%40cret&sslmode=disable",
	} {
		safe, password, err := migrationDumpConnection(dsn)
		if err != nil || password != "s@cret" || strings.Contains(safe, "cret") || !strings.Contains(safe, "sslmode=disable") {
			t.Fatal("password separation or connection parameter preservation failed")
		}
	}
	if _, _, err := migrationDumpConnection("host=localhost password=secret"); err == nil {
		t.Fatal("unsupported connection format must fail closed")
	}
}

func TestMigrationBackupPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an isolated PostgreSQL test database")
	}
	ctx := context.Background()
	d, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var pendingCount int
	// Simulate a failed backup: no migration may be applied.
	sentinel := errors.New("backup unavailable")
	applied, err := d.MigrateWithBackup(ctx, func(pending []string) error {
		pendingCount = len(pending)
		return sentinel
	})
	if pendingCount == 0 && err == nil {
		t.Skip("database already migrated; use a fresh CI database")
	}
	if !errors.Is(err, sentinel) || len(applied) != 0 {
		t.Fatalf("failed backup did not stop migration: %v %v", applied, err)
	}
	dir := t.TempDir()
	var archive string
	applied, err = d.MigrateWithBackup(ctx, func(pending []string) error {
		var err error
		archive, err = BackupBeforeMigration(ctx, dsn, dir, pending)
		return err
	})
	if err != nil || len(applied) != pendingCount {
		t.Fatalf("backup/migrate failed: %v %v", applied, err)
	}
	if info, err := os.Stat(archive); err != nil || info.Size() == 0 || info.Mode().Perm() != 0600 || filepath.Dir(archive) != filepath.Join(dir, "postgres-migration-backups") {
		t.Fatalf("backup missing or unsafe permissions: %v", err)
	}
	applied, err = d.MigrateWithBackup(ctx, func([]string) error {
		return errors.New("unexpected backup with no pending migrations")
	})
	if err != nil || len(applied) != 0 {
		t.Fatalf("no-op migration was not a no-op: %v %v", applied, err)
	}
}
