package posta

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
)

const autopilotShadowKind = "shadow_discovery"
const autopilotShadowScope = "public:shadow"
const autopilotAutoKind = "auto_discovery"
const autopilotAutoScope = "public"

func validAutopilotPublicJob(job *autopilotJob) bool {
	return job != nil && ((job.Scope == autopilotShadowScope && job.Kind == autopilotShadowKind) || (job.Scope == autopilotAutoScope && job.Kind == autopilotAutoKind)) && autopilotTopicKey(job.Topic) == job.TopicKey
}

// SourceIDFor lowercases whole URLs. A feed path can be case-sensitive, so
// the autopilot normalizes only scheme/host and preserves the escaped path.
func autopilotPublicRSSID(raw string) (string, bool) {
	if feed, ok := canonicalYouTubeFeed(raw); ok {
		return "rss:" + feed, true
	}
	if !sharedSourceEligible(PlatformRSS, jx.M{"id": raw}) && !autopilotPublicRedditFeed(raw) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	return "rss:" + u.String(), true
}

// reconcileAutopilotShadow creates one public job per active topic profile.
// Demand eligibility is independent of an explicit persistent pause.
func (a *App) reconcileAutopilotShadow(ctx context.Context) error {
	return a.reconcileAutopilotShadowEnabled(ctx, a.TBool("autopilot_shadow"))
}

func (a *App) reconcileAutopilotShadowEnabled(ctx context.Context, enabled bool) error {
	return a.reconcileAutopilotMode(ctx, enabled, "shadow", autopilotShadowScope, autopilotShadowKind)
}

