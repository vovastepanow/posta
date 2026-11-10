package posta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

func TestRepeatKeysCoverTextGalleryAndVideo(t *testing.T) {
	text := jx.M{"text": "Один и тот же достаточно длинный текст поста!"}
	normalized := jx.M{"text": "ОДИН и тот же достаточно длинный текст поста https://example.org"}
	if !candidateIsKnown(normalized, recordKnownKeys([]jx.M{text})) {
		t.Fatal("normalized text must match without text_post flag")
	}
	gallery := jx.M{"image": "first", "gallery": []jx.M{{"image": "first"}, {"image": "second"}}}
	if !candidateIsKnown(jx.M{"image": "second"}, recordKnownKeys([]jx.M{gallery})) {
		t.Fatal("second gallery image lost")
	}
	first := jx.M{"image": "cover", "video": "first.mp4", "phash": strings.Repeat("0", 64)}
	second := jx.M{"image": "cover", "video": "second.mp4", "phash": strings.Repeat("0", 64)}
	if candidateIsKnown(second, recordKnownKeys([]jx.M{first})) || len(repeatPhashes(first)) != 0 {
		t.Fatal("shared cover must not identify a video")
	}
	first["repeat_file_hashes"] = []string{"same-file"}
	second["repeat_file_hashes"] = []string{"same-file"}
	if !candidateIsKnown(second, recordKnownKeys([]jx.M{first})) {
		t.Fatal("same video bytes at a different URL must match")
	}
}

func TestRepeatWindowBoundary(t *testing.T) {
	at := int64(2000000000)
	records := []jx.M{{"timestamp": at - repeatMemorySeconds}, {"timestamp": at - repeatMemorySeconds + 1}, {"timestamp": at - repeatMemorySeconds - 1}, {"timestamp": at - repeatMemorySeconds - 100, "repeat_action_at": at - 1}}
	if got := recentRepeatRecords(records, at); len(got) != 2 {
		t.Fatalf("expected only unexpired records, got %v", got)
	}
}

