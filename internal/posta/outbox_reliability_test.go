package posta

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

func TestOutboxEditAndClaimPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set; requires disposable database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100)}
	uid := randHex(16)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	at := now()
	ready, retry, sending := randHex(12), randHex(12), randHex(12)
	items := []jx.M{
		{"id": ready, "status": "queued", "publish_at": at - 5},
		{"id": retry, "status": "queued", "publish_at": at - 100, "next_try_at": at + 1000},
		{"id": sending, "status": "sending", "publish_at": at - 10, "sending_at": at},
	}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return items }); err != nil {
		t.Fatal(err)
	}
	version := func(id string) string {
		t.Helper()
		var v string
		if err := d.Pool.QueryRow(ctx, `SELECT ctid::text FROM outbox WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := version(sending)
	ids, err := a.outboxDueIDs(ctx, at)
	if err != nil || !slices.Contains(ids, ready) || slices.Contains(ids, retry) || slices.Contains(ids, sending) {
		t.Fatalf("incorrect due list: %v (%v)", ids, err)
	}
	if _, err := u.OutboxUpdate(func(q []jx.M) []jx.M {
		for _, p := range q {
			if jx.Str(p, "id") == ready {
				p["publish_at"] = at + 3600
			}
		}
		return q
	}); err != nil {
		t.Fatal(err)
	}
	if version(sending) != before {
		t.Fatal("unrelated edit rewrote in-flight publication")
	}
	if _, _, err := a.outboxClaimDue(ctx, ready, at); err == nil {
		t.Fatal("rescheduled post was claimed too early")
	}
	if _, _, err := a.outboxClaimDue(ctx, retry, at); err == nil {
		t.Fatal("post in backoff was claimed too early")
	}
	if _, post, err := a.outboxClaimDue(ctx, ready, at+3600); err != nil || jx.Str(post, "status") != "sending" {
		t.Fatalf("due post was not claimed: %v", err)
	}
	if _, _, err := a.outboxClaimDue(ctx, ready, at+3600); err == nil {
		t.Fatal("claimed post was claimed a second time")
	}
}

func TestOutboxFinalizeAtomicPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set; requires disposable database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{DataDir: t.TempDir()}}
	uid, id := randHex(16), randHex(6)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	at := now()
	post := jx.M{"id": id, "status": "sending", "publish_at": at - 1, "sending_at": at, "attempts": 1, "text": "test"}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{post} }); err != nil {
		t.Fatal(err)
	}
	dir, err := a.OutboxMediaDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(file, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	constraint := "test_published_" + uid
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE published ADD CONSTRAINT `+constraint+` CHECK (user_id <> '`+uid+`')`); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(ctx, `ALTER TABLE published DROP CONSTRAINT IF EXISTS `+constraint)
	channel := &Channel{ID: "synthetic-channel", Platform: "telegram"}
	sent := &SentResult{URL: "https://example.org/sent"}
	if err := a.outboxFinishSent(ctx, id, uid, post, channel, sent); err == nil {
		t.Fatal("expected publish history failure")
	}
	if len(u.Outbox()) != 1 || len(u.Published(0)) != 0 || !fileExists(file) {
		t.Fatal("history failure lost the queue record or its files")
	}
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE published DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	if err := a.outboxFinishSent(ctx, id, uid, post, channel, sent); err != nil {
		t.Fatal(err)
	}
	if len(u.Outbox()) != 0 || len(u.Published(0)) != 1 || fileExists(file) {
		t.Fatal("successful finalization did not commit and clean up")
	}
	if err := a.outboxFinishSent(ctx, id, uid, post, channel, sent); err == nil || len(u.Published(0)) != 1 {
		t.Fatal("stale finalization duplicated published history")
	}
}

