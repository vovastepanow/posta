package posta

// Revisit previously tagged media that lack neutral observations.
const publicClassificationHasMediaSQL = `(coalesce(p.data->>'image','') <> ''
 OR coalesce(p.data->>'video','') <> ''
 OR (CASE WHEN jsonb_typeof(p.data->'gallery')='array'
 THEN jsonb_array_length(p.data->'gallery') ELSE 0 END) > 1)`
const publicClassificationNeedsLabelsSQL = `NOT EXISTS
 (SELECT 1 FROM public_post_topics t WHERE t.source_key=p.source_key AND t.post_key=p.post_key)`
const publicClassificationNeedsWorkSQL = `(` + publicClassificationNeedsLabelsSQL + `
 OR (` + publicClassificationHasMediaSQL + ` AND NOT EXISTS
 (SELECT 1 FROM public_post_media_observations m WHERE m.source_key=p.source_key AND m.post_key=p.post_key)))`

const publicClassificationClaimSQL = `UPDATE public_posts p SET classification_at=$3
 WHERE p.source_key=$1 AND p.post_key=$2 AND p.classification_at < $3-3600
 AND posta_public_revision(p.data)=posta_public_revision($4::jsonb)
 AND ` + publicEmbeddingSafeSQL + ` AND ` + publicClassificationNeedsWorkSQL + `
 RETURNING ` + publicClassificationNeedsLabelsSQL

const publicClassificationBackfillSQL = `SELECT p.source_key,p.post_key,p.data FROM public_posts p
 WHERE p.last_seen >= $1 AND p.classification_at < $2 AND p.source_key=ANY($3::text[])
 AND (` + publicClassificationHasMediaSQL + ` OR length(coalesce(p.data->>'text','')) >= 4
 OR length(coalesce(p.data->>'title','')) >= 4)
 AND ` + publicEmbeddingSafeSQL + ` AND ` + publicClassificationNeedsWorkSQL + `
 ORDER BY p.classification_at ASC,p.last_seen DESC,p.source_key,p.post_key LIMIT 30`
