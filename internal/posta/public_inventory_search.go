package posta

import (
	"context"
	"strings"
	"time"

	"posta/internal/jx"
)

// searchPublicInventory retrieves candidates from eligible public sources,
// regardless of legacy topic. The collector still checks each candidate.
func (u *U) searchPublicInventory(query string, sourceAllowed func(string) bool) map[string][]jx.M {
	query = strings.TrimSpace(query)
	out := map[string][]jx.M{}
	if query == "" {
		return out
	}
	var allowed []string
	catalog := u.Catalog()
	for _, kind := range CatalogKinds {
		for _, entry := range *catalog.List(kind.Kind) {
			if !sharedSourceEligible(kind.Platform, entry) {
				continue
			}
			id := SourceIDFor(kind.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(entry, "id")), "@"))
			if sourceAllowed(id) && u.UASourceBlocked(id, nil) == "" {
				allowed = append(allowed, id)
			}
		}
	}
	if len(allowed) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(u.Ctx, 3*time.Second)
	defer cancel()
	rows, err := u.DB.Pool.Query(ctx, publicInventorySearchSQL, trimRunes(query, 300), allowed, 240)
	if err != nil {
		if u.Ctx.Err() == nil {
			u.Log("Общий индекс недоступен: %v", err)
		}
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var platform string
		var raw []byte
		if err := rows.Scan(&platform, &raw); err != nil {
			u.Log("Чтение общего индекса: %v", err)
			return out
		}
		if post := jx.Unmarshal(raw); post != nil {
			out[platform] = append(out[platform], post)
		}
	}
	if err := rows.Err(); err != nil && u.Ctx.Err() == nil {
		u.Log("Чтение общего индекса: %v", err)
	}
	return out
}

// Freshness remains a downstream profile policy, not a universal cutoff.
const publicInventorySearchSQL = `WITH q AS (
	SELECT plainto_tsquery('pg_catalog.simple'::regconfig,$1)
		|| plainto_tsquery('pg_catalog.russian'::regconfig,$1)
		|| plainto_tsquery('pg_catalog.english'::regconfig,$1) AS terms
)
SELECT p.platform,p.data
FROM public_posts p CROSS JOIN q
WHERE p.source_key = ANY($2::text[])
	AND posta_public_search_vector(p.data) @@ q.terms
	AND NOT (p.data @> '{"text_incomplete":true}' OR p.data @> '{"media_incomplete":true}'
		OR p.data @> '{"video_unavailable":true}' OR p.data @> '{"source_sensitive":true}'
		OR p.data @> '{"nsfw":true}')
ORDER BY ts_rank_cd(posta_public_search_vector(p.data),q.terms) DESC,
	p.last_seen DESC,p.source_key,p.post_key
LIMIT $3`
