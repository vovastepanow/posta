package db

import (
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
