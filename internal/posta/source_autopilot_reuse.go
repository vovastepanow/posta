package posta

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
)

// A neutral registry row records identity, not a thematic or technical verdict.
// Existing registry decisions (including owner decisions) are never changed.
func rememberAutopilotIdentity(ctx context.Context, tx pgx.Tx, source string, at int64) error {
	platform, id, _, ok := autopilotSourceParts(source)
	if !ok {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO source_registry(source_id,platform,url,status,first_seen_at)
		VALUES($1,$2,$3,'discovered',$4) ON CONFLICT(source_id) DO NOTHING`,
		source, platform, registryURL(platform, id), at)
	return err
}

// Update only public publisher metadata. All per-topic verdicts stay in
// source_autopilot_sources; never import a sample from the legacy registry,
// which can contain account-specific data.
func (a *App) rememberAutopilotPublicProfile(ctx context.Context, job *autopilotJob, source string, info jx.M, at time.Time) error {
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := rememberAutopilotIdentity(ctx, tx, source, at.Unix()); err != nil {
			return err
		}
		title := trimRunes(strings.TrimSpace(jx.Str(info, "title")), 240)
		if title == "" {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE source_registry SET name=$2
			WHERE source_id=$1 AND status='discovered' AND name=''`, source, title)
		return err
	})
}

const autopilotListingTTL = 20 * time.Minute
const autopilotListingMaxBytes = 512 << 10

func (a *App) autopilotReviewListing(source, feed string, reuse bool, fetch func(string) FetchResultURL) (FetchResultURL, bool) {
	if reuse && a.autopilotListings != nil {
		if hit, ok := Mem[FetchResultURL](a.autopilotListings, source); ok {
			// A copy prevents a parser from mutating a shared observation.
			hit.Bytes = append([]byte(nil), hit.Bytes...)
			return hit, true
		}
	}
	return fetch(feed), false
}

func (a *App) rememberAutopilotListing(source string, res FetchResultURL, count int) {
	if a.autopilotListings == nil || !res.OK || res.Status == 206 || count <= 0 ||
		len(res.Bytes) == 0 || len(res.Bytes) > autopilotListingMaxBytes {
		return
	}
	if _, _, _, ok := autopilotSourceParts(source); !ok {
		return
	}
	res.Bytes = append([]byte(nil), res.Bytes...)
	a.autopilotListings.Set(source, res, autopilotListingTTL)
}

// Public classifications are discovery hints, not proof of quality or
// permission to deliver a post. Only independently collected public records
// may nominate a source; personal histories and pools are never consulted.
func (a *App) autopilotNominateIndexedSources(ctx context.Context, job *autopilotJob, limit int) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope || limit <= 0 {
		return nil
	}
	topic := a.TopicLookup(job.Topic)
	if topic == nil || topic.Key == "" {
		return nil
	}
	at := time.Now()
	// Limit both the index scan and new candidates per cycle. The demand
	// queue retains prior topic decisions and does not reopen a rejection.
	rows, err := a.DB.Pool.Query(ctx, `WITH recent AS (
		SELECT p.source_key,p.post_key,p.last_seen
		FROM public_post_topics t JOIN public_posts p USING(source_key,post_key)
		WHERE t.topic_key=$1 AND p.last_seen >= $4
			AND NOT (p.data @> '{"text_incomplete":true}' OR p.data @> '{"media_incomplete":true}' OR p.data @> '{"source_sensitive":true}' OR p.data @> '{"nsfw":true}')
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_sources s WHERE s.scope=$2 AND s.topic_key=$3 AND s.source_id=p.source_key)
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE o.scope=$2 AND o.topic_key=$3 AND o.owner_id='' AND o.source_id=p.source_key AND o.decision='excluded')
		ORDER BY p.last_seen DESC LIMIT 500
	) SELECT source_key,count(*) FROM recent GROUP BY source_key HAVING count(*)>=2
		ORDER BY count(*) DESC,max(last_seen) DESC,source_key LIMIT $5`,
		topic.Key, job.Scope, job.TopicKey, at.Add(-7*24*time.Hour).Unix(), min(32,limit*4))
	if err != nil {
		return err
	}
	type nomination struct { source string; posts int }
	var candidates []nomination
	for rows.Next() {
		var n nomination
		if err := rows.Scan(&n.source, &n.posts); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates,n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range candidates {
		platform, _, _, ok := autopilotSourceParts(n.source)
		if !ok {
			continue
		}
		switch platform {
		case PlatformTelegram:
			ok = a.TBool("search_telegram")
		case PlatformVK:
			ok = a.TBool("search_vk")
		case PlatformOK:
			ok = a.TBool("autopilot_search_ok")
		case PlatformTikTok:
			ok = a.TBool("autopilot_search_tiktok")
		}
		if !ok {
			continue
		}
		if err := registerAutopilotCandidateWithEvidence(ctx, a.DB, job, n.source, "discovered",
			jx.Marshal(jx.M{"discovery_origin":"indexed_post","indexed_post_count":n.posts}), at); err != nil {
			return err
		}
		limit--
		if limit == 0 {
			break
		}
	}
	return nil
}
