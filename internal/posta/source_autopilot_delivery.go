package posta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/config"
	"posta/internal/jx"
)

func autopilotFormatAllowsItem(u *U, topic string, post jx.M) bool {
	format := u.TopicFormatOf(topic)
	if format.Media == "required" && ContentImageKey(post) == "" {
		return false
	}
	if format.Text == "required" && runeLen(strings.TrimSpace(jx.Str(post, "text"))) < format.MinText {
		return false
	}
	return true
}

// Media is written under an immutable content-derived name while the lease and
// source decision are locked. A stale worker cannot replace another result.
func (a *App) cacheAutopilotMedia(ctx context.Context, job *autopilotJob, source, digest string, data []byte, video bool) (string, error) {
	if job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return "", nil
	}
	hash := sha256.Sum256(data)
	if len(data) == 0 || hex.EncodeToString(hash[:]) != digest {
		return "", errors.New("media digest mismatch")
	}
	var local string
	err := withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, source); err != nil {
			return err
		}
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, source).Scan(&state); err != nil {
			return err
		}
		if state != "probation" && state != "active" {
			return errAutopilotSourceChanged
		}
		if !video {
			local = a.MediaStoreImage("autopilot:image:"+digest, data)
			if local == "" {
				return errors.New("image cache unavailable")
			}
			return nil
		}
		ext := ""
		switch http.DetectContentType(data) {
		case "video/mp4":
			ext = "mp4"
		case "video/webm":
			ext = "webm"
		default:
			return errors.New("unsupported video format")
		}
		var err error
		local, err = a.MediaWrite(MediaCacheName("autopilot:video:"+digest, ext), data)
		return err
	})
	return local, err
}

func (a *App) saveAutopilotContentResult(ctx context.Context, job *autopilotJob, source string, post, analysis jx.M) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	posted := jx.Int(post, "posted_at")
	if !autopilotPublicPostURL(jx.Str(post, "url"), false) || posted <= 0 || posted > time.Now().Unix()+300 || IsPostOutdated(posted, 0, config.ContentMaxAgeDays) || jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") || len(jx.ToStrs(post["safety"])) > 0 || jx.Bool(analysis, "tags_classification_pending") {
		return nil
	}
	if ad, ok := analysis["advertising"].(bool); !ok || ad || PostIsAdvertisement(post) {
		return nil
	}
	if a.ContentIsAdult(jx.Merge(post, jx.M{"tags": analysis["tags"]})) {
		return nil
	}
	postJSON, analysisJSON := jx.Marshal(post), jx.Marshal(analysis)
	if len(postJSON) > 100000 || len(analysisJSON) > 50000 {
		return nil
	}
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, source); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_content_results(scope,topic_key,source_id,post_hash,post,analysis,published_at,assessed_at,policy_version)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 WHERE EXISTS(SELECT 1 FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND state IN ('probation','active'))
			ON CONFLICT(scope,topic_key,source_id,post_hash) DO UPDATE SET post=EXCLUDED.post,analysis=EXCLUDED.analysis,assessed_at=EXCLUDED.assessed_at,policy_version=EXCLUDED.policy_version`,
			job.Scope, job.TopicKey, source, repeatDigest(CandidatePostKey(post)), postJSON, analysisJSON, jx.Int(post, "posted_at"), time.Now().Unix(), TopicProfileVersion)
		return err
	})
}

func (a *App) deliverAutopilotTopicContent(ctx context.Context, job *autopilotJob) error {
	if job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT DISTINCT r.source_id FROM source_autopilot_content_results r JOIN source_autopilot_sources s ON s.scope=r.scope AND s.topic_key=r.topic_key AND s.source_id=r.source_id WHERE r.scope=$1 AND r.topic_key=$2 AND s.state IN ('probation','active') ORDER BY r.source_id`, job.Scope, job.TopicKey)
	if err != nil {
		return err
	}
	var sources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, source := range sources {
		if err := a.deliverAutopilotContent(ctx, job, source); err != nil {
			return err
		}
	}
	return nil
}

