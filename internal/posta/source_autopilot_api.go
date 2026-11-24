package posta

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
)

// A stable, machine-readable reason makes empty supply actionable without
// exposing other subscribers' activity or the worker's internal errors.
type autopilotStatusInput struct {
	Paused, Enabled, Eligible, Running                    bool
	Failure                                               string
	Available, Target, Deficit, Candidates, ActiveSources int
	FetchRemaining, ModelRemaining                        int64
	ServerFetchRemaining, ServerModelRemaining            int64
	ModelFailures, Promoted24h                            int
}

func autopilotStatusReason(in autopilotStatusInput) string {
	switch {
	case in.Paused:
		return "paused"
	case !in.Enabled || !in.Eligible:
		return "disabled"
	case in.Running:
		return "running"
	case in.Deficit <= 0:
		return "supplied"
	case in.Target > 0 && in.Available >= in.Target:
		return "pool_full"
	case in.ServerFetchRemaining <= 0:
		return "server_fetch_budget_exhausted"
	case in.ServerModelRemaining <= 0:
		return "server_model_budget_exhausted"
	case in.Failure != "":
		return in.Failure
	case in.FetchRemaining <= 0:
		return "fetch_budget_exhausted"
	case in.ModelRemaining <= 0:
		return "model_budget_exhausted"
	case in.ModelFailures > 0 && in.Promoted24h == 0 && in.Candidates > 0:
		return "classifier_unavailable"
	case in.Candidates == 0 && in.ActiveSources == 0:
		return "no_sources_found"
	case in.ActiveSources == 0:
		return "sources_under_review"
	default:
		return "low_eligible_yield"
	}
}

