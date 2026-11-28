package posta

import (
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkMultipartFileContentAndCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upload")
	data := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, ct, cleanup, err := networkMultipartFile(map[string]string{"message": "hello"}, map[string][]byte{"metadata": []byte(`{"ok":true}`)}, map[string]string{"z": path, "a": path})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(f, params["boundary"])
	for _, want := range []struct {
		name, filename, contentType, body string
	}{
		{"message", "", "", "hello"},
		{"metadata", "", "application/json", `{"ok":true}`},
		{"a", "upload.jpg", "image/jpeg", string(data)},
		{"z", "upload.jpg", "image/jpeg", string(data)},
	} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(part)
		if err != nil || part.FormName() != want.name || part.FileName() != want.filename || part.Header.Get("Content-Type") != want.contentType || string(raw) != want.body {
			t.Fatalf("unexpected part %q: %v %v", part.FormName(), part.Header, err)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("unexpected extra part: %v", err)
	}
	name := f.Name()
	cleanup()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("temporary upload was not removed: %v", err)
	}
	if f, _, cleanup, err := networkMultipartFile(nil, nil, map[string]string{"file": filepath.Join(dir, "missing")}); err == nil || f != nil {
		cleanup()
		t.Fatal("missing input accepted")
	}
}