func TestOutboxConfirmedReceiptRecoveryPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set; requires disposable database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{DataDir: t.TempDir()}}
	uid, id := randHex(16), randHex(6)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	at := now()
	post := jx.M{"id": id, "status": "sending", "channel": "original", "publish_at": at - 900, "sending_at": at - 900, "attempts": 1, "text": "test"}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{post} }); err != nil {
		t.Fatal(err)
	}
	channel := &Channel{ID: "original", ChatTitle: "Original channel", Platform: "telegram"}
	sent := &SentResult{MessageIDs: []int{123}, URL: "https://t.me/example/123"}
	if err := a.outboxStoreReceipt(ctx, id, uid, post, channel, sent); err != nil {
		t.Fatal(err)
	}
	// A stale worker's timeout must not erase the durable confirmation.
	if a.outboxRetryOrFail(ctx, id, uid, post, "timeout", true) {
		t.Fatal("confirmed publication became retryable")
	}
	rows := u.Outbox()
	if len(rows) != 1 || !jx.Has(rows[0], "confirmed_receipt") || jx.Str(rows[0], "status") != "sending" {
		t.Fatal("confirmation lost")
	}
	// Recovery needs neither an attached account nor another network call.
	if err := a.outboxRecoverReceipt(ctx, id, uid, rows[0]); err != nil {
		t.Fatal(err)
	}
	published := u.Published(0)
	if len(u.Outbox()) != 0 || len(published) != 1 || jx.Str(published[0], "url") != sent.URL ||
		!slices.Contains(jx.Strs(published[0], "external_ids"), "123") {
		t.Fatal("receipt did not become the original published record")
	}
	if err := a.outboxRecoverReceipt(ctx, id, uid, rows[0]); err == nil || len(u.Published(0)) != 1 {
		t.Fatal("stale recovery duplicated journal entry")
	}
}

func TestOutboxRepairDoesNotOverwriteClaimPostgres(t *testing.T) {
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
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{DataDir: t.TempDir()}}
	uid, id := randHex(16), randHex(6)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, "DELETE FROM users WHERE id=$1", uid)
	at := now()
	post := jx.M{"id": id, "status": "queued", "publish_at": at - 5, "text": "old"}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{post} }); err != nil {
		t.Fatal(err)
	}
	original := u.Outbox()[0]
	updated := jx.Merge(original, jx.M{"text": "repaired"})
	dir, _ := a.OutboxMediaDir(id)
	stage := filepath.Join(t.TempDir(), "staged.jpg")
	if err := os.WriteFile(stage, []byte("complete file"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "restored.jpg")
	if _, _, err := a.outboxClaimDue(ctx, id, at); err != nil {
		t.Fatal(err)
	}
	if applied, err := u.applyOutboxRepair(original, updated, map[string]string{dest: stage}); err != nil || applied {
		t.Fatalf("stale repair applied: %v %v", applied, err)
	}
	if fileExists(dest) || !fileExists(stage) || jx.Str(u.Outbox()[0], "status") != "sending" {
		t.Fatal("repair changed in-flight post or files")
	}
}

func TestOutboxDuplicateEditedPayloadPostgres(t *testing.T) {
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
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{DataDir: t.TempDir()}}
	uid, id := randHex(16), randHex(6)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, "DELETE FROM users WHERE id=$1", uid)
	root, _ := a.OutboxMediaDir(id)
	edit := filepath.Join(root, "edit-test")
	if err := os.MkdirAll(edit, 0700); err != nil {
		t.Fatal(err)
	}
	video, cover := filepath.Join(edit, "video.mp4"), filepath.Join(edit, "1-image.jpg")
	image := filepath.Join(edit, "image.jpg")
	for _, f := range []string{video, cover, image} {
		if err := os.WriteFile(f, []byte(f), 0600); err != nil {
			t.Fatal(err)
		}
	}
	post := jx.M{"id": id, "status": "queued", "publish_at": now() + 3600, "media": []jx.M{{"kind": "video", "path": video, "cover_path": cover}, {"kind": "photo", "path": image}}}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return []jx.M{post} }); err != nil {
		t.Fatal(err)
	}
	duplicate, err := u.OutboxDuplicatePost(id, now()+7200)
	if err != nil || duplicate == nil {
		t.Fatalf("duplicate edited payload: %v", err)
	}
	if !u.OutboxRemovePost(id) {
		t.Fatal("remove original")
	}
	if !outboxMediaReady(duplicate) {
		t.Fatal("copy still relies on removed original")
	}
	files := jx.Maps(duplicate, "media")
	if len(files) != 2 {
		t.Fatalf("duplicate lost attachments: %+v", files)
	}
	for _, tc := range []struct{ path, original string }{
		{jx.Str(files[0], "path"), video},
		{jx.Str(files[0], "cover_path"), cover},
		{jx.Str(files[1], "path"), image},
	} {
		data, err := os.ReadFile(tc.path)
		if err != nil || string(data) != tc.original {
			t.Fatalf("attachment overwritten: %s: %v", tc.path, err)
		}
	}
}