func (a *App) reconcileAutopilotMode(ctx context.Context, enabled bool, mode, scope, kind string) error {
	return a.DB.Tx(ctx, func(tx pgx.Tx) error {
		if enabled {
			_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_jobs(scope,topic_key,kind,topic,reason,next_run_at,requested_at)
				SELECT $2,topic_key,$1,min(topic),$3,floor(extract(epoch FROM clock_timestamp()))::bigint,floor(extract(epoch FROM clock_timestamp()))::bigint
				FROM source_autopilot_demands WHERE mode=$3 AND NOT paused AND ($3<>'auto' OR posts_per_day>0) GROUP BY topic_key
				ON CONFLICT(scope,topic_key,kind) DO UPDATE SET demand_active=true`, kind, scope, mode)
			if err != nil {
				return err
			}
		}
		// Repair obsolete long delays left by transient classifier/search
		// failures. The current worker retries such failures in minutes, not
		// days. Only jobs deferred beyond an hour are rescued; a fresh 15-minute
		// failure backoff, an operator pause, and real quota exhaustion remain
		// untouched. Safe and idempotent across every scheduler tick.
		if enabled {
			if _, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs j
				SET next_run_at=LEAST(j.next_run_at,$3)
				WHERE j.scope=$1 AND j.kind=$2 AND j.enabled AND j.demand_active
				AND j.lease_until <= extract(epoch FROM clock_timestamp())::bigint
				AND j.next_run_at > $3+3600
				AND j.last_error IN ('classifier_unavailable','search_unavailable','supply_unavailable')`,
				scope, kind, time.Now().Add(time.Minute).Unix()); err != nil {
				return err
			}
		}
		// One-time recovery from the shadow/auto projection mix-up:
		// remove only this known deterministic failure, not arbitrary errors
		// or quota backoffs. Next scheduler tick can review the queue.
		if enabled && scope == autopilotShadowScope {
			if _, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs
				SET next_run_at=LEAST(next_run_at,$2),last_error=''
				WHERE scope=$1 AND kind=$3 AND enabled AND demand_active
				AND last_error='invalid projection job'`,
				scope, time.Now().Unix(), kind); err != nil {
				return err
			}
		}
		// One-time migration of an old stuck queue: the previous source
		// sampler only considered 30-day posts. The worker will convert those
		// stale candidate reasons to probation/cooldown after reevaluation.
		// Do not override a failed job's backoff or a disabled mode.
		if enabled {
			if _, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs j
				SET next_run_at=LEAST(j.next_run_at,$3)
				WHERE j.scope=$1 AND j.kind=$2 AND j.enabled AND j.demand_active
				AND j.last_error='' AND j.next_run_at>$3
				AND EXISTS (SELECT 1 FROM source_autopilot_sources s
				WHERE s.scope=j.scope AND s.topic_key=j.topic_key
				AND s.state='candidate'
				AND s.reason IN ('feed_unavailable','insufficient_fresh_samples')
				AND s.checked_at > 0 AND s.checked_at < $3-3600)`,
				scope, kind, time.Now().Unix()); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs j SET demand_active=false,lease_token='',lease_until=0,generation=generation+1
			WHERE j.scope=$3 AND j.kind=$1 AND j.demand_active AND
			(NOT $2 OR NOT EXISTS (SELECT 1 FROM source_autopilot_demands d WHERE d.topic_key=j.topic_key AND d.mode=$4 AND NOT d.paused AND ($4<>'auto' OR d.posts_per_day>0)))`, kind, enabled, scope, mode)
		return err
	})
}

func (a *App) runAutopilotShadowWorker(ctx context.Context) {
	a.runAutopilotWorker(ctx, "shadow", autopilotShadowScope, autopilotShadowKind, "autopilot_shadow")
}

func (a *App) runAutopilotAutoWorker(ctx context.Context) {
	a.runAutopilotWorker(ctx, "auto", autopilotAutoScope, autopilotAutoKind, "autopilot_auto")
}

func (a *App) runAutopilotWorker(ctx context.Context, mode, scope, kind, setting string) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var done <-chan struct{}
	defer func() {
		if done != nil {
			<-done
		}
	}()
	for {
		enabled := a.TBool(setting)
		err := a.reconcileAutopilotMode(ctx, enabled, mode, scope, kind)
		if err != nil && ctx.Err() == nil {
			log.Printf("автопилот: планирование: %v", err)
		}
		if err == nil && enabled && ctx.Err() == nil {
			if transferred, importErr := a.reconcileLegacyPendingWithoutLease(ctx, mode, scope, kind, 100); importErr != nil {
				if ctx.Err() == nil {
					log.Printf("автопилот: перенос ручных источников: %v", importErr)
				}
			} else if transferred > 0 {
				log.Printf("автопилот: перенесено из ручной очереди в %s: %d источников", mode, transferred)
			}
		}
		if done != nil {
			select {
			case <-done:
				done = nil
			default:
			}
		}
		if err == nil && enabled && ctx.Err() == nil && done == nil {
			finished := make(chan struct{})
			done = finished
			go func() {
				defer close(finished)
				defer func() {
					if recovered := recover(); recovered != nil {
						log.Printf("автопилот: ошибка исполнителя: %v", recovered)
					}
				}()
				a.runNextAutopilotKind(ctx, kind)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) runNextAutopilotShadow(ctx context.Context) {
	a.runNextAutopilotKind(ctx, autopilotShadowKind)
}

func (a *App) runNextAutopilotKind(ctx context.Context, kind string) {
	job, err := claimAutopilotJobKind(ctx, a.DB.Pool, time.Now(), 10*time.Minute, kind)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("теневой автопилот: очередь: %v", err)
		}
		return
	}
	if job == nil {
		return
	}
	if kind == autopilotAutoKind {
		release, owned, ownershipErr := a.trySourceTopicOwnership(ctx, job.Topic)
		if ownershipErr != nil || !owned {
			if err := finishAutopilotJob(ctx, a.DB, job, time.Now().Add(5*time.Minute), job.Checkpoint, "topic_busy"); err != nil && !errors.Is(err, errAutopilotLeaseLost) && ctx.Err() == nil {
				log.Printf("автопилот: завершение: %v", err)
			}
			return
		}
		defer release()
	}
	// Never let an old admin override (often 3) block the public
	// discovery queue. Network and model budgets, not trial count, throttle
	// expensive work; the database still guards concurrent transitions.
	job.MaxProbation = max(1000, a.TInt("autopilot_max_probation"))
	nextInterval := 24 * time.Hour
	if kind == autopilotAutoKind {
		interval, demandErr := a.autopilotAutoInterval(ctx, job)
		if demandErr != nil {
			if finishErr := finishAutopilotJob(ctx, a.DB, job, time.Now().Add(time.Hour), job.Checkpoint, "supply_unavailable"); finishErr != nil && !errors.Is(finishErr, errAutopilotLeaseLost) {
				log.Printf("автопилот: завершение: %v", finishErr)
			}
			return
		}
		nextInterval = interval
	}
	ctx = withAutopilotPaidLimit(ctx, a.TFloat("autopilot_paid_daily_usd"))
	err = runAutopilotLeased(ctx, a.DB, job, 10*time.Minute, 2*time.Minute,
		int64(a.TInt("autopilot_model_server_daily")), int64(a.TInt("autopilot_model_topic_daily")),
		int64(a.TInt("autopilot_fetch_server_daily")), int64(a.TInt("autopilot_fetch_topic_daily")),
		func(workCtx context.Context) error { return a.autopilotShadowSearch(workCtx, job) })
	if ctx.Err() != nil || errors.Is(err, errAutopilotLeaseLost) {
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	if err != nil {
		// Most failures are transient. Retrying only after 24h made a topic
		// appear frozen even when there was ample daily budget left.
		// A real daily quota exhaustion waits for the next UTC budget reset.
		if errors.Is(err, errAutopilotBudgetExhausted) {
			nextInterval = time.Until(time.Now().UTC().Truncate(24 * time.Hour).Add(24*time.Hour + 2*time.Minute))
		} else {
			// Network/model outages should be retried regularly, with budgets
			// still checked before every paid or public request. A one-hour
			// delay obscured recoveries after the external provider came back.
			nextInterval = 15 * time.Minute
		}
	}
	// The earliest scheduled source check controls the next run, even when
	// the overall topic is healthy. This includes active sources: without it,
	// their requested 3-hour checks silently waited for the 24-hour search
	// cadence. A due backlog gets a 2-minute retry; future 429/cooldown and
	// rejection dates are never pulled forward.
	if err == nil {
		var earliest int64
		checkErr := a.DB.Pool.QueryRow(ctx, `SELECT COALESCE(MIN(
			CASE WHEN next_check_at <= $3 THEN $3+120 ELSE next_check_at END),0)
			FROM source_autopilot_sources s
			WHERE s.scope=$1 AND s.topic_key=$2
			AND s.state IN ('candidate','probation','active','cooldown','rejected')
			AND NOT (s.state='candidate' AND s.reason='probation_full')`,
			job.Scope, job.TopicKey, time.Now().Unix()).Scan(&earliest)
		if checkErr == nil && earliest > 0 {
			nextInterval = min(nextInterval, max(2*time.Minute, time.Until(time.Unix(earliest, 0))))
		} else if checkErr != nil && ctx.Err() == nil {
			log.Printf("автопилот: срок проверки источников: %v", checkErr)
		}
	}
	if finishErr := finishAutopilotJob(ctx, a.DB, job, time.Now().Add(nextInterval), job.Checkpoint, reason); finishErr != nil && !errors.Is(finishErr, errAutopilotLeaseLost) && ctx.Err() == nil {
		log.Printf("теневой автопилот: завершение: %v", finishErr)
	}
}

// Persist a coarse active phase so the UI can show which part of the
// pipeline is currently running. A phase is historical if the lease expires.
func (a *App) autopilotMarkPhase(ctx context.Context, job *autopilotJob, phase string) error {
	checkpoint := jx.Unmarshal(job.Checkpoint)
	if checkpoint == nil {
		checkpoint = jx.M{}
	}
	checkpoint["pipeline_phase"] = phase
	checkpoint["pipeline_phase_at"] = time.Now().Unix()
	job.Checkpoint = jx.Marshal(checkpoint)
	return saveAutopilotCheckpoint(ctx, a.DB, job)
}

func (a *App) autopilotShadowSearch(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) {
		return errors.New("invalid public topic job")
	}
	if a.TopicIsRestricted(job.Topic) {
		return errors.New("restricted_profile")
	}
	if err := a.autopilotMarkPhase(ctx, job, "prepare"); err != nil {
		return err
	}
	// Bring the old "waiting for approval" suggestions into this same
	// auditable review queue before searching for yet more sources.
	// Never activate topics or the main post parser on the user's behalf.
	if _, err := a.autopilotImportLegacyPending(ctx, job, 60); err != nil {
		return err
	}
	if job.Scope == autopilotAutoScope {
		// Source suggestions originating from fully accepted posts enter the
		// same candidate verification queue as backend discoveries.
		if err := a.autopilotConsumeSourceHints(ctx, job, 20); err != nil {
			return err
		}
		if err := a.reconcileAutopilotProjections(ctx, job); err != nil {
			return err
		}
		if err := a.deliverAutopilotTopicContent(ctx, job); err != nil {
			return err
		}
	}
	if err := a.autopilotMarkPhase(ctx, job, "review"); err != nil {
		return err
	}
	// Existing trials get first access to the budget; broad search must not
	// indefinitely starve their evaluation or projection after a restart.
	if err := a.autopilotReviewShadowCandidates(ctx, job, 4); err != nil {
		return err
	}
	if job.Scope == autopilotAutoScope {
		if err := a.reconcileAutopilotProjections(ctx, job); err != nil {
			return err
		}
		if err := a.deliverAutopilotTopicContent(ctx, job); err != nil {
			return err
		}
	}
	// Do not keep searching while the existing candidate queue needs
	// verification. This applies to shadow and auto modes alike: previously
	// shadow mode could accumulate hundreds of unreviewed sources.
	{
		var dueCandidates int
		if err := a.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources
			WHERE scope=$1 AND topic_key=$2 AND state='candidate'
			AND (next_check_at <= $3 OR reason='probation_full' OR
			     (reason IN ('feed_unavailable','insufficient_fresh_samples') AND checked_at < $3-3600))`,
			job.Scope, job.TopicKey, time.Now().Unix()).Scan(&dueCandidates); err != nil {
			return err
		}
		if autopilotShouldDrainCandidateBacklog(dueCandidates) {
			if err := a.autopilotMarkPhase(ctx, job, "review"); err != nil {
				return err
			}
			if err := a.autopilotReviewShadowCandidates(ctx, job, 30); err != nil {
				return err
			}
			return a.autopilotFinishBacklog(ctx, job)
		}
	}
	// An unavailable assessment is inconclusive for that source, not proof that
	// discovery as a whole is unavailable. Continue to public search so a single
	// failed model call cannot starve all new candidates. The model and fetch
	// budgets still bound subsequent work in this cycle.
	if err := a.autopilotMarkPhase(ctx, job, "search"); err != nil {
		return err
	}
	query, err := a.autopilotChooseQuery(ctx, job)
	if err != nil {
		return err
	}
	job.SearchQuery = query
	if err := saveAutopilotCheckpoint(ctx, a.DB, job); err != nil {
		return err
	}
	var searchErr error
	var sourceSearchCount, directSourceCount, enabledBackends int
	var lastSearchSourceIDs []string
	var statuses map[string]string
	directAttempted := false
	searchOutcome := "not_started"
	err = a.autopilotShadowSearchWith(ctx, job, func(ctx context.Context) ([]DiscoveryCandidate, error) {
		// Explore multiple platforms until a useful pool is found. Keep a
		// generous per-cycle safety ceiling so a broken backend cannot loop
		// indefinitely, while reserving the daily budget for source reviews.
		searchCtx := withAutopilotSearchFetchCap(ctx, 120)
		u := a.User(searchCtx, "", nil)
		statuses = map[string]string{}
		u.discoverySearchObservation = func(backend, _ string, result string) {
			recordAutopilotBackendStatus(statuses, backend, result)
		}
		var backends []func() []DiscoveryCandidate
		if a.TBool("search_telegram") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindPublicSocial(query, PlatformTelegram) })
		}
		if a.TBool("search_vk") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindPublicSocial(query, PlatformVK) })
		}
		if a.TBool("search_youtube") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindYouTube(query) })
		}
		if a.TBool("autopilot_search_tiktok") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindPublicSocial(query, PlatformTikTok) })
		}
		if a.TBool("autopilot_search_ok") {
			backends = append(backends, func() []DiscoveryCandidate { return u.discoveryFindOK([]string{query}, job.Topic) })
		}
		if a.TBool("search_bluesky") {
			backends = append(backends, func() []DiscoveryCandidate { return u.discoveryFindBluesky([]string{query}) })
		}
		if a.TBool("search_lemmy") {
			backends = append(backends, func() []DiscoveryCandidate { return u.discoveryFindLemmy([]string{query}, job.Topic) })
		}
		if a.TBool("search_mastodon") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindMastodon(query) })
		}
		if a.TBool("search_reddit") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindReddit(query) })
		}
		if a.TBool("autopilot_search_habr") {
			turn := int(jx.Int(jx.Unmarshal(job.Checkpoint), "query_turn") % 11)
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindHabr(query, turn) })
		}
		if a.TBool("autopilot_search_tumblr") {
			backends = append(backends, func() []DiscoveryCandidate { return u.autopilotFindTumblr(query) })
		}
		if a.TBool("search_news") {
			backends = append(backends, func() []DiscoveryCandidate { return u.discoveryFindNewsFeeds([]string{query}) })
		}
		enabledBackends = len(backends)
		turn := jx.Int(jx.Unmarshal(job.Checkpoint), "query_turn")
		// Broad discovery should not stop after the first three sites
		// return a handful of candidates. Legacy discovery searches across
		// platforms and queries, whereas auto runs only one query per cycle.
		// Inspect up to six different backends before stopping, with the
		// existing per-cycle 120-fetch ceiling and daily quotas still enforced.
		candidates := autopilotRunSearchBackendsAdaptive(searchCtx, turn, backends, 6, 64)
		sourceSearchCount = len(candidates)
		// Snapshot the actual source identities found by this search pass.
		// A candidate may already exist or change state later; querying only
		// today's 'discovered' decisions cannot reconstruct this list.
		seenSources := map[string]bool{}
		lastSearchSourceIDs = []string{}
		for _, candidate := range candidates {
			id, ok := autopilotCandidateID(candidate)
			if !ok || seenSources[id] {
				continue
			}
			seenSources[id] = true
			lastSearchSourceIDs = append(lastSearchSourceIDs, id)
		}
		if job.Scope == autopilotAutoScope && (len(candidates) < 3 || turn%3 == 0) && ctx.Err() == nil && autopilotBudgetError(ctx) == nil && !autopilotSearchFetchCapReached(searchCtx) {
			directAttempted = true
			// Periodic direct post exploration continues even when broad
			// source search reports plenty of unverified candidates.
			postCandidates, postErr := a.autopilotFindSourcesFromPosts(ctx, job, query, 6)
			if postErr != nil {
				return candidates, postErr
			}
			// Preserve source candidates even if the separate text model
			// fails: the search result can still enter normal vetting.
			candidates = append(candidates, postCandidates...)
			directSourceCount = len(postCandidates)
			if err := a.autopilotAssessDirectPosts(ctx, job, postCandidates); err != nil {
				return candidates, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := autopilotBudgetError(ctx); err != nil {
			return candidates, err
		}
		available := false
		for _, status := range statuses {
			if status == "ok" || status == "empty" {
				available = true
			}
		}
		searchOutcome = autopilotDiscoveryOutcome(enabledBackends, sourceSearchCount, directSourceCount, len(statuses) > 0, available, directAttempted)
		switch searchOutcome {
		case "disabled":
			searchErr = errors.New("news_search_disabled")
		case "unavailable":
			searchErr = errors.New("search_unavailable")
		}
		return candidates, nil
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Record an explicit outcome even when the source-review phase later
	// fails. These aggregate fields contain no user or source identifiers.
	autopilotRecordDiscoveryMetrics(job, sourceSearchCount, directSourceCount, enabledBackends, searchOutcome, time.Now().Unix(), statuses)
	metrics := jx.Unmarshal(job.Checkpoint)
	metrics["last_search_source_ids"] = lastSearchSourceIDs
	job.Checkpoint = jx.Marshal(metrics)
	if checkpointErr := saveAutopilotCheckpoint(ctx, a.DB, job); checkpointErr != nil {
		return checkpointErr
	}
	// A completed independent assessment is ready even if source review
	// subsequently slows down or consumes the remaining model budget.
	if job.Scope == autopilotAutoScope {
		if deliveryErr := a.deliverAutopilotDirectTopicContent(ctx, job); deliveryErr != nil {
			return deliveryErr
		}
	}
	if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
		return budgetErr
	}
	if phaseErr := a.autopilotMarkPhase(ctx, job, "review"); phaseErr != nil {
		return phaseErr
	}
	if reviewErr := a.autopilotReviewShadowCandidates(ctx, job, 8); reviewErr != nil {
		return reviewErr
	}
	if phaseErr := a.autopilotMarkPhase(ctx, job, "delivery"); phaseErr != nil {
		return phaseErr
	}
	if job.Scope == autopilotAutoScope {
		if projectionErr := a.reconcileAutopilotProjections(ctx, job); projectionErr != nil {
			return projectionErr
		}
		if deliveryErr := a.deliverAutopilotTopicContent(ctx, job); deliveryErr != nil {
			return deliveryErr
		}
	}
	if err != nil {
		return err
	}
	return searchErr
}

