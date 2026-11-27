package posta

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"posta/internal/jx"
)

// Stored queue files may live in a committed edit-* directory. Verify the
// resolved path, not just its basename, and never copy another post's file.
func outboxRegularFile(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err = filepath.Rel(resolvedRoot, resolvedPath)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// Called only after the new queue payload has committed. Remove obsolete
// files, never the active revision or a shared source cache entry.
func (a *App) OutboxCleanupReplacedMedia(ctx context.Context, id string, before, after []jx.M) {
	root, err := a.OutboxMediaDir(id)
	if err != nil {
		return
	}
	outboxCleanupReplacedMedia(root, before, after, func(path string) bool {
		return a.outboxFileReferenced(ctx, path, false)
	})
}

func outboxCleanupReplacedMedia(root string, before, after []jx.M, referenced func(string) bool) {
	keep := map[string]bool{}
	for _, media := range after {
		for _, key := range []string{"path", "cover_path"} {
			if path := jx.Str(media, key); path != "" {
				keep[filepath.Clean(path)] = true
			}
		}
	}
	dirs := map[string]bool{}
	for _, media := range before {
		for _, key := range []string{"path", "cover_path"} {
			path := jx.Str(media, key)
			if path == "" || keep[filepath.Clean(path)] || !outboxRegularFile(root, path) || referenced(path) {
				continue
			}
			if os.Remove(path) == nil {
				parent := filepath.Dir(path)
				if filepath.Dir(parent) == root && strings.HasPrefix(filepath.Base(parent), "edit-") {
					dirs[parent] = true
				}
			}
		}
	}
	for dir := range dirs {
		_ = os.Remove(dir) // only removes an empty revision directory
	}
}

// Old duplicates may still reference a cover inside another post's
// directory. Retain the file until the final reference disappears, and
// fail closed when PostgreSQL is unavailable.
func (a *App) outboxFileReferenced(ctx context.Context, path string, directory bool) bool {
	if a.DB == nil || path == "" {
		return true
	}
	if directory {
		path = filepath.Clean(path) + string(os.PathSeparator)
	}
	var used bool
	err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM outbox o CROSS JOIN LATERAL jsonb_array_elements(
			CASE WHEN jsonb_typeof(o.data->'media')='array' THEN o.data->'media' ELSE '[]'::jsonb END
		) f WHERE CASE WHEN $2 THEN
			left(f->>'path',length($1))=$1 OR left(f->>'cover_path',length($1))=$1
		ELSE f->>'path'=$1 OR f->>'cover_path'=$1 END
	)`, path, directory).Scan(&used)
	return err != nil || used
}

func (a *App) removeOutboxDirectory(ctx context.Context, id string) {
	root, err := a.OutboxMediaDir(id)
	if err == nil && !a.outboxFileReferenced(ctx, root, true) {
		_ = os.RemoveAll(root)
	}
}

// A repair may restore a committed edit-* revision as well as an original
// flat payload. Never follow a symlink introduced in either directory.
func outboxRepairDestination(root, dest string) bool {
	rel, err := filepath.Rel(root, dest)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return false
	}
	parent := filepath.Dir(rel)
	if parent != "." && (filepath.Dir(parent) != "." || !strings.HasPrefix(parent, "edit-")) {
		return false
	}
	for _, dir := range []string{root, filepath.Dir(dest)} {
		info, err := os.Lstat(dir)
		if err != nil && !os.IsNotExist(err) || err == nil && !info.IsDir() {
			return false
		}
	}
	return true
}
