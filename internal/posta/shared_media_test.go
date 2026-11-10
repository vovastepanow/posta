package posta

import (
	"bytes"
	"os"
	"testing"

	"posta/internal/config"
)

func TestSharedMediaVisibleAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	first := &App{Cfg: config.Server{MediaDir: dir, MediaURL: "media_cache"}}
	second := &App{Cfg: config.Server{MediaDir: dir, MediaURL: "media_cache"}}
	data := []byte("shared media")
	url, err := first.MediaWrite(MediaCacheName("https://example.com/item", "jpg"), data)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.MediaExisting(url); got != url {
		t.Fatalf("second instance cannot find media: %q", got)
	}
	got, err := os.ReadFile(second.MediaPath(url))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("second instance read: %q, %v", got, err)
	}
}
