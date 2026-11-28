package posta

import (
	"context"
	"sort"
	"strings"
	"time"

	"posta/internal/jx"
)

type publicInventoryCandidate struct {
	platform string
	post jx.M
}

// Reciprocal rank fusion combines lexical and semantic ranks without assuming
// their scores share a scale. Candidates are never accepted by this score.
func fusePublicInventory(lists ...[]publicInventoryCandidate) map[string][]jx.M {
	type scored struct {
		c publicInventoryCandidate
		score float64
	}
	var all []scored
	index := map[string]int{}
	for _, list := range lists {
		seen := map[string]bool{}
		for rank, c := range list {
			key := CandidatePostKey(c.post)
			if key == "" {
				key = ContentItemKey(c.post)
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			i, ok := index[key]
			if !ok {
				i = len(all)
				index[key] = i
				all = append(all, scored{c: c})
			}
			all[i].score += 1 / float64(60+rank+1)
		}
	}
	sort.SliceStable(all, func(i,j int) bool { return all[i].score > all[j].score })
	out := map[string][]jx.M{}
	for _, r := range all[:min(len(all),240)] {
		out[r.c.platform] = append(out[r.c.platform], r.c.post)
	}
	return out
}

// Search eligible public sources without requiring legacy topic labels.
func (u *U) searchPublicInventory(query string, sourceAllowed func(string) bool, variants ...string) map[string][]jx.M {
	query = trimRunes(strings.TrimSpace(query), 300)
	if query == "" {
		return nil
	}
	var allowed []string
	for _, id := range u.publicEmbeddingSources() {
		if sourceAllowed(id) && u.UASourceBlocked(id, nil) == "" {
			allowed = append(allowed, id)
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	// Keep the exact original wording and a small set of existing search
	// plan variants. No extra LLM call and no taxonomy required.
	terms := publicInventoryTerms(query, variants)
	ctx, cancel := context.WithTimeout(u.Ctx, 3*time.Second)
	defer cancel()
	lists := make([][]publicInventoryCandidate, 0, len(terms)+1)
	for _, term := range terms {
		lists = append(lists, u.searchPublicLexical(ctx, term, allowed))
		if ctx.Err() != nil {
			break
		}
	}
	cancel()
	lists = append(lists, u.searchPublicEmbeddings(query, allowed))
	return fusePublicInventory(lists...)
}

func publicInventoryTerms(query string, variants []string) []string {
	var terms []string
	seen := map[string]bool{}
	for _, term := range append([]string{query}, variants...) {
		term = trimRunes(strings.TrimSpace(term), 300)
		key := strings.ToLower(term)
		if key != "" && !seen[key] {
			seen[key] = true
			terms = append(terms, term)
		}
		if len(terms) >= 4 {
			break
		}
	}
	return terms
}

func (u *U) searchPublicLexical(ctx context.Context, query string, allowed []string) []publicInventoryCandidate {
	rows, err := u.DB.Pool.Query(ctx, publicInventorySearchSQL, query, allowed, 240)
	var lexical []publicInventoryCandidate
	if err == nil {
		for rows.Next() {
			var platform string
			var raw []byte
			if err = rows.Scan(&platform, &raw); err != nil {
				break
			}
			if post := jx.Unmarshal(raw); post != nil {
				lexical = append(lexical, publicInventoryCandidate{platform, post})
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	if err != nil && u.Ctx.Err() == nil && ctx.Err() == nil {
		u.Log("Общий текстовый индекс: %v", err)
	}
	return lexical
}

const publicInventorySearchSQL = `WITH q AS (
	SELECT plainto_tsquery('pg_catalog.simple'::regconfig,$1)
		|| plainto_tsquery('pg_catalog.russian'::regconfig,$1)
		|| plainto_tsquery('pg_catalog.english'::regconfig,$1) AS terms
)
SELECT p.platform,p.data
FROM public_posts p CROSS JOIN q
WHERE p.source_key = ANY($2::text[])
	AND posta_public_search_vector(p.data) @@ q.terms
	AND ` + publicEmbeddingSafeSQL + `
ORDER BY ts_rank_cd(posta_public_search_vector(p.data),q.terms) DESC,
	p.last_seen DESC,p.source_key,p.post_key
LIMIT $3`
