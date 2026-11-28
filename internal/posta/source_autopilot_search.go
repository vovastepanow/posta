package posta

import (
	"context"
	"errors"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Both the scheduler and the public diagnostics use this readiness rule.
// $3 is the observation timestamp in their source queue queries.
const autopilotCandidateReadySQL = `state='candidate' AND (next_check_at <= $3 OR reason='probation_full' OR
	(reason IN ('feed_unavailable','insufficient_fresh_samples') AND checked_at < $3-3600))`

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
	if err := a.autopilotMarkPhase(ctx, job, "prepare_legacy"); err != nil {
		return err
	}
	if _, err := a.autopilotImportLegacyPending(ctx, job, 60); err != nil {
		return err
	}
	if err := a.autopilotQueueFullRecheck(ctx, job); err != nil {
		return err
	}
	if job.Scope == autopilotAutoScope {
		// Source suggestions originating from fully accepted posts enter the
		// same candidate verification queue as backend discoveries.
		if err := a.autopilotMarkPhase(ctx, job, "prepare_hints"); err != nil {
			return err
		}
		if err := a.autopilotConsumeSourceHints(ctx, job, 20); err != nil {
			return err
		}
		if err := a.autopilotConsumeCitations(ctx, job); err != nil {
			return err
		}
		if err := a.autopilotNominateIndexedSources(ctx, job, 8); err != nil {
			return err
		}
		if err := a.autopilotMarkPhase(ctx, job, "prepare_projection"); err != nil {
			return err
		}
		if err := a.reconcileAutopilotProjections(ctx, job); err != nil {
			return err
		}
		if err := a.autopilotMarkPhase(ctx, job, "prepare_delivery"); err != nil {
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
	// A small independent read-only control panel exercises the configured
	// model on real publishers, after regular source reviews and delivery.
	if err := a.autopilotRunControlAudit(ctx, job); err != nil {
		return err
	}
	// Do not grow a raw candidate backlog indefinitely. In auto mode,
	// periodically reserve a narrow search for actual posts instead: a large
	// or temporarily blocked queue must not prevent useful content delivery.
	directOnly := false
	{
		var dueCandidates, dueReaudit int
		if err := a.DB.Pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE `+autopilotCandidateReadySQL+`),
			count(*) FILTER (WHERE evidence ? 'review_policy_v3_scheduled' AND NOT (evidence ? 'review_policy_v3_checked') AND next_check_at <= $3)
			FROM source_autopilot_sources
			WHERE scope=$1 AND topic_key=$2
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE (o.scope=$1 OR ($1='public:shadow' AND o.scope='public'))
				AND o.owner_id='' AND o.topic_key=$2 AND o.source_id=source_autopilot_sources.source_id AND o.decision='excluded')`,
			job.Scope, job.TopicKey, time.Now().Unix()).Scan(&dueCandidates, &dueReaudit); err != nil {
			return err
		}
		lastSearch := jx.Int(jx.Unmarshal(job.Checkpoint), "last_search_at")
		at := time.Now().Unix()
		directOnly = autopilotBacklogDirectSearch(job.Scope, dueCandidates, lastSearch, at)
		if !directOnly && (autopilotShouldDrainCandidateBacklog(dueCandidates) ||
			autopilotShouldDrainReaudit(dueReaudit, lastSearch, at)) {
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
		turn := jx.Int(jx.Unmarshal(job.Checkpoint), "query_turn")
		var backends []func() []DiscoveryCandidate
		if !directOnly {
			backends = u.autopilotSearchBackends(query, job.Topic, turn)
		}
		enabledBackends = len(backends)
		// Spend a bounded share of the existing search budget on finding
		// actual publications before scanning publisher directories. Only
		// independently admitted posts nominate their authors here. Broad
		// source search remains available for image/video and cold-start
		// topics or when the text classifier is unavailable.
		var verified, postNominations []DiscoveryCandidate
		var directErr error
		if job.Scope == autopilotAutoScope && ctx.Err() == nil && autopilotBudgetError(ctx) == nil {
			directAttempted = true
			postCtx := withAutopilotSearchFetchCap(searchCtx, 30)
			postCandidates, nominations, postErr := a.autopilotFindSourcesFromPosts(postCtx, job, query, autopilotDirectMaxPerRun*autopilotPostHintLimit)
			if !directOnly {
				postNominations = nominations
			}
			directErr = postErr
			if directErr == nil {
				directErr = a.autopilotAssessDirectPosts(ctx, job, postCandidates)
			}
			if directErr == nil {
				verified, directErr = a.autopilotVerifiedDirectSources(ctx, job, postCandidates)
			}
			directSourceCount = len(verified)
		}
		// Keep exploration across platforms. Raw directory hits are
		// still unverified and never bypass the normal probation gate.
		known, knownErr := a.autopilotKnownSearchSources(ctx, job)
		if knownErr != nil {
			return verified, knownErr
		}
		var raw []DiscoveryCandidate
		if !directOnly {
			raw = autopilotRunSearchBackendsAdaptiveKnown(searchCtx, turn, backends, 6, 64, known)
		}
		sourceSearchCount = len(raw) + len(postNominations)
		candidates := make([]DiscoveryCandidate, 0, len(verified)+len(postNominations)+len(raw))
		for _, group := range []struct {
			kind string
			list []DiscoveryCandidate
		}{{"verified_post", verified}, {"post_nomination", postNominations}, {"directory", raw}} {
			for _, candidate := range group.list {
				candidate.OriginKind = group.kind
				candidates = append(candidates, candidate)
			}
		}
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
		// The other discovery path can still provide candidates when the
		// model or a public-post backend is unavailable. Preserve the error
		// for the worker diagnostics rather than treating it as a mismatch.
		if directErr != nil {
			searchErr = directErr
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
	if deliveryErr := a.autopilotFinishBacklog(ctx, job); deliveryErr != nil {
		return deliveryErr
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
	if job.Scope != autopilotAutoScope {
		return nil
	}
	if err := a.autopilotMarkPhase(ctx, job, "delivery_projection"); err != nil {
		return err
	}
	if err := a.reconcileAutopilotProjections(ctx, job); err != nil {
		return err
	}
	if err := a.autopilotMarkPhase(ctx, job, "delivery_content"); err != nil {
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

// Keep ordinary discovery backpressure, but allow the direct-post path
// (and only its independently verified publishers) once per 15 minutes.
func autopilotBacklogDirectSearch(scope string, due int, lastSearch, now int64) bool {
	return scope == autopilotAutoScope && due > 0 && (lastSearch <= 0 || now-lastSearch >= 15*60)
}

// A historical audit must not suppress all new discovery for hours. The
// initial review batch still runs before every search, and real new candidates
// still take priority. Audit-only work yields to discovery every 15 minutes.
func autopilotShouldDrainReaudit(due int, lastSearch, now int64) bool {
	return due > 0 && lastSearch > 0 && now-lastSearch < 15*60
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
	return autopilotRunSearchBackendsAdaptiveKnown(ctx, turn, backends, minBackends, targetCandidates, nil)
}

// Existing sources and explicit global exclusions are not novel discoveries.
// Scope and topic isolation are the same as for the review worker.
func (a *App) autopilotKnownSearchSources(ctx context.Context, job *autopilotJob) (map[string]bool, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2
		UNION SELECT source_id FROM source_autopilot_overrides
		WHERE (scope=$1 OR ($1='public:shadow' AND scope='public'))
		AND topic_key=$2 AND owner_id='' AND decision='excluded'`, job.Scope, job.TopicKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
}

// Rediscovered publishers still supply query attribution, but must not
// satisfy the target for new sources and prevent later platforms from
// being explored. The caller supplies topic-scoped known source IDs.
func autopilotRunSearchBackendsAdaptiveKnown(ctx context.Context, turn int64, backends []func() []DiscoveryCandidate, minBackends, targetCandidates int, known map[string]bool) []DiscoveryCandidate {
	if len(backends) == 0 {
		return nil
	}
	if turn < 0 {
		turn = 0
	}
	start := int(turn % int64(len(backends)))
	var groups [][]DiscoveryCandidate
	seen := make(map[string]bool)
	for i := 0; i < len(backends); i++ {
		if ctx.Err() != nil || autopilotBudgetError(ctx) != nil || autopilotSearchFetchCapReached(ctx) {
			break
		}
		group := backends[(start+i)%len(backends)]()
		groups = append(groups, group)
		for _, candidate := range group {
			if id, ok := autopilotCandidateID(candidate); ok && !known[id] {
				seen[id] = true
			}
		}
		if i+1 >= minBackends && len(seen) >= targetCandidates {
			break
		}
	}
	return autopilotInterleaveSearchResults(groups)
}

// Retain the best-ranked result of every backend before its tail. A large
// directory cannot monopolize the first review slots, and repeated source
// aliases do not cost another registration or review slot.
func autopilotInterleaveSearchResults(groups [][]DiscoveryCandidate) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	visitInterleaved(groups, func(candidate DiscoveryCandidate) bool {
		if id, ok := autopilotCandidateID(candidate); ok && !seen[id] {
			seen[id] = true
			out = append(out, candidate)
		}
		return true
	})
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
	groups := make([][]jx.M, 0, len(by))
	for _, platform := range sortedKeys(by) {
		groups = append(groups, by[platform])
	}
	// Round-robin prevents the first platform alphabetically from consuming
	// every nomination slot. Source IDs are still deduplicated globally.
	visitInterleaved(groups, func(post jx.M) bool {
		published := jx.Int(post, "posted_at")
		if !autopilotPublicPostURL(jx.Str(post, "url"), false) ||
			published <= 0 || published > at+300 || IsPostOutdated(published, at, config.ContentMaxAgeDays) ||
			jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") || len(jx.ToStrs(post["safety"])) > 0 ||
			jx.Bool(post, "advertising") || PostIsAdvertisement(post) || PostSearchIsAd(post) {
			return true
		}
		candidate := PostSearchAuthor(post)
		if candidate == nil {
			return true
		}
		id, ok := autopilotCandidateID(*candidate)
		if !ok || seen[id] || !autopilotPostMatchesSource(id, post) {
			return true
		}
		seen[id] = true
		candidate.Origin = "public_post:" + jx.Str(post, "url")
		out = append(out, *candidate)
		return len(out) < limit
	})
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

// The independent direct path can attest only recent public text. Do not
// spend all nomination slots on video, old or empty posts that staging
// would immediately discard. Media discovery retains its separate path.
// Rotate the bounded inspection window so stable result lists still expose
// later authors after the first batch has been classified.
func autopilotAssessablePostCandidates(by map[string][]jx.M, at int64, limit int, turn int64) ([]DiscoveryCandidate, map[string][]jx.M) {
	if limit <= 0 {
		return nil, nil
	}
	filtered := map[string][]jx.M{}
	bySource := map[string][]jx.M{}
	seenBySource := map[string]map[string]bool{}
	total := 0
	for _, platform := range sortedKeys(by) {
		for _, post := range by[platform] {
			candidate := PostSearchAuthor(post)
			if candidate == nil {
				continue
			}
			source, ok := autopilotCandidateID(*candidate)
			if !ok {
				continue
			}
			safe := autopilotSafePostHints(source, []jx.M{post}, at)
			if len(safe) != 1 {
				continue
			}
			filtered[platform] = append(filtered[platform], safe[0])
			// Every item has already passed the same safety checks. Retain
			// the first unique hints without revalidating the whole prefix.
			if len(bySource[source]) < autopilotPostHintLimit {
				key := CandidatePostKey(safe[0])
				seen := seenBySource[source]
				if seen == nil {
					seen = map[string]bool{}
					seenBySource[source] = seen
				}
				if !seen[key] {
					seen[key] = true
					bySource[source] = append(bySource[source], jx.Copy(safe[0]))
				}
			}
			total++
		}
	}
	all := autopilotCandidatesFromPosts(filtered, at, total)
	if len(all) <= limit {
		return all, bySource
	}
	offset := int(max(int64(0), turn) % int64(len(all)))
	selected := make([]DiscoveryCandidate, 0, limit)
	for i := 0; i < limit; i++ {
		selected = append(selected, all[(offset+i)%len(all)])
	}
	return selected, bySource
}

// A recent public article with media may identify a relevant publisher
// even though it cannot enter the independent text assessment path.
// Require the query to appear in the article itself, not just the source
// title, and leave source admission to the regular unbiased feed review.
func autopilotPostSearchNominations(by map[string][]jx.M, query string, at int64, limit int, direct map[string][]jx.M) []DiscoveryCandidate {
	if strings.TrimSpace(query) == "" || limit <= 0 {
		return nil
	}
	filtered := map[string][]jx.M{}
	for _, platform := range sortedKeys(by) {
		for _, post := range by[platform] {
			if !queryPhraseHit(jx.Str(post, "title")+" "+AnalysisText(post), query) {
				continue
			}
			author := PostSearchAuthor(post)
			if author == nil {
				continue
			}
			id, ok := autopilotCandidateID(*author)
			if !ok || len(direct[id]) > 0 {
				// A source with assessable text must earn a nomination
				// through independent admission, not this media fallback.
				continue
			}
			filtered[platform] = append(filtered[platform], post)
		}
	}
	return autopilotCandidatesFromPosts(filtered, at, limit)
}

func (a *App) autopilotFindSourcesFromPosts(ctx context.Context, job *autopilotJob, query string, limit int) ([]DiscoveryCandidate, []DiscoveryCandidate, error) {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind || strings.TrimSpace(query) == "" || limit <= 0 || ctx.Err() != nil || autopilotBudgetError(ctx) != nil {
		return nil, nil, autopilotBudgetError(ctx)
	}
	u := a.User(ctx, "", nil)
	plan := autopilotPublicPostQueryPlan(query)
	// The public worker has no personal Telegram login. Use only backends
	// that can search without a subscriber's private account.
	// Bluesky's public post search is separate from account discovery.
	// Check it before the slower backends so it gets a share of the fetch
	// budget. The worker uses no user's private VK/Telegram session.
	var bluesky []jx.M
	if a.TBool("search_bluesky") {
		bluesky, _ = u.autopilotSearchBlueskyPosts(query)
	}
	only := map[string]bool{"vc/dtf": true, "Lemmy": true, "Mastodon": true}
	posts, _ := u.searchEngineRun(plan, 0, 25*time.Second, only)
	if len(bluesky) > 0 {
		posts["bluesky"] = bluesky
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := autopilotBudgetError(ctx); err != nil {
		return nil, nil, err
	}
	at := time.Now().Unix()
	candidates, bySource := autopilotAssessablePostCandidates(posts, at, limit, jx.Int(jx.Unmarshal(job.Checkpoint), "query_turn"))
	nominations := autopilotPostSearchNominations(posts, query, at, min(8, limit), bySource)
	// Stage only actually assessable posts tied to the selected authors.
	// The model still evaluates at most autopilotDirectMaxPerRun posts.
	for _, candidate := range candidates {
		source, ok := autopilotCandidateID(candidate)
		if !ok {
			continue
		}
		if err := a.autopilotStagePublicPosts(ctx, job, source, bySource[source]); err != nil {
			return candidates, nominations, err
		}
	}
	return candidates, nominations, nil
}

// A verified post is a reason to inspect an author, not to approve its
// source. The direct-results table contains only completed admission
// decisions, scoped to this public topic and the current review policy.
func (a *App) autopilotVerifiedDirectSources(ctx context.Context, job *autopilotJob, candidates []DiscoveryCandidate) ([]DiscoveryCandidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if id, ok := autopilotCandidateID(candidate); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT DISTINCT source_id FROM source_autopilot_direct_results
		WHERE scope=$1 AND topic_key=$2 AND source_id=ANY($3::text[])
		AND policy_version=$4 AND published_at >= $5 AND assessed_at >= $5`,
		job.Scope, job.TopicKey, ids, TopicProfileVersion, time.Now().Add(-autopilotDirectMaxAge).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	verified := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		verified[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return autopilotFilterVerifiedDirectSources(candidates, verified), nil
}

func autopilotFilterVerifiedDirectSources(candidates []DiscoveryCandidate, verified map[string]bool) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	for _, candidate := range candidates {
		id, ok := autopilotCandidateID(candidate)
		if ok && verified[id] && !seen[id] {
			out = append(out, candidate)
			seen[id] = true
		}
	}
	return out
}

func (a *App) autopilotShadowSearchWith(ctx context.Context, job *autopilotJob, find func(context.Context) ([]DiscoveryCandidate, error)) error {
	if !validAutopilotPublicJob(job) || find == nil {
		return errors.New("invalid public topic job")
	}
	candidates, findErr := find(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	seen := map[string]bool{}
	at := time.Now()
	for _, candidate := range candidates {
		sourceID, ok := autopilotCandidateID(candidate)
		if !ok || seen[sourceID] {
			continue
		}
		seen[sourceID] = true
		origin := candidate.OriginKind
		switch origin {
		case "verified_post", "post_nomination", "directory":
		default:
			origin = "unknown"
		}
		evidence := jx.Marshal(jx.M{"search_rank": len(seen), "discovery_origin": origin})
		if err := registerAutopilotCandidateWithEvidence(ctx, a.DB, job, sourceID, "discovered", evidence, at); err != nil {
			return err
		}
		if err := recordAutopilotQuerySource(ctx, a, job, sourceID); err != nil {
			return err
		}
	}
	return findErr
}
