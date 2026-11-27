package web

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"posta/internal/posta"
)

func uploadTestRequest(t *testing.T, data string) *Ctx {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	file, err := w.CreateFormFile("files", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return &Ctx{W: httptest.NewRecorder(), R: r}
}

func TestMultipartFilesEnforcesRequestLimit(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		c := uploadTestRequest(t, strings.Repeat("x", 2048))
		if chunked {
			c.R.ContentLength = -1
			c.R.TransferEncoding = []string{"chunked"}
		}
		files, cleanup, err := multipartFiles(c, "files", 1024)
		cleanup()
		var apiErr *posta.Error
		if len(files) != 0 || !errors.As(err, &apiErr) || apiErr.Status != 413 {
			t.Fatalf("chunked=%v: files=%v error=%v", chunked, files, err)
		}
	}
}

func TestMultipartFilesCopiesAndCleansUp(t *testing.T) {
	c := uploadTestRequest(t, "complete file")
	files, cleanup, err := multipartFiles(c, "files", 4096)
	defer cleanup()
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v error=%v", files, err)
	}
	data, err := os.ReadFile(files[0].Path)
	if err != nil || string(data) != "complete file" {
		t.Fatalf("data=%q error=%v", data, err)
	}
	cleanup()
	if _, err := os.Stat(files[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file not removed: %v", err)
	}
}
