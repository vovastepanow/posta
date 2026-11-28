package posta

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
	"posta/internal/llm"
)

// Source review diagnostics never store provider responses, prompts, URLs or keys.
// Only coarse failure categories are persisted for aggregate troubleshooting.
type autopilotAssessmentProbeKey struct{}

type autopilotAssessmentProbe struct {
	mu    sync.Mutex
	cause string
}

func withAutopilotAssessmentProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, autopilotAssessmentProbeKey{}, &autopilotAssessmentProbe{})
}

func autopilotAssessmentProbeOf(ctx context.Context) *autopilotAssessmentProbe {
	probe, _ := ctx.Value(autopilotAssessmentProbeKey{}).(*autopilotAssessmentProbe)
	return probe
}

func autopilotAssessmentCause(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "free-models-per-day"), strings.Contains(message, "429"), strings.Contains(message, "rate limit"), strings.Contains(message, "суточного лимита"):
		return "provider_rate_limited"
	case strings.Contains(message, "403"), strings.Contains(message, "forbidden"):
		return "provider_forbidden"
	case strings.Contains(message, "401"), strings.Contains(message, "unauthorized"):
		return "provider_unauthorized"
	case strings.Contains(message, "timed out"), strings.Contains(message, "deadline exceeded"):
		return "model_timeout"
	case strings.Contains(message, "нет доступной модели"), strings.Contains(message, "модель для этой задачи выключена"), strings.Contains(message, "не настроен"):
		return "no_model_configured"
	case strings.Contains(message, "budget"), strings.Contains(message, "бюджет"):
		return "model_budget_unavailable"
	default:
		return "model_error"
	}
}

func autopilotNoteAssessmentCause(ctx context.Context, cause string) {
	if probe := autopilotAssessmentProbeOf(ctx); probe != nil {
		probe.mu.Lock()
		probe.cause = cause
		probe.mu.Unlock()
	}
}

func autopilotResetAssessmentCause(ctx context.Context) {
	autopilotNoteAssessmentCause(ctx, "")
}

func autopilotReadAssessmentCause(ctx context.Context) string {
	if probe := autopilotAssessmentProbeOf(ctx); probe != nil {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		return probe.cause
	}
	return ""
}

const autopilotShadowKind = "shadow_discovery"
const autopilotShadowScope = "public:shadow"
const autopilotAutoKind = "auto_discovery"
const autopilotAutoScope = "public"

func validAutopilotPublicJob(job *autopilotJob) bool {
	return job != nil && ((job.Scope == autopilotShadowScope && job.Kind == autopilotShadowKind) || (job.Scope == autopilotAutoScope && job.Kind == autopilotAutoKind)) && autopilotTopicKey(job.Topic) == job.TopicKey
}

// SourceIDFor lowercases whole URLs. A feed path can be case-sensitive, so
// the autopilot normalizes only scheme/host and preserves the escaped path.
// Safely normalize known non-RSS public publishers through existing site
// adapters. This is not permission for arbitrary signed/query-specific URLs.
func autopilotPublicSiteFeed(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return "", false
	}
	if host, id := osnovaTimelineSource(raw); host != "" {
		query := u.Query()
		for key, vals := range query {
			if (key != "sorting" && key != "subsitesIds" && key != "subsitesids") || len(vals) != 1 {
				return "", false
			}
		}
		if query.Get("sorting") != "new" {
			return "", false
		}
		return strings.ToLower(osnovaTimelineURL(host, id)), true
	}
	if source := pikabuCommunityURL(raw); source != "" && u.RawQuery == "" {
		return source, true
	}
	if u.Hostname() == "habr.com" && strings.HasPrefix(u.Path, "/ru/rss/") && u.Query().Get("fl") == "ru" && len(u.Query()) == 1 {
		u.RawQuery = ""
		canonical := u.String()
		if sharedSourceEligible(PlatformRSS, jx.M{"id": canonical}) {
			return canonical, true
		}
	}
	return "", false
}