func TestPrepareRepeatFilesUsesCachePath(t *testing.T) {
	dir := t.TempDir()
	a := &App{Cfg: config.Server{MediaDir: dir, MediaURL: "media"}}
	name := strings.Repeat("a", 32) + ".mp4"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("video bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	item := jx.M{"video": "https://example.org/video.mp4", "video_local": "media/" + name, "image_local": "media/cover.jpg"}
	a.prepareRepeatFiles(item)
	hashes := jx.Strs(item, "repeat_file_hashes")
	if len(hashes) != 1 || hashes[0] != repeatDigest("video bytes") {
		t.Fatalf("file fingerprint missing: %v", item)
	}
}

func TestRepeatMemoryPostgres(t *testing.T) {
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
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d, Mem: NewMemCache(100)}
	uid, other := randHex(16), randHex(16)
	u := a.User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::text[])`, []string{uid, other})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,created_at) VALUES($1,$3),($2,$3)`, uid, other, now())
	load := func(user string) (*knownKeys, map[string]string) {
		t.Helper()
		k, h := recordKnownKeys(nil), map[string]string{}
		if err := loadRepeatMemory(ctx, d.Pool, user, k, h); err != nil {
			t.Fatal(err)
		}
		return k, h
	}
	item := jx.M{"image": "https://example.org/first.jpg", "source_url": "https://example.org/post/1", "topic": "test", "phash": strings.Repeat("0", 64), "pooled_at": now(), "gallery": []jx.M{{"image": "https://example.org/first.jpg"}, {"image": "https://example.org/second.jpg", "phash": strings.Repeat("f", 64)}}}
	if _, err = u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{item} }); err != nil {
		t.Fatal(err)
	}
	k, h := load(uid)
	if !repeatMatches(item, k, h) || !repeatMatches(jx.M{"image": "changed-url", "phash": strings.Repeat("f", 64)}, k, h) {
		t.Fatal("admitted post/gallery not remembered")
	}
	k, h = load(other)
	if repeatMatches(item, k, h) {
		t.Fatal("memory leaked between users")
	}
	var expiry int64
	if err = d.Pool.QueryRow(ctx, `SELECT min(expires_at) FROM content_repeat_memory WHERE user_id=$1`, uid).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	// A later collector sighting (and a maintenance edit) must not renew memory.
	exec(`UPDATE content_repeat_memory SET expires_at=expires_at-100 WHERE user_id=$1`, uid)
	// Move admission dates back too, simulating time elapsed since admission.
	exec(`UPDATE pool_items SET data=data || $2::jsonb WHERE user_id=$1`, uid, db.JSON(jx.M{"repeat_action_at": expiry - repeatMemorySeconds - 100, "pooled_at": expiry - repeatMemorySeconds - 100}))
	_, err = u.PoolUpdate(func(pool []jx.M) []jx.M {
		pool[0]["translation"] = "test"
		return pool
	})
	if err != nil {
		t.Fatal(err)
	}
	var unchanged int64
	if err = d.Pool.QueryRow(ctx, `SELECT max(expires_at) FROM content_repeat_memory WHERE user_id=$1`, uid).Scan(&unchanged); err != nil || unchanged != expiry-100 {
		t.Fatalf("maintenance extended expiry: %d, expected %d (%v)", unchanged, expiry-100, err)
	}
	// A fresh App represents a restart: no in-process repeat cache is required.
	restarted := (&App{DB: d, Mem: NewMemCache(100)}).User(ctx, uid, nil)
	if !candidateIsKnown(item, restarted.collectKnown()) {
		t.Fatal("memory lost across restart")
	}
	// Clearing pool and history must keep their fingerprints.
	if _, err = u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{} }); err != nil {
		t.Fatal(err)
	}
	k, h = load(uid)
	if !repeatMatches(item, k, h) {
		t.Fatal("pool removal forgot post")
	}
	if restarted.AddItemToPoolSafely(jx.Copy(item)) {
		t.Fatal("final admission allowed a remembered post after removal")
	}
	// A real rating renews the same keys for 30 days.
	record := jx.Merge(item, jx.M{"timestamp": now()})
	if err = u.HistoryAppend("liked", record); err != nil {
		t.Fatal(err)
	}
	var refreshed int64
	if err = d.Pool.QueryRow(ctx, `SELECT min(expires_at) FROM content_repeat_memory WHERE user_id=$1`, uid).Scan(&refreshed); err != nil || refreshed < now()+repeatMemorySeconds-2 {
		t.Fatalf("rating did not refresh memory: %d %v", refreshed, err)
	}
	// Legacy records not yet in memory must be saved before content compaction.
	exec(`DELETE FROM content_repeat_memory WHERE user_id=$1`, uid)
	if _, err = u.HistoryUpdate(func(h History) History {
		h["liked"][0] = jx.M{"image": "deleted:compact", "content_deleted": true, "timestamp": now()}
		return h
	}); err != nil {
		t.Fatal(err)
	}
	k, h = load(uid)
	if !repeatMatches(item, k, h) {
		t.Fatal("history compaction lost original fingerprints")
	}
	if _, err = u.HistoryUpdate(func(History) History { return EmptyHistory() }); err != nil {
		t.Fatal(err)
	}
	k, h = load(uid)
	if !repeatMatches(item, k, h) {
		t.Fatal("history clearing forgot post")
	}
	// Expired rows stop blocking immediately, even before scheduled GC runs.
	exec(`UPDATE content_repeat_memory SET expires_at=$2 WHERE user_id=$1`, uid, now())
	k, h = load(uid)
	if repeatMatches(item, k, h) {
		t.Fatal("expired memory blocks post")
	}
	a.CacheGC(ctx)
	var count int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM content_repeat_memory WHERE user_id=$1`, uid).Scan(&count); err != nil || count != 0 {
		t.Fatalf("GC left %d rows: %v", count, err)
	}
	// Two processes/Apps racing to admit the same fingerprint serialize their
	// final check and write in the same transaction.
	var wg sync.WaitGroup
	admitted := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := (&App{DB: d}).User(ctx, uid, nil)
			ok := false
			_, err := user.poolUpdate(func(tx pgx.Tx, pool []jx.M) ([]jx.M, error) {
				k, h := recordKnownKeys(nil), map[string]string{}
				if err := loadRepeatMemory(ctx, tx, uid, k, h); err != nil {
					return nil, err
				}
				if repeatMatches(item, k, h) {
					return nil, nil
				}
				ok = true
				return append(pool, jx.Copy(item)), nil
			})
			if err != nil {
				t.Error(err)
			}
			admitted <- ok
		}()
	}
	wg.Wait()
	close(admitted)
	n := 0
	for ok := range admitted {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("concurrent admissions=%d", n)
	}
	// Active queued items block repeats beyond the memory TTL, then publication
	// can renew memory without needing the full historical post.
	identity := jx.M{"keys": repeatKeys(item), "hashes": repeatPhashes(item)}
	exec(`INSERT INTO outbox(id,user_id,publish_at,status,data) VALUES($1,$2,$3,'queued',$4)`, randHex(8), uid, now()+60*86400, db.JSON(jx.M{"repeat_identity": identity}))
	exec(`DELETE FROM content_repeat_memory WHERE user_id=$1`, uid)
	k, h = load(uid)
	if !repeatMatches(item, k, h) {
		t.Fatal("queued post forgotten")
	}
}
