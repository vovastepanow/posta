package posta

import (
	"context"
	"fmt"
	"posta/internal/jx"
	"time"
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
	unavailable := 0
	for i, r := range records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if jx.Int(r.data, "tags_reclassified_version") == 14 && !jx.Bool(r.data, "tags_classification_pending") {
			continue
		}
		u := a.User(ctx, r.uid, nil)
		if jx.Bool(r.data, "nsfw") || u.analysisPrivate(jx.Str(r.data, "topic"), r.data) {
			progress(fmt.Sprintf("%d/%d post %d: sensitive, skipped", i+1, len(records), r.id))
			continue
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(8 * time.Second):
			}
		}
		tags := u.contentTagsFromText(jx.Str(r.data, "description"), PostCaptionForAI(r.data), false)
		if tags == nil {
			unavailable++
			progress(fmt.Sprintf("%d/%d post %d: classification unavailable; previous tags retained", i+1, len(records), r.id))
			if unavailable >= 3 {
				return fmt.Errorf("classification unavailable for %d posts; remaining posts retained", unavailable)
			}
			continue
		}
		if len(tags) == 0 && len(a.ContentTagsClean(r.data["tags"])) > 0 {
			result, err := a.DB.Pool.Exec(ctx, `UPDATE pool_items SET data=jsonb_set(jsonb_set(data,'{tags_recheck_no_confirmation_v14}','true'::jsonb),'{tags_reclassified_version}','14'::jsonb) WHERE id=$1 AND data=$2::jsonb`, r.id, jx.Marshal(r.data))
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				progress(fmt.Sprintf("%d/%d post %d: changed concurrently, skipped", i+1, len(records), r.id))
				continue
			}
			progress(fmt.Sprintf("%d/%d post %d: no confirmed labels, previous tags retained", i+1, len(records), r.id))
			continue
		}
		// Preserve mandatory safety labels, regardless of the topic model's answer.
		for _, tag := range a.ContentTagsClean(r.data["tags"]) {
			if tagInfo[tag].NSFW && !contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
		old := a.ContentTagsClean(r.data["tags"])
		same := len(old) == len(tags)
		for _, tag := range old {
			if !contains(tags, tag) {
				same = false
			}
		}
		// An explicitly pending, unlabeled record has no disputed topic to replace.
		// Existing or manually reviewed topic labels always stay on the review path.
		fillPending := canFillPendingLabels(r.data, old, tags)
		if !same && !fillPending {
			// A model disagreement is not proof that the existing label is wrong.
			result, err := a.DB.Pool.Exec(ctx, `UPDATE pool_items SET data=jsonb_set(jsonb_set(data,'{tags_recheck_suggestion_v14}',$2::jsonb),'{tags_reclassified_version}','14'::jsonb) WHERE id=$1 AND data=$3::jsonb`, r.id, jx.Marshal(tags), jx.Marshal(r.data))
			if err != nil {
				return err
			}
			if result.RowsAffected() == 1 {
				progress(fmt.Sprintf("%d/%d post %d: disagreement %v -> %v; retained for review", i+1, len(records), r.id, old, tags))
			}
			continue
		}
		result, err := a.DB.Pool.Exec(ctx, `UPDATE pool_items SET data=jsonb_set(jsonb_set(jsonb_set(data,'{tags_before_reclassification_v14}',COALESCE(data->'tags_before_reclassification_v14',data->'tags','[]'::jsonb)),'{tags}',$2::jsonb),'{tags_reclassified_version}','14'::jsonb) || '{"tags_classification_pending":false}'::jsonb WHERE id=$1 AND data=$3::jsonb`, r.id, jx.Marshal(tags), jx.Marshal(r.data))
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			progress(fmt.Sprintf("%d/%d post %d: changed concurrently, skipped", i+1, len(records), r.id))
			continue
		}
		progress(fmt.Sprintf("%d/%d post %d: %v", i+1, len(records), r.id, tags))
	}
	if unavailable > 0 {
		return fmt.Errorf("classification unavailable for %d posts; previous tags retained", unavailable)
	}
	return nil
}

func canFillPendingLabels(data jx.M, old, tags []string) bool {
	if !jx.Bool(data, "tags_classification_pending") || jx.Bool(data, "tags_reviewed") || len(tags) == 0 {
		return false
	}
	for _, tag := range old {
		if !tagInfo[tag].NSFW {
			return false
		}
	}
	return true
}
