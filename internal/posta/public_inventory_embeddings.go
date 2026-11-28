package posta

import (
	"context"
	"log"
	"strings"
	"time"

	"posta/internal/jx"
)

const publicEmbeddingVersion = ":multimodal-description:512:v2"

// All vectors use the same provider, model, input mode and normalization.
// Do not silently mix fallback models or index incomplete/account data.
func publicEmbeddingText(post jx.M) string {
	return publicEmbeddingTextWithObservation(post, "")
}

func publicEmbeddingTextWithObservation(post jx.M, observation string) string {
	if sourceReviewSensitive(post) || jx.Bool(post, "text_incomplete") ||
		jx.Bool(post, "media_incomplete") || jx.Bool(post, "video_unavailable") {
		return ""
	}
	text := strings.TrimSpace(jx.Str(post, "title") + " " + jx.Str(post, "text"))
	if observation = strings.TrimSpace(observation); observation != "" {
		text = strings.TrimSpace(trimRunes(text, 300) + "\n" + trimRunes(observation, 500))
	}
	if len([]rune(text)) < 12 {
		return ""
	}
	return trimRunes(text, 800)
}

func publicEmbeddingArray(vector []float64) []float32 {
	if len(vector) != 512 {
		return nil
	}
	normalized := unpackSourceEmbedding(packSourceEmbedding(vector))
	if len(normalized) != 512 {
		return nil
	}
	out := make([]float32, len(normalized))
	for i, x := range normalized {
		out[i] = float32(x)
	}
	return out
}

const publicEmbeddingSafeSQL = `NOT (p.data @> '{"text_incomplete":true}' OR p.data @> '{"media_incomplete":true}'
 OR p.data @> '{"video_unavailable":true}' OR p.data @> '{"source_sensitive":true}'
 OR p.data @> '{"nsfw":true}' OR p.data @> '{"attachment_recovered":true}')
 AND coalesce(p.data->>'fetched_via','') <> 'telegram_mtproto'
 AND coalesce(p.data->'safety','[]'::jsonb) IN ('[]'::jsonb,'null'::jsonb)
 AND p.media_safety_review = '[]'::jsonb`

const publicEmbeddingBackfillSQL = `SELECT p.source_key,p.post_key,p.data,coalesce(m.description,''),p.media_observation_revision
FROM public_posts p LEFT JOIN public_post_media_observations m USING(source_key,post_key)
WHERE p.source_key=ANY($1::text[]) AND ` + publicEmbeddingSafeSQL + `
 AND length(btrim(coalesce(p.data->>'title','') || ' ' || coalesce(p.data->>'text','') || ' ' || coalesce(m.description,''))) >= 12
 AND NOT EXISTS (SELECT 1 FROM public_post_embeddings e
  WHERE e.source_key=p.source_key AND e.post_key=p.post_key AND e.model=$2)
ORDER BY p.last_seen DESC,p.source_key,p.post_key LIMIT 10`

// A row lock fences concurrent content refreshes; a stale vector cannot be
// inserted after a new revision has invalidated the previous result.
const publicEmbeddingSaveSQL = `WITH current_post AS (
 SELECT source_key,post_key FROM public_posts
 WHERE source_key=$1 AND post_key=$2 AND media_safety_review = '[]'::jsonb
 AND posta_public_revision(data)=posta_public_revision($3::jsonb) FOR UPDATE
)
INSERT INTO public_post_embeddings(source_key,post_key,model,embedding,indexed_at)
SELECT source_key,post_key,$4,$5,$6 FROM current_post
ON CONFLICT(source_key,post_key,model) DO UPDATE SET embedding=EXCLUDED.embedding,indexed_at=EXCLUDED.indexed_at`

