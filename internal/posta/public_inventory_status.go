package posta

import (
	"context"
	"time"

	"posta/internal/jx"
)

// Aggregate only eligible shared catalog data, never private user queries or
// content. Cache this bounded diagnostic independently of the feed path.
func (a *App) publicInventoryStatus(ctx context.Context) jx.M {
	if a.catalog == nil || a.DB == nil {
		return jx.M{"available": false}
	}
	const cacheKey = "public_inventory_status:v1"
	if a.Mem != nil {
		if cached, ok := a.Mem.Get(cacheKey); ok {
			if result, ok := cached.(jx.M); ok {
				return result
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	model := ""
	if a.LLM != nil {
		if target := a.LLM.SourceEmbeddingTarget(); !target.Empty() {
			model = target.String() + publicEmbeddingVersion
		}
	}
	var posts, sources, embeddings, media, pendingMedia int64
	var hnsw bool
	allowed := a.publicEmbeddingSources()
	err := a.DB.Pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT p.source_key),
		count(*) FILTER(WHERE EXISTS(SELECT 1 FROM public_post_embeddings e
			WHERE e.source_key=p.source_key AND e.post_key=p.post_key AND e.model=$2)),
		count(*) FILTER(WHERE m.description IS NOT NULL),
		count(*) FILTER(WHERE m.description IS NULL AND (coalesce(p.data->>'image','') <> ''
			OR coalesce(p.data->>'video','') <> '' OR coalesce(p.data->>'audio','') <> ''
			OR coalesce(p.data->'gallery','[]'::jsonb) NOT IN ('[]'::jsonb,'null'::jsonb))),
		to_regclass('public_post_embeddings_hnsw') IS NOT NULL
		FROM public_posts p LEFT JOIN public_post_media_observations m USING(source_key,post_key)
		WHERE p.source_key=ANY($1::text[]) AND `+publicEmbeddingSafeSQL, allowed, model).
		Scan(&posts, &sources, &embeddings, &media, &pendingMedia, &hnsw)
	if err != nil {
		return jx.M{"available": false}
	}
	result := jx.M{"available": true, "open_search_enabled": a.TBool("public_open_search"), "checked_at": now(), "eligible_posts": posts,
		"eligible_sources": sources, "current_embeddings": embeddings, "media_descriptions": media,
		"media_without_description": pendingMedia, "embedding_configured": model != "", "pgvector_index": hnsw}
	if a.Mem != nil {
		a.Mem.Set(cacheKey, result, 2*time.Minute)
	}
	return result
}
