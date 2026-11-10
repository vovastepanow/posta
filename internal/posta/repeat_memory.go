package posta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"posta/internal/db"
	"posta/internal/jx"
)

const repeatMemorySeconds int64 = 30 * 86400

func repeatFileIsVideo(item jx.M) bool {
	return jx.Str(item, "video") != "" || jx.Str(item, "video_local") != "" || (jx.Str(item, "video_via") != "" && jx.Str(item, "video_page") != "")
}

func repeatDigest(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

// repeatKeys never uses a video's cover as its identity: different videos may
// share a cover. Each gallery member has its own identity.
func repeatKeys(item jx.M) []string {
	keys := map[string]bool{}
	add := func(s string) {
		if s != "" {
			keys[repeatDigest(s)] = true
		}
	}
	add(CandidatePostKey(item))
	// History and analysis records need not retain the parser's text_post flag.
	if ContentImageKey(item) == "" && !PostItemHasVideo(item) {
		text := jx.Merge(item, jx.M{"text_post": true})
		add(CandidateTextKey(text))
	}
	for _, f := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		if repeatFileIsVideo(f) {
			for _, field := range []string{"video", "video_page"} {
				if s := jx.Str(f, field); s != "" {
					add("video:" + s)
				}
			}
		} else {
			if s := ContentImageKey(f); s != "" && !strings.HasPrefix(s, "deleted:") {
				add("image:" + s)
			}
			for _, s := range jx.Strs(f, "images") {
				add("image:" + s)
			}
		}
		for _, s := range jx.Strs(f, "repeat_file_hashes") {
			add("file:" + s)
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	return out
}

func repeatPhashes(item jx.M) []string {
	var hashes []string
	for _, f := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		if h := jx.Str(f, "phash"); !repeatFileIsVideo(f) && len(h) == 64 {
			if _, err := hex.DecodeString(h); err == nil {
				hashes = append(hashes, h)
			}
		}
	}
	return hashes
}

// Compute file hashes only on admission, outside the database transaction.
// They survive local media deletion, without retaining a second copy of a file.
func (a *App) prepareRepeatFiles(item jx.M) {
	for _, f := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		if len(jx.Strs(f, "repeat_file_hashes")) > 0 {
			continue
		}
		var hashes []any
		for _, field := range []string{"image_local", "video_local"} {
			if field == "image_local" && repeatFileIsVideo(f) {
				continue
			}
			path := a.MediaPath(a.MediaExisting(jx.Str(f, field)))
			if path == "" {
				continue
			}
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			h := sha256.New()
			_, err = io.Copy(h, file)
			file.Close()
			if err == nil {
				hashes = append(hashes, hex.EncodeToString(h.Sum(nil)))
			}
		}
		if len(hashes) > 0 {
			f["repeat_file_hashes"] = hashes
		}
	}
}

func repeatRecordAt(item jx.M) int64 {
	at := jx.Int(item, "timestamp")
	for _, field := range []string{"pooled_at", "published_at", "repeat_action_at"} {
		if t := jx.Int(item, field); t > at {
			at = t
		}
	}
	return at
}

func recentRepeatRecords(records []jx.M, at int64) []jx.M {
	var out []jx.M
	for _, r := range records {
		if repeatRecordAt(r) > at-repeatMemorySeconds {
			out = append(out, r)
		}
	}
	return out
}

// rememberRepeat only receives admission/action dates, never collector sightings.
// Backfilling records being removed uses their original dates, so cleanup does
// not extend their lifetime. GREATEST also prevents an old record shortening it.
func rememberRepeat(ctx context.Context, q db.Querier, uid string, item jx.M, at int64) error {
	return rememberRepeatKeys(ctx, q, uid, repeatKeys(item), repeatPhashes(item), at)
}

