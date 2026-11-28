package posta

import (
	"context"
	"strings"

	"posta/internal/jx"
)

// Collect only validated descriptions from the media model, before legacy
// topic selection. A mixed album must have observations for every member.
func (u *U) recordPublicMediaDescription(description string) {
	if description = strings.TrimSpace(description); description != "" {
		u.publicMediaDescriptions = append(u.publicMediaDescriptions, trimRunes(description, 6000))
	}
}

func (u *U) completePublicMediaObservation(item jx.M) string {
	if sourceReviewSensitive(item) || u.publicContentExcluded(item) ||
		jx.Bool(item, "media_incomplete") || jx.Bool(item, "text_incomplete") || jx.Bool(item, "video_unavailable") ||
		len(u.classificationMediaSafety) > 0 || len(u.publicMediaDescriptions) == 0 {
		return ""
	}
	if len(u.publicMediaDescriptions) != max(1, len(ContentGallery(item))) {
		return "" // no partial album descriptions in the shared index
	}
	return trimRunes(strings.Join(u.publicMediaDescriptions, "\n"), 12000)
}

// Called under the same post revision lock as topic classification.
const publicMediaObservationSaveSQL = `INSERT INTO public_post_media_observations(source_key,post_key,description,observed_at)
VALUES($1,$2,$3,$4) ON CONFLICT(source_key,post_key) DO UPDATE SET description=EXCLUDED.description,observed_at=EXCLUDED.observed_at`

const publicMediaSearchSQL = `WITH q AS (
	SELECT plainto_tsquery('pg_catalog.simple'::regconfig,$1)
		|| plainto_tsquery('pg_catalog.russian'::regconfig,$1)
		|| plainto_tsquery('pg_catalog.english'::regconfig,$1) AS terms
)
SELECT p.platform,p.data FROM public_post_media_observations m
JOIN public_posts p USING(source_key,post_key) CROSS JOIN q
WHERE p.source_key=ANY($2::text[])
 AND posta_public_media_vector(m.description) @@ q.terms
 AND ` + publicEmbeddingSafeSQL + `
ORDER BY ts_rank_cd(posta_public_media_vector(m.description),q.terms) DESC,
 p.last_seen DESC,p.source_key,p.post_key LIMIT $3`

func (u *U) searchPublicMedia(ctx context.Context, query string, allowed []string) []publicInventoryCandidate {
	rows, err := u.DB.Pool.Query(ctx, publicMediaSearchSQL, query, allowed, 120)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []publicInventoryCandidate
	for rows.Next() {
		var platform string
		var raw []byte
		if err := rows.Scan(&platform, &raw); err != nil {
			return nil
		}
		if post := jx.Unmarshal(raw); post != nil {
			out = append(out, publicInventoryCandidate{platform, post})
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}