// The shadow backlog is only a review queue: catalog projections and
// delivery exist solely in auto mode. Keeping this boundary in one
// function prevents a shadow run from failing on "invalid projection job".
func (a *App) autopilotFinishBacklog(ctx context.Context, job *autopilotJob) error {
	if err := a.autopilotMarkPhase(ctx, job, "delivery"); err != nil {
		return err
	}
	if job.Scope != autopilotAutoScope {
		return nil
	}
	if err := a.reconcileAutopilotProjections(ctx, job); err != nil {
		return err
	}
	return a.deliverAutopilotTopicContent(ctx, job)
}

// The result distinguishes a broken platform from a healthy but empty
// search, and a fully disabled source search from a working post search.
// Search pauses whenever a due candidate is still waiting for a review.
// Adding up to 64 more sources before clearing the queue was starving
// already-discovered candidates and wasting the search budget.
func autopilotShouldDrainCandidateBacklog(dueCandidates int) bool {
	return dueCandidates > 0
}

func autopilotDiscoveryOutcome(backends, sourceCount, directCount int, observed, available, directAttempted bool) string {
	if sourceCount+directCount > 0 {
		return "results"
	}
	if backends == 0 {
		if directAttempted {
			return "empty"
		}
		return "disabled"
	}
	if !observed {
		return "unknown"
	}
	if !available {
		return "unavailable"
	}
	return "empty"
}

