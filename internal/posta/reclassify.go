package posta

import (
	"context"
	"fmt"
	"posta/internal/jx"
)

// ReclassifySaved updates only labels; retain the previous labels for recovery.
func (a *App) ReclassifySaved(ctx context.Context, progress func(string)) error {
	rows, err := a.DB.Pool.Query(ctx, `SELECT id,user_id,data FROM pool_items ORDER BY id`)
	if err != nil {
		return err
	}
	type record struct {
		id   int64
		uid  string
		data jx.M
	}
	var records []record
	for rows.Next() {
		var r record
		var raw []byte
		if err = rows.Scan(&r.id, &r.uid, &raw); err != nil {
			rows.Close()
			return err
		}
		r.data = jx.Unmarshal(raw)
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("Saved posts: %d", len(records)))
	for i, r := range records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if jx.Int(r.data, "tags_reclassified_version") == 12 {
			continue
		}
		u := a.User(ctx, r.uid, nil)
		tags := u.contentTagsFromText(jx.Str(r.data, "description"), PostCaptionForAI(r.data), u.analysisPrivate(jx.Str(r.data, "topic"), r.data))
		if tags == nil {
			return fmt.Errorf("post %d: classification unavailable; previous tags retained", r.id)
		}
		// Preserve mandatory safety labels, regardless of the topic model's answer.
		for _, tag := range a.ContentTagsClean(r.data["tags"]) {
			if tagInfo[tag].NSFW && !contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		_, err = a.DB.Pool.Exec(ctx, `UPDATE pool_items SET data=jsonb_set(jsonb_set(jsonb_set(data,'{tags_before_reclassification}',COALESCE(data->'tags_before_reclassification',data->'tags','[]'::jsonb)),'{tags}',$2::jsonb),'{tags_reclassified_version}','12'::jsonb) WHERE id=$1 AND data=$3::jsonb`, r.id, jx.Marshal(tags), jx.Marshal(r.data))
		if err != nil {
			return err
		}
		progress(fmt.Sprintf("%d/%d post %d: %v", i+1, len(records), r.id, tags))
	}
	return nil
}