// Fence against both the original post revision and a new media observation.
const publicEmbeddingSaveWithObservationSQL = `WITH current_post AS (
 SELECT source_key,post_key FROM public_posts
 WHERE source_key=$1 AND post_key=$2 AND media_safety_review = '[]'::jsonb
 AND posta_public_revision(data)=posta_public_revision($3::jsonb)
 AND media_observation_revision=$7 FOR UPDATE
)
INSERT INTO public_post_embeddings(source_key,post_key,model,embedding,indexed_at)
SELECT source_key,post_key,$4,$5,$6 FROM current_post
ON CONFLICT(source_key,post_key,model) DO UPDATE SET embedding=EXCLUDED.embedding,indexed_at=EXCLUDED.indexed_at`

func (a *App) publicEmbeddingSources() []string {
	var ids []string
	catalog := a.Catalog()
	for _, kind := range CatalogKinds {
		for _, entry := range *catalog.List(kind.Kind) {
			if sharedSourceEligible(kind.Platform, entry) && !sourceReviewSensitive(entry) {
				ids = append(ids, SourceIDFor(kind.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(entry, "id")), "@")))
			}
		}
	}
	return ids
}

// A conservative independent quota preserves the free model pool for actual
// editorial decisions. Reservations survive restarts and multiple processes.
func (a *App) publicEmbeddingReserve(ctx context.Context, resource string) bool {
	if a.Mem != nil {
		if _, unavailable := a.Mem.Get("public_embedding_unavailable"); unavailable {
			return false
		}
	}
	limit := min(int64(20), int64(a.LLM.Settings().FreeDailyLimit)/20)
	return limit > 0 && reserveAutopilotBudget(ctx, a.DB, randHex(16), "server", resource, time.Now(), 1, limit) == nil
}

