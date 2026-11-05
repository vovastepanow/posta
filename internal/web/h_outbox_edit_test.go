package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/posta"
	"posta/internal/secret"
)

func TestOutboxEditPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const uid = "f1111111111111111111111111111111"
	const id = "abcdef123456"
	_, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, uid, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	_, err = d.Pool.Exec(ctx, `INSERT INTO channels(id,user_id,platform,ref,connected_at) VALUES('test-edit-channel',$1,'telegram','test',0) ON CONFLICT(id) DO NOTHING`, uid)
	if err != nil {
		t.Fatal(err)
	}
	a := &posta.App{DB: d, Box: secret.FromKey([32]byte{1}), Cfg: config.Server{DataDir: t.TempDir()}}
	dir, err := a.OutboxMediaDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	media := []jx.M{}
	for i, name := range []string{"first.png", "second.png"} {
		path := filepath.Join(dir, name)
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		im := image.NewRGBA(image.Rect(0, 0, 8, 8))
		im.Set(0, 0, color.RGBA{R: uint8(i + 1), A: 255})
		if e = png.Encode(f, im); e != nil {
			t.Fatal(e)
		}
		f.Close()
		media = append(media, jx.M{"kind": "photo", "path": path, "name": name, "src": "https://example.com/" + name})
	}
	post := jx.M{"id": id, "status": "queued", "channel": "test-edit-channel", "publish_at": time.Now().Unix() + 3600, "text": "old", "media": media}
	_, err = d.Pool.Exec(ctx, `INSERT INTO outbox(id,user_id,publish_at,status,data) VALUES($1,$2,$3,'queued',$4)`, id, uid, jx.Int(post, "publish_at"), jx.Marshal(post))
	if err != nil {
		t.Fatal(err)
	}
	u := a.User(ctx, uid, nil)
	getCtx := &Ctx{A: a, U: u, W: httptest.NewRecorder(), R: httptest.NewRequest("GET", "/?id="+id, nil)}
	got, err := getOutboxEdit(getCtx)
	if err != nil {
		t.Fatal(err)
	}
	payload := got.(jx.M)
	if jx.Str(payload, "text") != "old" || len(jx.Maps(payload, "media")) != 2 {
		t.Fatalf("incomplete edit data: %v", payload)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range map[string]string{"id": id, "revision": jx.Str(payload, "revision"), "text": "new", "media": `[{"kind":"image","existing":1}]`} {
		if err = writer.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	request := httptest.NewRequest("POST", "/", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	_, err = saveOutboxEdit(&Ctx{A: a, U: u, W: httptest.NewRecorder(), R: request})
	if err != nil {
		t.Fatal(err)
	}
	updated := u.Outbox()[0]
	files := jx.Maps(updated, "media")
	if jx.Str(updated, "text") != "new" || len(files) != 1 || jx.Str(files[0], "name") != "second.png" || jx.Int(updated, "publish_at") != jx.Int(post, "publish_at") {
		t.Fatalf("edit lost data: %v", updated)
	}
	if _, err = os.Stat(jx.Str(files[0], "path")); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("POST", "/", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if _, err = saveOutboxEdit(&Ctx{A: a, U: u, W: httptest.NewRecorder(), R: request}); posta.StatusOf(err) != 409 {
		t.Fatalf("stale edit must be rejected: %v", err)
	}
	var added bytes.Buffer
	w := multipart.NewWriter(&added)
	for k, v := range map[string]string{"id": id, "revision": outboxRevision(updated), "text": "", "media": `[{"kind":"image","upload":0},{"kind":"image","existing":0}]`} {
		if err = w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("files[]", "new.png")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(dir, "first.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(file); err != nil {
		t.Fatal(err)
	}
	w.Close()
	request = httptest.NewRequest("POST", "/", bytes.NewReader(added.Bytes()))
	request.Header.Set("Content-Type", w.FormDataContentType())
	if _, err = saveOutboxEdit(&Ctx{A: a, U: u, W: httptest.NewRecorder(), R: request}); err != nil {
		t.Fatal(err)
	}
	updated = u.Outbox()[0]
	files = jx.Maps(updated, "media")
	if jx.Str(updated, "text") != "" || len(files) != 2 || jx.Int(updated, "publish_at") != jx.Int(post, "publish_at") {
		t.Fatalf("adding media or removing text failed: %v", updated)
	}
	other := a.User(ctx, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", nil)
	_, err = getOutboxEdit(&Ctx{A: a, U: other, W: httptest.NewRecorder(), R: httptest.NewRequest("GET", "/?id="+id, nil)})
	if posta.StatusOf(err) != 404 {
		t.Fatalf("another user must not see the post: %v", err)
	}
	_, err = d.Pool.Exec(ctx, `UPDATE outbox SET status='sending', data=jsonb_set(data, '{status}', '"sending"') WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = getOutboxEdit(getCtx)
	if posta.StatusOf(err) != 409 {
		t.Fatalf("sending post must not be editable: %v", err)
	}
}
