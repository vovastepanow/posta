package posta

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
	"posta/internal/llm"
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
	var pending int
	if err := a.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND state='candidate'`, job.Scope, job.TopicKey).Scan(&pending); err != nil {
		return err
	}
	limit = min(limit, 64-pending)
	if limit <= 0 {
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
		topic.Key, job.Scope, job.TopicKey, at.Add(-7*24*time.Hour).Unix(), min(32, limit*4))
	if err != nil {
		return err
	}
	type nomination struct {
		source string
		posts  int
	}
	var candidates []nomination
	for rows.Next() {
		var n nomination
		if err := rows.Scan(&n.source, &n.posts); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, n)
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
			jx.Marshal(jx.M{"discovery_origin": "indexed_post", "indexed_post_count": n.posts}), at); err != nil {
			return err
		}
		limit--
		if limit == 0 {
			break
		}
	}
	return nil
}

// Repair identities that predate the unified registry in bounded batches.
// This is metadata only; no fetch, model call, verdict or owner data changes.
func (a *App) backfillAutopilotRegistry(ctx context.Context) error {
	const cursorKey = "autopilot:registry_backfill_cursor:v1"
	var cursor string
	a.KVGet(ctx, cursorKey, &cursor)
	rows, err := a.DB.Pool.Query(ctx, `SELECT s.source_id,min(s.first_seen_at)
		FROM source_autopilot_sources s
		WHERE s.scope='public' AND s.source_id>$1
		AND NOT EXISTS (SELECT 1 FROM source_registry r WHERE r.source_id=s.source_id)
		GROUP BY s.source_id ORDER BY s.source_id LIMIT 512`, cursor)
	if err != nil {
		return err
	}
	type identity struct {
		source string
		at     int64
	}
	var pending []identity
	for rows.Next() {
		var item identity
		if err := rows.Scan(&item.source, &item.at); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return a.KVSet(ctx, cursorKey, "", 0)
	}
	if err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		for _, item := range pending {
			if err := rememberAutopilotIdentity(ctx, tx, item.source, item.at); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return a.KVSet(ctx, cursorKey, pending[len(pending)-1].source, 0)
}

// Cache only completed, sufficiently covered text reviews for initial
// public candidates. Identical sample, topic profile and resolved model
// configuration are required; explicit rechecks bypass this cache.
func sourceTextReviewKey(topic, context, hint, title string, captions []string, models []string) string {
	return "source_text_review:v1:" + repeatDigest(string(jx.Marshal([]any{
		TopicProfileVersion, topic, context, hint, title, captions, models,
	})))
}

func validCachedSourceTextReview(ev *sourceEvaluation, sampleCount int) bool {
	if ev == nil || !ev.TextOnly || ev.Text == nil || *ev.Text != ev.Score ||
		ev.Score < 0 || ev.Score > 100 || len(ev.Images) > 0 ||
		ev.ReviewedSamples < 2 || ev.ReviewedSamples > sampleCount ||
		ev.ReviewedSamples*3 < sampleCount*2 || ev.Quality == nil {
		return false
	}
	var total int64
	for _, field := range []string{"topic_relevant", "topic_adjacent", "topic_off_topic"} {
		if ev.Quality[field] == nil || jx.Int(ev.Quality, field) < 0 {
			return false
		}
		total += jx.Int(ev.Quality, field)
	}
	return total == int64(ev.ReviewedSamples) && jx.Int(ev.Quality, "topic_unknown") == int64(sampleCount)-total
}

func (a *App) autopilotReuseTextReview(ctx context.Context, job *autopilotJob, info jx.M, posts []jx.M, reuse bool,
	evaluate func(jx.M, []jx.M, string) *sourceEvaluation) *sourceEvaluation {
	if !reuse || a.LLM == nil || a.DB == nil || job.Scope != autopilotAutoScope {
		return evaluate(info, posts, job.Topic)
	}
	u := a.User(ctx, "", nil)
	format := u.TopicFormatOf(job.Topic)
	if format.Media != "optional" || format.Judge == "image" || sourcePostsHaveImages(posts) {
		return evaluate(info, posts, job.Topic)
	}
	for _, post := range posts {
		if PostItemHasVideo(post) || jx.Str(post, "video_via") != "" || jx.Str(post, "audio") != "" ||
			jx.Bool(post, "text_incomplete") || jx.Bool(post, "media_incomplete") || jx.Bool(post, "video_unavailable") ||
			len(jx.List(post, "source_attachments")) > 0 || len(jx.List(post, "gallery")) > 0 {
			return evaluate(info, posts, job.Topic)
		}
	}
	captions, readable := autopilotSourceCaptionSample(posts)
	if readable < 2 || readable*3 < len(captions)*2 {
		return evaluate(info, posts, job.Topic)
	}
	primary, fallback, _ := a.LLM.Resolve(ctx, llm.RoleSmart)
	if primary.Empty() {
		return evaluate(info, posts, job.Topic)
	}
	key := sourceTextReviewKey(job.Topic, u.TopicPromptContext(job.Topic), u.TopicProfileHint(job.Topic),
		trimRunes(jx.Str(info, "title"), 180), captions, []string{primary.String(), fallback.String()})
	var cached sourceEvaluation
	if a.CacheGet(ctx, key, &cached) && validCachedSourceTextReview(&cached, len(captions)) {
		return &cached
	}
	ev := evaluate(info, posts, job.Topic)
	if autopilotBudgetError(ctx) == nil && ctx.Err() == nil && validCachedSourceTextReview(ev, len(captions)) {
		a.CacheSet(ctx, key, ev, 24*time.Hour)
	}
	return ev
}
