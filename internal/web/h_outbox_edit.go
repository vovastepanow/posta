package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/posta"
)

func outboxRevision(post jx.M) string {
	sum := sha256.Sum256(jx.Marshal(post))
	return hex.EncodeToString(sum[:])
}

func editableOutboxPost(c *Ctx, id string) (jx.M, error) {
	if !postIDRe.MatchString(id) {
		return nil, fail(400, "Некорректный номер поста.")
	}
	for _, p := range c.U.Outbox() {
		if jx.Str(p, "id") == id {
			if !contains([]string{"queued", "failed"}, jx.Str(p, "status")) {
				return nil, fail(409, "Этот пост уже отправляется или находится в Telegram.")
			}
			return p, nil
		}
	}
	return nil, fail(404, "Пост не найден.")
}

func getOutboxEdit(c *Ctx) (any, error) {
	p, err := editableOutboxPost(c, c.Q("id"))
	if err != nil {
		return nil, err
	}
	id := jx.Str(p, "id")
	media := []jx.M{}
	for i, m := range jx.Maps(p, "media") {
		src := "index.php?" + url.Values{"action": {"outbox_media"}, "id": {id}, "index": {strconv.Itoa(i)}}.Encode()
		kind := "image"
		if jx.Str(m, "kind") == "video" {
			kind = "video"
		}
		media = append(media, jx.M{"kind": kind, "src": src, "name": jx.Str(m, "name"), "existing": i})
	}
	return jx.M{"id": id, "revision": outboxRevision(p), "text": jx.Str(p, "text"), "media": media, "poll": p["poll"], "channel": jx.Str(p, "channel"), "publish_at": jx.Int(p, "publish_at")}, nil
}

func saveOutboxEdit(c *Ctx) (any, error) {
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, 400<<20)
	if err := c.R.ParseMultipartForm(64 << 20); err != nil {
		return nil, fail(413, "Не удалось прочитать файлы поста.")
	}
	defer c.R.MultipartForm.RemoveAll()
	id, revision := c.R.FormValue("id"), c.R.FormValue("revision")
	post, err := editableOutboxPost(c, id)
	if err != nil {
		return nil, err
	}
	if revision == "" || revision != outboxRevision(post) {
		return nil, fail(409, "Пост изменился. Закройте редактор и откройте его заново.")
	}
	var raw []jx.M
	if err := json.Unmarshal([]byte(c.R.FormValue("media")), &raw); err != nil || len(raw) > config.TelegramMaxMedia {
		return nil, fail(400, "Некорректный список медиа.")
	}
	var poll jx.M
	if value := c.R.FormValue("poll"); value != "" {
		poll = jx.Unmarshal([]byte(value))
		if poll == nil {
			return nil, fail(400, "Некорректный опрос.")
		}
		if len(raw) != 0 {
			return nil, fail(422, "Опрос не может содержать медиа.")
		}
		if poll, err = posta.TelegramPollCheck(poll); err != nil {
			return nil, err
		}
	}
	text := posta.PostCaptionClean(c.R.FormValue("text"))
	if poll != nil {
		text = jx.Str(poll, "question")
	}
	ch := c.U.Channel(jx.Str(post, "channel"))
	if ch == nil {
		return nil, fail(409, "Канал больше не подключён.")
	}
	if poll == nil {
		if len(raw) == 0 && strings.TrimSpace(text) == "" {
			return nil, fail(400, "Добавьте текст или медиа.")
		}
		if ch.Platform == "telegram" {
			if err := posta.TelegramCheckText(text, len(raw)); err != nil {
				return nil, err
			}
		}
	}
	work, err := posta.TelegramWorkDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	uploads := map[string]string{}
	for i, fh := range c.R.MultipartForm.File["files[]"] {
		if fh.Size > config.TelegramUploadMaxBytes {
			return nil, fail(400, "Файл слишком большой.")
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(work, fmt.Sprintf("upload-%d", i))
		out, err := os.Create(path)
		if err != nil {
			f.Close()
			return nil, err
		}
		n, copyErr := io.Copy(out, io.LimitReader(f, config.TelegramUploadMaxBytes+1))
		f.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n > config.TelegramUploadMaxBytes {
			return nil, fail(400, "Не удалось сохранить файл.")
		}
		uploads[strconv.Itoa(i)] = path
	}
	existing := jx.Maps(post, "media")
	used := map[int]bool{}
	items := make([]posta.MediaItem, 0, len(raw))
	for _, m := range raw {
		item := posta.MediaItem{Kind: jx.Str(m, "kind"), Src: jx.Str(m, "src"), Name: jx.Str(m, "name")}
		if jx.Has(m, "existing") {
			i := int(jx.Int(m, "existing"))
			if i < 0 || i >= len(existing) || used[i] {
				return nil, fail(400, "Некорректный файл очереди.")
			}
			used[i] = true
			key := "existing-" + strconv.Itoa(i)
			uploads[key] = jx.Str(existing[i], "path")
			item.Upload = key
			item.Src = jx.Str(existing[i], "src")
			if item.Name == "" {
				item.Name = jx.Str(existing[i], "name")
			}
		} else if jx.Has(m, "upload") {
			item.Upload = jx.ToStr(m["upload"])
			if uploads[item.Upload] == "" {
				return nil, fail(400, "Загруженный файл не найден.")
			}
		}
		items = append(items, item)
	}
	prepared, err := c.A.TelegramPrepareMedia(items, uploads, work, "bot", nil)
	if err != nil {
		return nil, fail(422, err.Error())
	}
	if ch.Platform != "telegram" {
		kinds := []string{}
		for _, m := range raw {
			kinds = append(kinds, jx.Str(m, "kind"))
		}
		if poll != nil {
			return nil, fail(422, "Опрос доступен только для Telegram.")
		}
		if _, skipped, err := c.A.NetworkPreflight(ch.Platform, text, kinds); err != nil {
			return nil, err
		} else if len(skipped) > 0 {
			return nil, fail(422, strings.Join(skipped, "; "))
		}
	}
	dir, err := c.A.OutboxMediaDir(id)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0o775); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(dir, "edit-")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(stage)
		}
	}()
	media := posta.PreparedMaps(prepared)
	for i, m := range media {
		src := jx.Str(m, "path")
		dst := filepath.Join(stage, fmt.Sprintf("%d-%s", i, filepath.Base(src)))
		in, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		out, err := os.Create(dst)
		if err != nil {
			in.Close()
			return nil, err
		}
		_, copyErr := io.Copy(out, in)
		in.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			return nil, fail(500, "Не удалось сохранить медиа.")
		}
		m["path"] = dst
	}
	err = c.A.DB.Tx(c.R.Context(), func(tx pgx.Tx) error {
		var data []byte
		var status string
		if err := tx.QueryRow(c.R.Context(), `SELECT status,data FROM outbox WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, c.U.ID).Scan(&status, &data); err != nil {
			return fail(409, "Пост уже удалён или отправляется.")
		}
		current := jx.Unmarshal(data)
		if current == nil || !contains([]string{"queued", "failed"}, status) || revision != outboxRevision(current) {
			return fail(409, "Пост изменился или уже отправляется. Откройте его заново.")
		}
		current["text"], current["media"] = text, media
		if poll == nil {
			delete(current, "poll")
		} else {
			current["poll"] = poll
		}
		current["thumb_id"] = c.A.PostThumbMake(media)
		_, err := tx.Exec(c.R.Context(), `UPDATE outbox SET data=$1 WHERE id=$2 AND user_id=$3`, jx.Marshal(current), id, c.U.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	committed = true
	return ok(jx.M{"id": id}), nil
}
