package posta

import (
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
func (a *App) OutboxCleanupReplacedMedia(id string, before, after []jx.M) {
	root, err := a.OutboxMediaDir(id)
	if err != nil {
		return
	}
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
			if path == "" || keep[filepath.Clean(path)] || !outboxRegularFile(root, path) {
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