func autopilotRecordDiscoveryMetrics(job *autopilotJob, sourceCount, directCount, backends int, outcome string, at int64, statuses map[string]string) {
	checkpoint := jx.Unmarshal(job.Checkpoint)
	if checkpoint == nil {
		checkpoint = jx.M{}
	}
	checkpoint["last_search_at"] = at
	checkpoint["last_search_outcome"] = outcome
	checkpoint["last_source_candidates"] = max(0, sourceCount)
	checkpoint["last_direct_sources"] = max(0, directCount)
	checkpoint["last_search_backends"] = max(0, backends)
	okCount, emptyCount, unavailableCount := 0, 0, 0
	for _, status := range statuses {
		switch status {
		case "ok":
			okCount++
		case "empty":
			emptyCount++
		case "unavailable":
			unavailableCount++
		}
	}
	checkpoint["last_backends_ok"] = okCount
	checkpoint["last_backends_empty"] = emptyCount
	checkpoint["last_backends_unavailable"] = unavailableCount
	job.Checkpoint = jx.Marshal(checkpoint)
}

// A backend may be queried more than once in a discovery pass. Preserve
// evidence of a successful response instead of replacing "ok" with a later
// empty or unavailable response. Unknown values never hide known failures.
func recordAutopilotBackendStatus(statuses map[string]string, backend, status string) {
	if backend == "" || statuses == nil {
		return
	}
	rank := map[string]int{"unavailable": 1, "empty": 2, "ok": 3}
	if rank[status] == 0 {
		return
	}
	if rank[status] > rank[statuses[backend]] {
		statuses[backend] = status
	}
}