func (a *App) backfillPublicEmbeddings(ctx context.Context) {
	target := a.LLM.SourceEmbeddingTarget()
	if target.Empty() {
		return
	}
	allowed := a.publicEmbeddingSources()
	if len(allowed) == 0 {
		return
	}
	model := target.String() + publicEmbeddingVersion
	rows, err := a.DB.Pool.Query(ctx, publicEmbeddingBackfillSQL, allowed, model)
	if err != nil {
		return
	}
	type candidate struct {
		source, key, text string
		raw []byte
		observation string
		revision int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.source, &c.key, &c.raw, &c.observation, &c.revision); err != nil {
			rows.Close()
			return
		}
		c.text = publicEmbeddingTextWithObservation(jx.Unmarshal(c.raw), c.observation)
		if c.text != "" {
			candidates = append(candidates, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(candidates) == 0 {
		return
	}
	vectors := make(map[string][]float32)
	var missing []string
	for _, c := range candidates {
		if _, seen := vectors[c.text]; seen {
			continue
		}
		var raw []byte
		if a.CacheGet(ctx, sourceEmbeddingKey(target.String(), c.text), &raw) {
			vectors[c.text] = publicEmbeddingArray(unpackSourceEmbedding(raw))
		}
		if vectors[c.text] == nil {
			vectors[c.text] = nil
			missing = append(missing, c.text)
		}
	}
	if len(missing) > 0 && a.publicEmbeddingReserve(ctx, "public_post_embedding") {
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		got, err := a.LLM.EmbedSource(callCtx, target, missing)
		cancel()
		if err != nil || len(got) != len(missing) {
			if a.Mem != nil && ctx.Err() == nil {
				a.Mem.Set("public_embedding_unavailable", true, 10*time.Minute)
			}
		} else {
			for i, text := range missing {
				if v := publicEmbeddingArray(got[i]); v != nil {
					vectors[text] = v
					a.CacheSet(ctx, sourceEmbeddingKey(target.String(), text), packSourceEmbedding(got[i]), sourceEmbeddingTTL)
				}
			}
		}
	}
	for _, c := range candidates {
		if v := vectors[c.text]; v != nil {
			if _, err := a.DB.Pool.Exec(ctx, publicEmbeddingSaveWithObservationSQL, c.source, c.key, c.raw, model, v, now(), c.revision); err != nil && ctx.Err() == nil {
				log.Printf("public embedding save: %v", err)
			}
		}
	}
}

func (u *U) publicQueryEmbedding(query string) (string, []float32) {
	if u.LLM == nil {
		return "", nil
	}
	target := u.LLM.SourceEmbeddingTarget()
	if target.Empty() {
		return "", nil
	}
	model := target.String() + publicEmbeddingVersion
	// Query caches are owner-scoped; no profile text is stored in the shared
	// public document cache. The existing restricted-topic guard applies.
	key := "public_inventory_query:v1:" + repeatDigest(u.ID+"\x00"+model+"\x00"+query)
	var raw []byte
	if u.CacheGet(u.Ctx, key, &raw) {
		if v := publicEmbeddingArray(unpackSourceEmbedding(raw)); v != nil {
			return model, v
		}
	}
	// Avoid spending query quota before the corpus contains any embeddings.
	var available bool
	if err := u.DB.Pool.QueryRow(u.Ctx, "SELECT EXISTS(SELECT 1 FROM public_post_embeddings WHERE model=$1)", model).Scan(&available); err != nil || !available {
		return "", nil
	}
	if !u.publicEmbeddingReserve(u.Ctx, "public_query_embedding") {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(u.Ctx, 5*time.Second)
	defer cancel()
	got, err := u.LLM.EmbedSource(ctx, target, []string{query})
	if err != nil || len(got) != 1 {
		if u.Mem != nil && u.Ctx.Err() == nil {
			u.Mem.Set("public_embedding_unavailable", true, 10*time.Minute)
		}
		return "", nil
	}
	v := publicEmbeddingArray(got[0])
	if v != nil {
		u.CacheSet(u.Ctx, key, packSourceEmbedding(got[0]), sourceEmbeddingTTL)
	}
	return model, v
}

const publicEmbeddingSearchBase = `SELECT p.platform,p.data
FROM public_post_embeddings e JOIN public_posts p USING(source_key,post_key)
WHERE e.model=$1 AND p.source_key=ANY($2::text[]) AND ` + publicEmbeddingSafeSQL
const publicEmbeddingExactSQL = publicEmbeddingSearchBase + `
ORDER BY posta_public_embedding_dot(e.embedding,$3::real[]) DESC,p.last_seen DESC,p.source_key,p.post_key LIMIT $4`
const publicEmbeddingVectorSQL = publicEmbeddingSearchBase + `
ORDER BY e.embedding::vector(512) <=> $3::real[]::vector(512) LIMIT $4`

func (u *U) searchPublicEmbeddings(query string, allowed []string) []publicInventoryCandidate {
	model, vector := u.publicQueryEmbedding(query)
	if vector == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(u.Ctx, 3*time.Second)
	defer cancel()
	var indexed bool
	if err := u.DB.Pool.QueryRow(ctx, "SELECT to_regclass('public_post_embeddings_hnsw') IS NOT NULL").Scan(&indexed); err != nil {
		return nil
	}
	const limit = 120
	if indexed {
		candidates := u.readPublicVectorCandidates(ctx, publicEmbeddingVectorSQL, model, allowed, vector, limit)
		if len(candidates) >= limit || ctx.Err() != nil {
			return candidates
		}
		// Approximate indexes apply source/model filters after candidate
		// retrieval. Recover sparse filtered matches with an exact query.
		if exact := u.readPublicVectorCandidates(ctx, publicEmbeddingExactSQL, model, allowed, vector, limit); len(exact) > len(candidates) {
			return exact
		}
		return candidates
	}
	return u.readPublicVectorCandidates(ctx, publicEmbeddingExactSQL, model, allowed, vector, limit)
}

func (u *U) readPublicVectorCandidates(ctx context.Context, sql, model string, allowed []string, vector []float32, limit int) []publicInventoryCandidate {
	rows, err := u.DB.Pool.Query(ctx, sql, model, allowed, vector, limit)
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