func autopilotPublicRSSID(raw string) (string, bool) {
	if feed, ok := canonicalYouTubeFeed(raw); ok {
		return "rss:" + feed, true
	}
	if site, ok := autopilotPublicSiteFeed(raw); ok {
		return "rss:" + site, true
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
	warmEnabled := enabled && mode == "auto" && scope == autopilotAutoScope && a.TBool("public_warmup_enabled")
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
		if warmEnabled {
			_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_jobs(scope,topic_key,kind,topic,reason,priority,next_run_at,requested_at)
				SELECT $1,topic_key,$2,query,'public_warmup',0,GREATEST(start_after,$3),$3
				FROM public_warm_profiles WHERE enabled
				ON CONFLICT(scope,topic_key,kind) DO UPDATE SET demand_active=true`, scope, kind, time.Now().Unix())
			if err != nil {
				return err
			}
		}
		if enabled {
			if err := autopilotScheduleIdleFullRechecks(ctx, tx, scope, kind, mode); err != nil {
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
		// A previous day-cap failure must not keep the job asleep until
		// midnight after an administrator increases the cap (or after defaults
		// are upgraded). Resume only when the persisted ledgers confirm spare
		// fetch, model AND paid capacity. Respect explicit administrator
		// limits and never clear or refund charged attempts.
		if enabled {
			paidLimitMicro := int64(min(0.10, a.TFloat("autopilot_paid_daily_usd"))*1000000 + 0.5)
			if paidLimitMicro > 0 {
				if _, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs j
					SET next_run_at=LEAST(j.next_run_at,$3)
					WHERE j.scope=$1 AND j.kind=$2 AND j.enabled AND j.demand_active
					AND j.lease_until <= extract(epoch FROM clock_timestamp())::bigint
					AND j.last_error=$4 AND j.next_run_at>$3
					-- Resume only with evidence of reaching an older cap.
					-- A generic budget error alone must not erase a backoff.
					AND (COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope=(CASE WHEN j.scope='public:shadow' THEN 'public' ELSE j.scope END)||':'||j.topic_key
						AND resource='public_fetch' AND day=(now() AT TIME ZONE 'UTC')::date),0) >= 1000
					OR COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope=(CASE WHEN j.scope='public:shadow' THEN 'public' ELSE j.scope END)||':'||j.topic_key
						AND resource='llm_attempt' AND day=(now() AT TIME ZONE 'UTC')::date),0) >= 500)
					AND COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope=(CASE WHEN j.scope='public:shadow' THEN 'public' ELSE j.scope END)||':'||j.topic_key
						AND resource='public_fetch' AND day=(now() AT TIME ZONE 'UTC')::date),0) < $5
					AND COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope='server' AND resource='public_fetch'
						AND day=(now() AT TIME ZONE 'UTC')::date),0) < $6
					AND COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope=(CASE WHEN j.scope='public:shadow' THEN 'public' ELSE j.scope END)||':'||j.topic_key AND resource='llm_attempt'
						AND day=(now() AT TIME ZONE 'UTC')::date),0) < $7
					AND COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope='server' AND resource='llm_attempt'
						AND day=(now() AT TIME ZONE 'UTC')::date),0) < $8
					AND COALESCE((SELECT used FROM source_autopilot_budget
						WHERE scope='autopilot_paid' AND resource='usd_micro'
						AND day=(now() AT TIME ZONE 'UTC')::date),0) < $9`,
					scope, kind, time.Now().Add(15*time.Second).Unix(), errAutopilotBudgetExhausted.Error(),
					a.TInt("autopilot_fetch_topic_daily"), a.TInt("autopilot_fetch_server_daily"),
					a.TInt("autopilot_model_topic_daily"), a.TInt("autopilot_model_server_daily"),
					paidLimitMicro); err != nil {
					return err
				}
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
			(NOT $2 OR (NOT EXISTS (SELECT 1 FROM source_autopilot_demands d WHERE d.topic_key=j.topic_key AND d.mode=$4 AND NOT d.paused AND ($4<>'auto' OR d.posts_per_day>0))
			 AND NOT ($5 AND EXISTS (SELECT 1 FROM public_warm_profiles p WHERE p.topic_key=j.topic_key AND p.enabled))))`, kind, enabled, scope, mode, warmEnabled)
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
	warmSeeded := false
	for {
		enabled := a.TBool(setting)
		if enabled && mode == "auto" && a.TBool("public_warmup_enabled") && !warmSeeded {
			if err := a.seedPublicWarmProfiles(ctx); err != nil {
				if ctx.Err() == nil {
					log.Printf("public warmup seeds: %v", err)
				}
			} else {
				warmSeeded = true
			}
		}
		err := a.reconcileAutopilotMode(ctx, enabled, mode, scope, kind)
		a.recordAutopilotWorkerTick(mode, err)
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

// autopilotNextReviewDelay never moves a retry ahead of a source's own
// next_check_at. It does allow a due review to run before a 24-hour general
// search interval, while leaving enough time to avoid tight retry loops.
func autopilotNextReviewDelay(base time.Duration, now time.Time, earliest int64) time.Duration {
	if earliest <= 0 {
		return base
	}
	return min(base, max(2*time.Minute, time.Unix(earliest, 0).Sub(now)))
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
	if job.WarmOnly && job.WarmReady {
		if err := finishAutopilotJob(ctx, a.DB, job, time.Now().Add(nextInterval), job.Checkpoint, ""); err != nil && !errors.Is(err, errAutopilotLeaseLost) {
			log.Printf("public warmup finish: %v", err)
		}
		return
	}
	// Keep an additional conservative cap on the source autopilot paid reserve.
	// The existing paid ledger still blocks requests once recorded spend reaches it.
	paidLimit := min(0.10, a.TFloat("autopilot_paid_daily_usd"))
	sm, tm, sf, tf := a.TInt("autopilot_model_server_daily"), a.TInt("autopilot_model_topic_daily"), a.TInt("autopilot_fetch_server_daily"), a.TInt("autopilot_fetch_topic_daily")
	if job.WarmOnly {
		paidLimit = 0
		sm, tm, sf, tf = publicWarmBudget(sm, tm, sf, tf, a.LLM.Settings().FreeDailyLimit)
	} else {
		ctx = llm.WithSourceAutopilotPaidNemotronFallback(ctx)
	}
	ctx = withAutopilotPaidLimit(ctx, paidLimit)
	ctx = llm.WithSourceAutopilotFreeFallback(ctx)
	err = runAutopilotLeased(ctx, a.DB, job, 10*time.Minute, 2*time.Minute,
		int64(sm), int64(tm), int64(sf), int64(tf),
		func(workCtx context.Context) error { return a.autopilotShadowSearch(workCtx, job) })
	if ctx.Err() != nil || errors.Is(err, errAutopilotLeaseLost) {
		return
	}
	if job.WarmOnly {
		if supply, supplyErr := a.refreshPublicWarmSupply(ctx, job); supplyErr == nil {
			nextInterval = publicWarmInterval(supply.Ready, supply.Target)
		} else if !errors.Is(supplyErr, errAutopilotLeaseLost) && ctx.Err() == nil {
			log.Printf("public warmup supply: %v", supplyErr)
		}
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
	if err == nil && !job.WarmOnly {
		var earliest int64
		checkErr := a.DB.Pool.QueryRow(ctx, `SELECT COALESCE(MIN(
			CASE WHEN next_check_at <= $3 THEN $3+120 ELSE next_check_at END),0)
			FROM source_autopilot_sources s
			WHERE s.scope=$1 AND s.topic_key=$2
			AND s.state IN ('candidate','probation','active','cooldown','rejected')
			AND NOT (s.state='candidate' AND s.reason='probation_full')`,
			job.Scope, job.TopicKey, time.Now().Unix()).Scan(&earliest)
		if checkErr == nil && earliest > 0 {
			nextInterval = autopilotNextReviewDelay(nextInterval, time.Now(), earliest)
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