// Deliver public, already assessed content separately for each subscriber. The
// final pool transaction rechecks that owner's demand, override, source state
// and lease; no user history or private content enters the public evidence.
func (a *App) deliverAutopilotContent(ctx context.Context, job *autopilotJob, source string) error {
	if job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT post,analysis,assessed_at FROM source_autopilot_content_results WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND published_at >= $4 AND policy_version=$5 ORDER BY published_at DESC LIMIT 100`, job.Scope, job.TopicKey, source, time.Now().Add(-time.Duration(config.ContentMaxAgeDays)*24*time.Hour).Unix(), TopicProfileVersion)
	if err != nil {
		return err
	}
	type assessed struct {
		post, analysis jx.M
		at             int64
	}
	var items []assessed
	for rows.Next() {
		var post, analysis []byte
		var at int64
		if err := rows.Scan(&post, &analysis, &at); err != nil {
			rows.Close()
			return err
		}
		items = append(items, assessed{jx.Unmarshal(post), jx.Unmarshal(analysis), at})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = a.DB.Pool.Query(ctx, `SELECT user_id,topic,posts_per_day,horizon_days FROM source_autopilot_demands WHERE topic_key=$1 AND mode='auto' AND NOT paused AND posts_per_day>0 ORDER BY user_id`, job.TopicKey)
	if err != nil {
		return err
	}
	type subscriber struct {
		owner, topic  string
		rate, horizon float64
	}
	var subscribers []subscriber
	for rows.Next() {
		var s subscriber
		if err := rows.Scan(&s.owner, &s.topic, &s.rate, &s.horizon); err != nil {
			rows.Close()
			return err
		}
		subscribers = append(subscribers, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var firstUserErr error
	for _, s := range subscribers {
		if err := ctx.Err(); err != nil {
			return err
		}
		u := a.User(contextWithAutopilotLease(ctx, job), s.owner, nil)
		if sourceExcludedByOverride(u.sourceOverridesForTopic(s.topic), source) {
			continue
		}
		available, err := u.AutopilotAvailableSupply(s.topic)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if firstUserErr == nil {
				firstUserErr = err
			}
			continue
		}
		demand := calculateAutopilotDemand(s.rate, s.horizon, available)
		remaining := max(0, min(demand.Deficit, u.TInt("pool_target")-available))
		if remaining == 0 {
			continue
		}
		filters := u.ContentFilterValues()
		frozen := map[string]bool{}
		if jx.Bool(filters, "active_sources_only") {
			frozen = u.FrozenSourceLabels()
		}
		for _, item := range items {
			if remaining == 0 {
				break
			}
			post, analysis := item.post, item.analysis
			if post == nil || analysis == nil || jx.Bool(analysis, "tags_classification_pending") {
				continue
			}
			isVideo := PostItemHasVideo(post)
			isImage := ContentImageKey(post) != ""
			if isVideo || isImage {
				// Only locally retained, fully assessed bytes can reach a pool.
				if item.at <= 0 || (isVideo && (jx.Str(post, "video") == "" || jx.Bool(post, "video_unavailable") || a.MediaExisting(jx.Str(post, "video_local")) == "")) || (!isVideo && a.MediaExisting(jx.Str(post, "image_local")) == "") {
					continue
				}
			} else if !u.TBool("text_posts") || !u.TopicsAcceptText([]string{TopicBase(s.topic)}) {
				continue
			}
			if jx.Int(post, "posted_at") <= 0 || IsPostOutdated(jx.Int(post, "posted_at"), 0, config.ContentMaxAgeDays) {
				continue
			}
			if !autopilotFormatAllowsItem(u, s.topic, post) {
				continue
			}
			post = jx.Copy(post)
			post["source_key"], post["source_url"] = source, jx.Str(post, "url")
			if jx.Str(post, "source") == "" {
				platform, id, _, _ := autopilotSourceParts(source)
				if platform == PlatformRSS {
					id = hostOf(id)
				}
				post["source"] = SourceDisplayName(platform, id)
			}
			text := AnalysisText(post)
			if (!isImage && !isVideo && text == "") || u.contentAdmissionReason(analysis, post, s.topic, text) != "" {
				continue
			}
			format := ""
			if !isImage && !isVideo {
				format = "text"
			}
			rec := analysisRecord(analysis, post, s.topic, format)
			if !isImage && !isVideo {
				rec["image_local"] = ""
				rec["phash"], rec["image"], rec["images"], rec["captions"] = "", "", []any{}, []any{text}
			} else if isVideo {
				// Do not fall back to a mutable remote URL if the cache disappears.
				rec["video"] = jx.Str(post, "video_local")
			} else {
				rec["image"] = jx.Str(post, "image_local")
			}
			if !u.PoolItemInTopic(rec, s.topic, s.topic, u.AdultAllowed()) || !poolMatchesDisplayFilters(rec, filters, frozen) {
				continue
			}
			delivery := a.User(withAutopilotDelivery(u.Ctx, s.owner, source, s.topic, post, analysis), s.owner, nil)
			if delivery.AddItemToPoolSafely(rec) {
				remaining--
			}
		}
	}
	return firstUserErr
}
