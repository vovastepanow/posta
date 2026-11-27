package web

import (
	"io"
	"mime/multipart"
	"os"
	"strconv"

	"posta/internal/config"
)

const publishRequestMaxBytes = 400 << 20

// publishUploadPaths uses the same limits and temporary-file handling for
// new posts and queue edits. The caller owns dir and must remove it.
func publishUploadPaths(form *multipart.Form, dir string) (map[string]string, error) {
	uploads := map[string]string{}
	files := form.File["files[]"]
	if len(files) == 0 {
		files = form.File["files"]
	}
	if len(files) > 2*config.TelegramMaxMedia {
		return nil, fail(400, "Слишком много файлов.")
	}
	for i, fh := range files {
		if fh.Size <= 0 || fh.Size > config.PublishUploadMaxBytes {
			return nil, fail(400, "Файл пуст или превышает локальный предел загрузки.")
		}
		path, err := copyPublishUpload(fh, dir)
		if err != nil {
			return nil, err
		}
		uploads[strconv.Itoa(i)] = path
	}
	return uploads, nil
}

func copyPublishUpload(fh *multipart.FileHeader, dir string) (string, error) {
	src, err := fh.Open()
	if err != nil {
		return "", fail(400, "Не удалось прочитать загруженный файл.")
	}
	defer src.Close()
	dst, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return "", fail(500, "Не удалось сохранить загруженный файл.")
	}
	n, copyErr := io.Copy(dst, io.LimitReader(src, config.PublishUploadMaxBytes+1))
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil || n != fh.Size || n > config.PublishUploadMaxBytes {
		os.Remove(dst.Name())
		return "", fail(400, "Не удалось полностью сохранить файл.")
	}
	return dst.Name(), nil
}