// SourceAutopilotStatus returns only this user's demands and public shadow
// aggregates. It does not expose other owners or private source metadata.
func (u *U) SourceAutopilotStatus() (jx.M, error) {
	demands, err := listAutopilotDemands(u.Ctx, u.DB.Pool, u.ID)
	if err != nil {
		return nil, err
	}
	var serverFetchUsed, serverModelUsed int64
	if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT
		COALESCE(sum(used) FILTER (WHERE resource='public_fetch'),0),
		COALESCE(sum(used) FILTER (WHERE resource='llm_attempt'),0)
		FROM source_autopilot_budget
		WHERE scope='server' AND day=(now() AT TIME ZONE 'UTC')::date`).Scan(&serverFetchUsed, &serverModelUsed); err != nil {
		return nil, err
	}
	serverFetchRemaining := max(int64(0), int64(u.TInt("autopilot_fetch_server_daily"))-serverFetchUsed)
	serverModelRemaining := max(int64(0), int64(u.TInt("autopilot_model_server_daily"))-serverModelUsed)
	topics := make([]jx.M, 0, len(demands))
	for _, d := range demands {
		key := autopilotTopicKey(d.Topic)
		var candidates, probation, confirmed, suspended, rejected int
		err := u.DB.Pool.QueryRow(u.Ctx, `SELECT
			count(*) FILTER(WHERE state='candidate'),
			count(*) FILTER(WHERE state='probation'),
			count(*) FILTER(WHERE state='active'),
			count(*) FILTER(WHERE state='cooldown'),
			count(*) FILTER(WHERE state='rejected')
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`, autopilotShadowScope, key).
			Scan(&candidates, &probation, &confirmed, &suspended, &rejected)
		if err != nil {
			return nil, err
		}
		available, err := u.AutopilotAvailableSupply(d.Topic)
		if err != nil {
			return nil, err
		}
		active, autoProbation, err := u.App.autopilotProjectedCounts(u.Ctx, d.Topic)
		if err != nil {
			return nil, err
		}
		supply := decideAutopilotSupply(autopilotSupplyInput{Rate: d.PostsPerDay, Horizon: d.HorizonDays, Available: available, Capacity: u.TInt("pool_target"), ConfirmedSources: active, Now: time.Now()})
		row := jx.M{"topic": d.Topic, "mode": d.Mode, "posts_per_day": d.PostsPerDay, "horizon_days": d.HorizonDays, "personal_blog_mode": u.personalBlogMode(d.Topic), "shadow_candidates": candidates, "shadow_probation": probation, "shadow_confirmed": confirmed,
			"shadow_cooldown": suspended, "shadow_rejected": rejected}
		// The mode switch and the PostgreSQL job are not proof that the
		// background goroutine exists. Expose liveness from the serving
		// process so an idle queue cannot mask a dead worker.
		worker := u.App.autopilotWorkerHealth(d.Mode)
		workerAlive := autopilotWorkerAlive(worker, time.Now().Unix())
		backgroundEnabled := !u.Cfg.Dev || os.Getenv("POSTA_DEV_PARSERS") == "1"
		row["worker_alive"], row["worker_last_tick"] = workerAlive, worker.LastTick
		row["worker_reconcile_error"], row["worker_panics"], row["worker_last_panic"] = worker.ReconcileError, worker.Panics, worker.LastPanic
		row["background_enabled"] = backgroundEnabled
		row["job_exists"] = false
		row["paused"] = d.Paused
		row["available"], row["target"], row["deficit"], row["capacity_limited"], row["active_sources"] = available, supply.Demand.Target, supply.Demand.Deficit, supply.CapacityLimited, active
		row["auto_probation"], row["supply_mode"] = autoProbation, supply.Mode
		var fetchUsed, modelUsed int64
		if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT COALESCE(sum(used) FILTER (WHERE resource='public_fetch'),0),COALESCE(sum(used) FILTER (WHERE resource='llm_attempt'),0) FROM source_autopilot_budget WHERE scope=$1 AND day=(now() AT TIME ZONE 'UTC')::date`, autopilotAutoScope+":"+key).Scan(&fetchUsed, &modelUsed); err != nil {
			return nil, err
		}
		row["fetch_remaining"] = max(0, int64(u.TInt("autopilot_fetch_topic_daily"))-fetchUsed)
		row["model_remaining"] = max(0, int64(u.TInt("autopilot_model_topic_daily"))-modelUsed)
		row["server_fetch_remaining"], row["server_model_remaining"] = serverFetchRemaining, serverModelRemaining
		row["server_fetch_limit"], row["server_model_limit"] = u.TInt("autopilot_fetch_server_daily"), u.TInt("autopilot_model_server_daily")
		var next, lease int64
		var attempts int
		var enabled, eligible bool
		var lastError string
		var checkpoint []byte
		scope, kind := autopilotShadowScope, autopilotShadowKind
		if d.Mode == "auto" {
			scope, kind = autopilotAutoScope, autopilotAutoKind
		}
		// Aggregate queue health for the selected worker only. Unlike the
		// legacy discovery "found" counter these are persistent source rows,
		// so operators can distinguish backlog from scheduled retry and AI stalls.
		var ready, delayed, unchecked, recentChecks, aiPending int
		var reauditTotal, reauditChecked, reauditReady int
		err = u.DB.Pool.QueryRow(u.Ctx, `SELECT
			count(*) FILTER (WHERE state='candidate' AND (next_check_at <= $3 OR reason='probation_full' OR
				(reason IN ('feed_unavailable','insufficient_fresh_samples') AND checked_at < $3-3600))),
			count(*) FILTER (WHERE state='candidate' AND next_check_at > $3 AND reason<>'probation_full'),
			count(*) FILTER (WHERE state='candidate' AND checked_at=0),
			count(*) FILTER (WHERE checked_at >= $4),
			count(*) FILTER (WHERE reason='assessment_unavailable' AND checked_at >= $4),
			count(*) FILTER (WHERE evidence ? 'review_policy_v3_scheduled'),
			count(*) FILTER (WHERE evidence ? 'review_policy_v3_scheduled' AND evidence ? 'review_policy_v3_checked'),
			count(*) FILTER (WHERE evidence ? 'review_policy_v3_scheduled' AND NOT (evidence ? 'review_policy_v3_checked') AND next_check_at <= $3)
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`,
			scope, key, time.Now().Unix(), time.Now().Add(-24*time.Hour).Unix()).Scan(&ready, &delayed, &unchecked, &recentChecks, &aiPending, &reauditTotal, &reauditChecked, &reauditReady)
		if err != nil {
			return nil, err
		}
		row["queue_ready"], row["queue_delayed"], row["queue_unchecked"] = ready, delayed, unchecked
		row["checked_24h"], row["assessment_unavailable"] = recentChecks, aiPending
		row["reaudit_total"], row["reaudit_checked"], row["reaudit_ready"] = reauditTotal, reauditChecked, reauditReady
		// Progress counters based on persisted decisions, not the instantaneous
		// candidate total. This distinguishes real verification from a stuck job.
		var decisions24h, promoted24h, rejected24h int
		if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT
			count(*),
			count(*) FILTER (WHERE old_state='candidate' AND new_state='probation'),
			count(*) FILTER (WHERE old_state='candidate' AND new_state='rejected')
			FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2 AND at >= $3`,
			scope, key, time.Now().Add(-24*time.Hour).Unix()).Scan(&decisions24h, &promoted24h, &rejected24h); err != nil {
			return nil, err
		}
		row["decisions_24h"], row["promoted_24h"], row["rejected_24h"] = decisions24h, promoted24h, rejected24h
		row["probation_limit"] = max(1000, u.TInt("autopilot_max_probation"))
		row["yield_policy_enabled"] = u.TBool("autopilot_yield_policy")
		// Persisted last outcomes explain why a nonempty candidate queue has
		// not produced probation sources. Aggregate only; no source IDs leak.
		outcomes := jx.M{}
		outcomeRows, outcomeErr := u.DB.Pool.Query(u.Ctx, `SELECT reason,count(*)
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND state='candidate'
			GROUP BY reason`, scope, key)
		if outcomeErr != nil {
			return nil, outcomeErr
		}
		for outcomeRows.Next() {
			var reason string
			var count int
			if err := outcomeRows.Scan(&reason, &count); err != nil {
				outcomeRows.Close()
				return nil, err
			}
			if reason == "" {
				reason = "not_checked"
			}
			outcomes[reason] = count
		}
		outcomeErr = outcomeRows.Err()
		outcomeRows.Close()
		if outcomeErr != nil {
			return nil, outcomeErr
		}
		row["candidate_reasons"] = outcomes
		auditCtx, auditCancel := context.WithTimeout(u.Ctx, 2*time.Second)
		funnel, auditErr := AutopilotAuditFunnel(auditCtx, u.DB.Pool, scope, key, time.Now())
		auditCancel()
		if auditErr == nil {
			row["audit_funnel"] = funnel
		} else {
			row["audit_funnel_unavailable"] = true
		}
		// Only fixed categories are persisted; never expose raw provider replies,
		// prompts, credentials or source URLs in the owner-facing diagnostics.
		assessmentCauses := jx.M{}
		causeRows, causeErr := u.DB.Pool.Query(u.Ctx, `SELECT COALESCE(NULLIF(evidence->>'assessment_cause',''),'not_recorded'),count(*)
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2
			AND state='candidate' AND reason='assessment_unavailable'
			GROUP BY 1`, scope, key)
		if causeErr != nil {
			return nil, causeErr
		}
		for causeRows.Next() {
			var cause string
			var count int
			if err := causeRows.Scan(&cause, &count); err != nil {
				causeRows.Close()
				return nil, err
			}
			assessmentCauses[cause] = count
		}
		causeErr = causeRows.Err()
		causeRows.Close()
		if causeErr != nil {
			return nil, causeErr
		}
		row["candidate_assessment_causes"] = assessmentCauses
		probationReasons, rejectedReasons, cooldownReasons := jx.M{}, jx.M{}, jx.M{}
		probationRows, probationErr := u.DB.Pool.Query(u.Ctx, `SELECT state,COALESCE(NULLIF(reason,''),'not_checked'),count(*)
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND state IN ('probation','rejected','cooldown')
			GROUP BY 1,2`, scope, key)
		if probationErr != nil {
			return nil, probationErr
		}
		for probationRows.Next() {
			var state, reason string
			var count int
			if err := probationRows.Scan(&state, &reason, &count); err != nil {
				probationRows.Close()
				return nil, err
			}
			switch state {
			case "probation":
				probationReasons[reason] = count
			case "rejected":
				rejectedReasons[reason] = count
			case "cooldown":
				cooldownReasons[reason] = count
			}
		}
		probationErr = probationRows.Err()
		probationRows.Close()
		if probationErr != nil {
			return nil, probationErr
		}
		row["probation_reasons"] = probationReasons
		row["rejected_reasons"], row["cooldown_reasons"] = rejectedReasons, cooldownReasons
		var lastSuccess int64
		if err = u.DB.Pool.QueryRow(u.Ctx, `SELECT COALESCE(max(fetched_at),0) FROM source_autopilot_collection_passes WHERE scope=$1 AND topic_key=$2`, scope, key).Scan(&lastSuccess); err != nil {
			return nil, err
		}
		if d.Mode == "auto" {
			var directSuccess int64
			if err = u.DB.Pool.QueryRow(u.Ctx, `SELECT COALESCE(max(assessed_at),0)
				FROM source_autopilot_direct_results WHERE scope='public' AND topic_key=$1
				AND published_at >= $2 AND assessed_at >= $2 AND policy_version=$3`,
				key, time.Now().Add(-autopilotDirectMaxAge).Unix(), TopicProfileVersion).Scan(&directSuccess); err != nil {
				return nil, err
			}
			lastSuccess = max(lastSuccess, directSuccess)
		}
		row["last_success"] = lastSuccess
		err = u.DB.Pool.QueryRow(u.Ctx, `SELECT next_run_at,lease_until,enabled,demand_active,last_error,checkpoint,attempts FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, key, kind).Scan(&next, &lease, &enabled, &eligible, &lastError, &checkpoint, &attempts)
		if err == nil {
			row["job_exists"] = true
			failure := ""
			switch {
			case strings.Contains(lastError, errAutopilotBudgetExhausted.Error()):
				failure = "budget_exhausted"
			case lastError == "news_search_disabled":
				failure = "news_search_disabled"
			case lastError == "news_search_unavailable":
				failure = "news_search_unavailable"
			case lastError == "search_unavailable":
				failure = "search_unavailable"
			case lastError == "topic_busy":
				failure = "topic_busy"
			case lastError == "supply_unavailable":
				failure = "supply_unavailable"
			case lastError == "classifier_unavailable":
				failure = "classifier_unavailable"
			case lastError == "restricted_profile":
				failure = "restricted_profile"
			case lastError != "":
				failure = "other_error"
			}
			row["next_run_at"], row["running"], row["job_paused"], row["eligible"], row["failure"] = next, lease > now() && !d.Paused, !enabled, eligible && !d.Paused, failure
			row["attempts"] = attempts
			stats := jx.Unmarshal(checkpoint)
			// Pipeline telemetry is written in both auto and shadow modes.
			for key, value := range map[string]string{
				"pipeline_phase":      "pipeline_phase",
				"last_search_outcome": "last_search_outcome",
			} {
				row[key] = jx.Str(stats, value)
			}
			for _, key := range []string{"pipeline_phase_at", "last_search_at", "last_source_candidates", "last_direct_sources", "last_search_backends", "last_backends_ok", "last_backends_empty", "last_backends_unavailable"} {
				row[key] = jx.Int(stats, key)
			}
			if d.Mode == "auto" {
				row["direct_reviewed_total"] = jx.Int(stats, "direct_reviewed_total")
				row["direct_accepted_total"] = jx.Int(stats, "direct_accepted_total")
				row["direct_rejected_total"] = jx.Int(stats, "direct_rejected_total")
				var recent int
				err := u.DB.Pool.QueryRow(u.Ctx, `SELECT count(*) FROM source_autopilot_direct_results
					WHERE scope='public' AND topic_key=$1 AND published_at >= $2 AND assessed_at >= $2 AND policy_version=$3`,
					key, time.Now().Add(-autopilotDirectMaxAge).Unix(), TopicProfileVersion).Scan(&recent)
				if err != nil {
					return nil, err
				}
				row["direct_qualified_recent"] = recent
				var autoCandidates, autoProbation, autoActive, autoCooldown, autoRejected int
				if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT
					count(*) FILTER(WHERE state='candidate'),
					count(*) FILTER(WHERE state='probation'),
					count(*) FILTER(WHERE state='active'),
					count(*) FILTER(WHERE state='cooldown'),
					count(*) FILTER(WHERE state='rejected')
					FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`,
					autopilotAutoScope, key).Scan(&autoCandidates, &autoProbation, &autoActive, &autoCooldown, &autoRejected); err != nil {
					return nil, err
				}
				row["auto_candidates"] = autoCandidates
				row["auto_sources_probation"] = autoProbation
				row["auto_sources_active"] = autoActive
				row["auto_sources_cooldown"], row["auto_sources_rejected"] = autoCooldown, autoRejected
				row["diagnosis"] = autopilotStatusReason(autopilotStatusInput{
					Paused: d.Paused, Enabled: enabled, Eligible: eligible,
					Running: lease > now(), Failure: failure,
					Available: available, Target: supply.Demand.Target,
					Deficit: supply.Demand.Deficit, Candidates: autoCandidates + autoProbation,
					ActiveSources: autoActive, FetchRemaining: jx.Int(row, "fetch_remaining"),
					ModelRemaining:       jx.Int(row, "model_remaining"),
					ServerFetchRemaining: serverFetchRemaining, ServerModelRemaining: serverModelRemaining,
					ModelFailures: aiPending, Promoted24h: promoted24h,
				})
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		// Put scheduler failures ahead of misleading "waiting" summaries.
		// Pauses and saved administrator OFF settings are intentional and
		// must not be presented as crashes.
		if d.Mode != "off" && !d.Paused {
			setting := "autopilot_shadow"
			if d.Mode == "auto" {
				setting = "autopilot_auto"
			}
			if u.TBool(setting) {
				switch {
				case !backgroundEnabled:
					row["diagnosis"] = "worker_disabled_in_dev"
				case !workerAlive:
					row["diagnosis"] = "worker_not_running"
				case worker.ReconcileError:
					row["diagnosis"] = "scheduler_error"
				case !jx.Bool(row, "job_exists"):
					row["diagnosis"] = "job_missing"
				}
			}
		}
		topics = append(topics, row)
	}
	backends := []jx.M{
		{"name": "Telegram", "enabled": u.TBool("search_telegram"), "access": "public"},
		{"name": "ВКонтакте", "enabled": u.TBool("search_vk"), "access": "public"},
		{"name": "YouTube", "enabled": u.TBool("search_youtube"), "access": "public"},
		{"name": "TikTok", "enabled": u.TBool("autopilot_search_tiktok"), "access": "public"},
		{"name": "Одноклассники", "enabled": u.TBool("autopilot_search_ok"), "access": "public"},
		{"name": "Bluesky", "enabled": u.TBool("search_bluesky"), "access": "public"},
		{"name": "Lemmy", "enabled": u.TBool("search_lemmy"), "access": "public"},
		{"name": "Mastodon", "enabled": u.TBool("search_mastodon"), "access": "public"},
		{"name": "Reddit", "enabled": u.TBool("search_reddit"), "access": "public"},
		{"name": "Хабр", "enabled": u.TBool("autopilot_search_habr"), "access": "public"},
		{"name": "Tumblr", "enabled": u.TBool("autopilot_search_tumblr"), "access": "public"},
		{"name": "Новости RSS", "enabled": u.TBool("search_news"), "access": "public"},
	}
	revision := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
				revision = setting.Value[:7]
				break
			}
		}
	}
	return jx.M{"topics": topics, "shadow_enabled": u.TBool("autopilot_shadow"), "auto_enabled": u.TBool("autopilot_auto"), "auto_available": true, "backends": backends, "server_revision": revision, "background_content_enabled": u.BackgroundContentEnabled()}, nil
}

// Expose only known numeric evidence needed to audit source decisions.
// Provider replies, prompts, arbitrary metadata and URLs are never copied.
func autopilotPublicSourceReviewEvidence(raw []byte) jx.M {
	original := jx.Unmarshal(raw)
	out := jx.M{}
	for _, field := range []string{
		"preliminary_score", "threshold", "quality_samples", "fresh_samples",
		"reviewed_samples", "text_score", "image_score",
		"topic_relevant", "topic_adjacent", "topic_off_topic", "topic_unknown",
		"evaluated", "relevant", "eligible", "effective_relevant", "advertising",
		"duplicates", "same_source_duplicates", "fresh_unique", "curated_unique", "successful_passes",
		"window_days", "http_status",
	} {
		if _, exists := original[field]; exists {
			out[field] = jx.Int(original, field)
		}
	}
	// The audit bundle contains only selected public post excerpts. Check
	// every nested key again here: source evidence is untrusted historical
	// JSON and must not turn this endpoint into a raw metadata exporter.
	reviewSamples := make([]jx.M, 0, 5)
	for _, sample := range jx.Maps(original, "review_samples") {
		if len(reviewSamples) >= 5 {
			break
		}
		text := trimRunes(strings.TrimSpace(jx.Str(sample, "excerpt")), 850)
		if text == "" && !jx.Bool(sample, "media_present") {
			continue
		}
		item := jx.M{"excerpt": text, "media_present": jx.Bool(sample, "media_present")}
		if raw := jx.Str(sample, "url"); raw != "" {
			if parsed, err := url.Parse(raw); err == nil && parsed.User == nil && parsed.Hostname() != "" &&
				(parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.RawQuery == "" &&
				parsed.Fragment == "" && parsed.Port() == "" && len(parsed.Path) < 300 {
				item["url"] = raw
			}
		}
		if published := jx.Int(sample, "published_at"); published > 0 {
			item["published_at"] = published
		}
		reviewSamples = append(reviewSamples, item)
	}
	if len(reviewSamples) > 0 {
		out["review_samples"] = reviewSamples
	}
	if probe := jx.Map(original, "preliminary_post_probe"); len(probe) > 0 {
		safe := jx.M{}
		for _, key := range []string{"at", "fresh_sample", "media_excluded", "local_excluded", "submitted", "completed", "eligible"} {
			if jx.Has(probe, key) {
				safe[key] = jx.Int(probe, key)
			}
		}
		switch status := jx.Str(probe, "status"); status {
		case "no_text_only_input", "provider_failure", "completed_or_partial":
			safe["status"] = status
		}
		safe["has_eligible"] = jx.Bool(probe, "has_eligible")
		out["preliminary_post_probe"] = safe
	}
	if comparison := jx.Map(original, "yield_comparison"); len(comparison) > 0 {
		safe := jx.M{}
		for _, key := range []string{"at", "useful_unique", "live_unique", "trial_passes", "trial_started_at"} {
			if jx.Has(comparison, key) {
				safe[key] = jx.Int(comparison, key)
			}
		}
		for _, key := range []string{"strict_next", "yield_next"} {
			switch state := jx.Str(comparison, key); state {
			case "candidate", "probation", "active", "rejected", "cooldown":
				safe[key] = state
			}
		}
		safe["complete_sample"] = jx.Bool(comparison, "complete_sample")
		out["yield_comparison"] = safe
	}
	if cause := jx.Str(original, "assessment_cause"); cause != "" {
		switch cause {
		case "provider_rate_limited", "provider_forbidden", "provider_unauthorized",
			"model_timeout", "no_model_configured", "model_budget_unavailable",
			"model_error", "invalid_model_response", "media_unavailable", "no_reviewable_content":
			out["assessment_cause"] = cause
		}
	}
	return out
}

// Prefer verified publisher titles, then names already in the local
// registry/catalog. API URLs and generic hosts are not human source names.
func autopilotDisplayName(platform, id string, names ...string) string {
	host := hostOf(id)
	for _, candidate := range names {
		name := strings.TrimSpace(strings.TrimPrefix(candidate, SourceNamePrefixes[platform]))
		if name == "" || name == id || name == host || strings.Contains(name, "api.vc.ru") ||
			strings.Contains(name, "api.dtf.ru") || strings.HasPrefix(name, "rss:https://") {
			continue
		}
		return trimRunes(CleanHTMLText(name), 180)
	}
	if platform == PlatformRSS {
		if site, subsite := osnovaTimelineSource(id); site != "" {
			return "Раздел " + site + " · #" + strconv.FormatInt(subsite, 10)
		}
		if community := pikabuCommunityURL(id); community != "" {
			return "Сообщество Пикабу · " + strings.TrimPrefix(community, "https://pikabu.ru/community/")
		}
		return host
	}
	if platform == PlatformTelegram || platform == PlatformTikTok {
		return "@" + id
	}
	return id
}

// SourceAutopilotSources returns a bounded list of public sources in one
// lifecycle state, available only to subscribers of that exact topic.
// Pagination is by stable source ID, without scanning or sending a whole queue.
func (u *U) SourceAutopilotSources(topic, mode, state, after string, limit int) (jx.M, error) {
	if strings.TrimSpace(topic) == "" || (mode != "shadow" && mode != "auto") ||
		(state != "found" && state != "candidate" && state != "probation" && state != "active" && state != "cooldown" && state != "rejected") {
		return nil, Bad("Некорректный список источников.")
	}
	// Source explorer loads all rows in one request for normal queues.
	// Preserve the keyset cursor as a safeguard for unusually large topics.
	if limit < 1 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	key := autopilotTopicKey(topic)
	var owns bool
	if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT EXISTS(
		SELECT 1 FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2 AND mode=$3)`,
		u.ID, key, mode).Scan(&owns); err != nil {
		return nil, err
	}
	if !owns {
		return nil, NotFound("Тема автопилота не найдена.")
	}
	scope := autopilotShadowScope
	if mode == "auto" {
		scope = autopilotAutoScope
	}
	// The latest search cohort is stored in the job checkpoint: existing
	// sources may be rediscovered without a new discovery decision, and they
	// may already have moved to probation or active.
	var latestIDs []string
	if state == "found" {
		kind := autopilotShadowKind
		if mode == "auto" {
			kind = autopilotAutoKind
		}
		var checkpoint []byte
		err := u.DB.Pool.QueryRow(u.Ctx, `SELECT checkpoint FROM source_autopilot_jobs
			WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, key, kind).Scan(&checkpoint)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		latestIDs = jx.Strs(jx.Unmarshal(checkpoint), "last_search_source_ids")
		if len(latestIDs) == 0 {
			return jx.M{"sources": []jx.M{}, "next_after": "", "snapshot_available": false}, nil
		}
	}
	query := `SELECT s.source_id,s.state,s.reason,s.first_seen_at,s.checked_at,s.next_check_at,
		COALESCE(s.evidence->>'personal_blog','unknown'),s.evidence,COALESCE(r.name,''),r.sample,COALESCE(r.sample_at,0)
		FROM source_autopilot_sources s LEFT JOIN source_registry r ON r.source_id=s.source_id
		WHERE s.scope=$1 AND s.topic_key=$2 AND s.state=$3 AND s.source_id>$4
		ORDER BY s.source_id LIMIT $5`
	var rows pgx.Rows
	var err error
	if state == "found" {
		rows, err = u.DB.Pool.Query(u.Ctx, `SELECT s.source_id,s.state,s.reason,s.first_seen_at,s.checked_at,s.next_check_at,
		COALESCE(s.evidence->>'personal_blog','unknown'),s.evidence,COALESCE(r.name,''),r.sample,COALESCE(r.sample_at,0)
			FROM source_autopilot_sources s LEFT JOIN source_registry r ON r.source_id=s.source_id
			WHERE s.scope=$1 AND s.topic_key=$2
			AND s.source_id=ANY($5) AND s.source_id>$3
			ORDER BY s.source_id LIMIT $4`, scope, key, after, limit+1, latestIDs)
	} else {
		rows, err = u.DB.Pool.Query(u.Ctx, query, scope, key, state, after, limit+1)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Saved catalog proposals often already contain a human channel name.
	// This is a local lookup, not hundreds of network requests to render cards.
	catalogNames := map[string]string{}
	catalog := u.Catalog()
	for _, kind := range CatalogKinds {
		for _, entry := range *catalog.List(kind.Kind) {
			sid := SourceIDFor(kind.Platform, jx.Str(entry, "id"))
			catalogNames[sid] = jx.Str(entry, "name")
		}
	}
	for _, entry := range catalog.Suggestions {
		sid := jx.Str(entry, "source_id")
		if catalogNames[sid] == "" {
			catalogNames[sid] = jx.Str(entry, "name")
		}
	}
	items := make([]jx.M, 0, limit)
	hasMore := false
	for rows.Next() {
		var source, currentState, reason, blogKind, registryName string
		var discovered, checked, next, registrySampleAt int64
		var evidence, historicalSample []byte
		if err := rows.Scan(&source, &currentState, &reason, &discovered, &checked, &next, &blogKind, &evidence, &registryName, &historicalSample, &registrySampleAt); err != nil {
			return nil, err
		}
		if len(items) == limit {
			hasMore = true
			break
		}
		platform, id, _, valid := autopilotSourceParts(source)
		publicURL := ""
		if valid {
			publicURL = SourceWebURL(platform, id)
		}
		metadata := jx.Unmarshal(evidence)
		name := autopilotDisplayName(platform, id, jx.Str(metadata, "source_title"), registryName, catalogNames[source])
		kind := jx.Str(metadata, "source_kind")
		if platform == PlatformRSS {
			if host, _ := osnovaTimelineSource(id); host != "" {
				if page := jx.Str(metadata, "source_page"); page != "" &&
					osnovaPublisherPage(host, strings.TrimPrefix(page, "https://"+host)) == page {
					publicURL = page
				}
				// Historical AI labels are not reliable category identification.
				// Treat unverified API sources as unknown, not personal blogs.
				if kind != "category" {
					blogKind = "unknown"
				}
			}
		}
		if kind == "category" || kind == "community" {
			blogKind = "no"
		}
		review := autopilotPublicSourceReviewEvidence(evidence)
		if len(jx.Maps(review, "review_samples")) == 0 && len(historicalSample) > 0 {
			var posts []jx.M
			if json.Unmarshal(historicalSample, &posts) == nil {
				if snippets := autopilotAuditPostSamples(posts); len(snippets) > 0 {
					review["review_samples"] = snippets
					review["sample_origin"] = "historical_registry"
					review["sample_at"] = registrySampleAt
				}
			}
		}
		items = append(items, jx.M{"source_id": source, "state": currentState, "reason": reason,
			"first_seen_at": discovered, "checked_at": checked, "next_check_at": next, "personal_blog": personalBlogKind(blogKind), "url": publicURL,
			"name": name, "source_kind": kind, "review_evidence": review})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cursor := ""
	if hasMore {
		cursor = jx.Str(items[len(items)-1], "source_id")
	}
	return jx.M{"sources": items, "next_after": cursor, "snapshot_available": true}, nil
}

// SourceAutopilotHistory exposes public decisions only for a profile the
// caller has configured. The cursor is a decision ID, not a user identifier.
func (u *U) SourceAutopilotHistory(topic, mode string, before int64, limit int) (jx.M, error) {
	if strings.TrimSpace(topic) == "" || (mode != "shadow" && mode != "auto") || before < 0 {
		return nil, Bad("Некорректные параметры истории.")
	}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	key := autopilotTopicKey(topic)
	var owns bool
	if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2)`, u.ID, key).Scan(&owns); err != nil {
		return nil, err
	}
	if !owns {
		return nil, NotFound("Тема автопилота не найдена.")
	}
	scope := autopilotShadowScope
	if mode == "auto" {
		scope = autopilotAutoScope
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT id,source_id,old_state,new_state,reason,evidence,policy_version,at FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2 AND ($3=0 OR id<$3) ORDER BY id DESC LIMIT $4`, scope, key, before, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]jx.M, 0, limit)
	hasMore := false
	for rows.Next() {
		var id, at int64
		var source, oldState, newState, reason string
		var evidence json.RawMessage
		var version int
		if err := rows.Scan(&id, &source, &oldState, &newState, &reason, &evidence, &version, &at); err != nil {
			return nil, err
		}
		if len(items) == limit {
			hasMore = true
			continue
		}
		items = append(items, jx.M{"id": id, "source_id": source, "old_state": oldState, "new_state": newState, "reason": reason, "evidence": evidence, "policy_version": version, "at": at})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var next int64
	if hasMore {
		next = jx.Int(items[len(items)-1], "id")
	}
	return jx.M{"decisions": items, "next_before": next}, nil
}

func (u *U) SourceAutopilotDemandSave(topic, mode string, rate, horizon float64) error {
	topic = strings.TrimSpace(topic)
	if len([]rune(topic)) > config.TopicMaxLength {
		return Bad("Название темы слишком длинное.")
	}
	if mode != "off" && mode != "shadow" && mode != "auto" {
		return Bad("Неизвестный режим автопилота.")
	}
	// When a subscriber enables or unpauses a topic, do not leave an
	// existing 12/24-hour scheduled job idle. A plain save of an unchanged
	// active mode must not bypass a real worker backoff.
	var previousMode string
	var wasPaused bool
	previousErr := u.DB.Pool.QueryRow(u.Ctx, `SELECT mode,paused FROM source_autopilot_demands
		WHERE user_id=$1 AND topic_key=$2`, u.ID, autopilotTopicKey(topic)).Scan(&previousMode, &wasPaused)
	if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
		return previousErr
	}
	d := autopilotDemandSettings{Topic: topic, Mode: mode, PostsPerDay: rate, HorizonDays: horizon}
	if err := saveAutopilotDemand(u.Ctx, u.DB.Pool, u.ID, d, now()); err != nil {
		if errors.Is(err, errAutopilotInvalidDemand) {
			return Bad("Проверьте тему, частоту и горизонт запаса.")
		}
		return err
	}
	if mode == "off" || (mode == "auto" && rate == 0) || (previousErr == nil && previousMode == mode && !wasPaused) {
		return nil
	}
	scope, kind, setting := autopilotShadowScope, autopilotShadowKind, "autopilot_shadow"
	if mode == "auto" {
		scope, kind, setting = autopilotAutoScope, autopilotAutoKind, "autopilot_auto"
	}
	// Respect a persisted administrator disable; the response explicitly
	// exposes that switch so users see why no job was started.
	if !u.TBool(setting) {
		return nil
	}
	if err := u.App.reconcileAutopilotMode(u.Ctx, true, mode, scope, kind); err != nil {
		return err
	}
	_, err := u.DB.Pool.Exec(u.Ctx, `UPDATE source_autopilot_jobs
		SET next_run_at=LEAST(next_run_at,$4)
		WHERE scope=$1 AND topic_key=$2 AND kind=$3 AND enabled AND demand_active
		AND last_error='' AND lease_until <= extract(epoch FROM clock_timestamp())::bigint`,
		scope, autopilotTopicKey(topic), kind, time.Now().Unix())
	return err
}

// SourceAutopilotRequest raises the priority of this user's configured profile
// without shortening an existing retry delay or changing another owner's mode.
func (u *U) SourceAutopilotRequest(topic string) error {
	var mode string
	var rate float64
	var paused bool
	err := u.DB.Pool.QueryRow(u.Ctx, `SELECT mode,posts_per_day,paused FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2`, u.ID, autopilotTopicKey(topic)).Scan(&mode, &rate, &paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotFound("Тема автопилота не найдена.")
	}
	if err != nil {
		return err
	}
	if paused || mode == "off" || (mode == "auto" && rate == 0) {
		return Bad("Включите автопилот и задайте ненулевую частоту публикаций.")
	}
	scope, kind, setting := autopilotShadowScope, autopilotShadowKind, "autopilot_shadow"
	if mode == "auto" {
		scope, kind, setting = autopilotAutoScope, autopilotAutoKind, "autopilot_auto"
	}
	if !u.TBool(setting) {
		return Bad("Этот режим автопилота выключен на сервере.")
	}
	if err := u.App.reconcileAutopilotMode(u.Ctx, true, mode, scope, kind); err != nil {
		return err
	}
	// An explicit user request may advance a scheduled check. The job lease,
	// daily network/model budgets and per-source rate-limit backoffs still apply.
	at := time.Now()
	if err := enqueueAutopilotJob(u.Ctx, u.DB.Pool, scope, autopilotTopicKey(topic), kind, topic, "requested", 10, at); err != nil {
		return err
	}
	_, err = u.DB.Pool.Exec(u.Ctx, `UPDATE source_autopilot_jobs
		SET next_run_at=LEAST(next_run_at,$4), requested_at=$4
		WHERE scope=$1 AND topic_key=$2 AND kind=$3 AND enabled AND demand_active`,
		scope, autopilotTopicKey(topic), kind, at.Unix())
	return err
}

func (u *U) SourceAutopilotPause(topic string, paused bool) error {
	tag, err := u.DB.Pool.Exec(u.Ctx, `UPDATE source_autopilot_demands SET paused=$3,updated_at=$4 WHERE user_id=$1 AND topic_key=$2 AND mode<>'off'`, u.ID, autopilotTopicKey(topic), paused, now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return NotFound("Включённая тема автопилота не найдена.")
	}
	return nil
}