// Rotate first access to the shared budget; otherwise an early backend can
// repeatedly consume every request before later platforms get a chance.
// autopilotRunSearchBackendsAdaptive rotates the starting platform on each
// pass. Once several platforms have contributed enough candidates, stop
// searching and spend the remaining budget on verifying those candidates.
// The limits are per pass, never daily; later passes can explore other sites.
func autopilotRunSearchBackendsAdaptive(ctx context.Context, turn int64, backends []func() []DiscoveryCandidate, minBackends, targetCandidates int) []DiscoveryCandidate {
	if len(backends) == 0 {
		return nil
	}
	if turn < 0 {
		turn = 0
	}
	start := int(turn % int64(len(backends)))
	var out []DiscoveryCandidate
	seen := make(map[string]bool)
	for i := 0; i < len(backends); i++ {
		if ctx.Err() != nil || autopilotBudgetError(ctx) != nil || autopilotSearchFetchCapReached(ctx) {
			break
		}
		for _, candidate := range backends[(start+i)%len(backends)]() {
			out = append(out, candidate)
			if id, ok := autopilotCandidateID(candidate); ok {
				seen[id] = true
			}
		}
		if i+1 >= minBackends && len(seen) >= targetCandidates {
			break
		}
	}
	return out
}

func autopilotRunSearchBackends(ctx context.Context, turn int64, backends []func() []DiscoveryCandidate) []DiscoveryCandidate {
	if len(backends) == 0 {
		return nil
	}
	if turn < 0 {
		turn = 0
	}
	start := int(turn % int64(len(backends)))
	var out []DiscoveryCandidate
	for i := 0; i < len(backends); i++ {
		if ctx.Err() != nil || autopilotBudgetError(ctx) != nil || autopilotSearchFetchCapReached(ctx) {
			break
		}
		out = append(out, backends[(start+i)%len(backends)]()...)
	}
	return out
}

// Find additional source candidates through recent public posts when a topic
// has weak source-search coverage. This only nominates authors: individual
// posts cannot be delivered until the source has passed probation and each
// post has been assessed by the normal public pipeline.
func autopilotCandidatesFromPosts(by map[string][]jx.M, at int64, limit int) []DiscoveryCandidate {
	if limit <= 0 {
		return nil
	}
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	platforms := sortedKeys(by)
	maxRows := 0
	for _, platform := range platforms {
		maxRows = max(maxRows, len(by[platform]))
	}
	// Round-robin prevents the first platform alphabetically from consuming
	// every nomination slot. Source IDs are still deduplicated globally.
	for row := 0; row < maxRows; row++ {
		for _, platform := range platforms {
			if row >= len(by[platform]) {
				continue
			}
			post := by[platform][row]
			published := jx.Int(post, "posted_at")
			if !autopilotPublicPostURL(jx.Str(post, "url"), false) ||
				published <= 0 || published > at+300 || IsPostOutdated(published, at, config.ContentMaxAgeDays) ||
				jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") || len(jx.ToStrs(post["safety"])) > 0 ||
				jx.Bool(post, "advertising") || PostIsAdvertisement(post) || PostSearchIsAd(post) {
				continue
			}
			candidate := PostSearchAuthor(post)
			if candidate == nil {
				continue
			}
			id, ok := autopilotCandidateID(*candidate)
			if !ok || seen[id] || !autopilotPostMatchesSource(id, post) {
				continue
			}
			seen[id] = true
			candidate.Origin = "public_post:" + jx.Str(post, "url")
			out = append(out, *candidate)
			if len(out) == limit {
				return out
			}
		}
	}
	return out
}

// Public post backends use different term types: Mastodon searches tags,
// Lemmy searches English phrases, while VK and vc/dtf use Russian phrases.
// Without these, a bare RU term silently disables Mastodon altogether.
func autopilotPublicPostQueryPlan(query string) QueryPlan {
	query = strings.TrimSpace(query)
	plan := QueryPlan{RU: []string{query}}
	if query == "" {
		return plan
	}
	plan.Hashtags = hashtagsFrom([]string{query})
	if !cyrRe.MatchString(query) {
		plan.EN = []string{query}
	}
	return plan
}

func (a *App) autopilotFindSourcesFromPosts(ctx context.Context, job *autopilotJob, query string, limit int) ([]DiscoveryCandidate, error) {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind || strings.TrimSpace(query) == "" || limit <= 0 || ctx.Err() != nil || autopilotBudgetError(ctx) != nil {
		return nil, autopilotBudgetError(ctx)
	}
	u := a.User(ctx, "", nil)
	plan := autopilotPublicPostQueryPlan(query)
	// The public worker has no personal Telegram login. Use only backends
	// that can search without a subscriber's private account.
	only := map[string]bool{"VK": true, "vc/dtf": true, "Lemmy": true, "Mastodon": true}
	posts, _ := u.searchEngineRun(plan, 0, 25*time.Second, only)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := autopilotBudgetError(ctx); err != nil {
		return nil, err
	}
	candidates := autopilotCandidatesFromPosts(posts, time.Now().Unix(), limit)
	// Store only small, public text candidates tied to the verified author
	// identity. Media never enters this staging queue.
	for _, candidate := range candidates {
		source, ok := autopilotCandidateID(candidate)
		if !ok {
			continue
		}
		var sourcePosts []jx.M
		for _, platform := range sortedKeys(posts) {
			sourcePosts = append(sourcePosts, posts[platform]...)
		}
		if err := a.autopilotStagePublicPosts(ctx, job, source, sourcePosts); err != nil {
			return candidates, err
		}
	}
	return candidates, nil
}