func rememberRepeatKeys(ctx context.Context, q db.Querier, uid string, keys, hashes []string, at int64) error {
	if at <= now()-repeatMemorySeconds {
		return nil
	}
	at = min(at, now())
	kinds, fingerprints := []string{}, []string{}
	for _, key := range keys {
		kinds = append(kinds, "key")
		fingerprints = append(fingerprints, key)
	}
	seen := map[string]bool{}
	for _, h := range hashes {
		if !seen[h] {
			kinds = append(kinds, "phash")
			fingerprints = append(fingerprints, h)
			seen[h] = true
		}
	}
	if len(kinds) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO content_repeat_memory(user_id,kind,fingerprint,expires_at)
 SELECT $1,k,f,$4 FROM unnest($2::text[],$3::text[]) AS x(k,f)
 ON CONFLICT(user_id,kind,fingerprint) DO UPDATE SET expires_at=GREATEST(content_repeat_memory.expires_at,EXCLUDED.expires_at)`, uid, kinds, fingerprints, at+repeatMemorySeconds)
	return err
}

func loadRepeatMemory(ctx context.Context, q db.Querier, uid string, known *knownKeys, hashes map[string]string) error {
	rows, err := q.Query(ctx, `SELECT kind,fingerprint FROM content_repeat_memory WHERE user_id=$1 AND expires_at>$2`, uid, now())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, key string
		if err := rows.Scan(&kind, &key); err != nil {
			return err
		}
		if kind == "phash" && hashes != nil {
			hashes[key] = ""
		}
		if kind == "key" && known != nil {
			known.keys["remember:"+key] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return loadQueuedRepeats(ctx, q, uid, known, hashes)
}

func repeatMatches(item jx.M, known *knownKeys, hashes map[string]string) bool {
	if candidateIsKnown(item, known) {
		return true
	}
	for _, h := range repeatPhashes(item) {
		if _, found := PhashFind(h, hashes); found {
			return true
		}
	}
	return false
}

// Snapshot before callbacks: several existing callbacks mutate maps in place.
func repeatSnapshot(records []jx.M) []jx.M {
	out := make([]jx.M, 0, len(records))
	for _, r := range records {
		out = append(out, jx.Unmarshal(jx.Marshal(r)))
	}
	return out
}

func repeatRecordKey(item jx.M) string {
	if key := CandidatePostKey(item); key != "" {
		return key
	}
	return ContentItemKey(item)
}

func syncRepeatRecords(ctx context.Context, q db.Querier, uid string, before, after []jx.M, history bool) error {
	old, next := map[string]jx.M{}, map[string]jx.M{}
	for _, r := range before {
		old[repeatRecordKey(r)] = r
	}
	for _, r := range after {
		next[repeatRecordKey(r)] = r
	}
	for key, r := range old {
		n := next[key]
		if string(jx.Marshal(r)) != string(jx.Marshal(n)) {
			if err := rememberRepeat(ctx, q, uid, r, repeatRecordAt(r)); err != nil {
				return err
			}
		}
	}
	for key, r := range next {
		if jx.Bool(r, "content_deleted") {
			continue
		}
		prev := old[key]
		action := prev == nil
		if history && prev != nil {
			action = jx.Int(r, "timestamp") != jx.Int(prev, "timestamp") ||
				(jx.Bool(r, "proposal_hidden") && !jx.Bool(prev, "proposal_hidden")) ||
				jx.Int(r, "published_at") > jx.Int(prev, "published_at") ||
				jx.Int(r, "repeat_action_at") > jx.Int(prev, "repeat_action_at")
		}
		if !action && prev != nil && string(jx.Marshal(r)) != string(jx.Marshal(prev)) {
			if err := rememberRepeat(ctx, q, uid, r, repeatRecordAt(prev)); err != nil {
				return err
			}
		}
		if action {
			r["repeat_action_at"] = now()
			if err := rememberRepeat(ctx, q, uid, r, now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Queue identity survives selected-history compaction. It is kept only while
// the queue entry exists; publication starts the normal 30-day window.
func (u *U) repeatQueueIdentity(record string) jx.M {
	if record == "" {
		return nil
	}
	for _, r := range append(u.Pool(), u.History().All()...) {
		if ContentItemKey(r) == record {
			return jx.M{"keys": repeatKeys(r), "hashes": repeatPhashes(r)}
		}
	}
	return jx.M{"keys": []string{repeatDigest("image:" + record)}}
}

func loadQueuedRepeats(ctx context.Context, q db.Querier, uid string, known *knownKeys, hashes map[string]string) error {
	rows, err := q.Query(ctx, `SELECT data FROM outbox WHERE user_id=$1`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		post := jx.Unmarshal(raw)
		identity := jx.Map(post, "repeat_identity")
		for _, key := range jx.Strs(identity, "keys") {
			if known != nil {
				known.keys["remember:"+key] = true
			}
		}
		for _, h := range jx.Strs(identity, "hashes") {
			if hashes != nil {
				hashes[h] = ""
			}
		}
		if key := jx.Str(post, "record"); key != "" && known != nil {
			known.keys[key] = true
			known.keys["remember:"+repeatDigest("image:"+key)] = true
		}
	}
	return rows.Err()
}
