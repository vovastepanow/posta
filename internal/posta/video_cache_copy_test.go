package posta

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"posta/internal/config"
)

func TestMediaVideoCopyIsAtomicAndRequiresIdentity(t *testing.T) {
	a := &App{Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	u := a.User(context.Background(), "test", nil)
	file := filepath.Join(t.TempDir(), "source.mp4")
	data := append([]byte{0, 0, 0, 24}, []byte("ftypmp42")...)
	data = append(data, bytes.Repeat([]byte{1}, 1024)...)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if local := u.MediaStoreVideo("", file, true, nil); local != "" {
		t.Fatal("empty video URL created a shared cache file")
	}
	key := "https://example.org/video/1"
	local := u.MediaStoreVideo(key, file, true, nil)
	if local == "" {
		t.Fatal("video not stored")
	}
	got, err := os.ReadFile(a.MediaPath(local))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("video bytes changed")
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if newLocal := u.MediaStoreVideo(key, file, true, nil); newLocal != "" {
		t.Fatal("empty video replaced valid cache")
	}
	if got, err := os.ReadFile(a.MediaPath(local)); err != nil || !bytes.Equal(got, data) {
		t.Fatal("failed video write damaged previous cache")
	}
}