func (a *App) autopilotShadowSearchWith(ctx context.Context, job *autopilotJob, find func(context.Context) ([]DiscoveryCandidate, error)) error {
	if !validAutopilotPublicJob(job) || find == nil {
		return errors.New("invalid public topic job")
	}
	candidates, findErr := find(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, candidate := range candidates {
		sourceID, ok := autopilotCandidateID(candidate)
		if !ok {
			continue
		}
		if err := registerAutopilotCandidate(ctx, a.DB, job, sourceID, time.Now()); err != nil {
			return err
		}
		if err := recordAutopilotQuerySource(ctx, a, job, sourceID); err != nil {
			return err
		}
	}
	return findErr
}

// Source quality is evaluated across a longer publication history than the
// 30-day limit used to decide which individual posts belong in the live pool.
// Infrequent authors must not be mistaken for irrelevant or nonexistent feeds.
const autopilotSourceQualityDays = 365

func autopilotSourceQualitySamples(posts []jx.M, at int64) []jx.M {
	return autopilotSourceSamples(posts, at, autopilotSourceQualityDays)
}

func autopilotFreshRSSSamples(posts []jx.M, at int64) []jx.M {
	return autopilotSourceSamples(posts, at, config.ContentMaxAgeDays)
}

func autopilotSourceSamples(posts []jx.M, at int64, days int) []jx.M {
	seen := map[string]bool{}
	var out []jx.M
	for _, post := range posts {
		if jx.Str(post, "url") == "" || jx.Int(post, "posted_at") <= 0 || jx.Int(post, "posted_at") > at+300 || IsPostOutdated(jx.Int(post, "posted_at"), at, days) {
			continue
		}
		key := CandidatePostKey(post)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, post)
	}
	sort.SliceStable(out, func(i, j int) bool { return jx.Int(out[i], "posted_at") > jx.Int(out[j], "posted_at") })
	spread := make([]jx.M, 0, len(out))
	for left, right := 0, len(out)-1; left <= right; left, right = left+1, right-1 {
		spread = append(spread, out[left])
		if left != right {
			spread = append(spread, out[right])
		}
	}
	return spread
}

func autopilotRetryAt(at time.Time, source, retryAfter string) time.Time {
	delay := 24 * time.Hour
	if seconds, err := strconv.ParseInt(strings.TrimSpace(retryAfter), 10, 64); err == nil && seconds > 0 {
		if seconds > int64((365 * 24 * time.Hour).Seconds()) {
			seconds = int64((365 * 24 * time.Hour).Seconds())
		}
		delay = max(delay, time.Duration(seconds)*time.Second)
	} else if date, err := http.ParseTime(retryAfter); err == nil && date.After(at) {
		delay = max(delay, min(date.Sub(at), 365*24*time.Hour))
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(source))
	return at.Add(delay + time.Duration(h.Sum32()%3600)*time.Second)
}

