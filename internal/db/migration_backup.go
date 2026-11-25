package db

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Never pass the password in process arguments or print the child output:
// libpq diagnostics can include connection parameters.
func migrationDumpConnection(dsn string) (string, string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", "", fmt.Errorf("automatic backup requires a PostgreSQL URL")
	}
	var password string
	if u.User != nil {
		password, _ = u.User.Password()
		u.User = url.User(u.User.Username())
	}
	q := u.Query()
	if q.Has("password") {
		password = q.Get("password")
		q.Del("password")
	}
	u.RawQuery = q.Encode()
	return u.String(), password, nil
}

// BackupBeforeMigration writes a private custom-format archive and validates
// its table of contents before allowing migration. Reuse a recent archive
// for the same batch if systemd is retrying a failed startup.
func BackupBeforeMigration(ctx context.Context, dsn, dataDir string, pending []string) (string, error) {
	safeDSN, password, err := migrationDumpConnection(dsn)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "postgres-migration-backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(safeDSN+"\n"+strings.Join(pending, "\n"))))[:16]
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	verify := func(path string) error {
		return exec.CommandContext(ctx, "pg_restore", "--list", path).Run()
	}
	previous, err := filepath.Glob(filepath.Join(dir, key+"-*.dump"))
	if err != nil {
		return "", err
	}
	for i := len(previous) - 1; i >= 0; i-- {
		info, err := os.Stat(previous[i])
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 && time.Since(info.ModTime()) < 15*time.Minute && verify(previous[i]) == nil {
			return previous[i], nil
		}
	}
	f, err := os.CreateTemp(dir, ".pending-*.dump")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Close(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "pg_dump", "--no-password", "--format=custom", "--file", tmp, "--dbname", safeDSN)
	cmd.Env = os.Environ()
	if password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+password)
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pg_dump failed: %w", err)
	}
	if err := verify(tmp); err != nil {
		return "", fmt.Errorf("pg_restore archive verification failed: %w", err)
	}
	archive := filepath.Join(dir, key+"-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".dump")
	if err := os.Rename(tmp, archive); err != nil {
		return "", err
	}
	return archive, nil
}