func TestOutboxCleanupReplacedMediaOnlyOwnFiles(t *testing.T) {
	a := &App{Cfg: config.Server{DataDir: t.TempDir()}}
	id := randHex(6)
	root, _ := a.OutboxMediaDir(id)
	oldDir, newDir := filepath.Join(root, "edit-old"), filepath.Join(root, "edit-new")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old, keep, foreign := filepath.Join(oldDir, "photo.jpg"), filepath.Join(newDir, "photo.jpg"), filepath.Join(t.TempDir(), "foreign.jpg")
	for _, path := range []string{old, keep, foreign} {
		if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outboxCleanupReplacedMedia(root, []jx.M{{"path": old}, {"path": keep}, {"path": foreign}}, []jx.M{{"path": keep}}, func(string) bool { return false })
	if fileExists(old) || !fileExists(keep) || !fileExists(foreign) {
		t.Fatal("cleanup removed active or unrelated files")
	}
}

func TestOutboxLegacySharedCoverSurvivesRemovalPostgres(t *testing.T) {
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
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100), Cfg: config.Server{DataDir: t.TempDir()}}
	uid, first, second := randHex(16), randHex(6), randHex(6)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, "DELETE FROM users WHERE id=$1", uid)
	root, _ := a.OutboxMediaDir(first)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(root, "cover.jpg")
	if err := os.WriteFile(cover, []byte("cover"), 0600); err != nil {
		t.Fatal(err)
	}
	items := []jx.M{
		{"id": first, "status": "queued", "media": []jx.M{{"cover_path": cover}}},
		{"id": second, "status": "queued", "media": []jx.M{{"cover_path": cover}}},
	}
	if _, err := u.OutboxUpdate(func([]jx.M) []jx.M { return items }); err != nil {
		t.Fatal(err)
	}
	a.OutboxCleanupReplacedMedia(ctx, first, []jx.M{{"cover_path": cover}}, nil)
	if !fileExists(cover) || !u.OutboxRemovePost(first) || !fileExists(cover) {
		t.Fatal("legacy duplicate lost its shared cover")
	}
	if !u.OutboxRemovePost(second) {
		t.Fatal("could not remove remaining reference")
	}
	a.removeOutboxDirectory(ctx, first)
	if fileExists(cover) {
		t.Fatal("unreferenced directory not cleaned")
	}
}

func TestOutboxRepairDestinationAcceptsEditedRevision(t *testing.T) {
	root := t.TempDir()
	if !outboxRepairDestination(root, filepath.Join(root, "edit-abc", "video.mp4")) ||
		outboxRepairDestination(root, filepath.Join(root, "..", "other.mp4")) ||
		outboxRepairDestination(root, filepath.Join(root, "other", "video.mp4")) {
		t.Fatal("incorrect repair destination validation")
	}
	link := filepath.Join(root, "edit-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if outboxRepairDestination(root, filepath.Join(link, "video.mp4")) {
		t.Fatal("repair followed symlink")
	}
}