// Review only public feed data. An unavailable feed, model error or small
// sample remains uncertain; none of them proves poor source quality.
func (a *App) autopilotReviewShadowCandidates(ctx context.Context, job *autopilotJob, limit int) error {
	u := a.User(ctx, "", nil)
	return a.autopilotReviewShadowCandidatesWith(ctx, job, limit, a.TInt("autopilot_preliminary_score"),
		func(feed string) FetchResultURL {
			if platform, id, _, ok := autopilotSourceParts(feed); ok {
				switch platform {
				case PlatformTelegram:
					return u.RSSGet("https://t.me/s/"+id, rssMaxBytes, 15*time.Second, BrowserUA)
				case PlatformVK:
					return u.RSSGet("https://vk.com/"+id, rssMaxBytes, 15*time.Second, BrowserUA)
				case PlatformOK:
					return u.RSSGet(okGroupURL(id), rssMaxBytes, 15*time.Second, BrowserUA)
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
			return u.RSSGet(feed, rssMaxBytes, 15*time.Second, HTTPUA)
		}, u.evaluateSource,
		func(ctx context.Context, source string, posts []jx.M) error {
			return a.assessAutopilotPublicPosts(ctx, job, source, posts)
		}, a.autopilotSourcePolicy())
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

func autopilotStreamUnavailableReason(err error) string {
	if errors.Is(err, errAutopilotTikTokStreamUnavailable) {
		return "tiktok_stream_unavailable"
	}
	if errors.Is(err, errAutopilotYouTubeStreamUnavailable) {
		return "youtube_stream_unavailable"
	}
	return ""
}

func (a *App) autopilotReviewShadowCandidatesWith(ctx context.Context, job *autopilotJob, limit, threshold int, fetch func(string) FetchResultURL, evaluate func(jx.M, []jx.M, string) *sourceEvaluation, assess func(context.Context, string, []jx.M) error, policy sourcePolicy) error {
	if !validAutopilotPublicJob(job) || limit < 1 {
		return errors.New("invalid shadow review")
	}
	if fetch == nil || evaluate == nil || assess == nil || threshold < 0 || threshold > 100 {
		return errors.New("invalid shadow review policy")
	}
	// Give probation, new candidates and established feeds separate lanes.
	// Otherwise a large backlog of never-checked candidates (checked_at=0)
	// always wins over probation sources, preventing them from collecting the
	// evidence required to become active. Interleave by rank to reserve review
	// capacity for each lane even when another lane has thousands of entries.
	rows, err := a.DB.Pool.Query(ctx, `WITH due AS (
		SELECT s.source_id,s.state,s.resume_state,s.checked_at,s.first_seen_at,
			CASE s.state WHEN 'probation' THEN 0 WHEN 'candidate' THEN 1 WHEN 'active' THEN 2 ELSE 3 END AS lane
		FROM source_autopilot_sources s
		WHERE s.scope=$1 AND s.topic_key=$2 AND s.state IN ('candidate','probation','active','rejected','cooldown')
		AND (s.next_check_at <= $3 OR (s.state='candidate' AND
			(s.reason='probation_full' OR
			 (s.reason IN ('feed_unavailable','insufficient_fresh_samples') AND s.checked_at < $3-3600))))
		AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE (o.scope=$1 OR ($1='public:shadow' AND o.scope='public')) AND o.owner_id='' AND o.topic_key=$2 AND o.source_id=s.source_id AND o.decision='excluded')
	), ranked AS (
		SELECT source_id,state,resume_state,lane,
			row_number() OVER (PARTITION BY lane ORDER BY checked_at,first_seen_at,source_id) AS lane_rank
		FROM due
	)
	SELECT source_id,state,resume_state FROM ranked
	ORDER BY lane_rank,lane LIMIT $4`, job.Scope, job.TopicKey, time.Now().Unix(), limit)
	if err != nil {
		return err
	}
	type sourceRow struct{ id, state, resume string }
	var ids []sourceRow
	for rows.Next() {
		var row sourceRow
		if err := rows.Scan(&row.id, &row.state, &row.resume); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range ids {
		source, state := row.id, row.state
		newTrial := state == "candidate"
		if err := ctx.Err(); err != nil {
			return err
		}
		if state == "cooldown" {
			at := time.Now()
			if job.Scope == autopilotAutoScope && a.autopilotProjectionPossible(job, source) != nil {
				if err := recordAutopilotSourceCheck(ctx, a.DB, job, source, state, "projection_conflict", []byte(`{}`), at, at.Add(7*24*time.Hour)); err != nil && !errors.Is(err, errAutopilotSourceChanged) {
					return err
				}
				continue
			}
			if err := transitionAutopilotSource(ctx, a.DB, job, source, "cooldown", row.resume, "cooldown_elapsed", []byte(`{}`), at, at); err != nil && !errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) && !errors.Is(err, errAutopilotProbationLimit) {
				return err
			}
			continue
		}
		if state == "rejected" {
			at := time.Now()
			if err := transitionAutopilotSource(ctx, a.DB, job, source, "rejected", "candidate", "periodic_reconsideration", []byte(`{}`), at, at); err != nil && !errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
			continue
		}
		if state == "candidate" {
			if job.Scope == autopilotAutoScope {
				if err := a.autopilotProjectionPossible(job, source); err != nil {
					at := time.Now()
					if saveErr := recordAutopilotSourceCheck(ctx, a.DB, job, source, state, "projection_conflict", []byte(`{}`), at, at.Add(7*24*time.Hour)); saveErr != nil && !errors.Is(saveErr, errAutopilotSourceChanged) {
						return saveErr
					}
					continue
				}
			}

		}
		platform, id, _, ok := autopilotSourceParts(source)
		if !ok {
			continue
		}
		feed := id
		if platform != PlatformRSS {
			feed = source
		}
		at := time.Now()
		res := fetch(feed)
		if err := autopilotBudgetError(ctx); err != nil {
			return err
		}
		reason := ""
		nextCheck := at.Add(autopilotSourceReviewInterval(job, state))
		var posts []jx.M
		if !res.OK {
			reason = "feed_unavailable"
			if res.Status == http.StatusTooManyRequests {
				reason = "rate_limited"
			}
			if res.Status == http.StatusNotFound || res.Status == http.StatusGone {
				reason = "source_not_found"
			}
			nextCheck = autopilotRetryAt(at, source, res.RetryAfter)
		} else {
			if platform == PlatformTikTok {
				err = json.Unmarshal(res.Bytes, &posts)
			} else if platform == PlatformTelegram || platform == PlatformVK || platform == PlatformOK {
				posts, err = autopilotPublicSocialPosts(platform, id, res)
			} else if autopilotPublicRedditFeed(feed) {
				posts, err = autopilotRedditListing(res.Bytes, feed, res.URL)
			} else if _, ok := autopilotMastodonFeed(feed); ok {
				posts, err = autopilotMastodonSafeStatuses(res.Bytes, feed)
			} else if !rssIsFeed(rssLoad(res.Bytes)) {
				reason = "unrecognized_feed"
			} else {
				posts, err = RSSParse(res.Bytes, 20, "", res.URL, true, true, 0)
			}
			if err != nil {
				reason = "feed_parse_failed"
			} else if reason == "" {
				if err := recordAutopilotShadowFetch(ctx, a.DB, job, source, at.Unix(), len(posts)); err != nil {
					return err
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
				!errors.Is(err, errAutopilotSourceChanged) &&
				!errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
			continue
		}
		// A temporary network/format problem is not a judgement on
		// content quality. Move unreachable candidates to cooldown, retaining
		// their history and a scheduled retry instead of clogging the review
		// queue. Confirmed missing sources are rejected separately below.
		if state == "candidate" && (reason == "feed_unavailable" || reason == "rate_limited" ||
			reason == "unrecognized_feed" || reason == "feed_parse_failed") {
			retry := nextCheck
			if reason == "unrecognized_feed" || reason == "feed_parse_failed" {
				retry = at.Add(7 * 24 * time.Hour)
			}
			if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate",
				"cooldown", reason, jx.Marshal(jx.M{"http_status": res.Status, "retry_reason": reason}),
				at, retry); err != nil &&
				!errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
			continue
		}
		// A confirmed 404/410 is not an inconclusive fetch: reject the
		// missing source and keep an auditable decision, rather than leaving
		// a dead feed in the candidate queue forever.
		if state == "candidate" && reason == "source_not_found" {
			if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate",
				"rejected", reason, jx.Marshal(jx.M{"http_status": res.Status}), at,
				at.Add(30*24*time.Hour)); err != nil &&
				!errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
			continue
		}
		// Historical material informs source quality, not eligibility of a
		// particular old post for today's feed.
		samples := autopilotSourceQualitySamples(posts, at.Unix())
		safeSamples := samples[:0]
		for _, post := range samples {
			if !jx.Bool(post, "source_sensitive") && !jx.Bool(post, "nsfw") && len(jx.ToStrs(post["safety"])) == 0 && !a.ContentIsAdult(post) {
				safeSamples = append(safeSamples, post)
			}
		}
		samples = safeSamples
		// Previously discovered OK groups were admitted by vague substring
		// matches in an aggregator feed. Recheck the actual group title before
		// they can occupy probation slots or be activated. Preserve the audit
		// trail by rejecting the source instead of silently deleting it.
		if (state == "candidate" || state == "probation") && platform == PlatformOK &&
			autopilotCanonicalTopic(job.Topic) == "SMM и блогинг" {
			unverified := res.OK && (OKTitleFromHTML(string(res.Bytes), id) == id ||
				!okDiscoveryGroupTitleMatches(OKTitleFromHTML(string(res.Bytes), id), job.Topic, nil))
			missing := res.Status == http.StatusNotFound || res.Status == http.StatusGone
			if unverified || missing {
				why := "off_topic_group"
				if missing {
					why = "source_not_found"
				}
				if err := transitionAutopilotSource(ctx, a.DB, job, source, state, "rejected",
					why, jx.Marshal(jx.M{"http_status": res.Status}), at, at.Add(30*24*time.Hour)); err != nil &&
					!errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
					return err
				}
				continue
			}
		}
		if reason == "" && state == "candidate" && len(samples) < 2 {
			// A sparse publisher is worth revisiting, not rejecting. It also
			// must not block all other candidates for another 24 hours.
			if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate",
				"cooldown", "sparse_publisher", jx.Marshal(jx.M{
					"quality_samples":     len(samples),
					"quality_window_days": autopilotSourceQualityDays,
				}), at, at.Add(30*24*time.Hour)); err != nil &&
				!errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
			continue
		}
		evidence := jx.M{"quality_samples": len(samples), "fresh_samples": len(autopilotFreshRSSSamples(samples, at.Unix())), "sample_limit": 20, "quality_window_days": autopilotSourceQualityDays}
		if res.Status > 0 {
			evidence["http_status"] = res.Status
		}
		if reason == "" && state == "candidate" {
			title := id
			if platform == PlatformRSS {
				title = hostOf(feed)
			}
			evaluation := evaluate(jx.M{"id": id, "title": title}, samples, job.Topic)
			if err := autopilotBudgetError(ctx); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if evaluation == nil {
				reason = "assessment_unavailable"
			} else {
				evidence["preliminary_score"] = evaluation.Score
				if format := personalBlogLabelFromReview(evaluation.PersonalBlog); format != personalBlogUnknown {
					evidence["personal_blog"] = format
				}
				if autopilotPreliminaryPass(evaluation.Score, threshold, len(samples)) {
					if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate", "probation", "shadow_preliminary_pass", jx.Marshal(evidence), at, at.Add(autopilotSourceReviewInterval(job, "probation"))); err != nil {
						if errors.Is(err, errAutopilotProbationLimit) {
							// Actual source evaluation passed. Keep a record of the
							// successful prescreen instead of marking it unverified.
							if saveErr := recordAutopilotSourceCheck(ctx, a.DB, job, source, "candidate",
								"preliminary_pass_waiting_trial", jx.Marshal(evidence), at, at.Add(30*time.Minute)); saveErr != nil &&
								!errors.Is(saveErr, errAutopilotSourceChanged) {
								return saveErr
							}
							continue
						}
						if errors.Is(err, errAutopilotSourceChanged) || errors.Is(err, errAutopilotSourceOverride) {
							continue
						}
						return err
					}
					state = "probation"
					nextCheck = at.Add(autopilotSourceReviewInterval(job, state))
				} else {
					// A completed preliminary assessment with enough fresh posts
					// but insufficient topic relevance is a negative decision,
					// not an indefinitely waiting unverified candidate.
					evidence["threshold"] = threshold
					if err := transitionAutopilotSource(ctx, a.DB, job, source, "candidate",
						"rejected", "preliminary_topic_mismatch", jx.Marshal(evidence), at,
						at.Add(30*24*time.Hour)); err != nil &&
						!errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
						return err
					}
					continue
				}
			}
		}
		if reason == "" && (state == "probation" || state == "active") {
			// Only after source verification: the staged public-search posts
			// pass the same full analysis and admission checks as feed posts.
			if job.Scope == autopilotAutoScope {
				samples = autopilotMergePostHints(source, samples, a.autopilotPendingPublicPosts(ctx, job, source), at.Unix())
			}
			// Only live-window posts are candidates for delivery. Historical
			// posts can qualify a publisher but must never be pushed to users.
			if err := assess(ctx, source, autopilotFreshRSSSamples(samples, at.Unix())); err != nil {
				if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
					return budgetErr
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if reason := autopilotStreamUnavailableReason(err); newTrial && reason != "" {
					if transitionErr := transitionAutopilotSource(ctx, a.DB, job, source, state, "cooldown", reason, jx.Marshal(evidence), at, at.Add(24*time.Hour)); transitionErr != nil && !errors.Is(transitionErr, errAutopilotSourceChanged) && !errors.Is(transitionErr, errAutopilotSourceOverride) {
						return transitionErr
					}
					continue
				}
				reason = "assessment_unavailable"
			} else {
				stats, err := autopilotEvidenceWindow(ctx, a.DB.Pool, job.Scope, job.TopicKey, source, at.Add(-30*24*time.Hour), at.Add(time.Second))
				if err != nil {
					return err
				}
				professional := a.User(ctx, "", nil).professionalValueTopic(job.Topic)
				effectiveRelevant := effectiveSourceRelevant(stats.Relevant, stats.Eligible, professional)
				evidence = jx.M{"window_days": 30, "evaluated": stats.Evaluated, "relevant": stats.Relevant, "eligible": stats.Eligible, "effective_relevant": effectiveRelevant, "advertising": stats.Advertising, "duplicates": stats.Duplicates, "fresh_unique": stats.FreshUnique, "curated_unique": stats.CuratedUnique, "successful_passes": stats.SuccessfulPasses}
				next, why := decideSourceState(state, sourceEvidence{Evaluated: stats.Evaluated, Relevant: effectiveRelevant, FreshUnique: stats.FreshUnique, CuratedUnique: stats.CuratedUnique, SuccessfulPasses: stats.SuccessfulPasses, Advertising: stats.Advertising, Duplicates: stats.Duplicates}, policy)
				if next != state {
					nextCheck := at.Add(autopilotSourceReviewInterval(job, next))
					if next == "rejected" {
						nextCheck = at.Add(30 * 24 * time.Hour)
					}
					if err := transitionAutopilotSource(ctx, a.DB, job, source, state, next, why, jx.Marshal(evidence), at, nextCheck); err != nil && !errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) {
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
			nextCheck = at.Add(time.Hour)
		}
		if err := recordAutopilotSourceCheck(ctx, a.DB, job, source, state, reason, jx.Marshal(evidence), at, nextCheck); err != nil && !errors.Is(err, errAutopilotSourceChanged) {
			return err
		}
	}
	return nil
}
