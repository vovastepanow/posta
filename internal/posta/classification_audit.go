package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"posta/internal/jx"
	"posta/internal/llm"
)

// AuditClassifications snapshots both stores before any external request. Results
// are proposals only: disagreement and missing evidence never overwrite labels.
func (a *App) AuditClassifications(ctx context.Context, dir string, snapshotOnly bool, limit int, progress func(string)) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "audit.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("audit already running: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	records, err := a.classificationSnapshot(ctx, dir)
	if err != nil {
		return err
	}
	if snapshotOnly {
		progress(fmt.Sprintf("Snapshot saved: %d records", len(records)))
		return nil
	}
	done := map[string]bool{}
	if f, e := os.Open(filepath.Join(dir, "review.jsonl")); e == nil {
		decoder := json.NewDecoder(f)
		for {
			var row jx.M
			e = decoder.Decode(&row)
			if e == io.EOF {
				break
			}
			if e != nil {
				f.Close()
				return e
			}
			status := jx.Str(row, "status")
			if status == "agrees" || status == "proposal" || status == "sensitive_skipped" || status == "safety_review" {
				done[jx.Str(row, "store")+"\x00"+jx.Str(row, "key")] = true
			}
		}
		f.Close()
	} else if !os.IsNotExist(e) {
		return e
	}
	out, err := os.OpenFile(filepath.Join(dir, "review.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	results := json.NewEncoder(out)
	progress(fmt.Sprintf("Snapshot saved: %d records", len(records)))
	unavailable := 0
	attempted := 0
	previousHook := a.LLM.OnCall
	var models []string
	var errors []string
	a.LLM.OnCall = func(c llm.Call) {
		if previousHook != nil {
			previousHook(c)
		}
		models = append(models, c.Target.String())
		if c.Err != nil {
			errors = append(errors, trimRunes(c.Err.Error(), 400))
		}
	}
	defer func() { a.LLM.OnCall = previousHook }()
	for i, r := range diverseAuditOrder(records) {
		if done[r.Store+"\x00"+r.Key] {
			continue
		}
		if limit > 0 && attempted >= limit {
			progress(fmt.Sprintf("Batch limit %d reached; resume using the same directory", limit))
			return nil
		}
		attempted++
		if err = ctx.Err(); err != nil {
			return err
		}
		timeout := 90 * time.Second
		if PostItemHasVideo(r.Data) {
			timeout = 5 * time.Minute
		}
		recordCtx, cancel := context.WithTimeout(ctx, timeout)
		u := a.User(recordCtx, r.User, nil)
		models = nil
		errors = nil
		status := "unresolved"
		var tags []string
		if jx.Bool(r.Data, "source_sensitive") || jx.Bool(r.Data, "nsfw") || u.analysisPrivate(jx.Str(r.Data, "topic"), r.Data) {
			status = "sensitive_skipped"
		} else {
			if i > 0 {
				select {
				case <-ctx.Done():
					cancel()
					return ctx.Err()
				case <-time.After(8 * time.Second):
				}
			}
			var unsafe bool
			tags, unsafe = u.classifyPublicItem(r.Data, PostCaptionForAI(r.Data), jx.Str(r.Data, "image"))
			if unsafe {
				status = "safety_review"
				tags = nil
			}
			if tags == nil && u.classificationErr != nil {
				errors = append(errors, trimRunes(u.classificationErr.Error(), 400))
			}
			if tags != nil {
				status = "proposal"
				if sameContentLabels(tags, r.Tags) {
					status = "agrees"
				}
				unavailable = 0
			} else if len(errors) > 0 {
				status = "model_unavailable"
				unavailable++
			} else {
				unavailable = 0
			}
		}
		cancel()
		if err = results.Encode(jx.M{"store": r.Store, "key": r.Key, "status": status, "previous": r.Tags, "proposed": tags, "version": 14, "reason": u.classificationReason, "models": models, "errors": errors}); err != nil {
			return err
		}
		if err = out.Sync(); err != nil {
			return err
		}
		progress(fmt.Sprintf("%d/%d %s %s: %s %v", i+1, len(records), r.Store, r.Key, status, tags))
		// No hot retry loop against a provider that is unavailable. Unvisited records
		// remain in the snapshot and can be audited in a subsequent run.
		if unavailable >= 3 {
			return fmt.Errorf("three unavailable records; stopped without changing labels")
		}
	}
	return nil
}

type auditRecord struct {
	Store string   `json:"store"`
	Key   string   `json:"key"`
	User  string   `json:"user"`
	Data  jx.M     `json:"data"`
	Tags  []string `json:"tags"`
}

func (a *App) classificationSnapshot(ctx context.Context, dir string) ([]auditRecord, error) {
	path := filepath.Join(dir, "snapshot.jsonl")
	if existing, err := os.Open(path); err == nil {
		defer existing.Close()
		decoder := json.NewDecoder(existing)
		var records []auditRecord
		for {
			var r auditRecord
			err = decoder.Decode(&r)
			if err == io.EOF {
				return records, nil
			}
			if err != nil {
				return nil, err
			}
			records = append(records, r)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	snapshot, err := os.CreateTemp(dir, ".classification-snapshot-*.tmp")
	if err != nil {
		return nil, err
	}
	defer snapshot.Close()
	defer os.Remove(snapshot.Name())
	rows, err := a.DB.Pool.Query(ctx, `SELECT 'pool' AS store,id::text,user_id,data,COALESCE(data->'tags','[]'::jsonb) FROM pool_items
 UNION ALL SELECT 'public',source_key || E'\n' || post_key,'',data,COALESCE((SELECT jsonb_agg(topic_key ORDER BY topic_key) FROM public_post_topics t WHERE t.source_key=p.source_key AND t.post_key=p.post_key),'[]'::jsonb) FROM public_posts p ORDER BY 1,2`)
	if err != nil {
		return nil, err
	}
	var records []auditRecord
	enc := json.NewEncoder(snapshot)
	for rows.Next() {
		var r auditRecord
		var raw, tags []byte
		if err = rows.Scan(&r.Store, &r.Key, &r.User, &raw, &tags); err != nil {
			rows.Close()
			return nil, err
		}
		r.Data = jx.Unmarshal(raw)
		if err = json.Unmarshal(tags, &r.Tags); err != nil {
			rows.Close()
			return nil, err
		}
		if err = enc.Encode(r); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = snapshot.Sync(); err != nil {
		return nil, err
	}
	if err = snapshot.Close(); err != nil {
		return nil, err
	}
	if err = os.Link(snapshot.Name(), path); err != nil {
		return nil, err
	}
	return records, nil
}

// Interleave stores and media types so a provider outage does not leave the
// catalog untested while all successful calls went to similar saved posts.
func diverseAuditOrder(records []auditRecord) []auditRecord {
	var pool, public []auditRecord
	groups := map[string][]auditRecord{}
	for _, r := range records {
		if r.Store == "pool" {
			pool = append(pool, r)
			continue
		}
		kind := "text"
		if jx.Str(r.Data, "video") != "" {
			kind = "video"
		} else if runeLen(PostCaptionForAI(r.Data)) < 80 {
			kind = "image"
		}
		platform, _, _ := strings.Cut(jx.Str(r.Data, "source_key"), ":")
		group := kind + ":" + platform
		groups[group] = append(groups[group], r)
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for round := 0; ; round++ {
		added := false
		for _, k := range keys {
			if round < len(groups[k]) {
				public = append(public, groups[k][round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	out := make([]auditRecord, 0, len(records))
	for i := 0; i < len(pool) || i < len(public); i++ {
		if i < len(pool) {
			out = append(out, pool[i])
		}
		if i < len(public) {
			out = append(out, public[i])
		}
	}
	return out
}
