package web

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"posta/internal/config"
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

func TestPublishUploadPaths(t *testing.T) {
	for _, field := range []string{"files", "files[]"} {
		t.Run(field, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for _, content := range []string{"one", "two"} {
				part, err := writer.CreateFormFile(field, "../same.jpg")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write([]byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/", &body)
			r.Header.Set("Content-Type", writer.FormDataContentType())
			c := &Ctx{W: httptest.NewRecorder(), R: r}
			if err := parseUploadForm(c, publishRequestMaxBytes, 32<<20); err != nil {
				t.Fatal(err)
			}
			defer r.MultipartForm.RemoveAll()
			uploads, err := publishUploadPaths(r.MultipartForm, t.TempDir())
			if err != nil || len(uploads) != 2 || uploads["0"] == uploads["1"] {
				t.Fatalf("uploads=%v error=%v", uploads, err)
			}
			for key, want := range map[string]string{"0": "one", "1": "two"} {
				b, err := os.ReadFile(uploads[key])
				if err != nil || string(b) != want {
					t.Fatalf("%s: data=%q error=%v", key, b, err)
				}
			}
		})
	}
}

func TestPublishUploadPathsRejectsEmptyOrExcessFiles(t *testing.T) {
	for _, count := range []int{1, 2*config.TelegramMaxMedia + 1} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for i := 0; i < count; i++ {
			if _, err := writer.CreateFormFile("files[]", "empty.jpg"); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		c := &Ctx{W: httptest.NewRecorder(), R: r}
		if err := parseUploadForm(c, publishRequestMaxBytes, 32<<20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		uploads, err := publishUploadPaths(r.MultipartForm, t.TempDir())
		var apiErr *posta.Error
		if len(uploads) != 0 || !errors.As(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("count=%d: uploads=%v error=%v", count, uploads, err)
		}
	}
}
