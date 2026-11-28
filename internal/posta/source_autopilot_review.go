package posta

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
)

// Schedule all previously inspected sources once under the new review policy.
// Keep their states and historical decisions; the worker owns the transitions.
// Respect explicit exclusions and provider backoff instead of resetting them.
func (a *App) autopilotQueueSourceReaudit(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) {
		return errors.New("invalid source reaudit job")
	}
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH scheduled AS (
			UPDATE source_autopilot_sources s
			SET next_check_at=CASE WHEN s.reason='rate_limited' THEN s.next_check_at ELSE LEAST(s.next_check_at,$3) END,
				evidence=s.evidence || '{"review_policy_v3_scheduled":true}'::jsonb
			WHERE s.scope=$1 AND s.topic_key=$2 AND s.checked_at>0
			AND s.state IN ('candidate','probation','active','cooldown','rejected')
			AND s.reason<>'source_identity_recovered'
			AND NOT (s.evidence ? 'review_policy_v3_scheduled')
			AND NOT (s.evidence ? 'review_policy_v3_checked')
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o
				WHERE (o.scope=$1 OR ($1='public:shadow' AND o.scope='public'))
				AND o.owner_id='' AND o.topic_key=$2 AND o.source_id=s.source_id AND o.decision='excluded')
			RETURNING source_id,state
		)
		INSERT INTO source_autopilot_decisions
			(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
		SELECT $1,$2,source_id,state,state,'source_reaudit_v3_scheduled','{"review_policy_v3_scheduled":true}'::jsonb,$4,$3 FROM scheduled`,
			job.Scope, job.TopicKey, time.Now().Unix(), TopicProfileVersion)
		return err
	})
}

// Review only public feed data. An unavailable feed, model error or small
// sample remains uncertain; none of them proves poor source quality.
func (a *App) autopilotReviewShadowCandidates(ctx context.Context, job *autopilotJob, limit int) error {
	if err := a.autopilotQueueSourceReaudit(ctx, job); err != nil {
		return err
	}
	if job.Scope == autopilotAutoScope {
		if err := a.autopilotSchedulePreliminaryProbe(ctx, job, false); err != nil {
			return err
		}
		if err := a.autopilotRecoverLegacyYouTube(ctx, job); err != nil {
			return err
		}
	}
	ctx = withAutopilotAssessmentProbe(ctx)
	u := a.User(ctx, "", nil)
	return a.autopilotReviewShadowCandidatesWith(ctx, job, limit, a.TInt("autopilot_preliminary_score"),
		func(feed string) FetchResultURL {
			if platform, id, _, ok := autopilotSourceParts(feed); ok {
				switch platform {
				case PlatformTelegram:
					return autopilotReadListing("https://t.me/s/"+id, BrowserUA, u.RSSGet)
				case PlatformVK:
					return autopilotReadListing("https://vk.com/"+id, BrowserUA, u.RSSGet)
				case PlatformOK:
					return autopilotReadListing(okGroupURL(id), BrowserUA, u.RSSGet)
				case PlatformTikTok:
					return u.autopilotTikTokListing(id)
				}
			}
			if autopilotPublicRedditFeed(feed) {
				return u.RSSGet("https://www.reddit.com/r/"+redditSourceName(feed)+"/new.json?limit=25", rssMaxBytes, 15*time.Second, HTTPUA)
			}
			if _, ok := autopilotMastodonFeed(feed); ok {
				return u.autopilotMastodonListing(feed)
			}
			if host, id := osnovaTimelineSource(feed); host != "" {
				return u.RSSGet(osnovaTimelineURL(host, id), rssMaxBytes, 15*time.Second, HTTPUA)
			}
			if id := RutubeChannelID(feed); id != "" {
				return u.RSSGet("https://rutube.ru/api/video/person/"+id+"/?page=1&format=json", rssMaxBytes, 15*time.Second, HTTPUA)
			}
			if pikabuCommunityURL(feed) != "" {
				return u.RSSGet(feed, rssMaxBytes, 15*time.Second, BrowserUA)
			}
			return autopilotReadRSS(feed, u.RSSGet)
		}, u.autopilotEvaluateSource,
		func(ctx context.Context, source string, posts []jx.M) error {
			return a.assessAutopilotPublicPosts(ctx, job, source, posts)
		}, a.autopilotSourcePolicy())
}

// autopilotOKSMMTopicReason rejects only groups with a readable off-topic
// title AND several readable unrelated captions. When the mobile HTML doesn't
// expose a title, or the posts are mainly visual, defer instead of inventing
// a negative content verdict. Two independent relevant posts are sufficient
// to send a neutral-titled group onward to the normal AI assessment.
func autopilotOKSMMTopicReason(title, id string, posts []jx.M) string {
	if okDiscoveryGroupTitleMatches(title, "SMM и блогинг", nil) {
		return ""
	}
	readable := 0
	relevant := 0
	for _, post := range posts {
		caption := PostCaptionClean(jx.Str(post, "text")) + " " + jx.Str(post, "title")
		if len([]rune(strings.TrimSpace(caption))) < 24 {
			continue
		}
		readable++
		if okDiscoveryTopicTextMatches(caption, "SMM и блогинг", nil) {
			relevant++
			if relevant >= 2 {
				return ""
			}
		}
	}
	if strings.TrimSpace(title) == "" || title == id || readable < 3 || relevant > 0 {
		return "source_metadata_unavailable"
	}
	return "off_topic_group"
}

// A source with only two fresh posts can enter probation when the model is
// exceptionally confident. Single-post feeds remain unverified; probation
// still requires per-post assessments before content can be delivered.
// Frequent auto-discovery is useful only if known feeds are revisited too.
// Healthy topics still run at their normal scheduler cadence, and explicit
// 429/unavailable backoff is never shortened by these intervals.
func autopilotSourceReviewInterval(job *autopilotJob, state string) time.Duration {
	if job != nil && job.Scope == autopilotAutoScope && job.Kind == autopilotAutoKind {
		switch state {
		case "candidate":
			return 12 * time.Hour
		case "probation":
			return 6 * time.Hour
		case "active":
			return 3 * time.Hour
		}
	}
	return 24 * time.Hour
}

func autopilotPreliminaryPass(score, threshold, freshSamples int) bool {
	if freshSamples < 2 || threshold < 0 || threshold > 100 {
		return false
	}
	if freshSamples == 2 {
		threshold = min(100, threshold+10)
	}
	return score >= threshold
}

// Failing the admission threshold is not proof that the publisher is off-topic.
// A negative verdict needs a broader sample and a clear margin from the gate.
func autopilotPreliminaryReject(score, threshold, samples int) bool {
	return samples >= 5 && score >= 0 && score <= 100 && threshold >= 10 && threshold <= 100 && score <= threshold-10
}

// A mixed publisher with a substantial number of directly relevant posts
// may enter probation, not active status. Each post still passes the normal
// admission pipeline, and the established promotion threshold is unchanged.
func autopilotSourcePreliminaryDecision(ev *sourceEvaluation, threshold, fetched int) (pass, reject bool) {
	if ev == nil || threshold < 0 || threshold > 100 {
		return false, false
	}
	reviewed := min(fetched, ev.ReviewedSamples)
	if reviewed == 0 && ev.Text == nil && len(ev.Images) == 0 && ev.Quality == nil {
		// Legacy injected evaluations without component scores.
		reviewed = fetched
	}
	pass = autopilotPreliminaryPass(ev.Score, threshold, reviewed)
	reject = autopilotPreliminaryReject(ev.Score, threshold, min(fetched, ev.ReviewedSamples))
	if ev.Quality != nil && ev.Quality["topic_relevant"] != nil {
		relevant := int(jx.Int(ev.Quality, "topic_relevant"))
		offTopic := int(jx.Int(ev.Quality, "topic_off_topic"))
		pass = pass || (reviewed >= 5 && relevant >= 3 && relevant*2 >= reviewed &&
			ev.Score >= max(60, threshold-20))
		reject = reject && offTopic*5 >= reviewed*3
	}
	return pass, reject && !pass
}

func autopilotStreamUnavailableReason(err error) string {
	if errors.Is(err, errAutopilotTikTokStreamUnavailable) {
		return "tiktok_stream_unavailable"
	}
	if errors.Is(err, errAutopilotYouTubeStreamUnavailable) {
		return "youtube_stream_unavailable"
	}
	return ""
}

// Technical failures never establish thematic relevance. New candidates
// cool down on transient failures; a confirmed missing feed is rejected.
// Previously admitted sources get a reversible cooldown instead.
func autopilotReviewFetchFailure(state, reason string, at, nextCheck time.Time) (string, time.Time) {
	if reason == "source_not_found" {
		switch state {
		case "candidate":
			return "rejected", at.Add(30 * 24 * time.Hour)
		case "probation", "active":
			return "cooldown", at.Add(24 * time.Hour)
		}
	}
	if state == "candidate" {
		switch reason {
		case "feed_unavailable", "rate_limited", "feed_access_restricted", "feed_too_large":
			return "cooldown", nextCheck
		case "unrecognized_feed", "feed_parse_failed":
			return "cooldown", at.Add(7 * 24 * time.Hour)
		}
	}
	return "", time.Time{}
}

// Both strict admission and yield admission share the same trial-capacity
// handling. A passed review waiting for a place remains a verified candidate.
func (a *App) autopilotBeginReviewTrial(ctx context.Context, job *autopilotJob, source, reason string, evidence jx.M, at time.Time) (bool, error) {
	data := jx.Marshal(evidence)
	err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate", "probation", reason, data, at, at.Add(autopilotSourceReviewInterval(job, "probation")))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errAutopilotProbationLimit):
		err = recordAutopilotSourceCheck(ctx, a.DB, job, source, "candidate", "preliminary_pass_waiting_trial", data, at, at.Add(30*time.Minute))
		if errors.Is(err, errAutopilotSourceChanged) {
			err = nil
		}
		return false, err
	case autopilotSourceSuperseded(err):
		return false, nil
	default:
		return false, err
	}
}

func (a *App) autopilotReviewShadowCandidatesWith(ctx context.Context, job *autopilotJob, limit, threshold int, fetch func(string) FetchResultURL, evaluate func(jx.M, []jx.M, string) *sourceEvaluation, assess func(context.Context, string, []jx.M) error, policy sourcePolicy) error {
	if !validAutopilotPublicJob(job) || limit < 1 {
		return errors.New("invalid shadow review")
	}
	if fetch == nil || evaluate == nil || assess == nil || threshold < 0 || threshold > 100 {
		return errors.New("invalid shadow review policy")
	}
	ids, err := a.autopilotDueReviewSources(ctx, job, limit)
	if err != nil {
		return err
	}
	diagnosticAttempted := false
	basePolicy := policy
	for _, row := range ids {
		policy = basePolicy
		source, state := row.id, row.state
		// A pilot applies only to a small cohort backed by a completed,
		// independently admitted post from the publisher's own feed.
		pilotAllowed := job.Scope == autopilotAutoScope && policy.YieldPilotEnabled && !policy.YieldEnabled
		pilot := pilotAllowed && (row.pilot || row.probePositive)
		if pilot {
			policy.YieldEnabled = true
		}
		newTrial := state == "candidate"
		// A current admitted post (direct search or independent probe) can
		// override an unreliable source-level score, but readability cannot.
		verifiedAdmission := row.probePositive || row.lane == 1
		if err := ctx.Err(); err != nil {
			return err
		}
		platform, id, _, valid := autopilotSourceParts(source)
		if !valid {
			if err := a.autopilotRejectUnsupportedReviewSource(ctx, job, source, state); err != nil {
				return err
			}
			continue
		}
		handled, err := a.autopilotPrepareReviewSource(ctx, job, row)
		if err != nil {
			return err
		}
		if handled {
			continue
		}
		feed := id
		if platform != PlatformRSS {
			feed = source
		}
		at := time.Now()
		// Only initial candidate reviews reuse public listings. Established
		// sources and explicit rechecks always obtain a new observation.
		reuse := job.Scope == autopilotAutoScope && state == "candidate" && !row.reaudit && !row.freshRecheck
		res, reused := a.autopilotReviewListing(source, feed, reuse, fetch)
		if err := autopilotBudgetError(ctx); err != nil {
			return err
		}
		reason := ""
		nextCheck := at.Add(autopilotSourceReviewInterval(job, state))
		var posts []jx.M
		if !res.OK {
			reason = autopilotFeedReadReason(res)
			nextCheck = autopilotRetryAt(at, source, res.RetryAfter)
		} else {
			posts, reason = autopilotParseReviewListing(platform, id, feed, res)
			if reason == "" && !reused {
				if err := recordAutopilotShadowFetch(ctx, a.DB, job, source, at.Unix(), len(posts)); err != nil {
					return err
				}
				if job.Scope == autopilotAutoScope {
					a.rememberAutopilotListing(source, res, len(posts))
				}
			}
		}
		// Older Lemmy community candidates may have entered this queue
		// solely because its search engine matched "smm" in "womensmma".
		// Recheck the published community and posts before paying for AI.
		if reason == "" && res.OK && platform == PlatformRSS &&
			(state == "candidate" || state == "probation") &&
			autopilotCanonicalTopic(job.Topic) == "SMM и блогинг" &&
			lemmySMMFeedOffTopic(feed, res.Bytes, posts) {
			if err := transitionAutopilotSource(ctx, a.DB, job, source, state,
				"rejected", "off_topic_group", jx.Marshal(jx.M{
					"origin": "lemmy", "topic": job.Topic,
				}), at, at.Add(30*24*time.Hour)); err != nil &&
				!autopilotSourceSuperseded(err) {
				return err
			}
			continue
		}
		if next, retry := autopilotReviewFetchFailure(state, reason, at, nextCheck); next != "" {
			evidence := jx.M{"http_status": res.Status}
			if reason != "source_not_found" {
				evidence["retry_reason"] = reason
			}
			if err := transitionAutopilotSource(ctx, a.DB, job, source, state, next, reason, jx.Marshal(evidence), at, retry); err != nil &&
				!autopilotSourceSuperseded(err) {
				return err
			}
			continue
		}
		// Historical material informs source quality, not eligibility of a
		// particular old post for today's feed.
		samples := autopilotSourceQualitySamples(posts, at.Unix())
		safeSamples := samples[:0]
		for _, post := range samples {
			if !a.publicContentExcluded(post) {
				safeSamples = append(safeSamples, post)
			}
		}
		samples = safeSamples
		auditSamples := autopilotAuditPostSamples(samples)
		sourceInfo := autopilotSourceReviewInfo(platform, id, feed, res, samples)
		displayEvidence := autopilotSourceDisplayEvidence(sourceInfo, platform, feed)
		if reason == "" && len(samples) > 0 && job.Scope == autopilotAutoScope {
			if err := a.rememberAutopilotPublicProfile(ctx, job, source, sourceInfo, at); err != nil {
				return err
			}
		}
		// A missing OK title is a parsing/metadata failure, NOT evidence
		// that the group is about another topic. Look at multiple published
		// captions before rejecting a group with a generic title.
		if reason == "" && (state == "candidate" || state == "probation") &&
			platform == PlatformOK && autopilotCanonicalTopic(job.Topic) == "SMM и блогинг" {
			groupTitle := OKTitleFromHTML(string(res.Bytes), id)
			topicReason := autopilotOKSMMTopicReason(groupTitle, id, samples)
			if topicReason != "" {
				if topicReason == "off_topic_group" {
					if err := transitionAutopilotSource(ctx, a.DB, job, source, state, "rejected",
						topicReason, jx.Marshal(jx.M{"http_status": res.Status, "ok_topic_reaudit": true, "review_samples": auditSamples}),
						at, at.Add(30*24*time.Hour)); err != nil &&
						!autopilotSourceSuperseded(err) {
						return err
					}
				} else {
					if err := transitionAutopilotSource(ctx, a.DB, job, source, state, "cooldown",
						topicReason, jx.Marshal(jx.M{"http_status": res.Status, "ok_topic_reaudit": true, "review_samples": auditSamples}),
						at, at.Add(12*time.Hour)); err != nil &&
						!autopilotSourceSuperseded(err) {
						return err
					}
				}
				continue
			}
		}
		if reason == "" && state == "candidate" && (len(samples) == 0 || (!policy.YieldEnabled && len(samples) < 2)) {
			// A sparse publisher is worth revisiting, not rejecting. It also
			// must not block all other candidates for another 24 hours.
			retry := 30 * 24 * time.Hour
			if policy.YieldEnabled {
				retry = 7 * 24 * time.Hour
			}
			if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate",
				"cooldown", "sparse_publisher", jx.Marshal(jx.M{
					"quality_samples":          len(samples),
					"quality_window_days":      autopilotSourceQualityDays,
					"review_samples":           auditSamples,
					"review_policy_v3_checked": true,
				}), at, at.Add(retry)); err != nil &&
				!autopilotSourceSuperseded(err) {
				return err
			}
			continue
		}
		freshSamples := autopilotFreshRSSSamples(samples, at.Unix())
		evidence := jx.M{"quality_samples": len(samples), "fresh_samples": len(freshSamples), "sample_limit": 20, "quality_window_days": autopilotSourceQualityDays, "review_samples": auditSamples}
		for key, value := range displayEvidence {
			evidence[key] = value
		}
		evidence["shared_listing_reused"] = reused
		if res.Status > 0 {
			evidence["http_status"] = res.Status
		}
		// Even a thematically excellent historical feed cannot replenish
		// the live pool if it no longer publishes. Keep it on a retry
		// schedule, not in the active trial queue or thematic rejects.
		if reason == "" && autopilotSourceDefinitelyStale(posts, at.Unix()) {
			if err := transitionAutopilotSource(ctx, a.DB, job, source, state,
				"cooldown", "no_recent_publications", jx.Marshal(evidence),
				at, at.Add(7*24*time.Hour)); err != nil &&
				!autopilotSourceSuperseded(err) {
				return err
			}
			continue
		}
		if policy.YieldEnabled && row.reaudit && (state == "probation" || state == "active") {
			evidence["review_policy_v3_checked"] = true
		}
		if reason == "" && policy.YieldEnabled && state == "candidate" && len(freshSamples) == 0 {
			if err := transitionAutopilotSource(ctx, a.DB, job, source, state, "cooldown", "insufficient_fresh_samples", jx.Marshal(evidence), at, at.Add(7*24*time.Hour)); err != nil &&
				!autopilotSourceSuperseded(err) {
				return err
			}
			continue
		}
		if reason == "" && state == "candidate" && job.Scope == autopilotAutoScope && a.LLM != nil &&
			!verifiedAdmission && !row.reaudit && !row.freshRecheck && !row.embedDeferred &&
			a.autopilotEmbeddingEligible(ctx, job.Topic, samples) {
			score, status := a.autopilotEmbeddingPrescreen(ctx, job, sourceInfo, samples)
			evidence["embedding_prescreen_status"] = status
			if score != nil {
				evidence["embedding_prescreen"] = score
				if sourceEmbeddingShouldDefer(score, a.TInt("autopilot_embed_defer_score"), true, min(10, len(samples))) {
					evidence["embedding_deferred_at"] = at.Unix()
					if err := recordAutopilotSourceCheck(ctx, a.DB, job, source, state,
						"semantic_prescreen_deferred", jx.Marshal(evidence), at, at.Add(2*time.Hour)); err != nil &&
						!autopilotSourceSuperseded(err) {
						return err
					}
					continue
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if reason == "" && (state == "candidate" || (!policy.YieldEnabled && row.reaudit && (state == "probation" || state == "active"))) {
			autopilotResetAssessmentCause(ctx)
			evaluation := a.autopilotReuseTextReview(ctx, job, sourceInfo, samples, reuse && !verifiedAdmission, evaluate)
			if err := autopilotBudgetError(ctx); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if evaluation == nil {
				if !policy.YieldEnabled || !verifiedAdmission {
					reason = "assessment_unavailable"
				}
				cause := autopilotReadAssessmentCause(ctx)
				if cause == "" {
					if sourcePostsHaveImages(samples) {
						cause = "media_unavailable"
					} else {
						cause = "no_reviewable_content"
					}
				}
				evidence["assessment_cause"] = cause
			} else {
				evidence["preliminary_score"] = evaluation.Score
				evidence["threshold"] = threshold
				evidence["reviewed_samples"] = evaluation.ReviewedSamples
				evidence["review_policy_v3_checked"] = true
				for _, field := range []string{"topic_relevant", "topic_adjacent", "topic_off_topic", "topic_unknown"} {
					if value, ok := evaluation.Quality[field]; ok {
						evidence[field] = value
					}
				}
				if evaluation.Text != nil {
					evidence["text_score"] = *evaluation.Text
				}
				if len(evaluation.Images) > 0 {
					evidence["image_score"] = evaluation.Images[0]
				}
				if jx.Str(sourceInfo, "source_kind") != "category" {
					if format := personalBlogLabelFromReview(evaluation.PersonalBlog); format != personalBlogUnknown {
						evidence["personal_blog"] = format
					}
				}
				pass, reject := autopilotSourcePreliminaryDecision(evaluation, threshold, len(samples))
				// Only explicit per-post off-topic evidence may penalize the
				// search query. A low aggregate score or unavailable media
				// is not a confirmed retrieval error.
				evidence["preliminary_off_topic_confirmed"] = reject &&
					evaluation.Quality["topic_relevant"] != nil && evaluation.Quality["topic_off_topic"] != nil
				if policy.YieldEnabled && (pass || verifiedAdmission) {
					// Admit only a positive prescreen or an independently
					// admitted publication, never mere readability.
				} else if state != "candidate" {
					if reject {
						// Revisit confirmed publishers conservatively: demote
						// active sources to probation before a final rejection.
						next := "rejected"
						retry := 30 * 24 * time.Hour
						if state == "active" {
							next = "probation"
							retry = 6 * time.Hour
						}
						if err := transitionAutopilotSource(ctx, a.DB, job, source, state, next,
							"preliminary_topic_mismatch", jx.Marshal(evidence), at, at.Add(retry)); err != nil &&
							!autopilotSourceSuperseded(err) {
							return err
						}
						continue
					}
					// A borderline new sample is not sufficient to revoke
					// an existing decision. Continue post-level observation.
				} else if pass {
					started, err := a.autopilotBeginReviewTrial(ctx, job, source, "shadow_preliminary_pass", evidence, at)
					if err != nil {
						return err
					}
					if !started {
						continue
					}
					state = "probation"
					nextCheck = at.Add(autopilotSourceReviewInterval(job, state))
				} else {
					// Keep the admission threshold unchanged. Small or borderline
					// samples do not justify a definitive thematic rejection.
					evidence["threshold"] = threshold
					decision, decisionReason, retry := "cooldown", "preliminary_match_uncertain", 7*24*time.Hour
					if reject {
						decision, decisionReason, retry = "rejected", "preliminary_topic_mismatch", 30*24*time.Hour
					}
					if !diagnosticAttempted && job.Scope == autopilotAutoScope && a.LLM != nil {
						probe, attempted, err := a.autopilotProbePreliminaryRejection(ctx, job, source, autopilotFreshRSSSamples(samples, at.Unix()))
						if err != nil {
							return err
						}
						if attempted {
							diagnosticAttempted = true
							evidence["preliminary_post_probe"] = probe
						}
					}
					if (pilotAllowed || policy.YieldEnabled) && jx.Bool(jx.Map(evidence, "preliminary_post_probe"), "has_eligible") {
						// Preserve the old outcome, but allow normal bounded
						// probation. The probe itself never delivers posts.
						evidence["strict_preliminary_outcome"] = decisionReason
						pilot, policy.YieldEnabled = pilotAllowed, true
					} else {
						if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate",
							decision, decisionReason, jx.Marshal(evidence), at,
							at.Add(retry)); err != nil &&
							!autopilotSourceSuperseded(err) {
							return err
						}
						continue
					}
				}
			}
		}
		if reason == "" && policy.YieldEnabled && state == "candidate" {
			evidence["review_policy_v3_checked"] = true
			evidence["yield_trial_started_at"] = at.Unix()
			evidence["source_decision_policy"] = "yield_v2"
			if pilot {
				evidence["yield_pilot"] = true
				evidence["source_decision_policy"] = "yield_pilot"
			}
			started, err := a.autopilotBeginReviewTrial(ctx, job, source, "yield_verified_trial", evidence, at)
			if err != nil {
				return err
			}
			if !started {
				continue
			}
			state = "probation"
			nextCheck = at.Add(autopilotSourceReviewInterval(job, state))
		}
		if reason == "" && (state == "probation" || state == "active") {
			// Source quality must be measured on its feed, not on posts
			// preselected for relevance by an independent search. Staged
			// search results have their own assessment and delivery path.
			// Only live-window posts are candidates for delivery. Historical
			// posts can qualify a publisher but must never be pushed to users.
			reviewPosts := autopilotFreshRSSSamples(samples, at.Unix())
			traceCtx, trace := newAutopilotPostAudit(ctx, reviewPosts)
			assessErr := assess(traceCtx, source, reviewPosts)
			autopilotAttachPostAudit(evidence, trace, time.Now())
			if err := assessErr; err != nil {
				evidence["assessment_cause"] = autopilotAssessmentCause(err)
				if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
					return budgetErr
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if reason := autopilotStreamUnavailableReason(err); newTrial && reason != "" && !autopilotPostAuditHasUseful(trace) {
					if transitionErr := transitionAutopilotSource(ctx, a.DB, job, source, state, "cooldown", reason, jx.Marshal(evidence), at, at.Add(24*time.Hour)); transitionErr != nil && !autopilotSourceSuperseded(transitionErr) {
						return transitionErr
					}
					continue
				}
				reason = "assessment_unavailable"
			} else {
				decisionAt := time.Now()
				stats, err := autopilotEvidenceWindow(ctx, a.DB.Pool, job.Scope, job.TopicKey, source, decisionAt.Add(-30*24*time.Hour), decisionAt.Add(time.Second))
				if err != nil {
					return err
				}
				professional := a.User(ctx, "", nil).professionalValueTopic(job.Topic)
				effectiveRelevant := effectiveSourceRelevant(stats.Relevant, stats.Eligible, professional)
				statsEvidence := jx.M{"window_days": 30, "evaluated": stats.Evaluated, "relevant": stats.Relevant, "eligible": stats.Eligible, "effective_relevant": effectiveRelevant, "advertising": stats.Advertising, "duplicates": stats.Duplicates, "same_source_duplicates": stats.SameSourceDuplicates, "fresh_unique": stats.FreshUnique, "curated_unique": stats.CuratedUnique, "successful_passes": stats.SuccessfulPasses, "review_samples": auditSamples}
				for key, value := range statsEvidence {
					evidence[key] = value
				}
				strictEvidence := sourceEvidence{Evaluated: stats.Evaluated, Relevant: stats.Relevant, Eligible: stats.Eligible, RequireEligible: professional, FreshUnique: stats.FreshUnique, CuratedUnique: stats.CuratedUnique, SuccessfulPasses: stats.SuccessfulPasses, Advertising: stats.Advertising, Duplicates: stats.Duplicates, SameSourceDuplicates: stats.SameSourceDuplicates}
				next, why := decideSourceState(state, strictEvidence, policy)
				if policy.YieldMinLive > 0 || policy.RequireLiveTrial {
					yieldEvidence, err := autopilotYieldEvidenceWindow(ctx, a.DB.Pool, job.Scope, job.TopicKey, source, decisionAt.Add(-30*24*time.Hour), decisionAt.Add(time.Second))
					if err != nil {
						return err
					}
					yieldEvidence.Evidence = strictEvidence
					yieldEvidence.CompleteSample = autopilotPostAuditComplete(trace)
					yieldNext, yieldWhy := decideSourceYieldState(state, yieldEvidence, policy)
					evidence["yield_comparison"] = jx.M{"strict_next": next, "strict_reason": why, "yield_next": yieldNext, "yield_reason": yieldWhy, "useful_unique": yieldEvidence.UsefulUnique, "live_unique": yieldEvidence.LiveUnique, "trial_passes": yieldEvidence.TrialPasses, "trial_started_at": yieldEvidence.TrialStartedAt, "complete_sample": yieldEvidence.CompleteSample, "at": decisionAt.Unix()}
					evidence["source_decision_policy"] = "strict_v1"
					if policy.YieldEnabled {
						next, why = yieldNext, yieldWhy
						evidence["source_decision_policy"] = "yield_v2"
						if pilot {
							evidence["yield_pilot"] = true
							evidence["source_decision_policy"] = "yield_pilot"
						}
						if yieldEvidence.CompleteSample {
							nextCheck = at.Add(autopilotYieldPollInterval(reviewPosts, autopilotSourceReviewInterval(job, state)))
						}
					}
					next, why = autopilotGuardLiveTrial(state, next, why, row.legacyActive, yieldEvidence, policy)
					if policy.RequireLiveTrial && !policy.YieldEnabled {
						evidence["source_decision_policy"] = "strict_v1_live_guard"
					}
				}
				if next != state {
					nextCheck := at.Add(autopilotSourceReviewInterval(job, next))
					if next == "rejected" {
						nextCheck = at.Add(30 * 24 * time.Hour)
					}
					if err := transitionAutopilotSource(ctx, a.DB, job, source, state, next, why, jx.Marshal(evidence), at, nextCheck); err != nil && !autopilotSourceSuperseded(err) {
						return err
					}
					continue
				}
				reason = why
			}
		}
		// Retry inconclusive model assessments sooner than the normal source
		// cadence. Keep the source in its existing state and let daily LLM
		// budgets bound retries rather than silently losing candidates.
		if reason == "assessment_unavailable" && job.Scope == autopilotAutoScope {
			// No readable media is a fetch/input issue, not an AI quota issue.
			// Do not hammer the same feed every hour without new evidence.
			if jx.Str(evidence, "assessment_cause") == "media_unavailable" || jx.Str(evidence, "assessment_cause") == "no_reviewable_content" {
				nextCheck = at.Add(6 * time.Hour)
			} else {
				nextCheck = at.Add(time.Hour)
			}
		}
		if err := recordAutopilotSourceCheck(ctx, a.DB, job, source, state, reason, jx.Marshal(evidence), at, nextCheck); err != nil && !errors.Is(err, errAutopilotSourceChanged) {
			return err
		}
	}
	return nil
}
