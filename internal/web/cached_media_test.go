package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCachedMediaNotBrowsable(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("a", 32) + ".jpg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	h := staticFiles(dir, time.Hour)
	for _, path := range []string{"/", "/index.html", "/../index.html", "/sub/"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+name, nil))
	if w.Code != http.StatusOK || w.Body.String() != "media" {
		t.Fatalf("valid file: %d %q", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(w.Header().Get("Cache-Control"), "private") {
		t.Fatal("shared proxy caching allowed")
	}
}
