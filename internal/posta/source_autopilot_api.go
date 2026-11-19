package posta

import (
	"encoding/json"
	"errors"
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
}

func autopilotStatusReason(in autopilotStatusInput) string {
	switch {
	case in.Paused:
		return "paused"
	case !in.Enabled || !in.Eligible:
		return "disabled"
	case in.Running:
		return "running"
	case in.Failure != "":
		return in.Failure
	case in.Deficit <= 0:
		return "supplied"
	case in.Target > 0 && in.Available >= in.Target:
		return "pool_full"
	case in.FetchRemaining <= 0:
		return "fetch_budget_exhausted"
	case in.ModelRemaining <= 0:
		return "model_budget_exhausted"
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
		var candidates, probation, confirmed int
		err := u.DB.Pool.QueryRow(u.Ctx, `SELECT count(*) FILTER(WHERE state='candidate'),count(*) FILTER(WHERE state='probation'),count(*) FILTER(WHERE state='active') FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`, autopilotShadowScope, key).Scan(&candidates, &probation, &confirmed)
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
		row := jx.M{"topic": d.Topic, "mode": d.Mode, "posts_per_day": d.PostsPerDay, "horizon_days": d.HorizonDays, "shadow_candidates": candidates, "shadow_probation": probation, "shadow_confirmed": confirmed}
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
		err = u.DB.Pool.QueryRow(u.Ctx, `SELECT
			count(*) FILTER (WHERE state='candidate' AND next_check_at <= $3),
			count(*) FILTER (WHERE state='candidate' AND next_check_at > $3),
			count(*) FILTER (WHERE state='candidate' AND checked_at=0),
			count(*) FILTER (WHERE checked_at >= $4),
			count(*) FILTER (WHERE reason='assessment_unavailable')
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`,
			scope, key, time.Now().Unix(), time.Now().Add(-24*time.Hour).Unix()).Scan(&ready, &delayed, &unchecked, &recentChecks, &aiPending)
		if err != nil {
			return nil, err
		}
		row["queue_ready"], row["queue_delayed"], row["queue_unchecked"] = ready, delayed, unchecked
		row["checked_24h"], row["assessment_unavailable"] = recentChecks, aiPending
		row["probation_limit"] = u.TInt("autopilot_max_probation")
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
			if d.Mode == "auto" {
				stats := jx.Unmarshal(checkpoint)
				row["direct_reviewed_total"] = jx.Int(stats, "direct_reviewed_total")
				row["direct_accepted_total"] = jx.Int(stats, "direct_accepted_total")
				row["direct_rejected_total"] = jx.Int(stats, "direct_rejected_total")
				row["pipeline_phase"] = jx.Str(stats, "pipeline_phase")
				row["pipeline_phase_at"] = jx.Int(stats, "pipeline_phase_at")
				row["last_search_at"] = jx.Int(stats, "last_search_at")
				row["last_search_outcome"] = jx.Str(stats, "last_search_outcome")
				row["last_source_candidates"] = jx.Int(stats, "last_source_candidates")
				row["last_direct_sources"] = jx.Int(stats, "last_direct_sources")
				row["last_search_backends"] = jx.Int(stats, "last_search_backends")
				row["last_backends_ok"] = jx.Int(stats, "last_backends_ok")
				row["last_backends_empty"] = jx.Int(stats, "last_backends_empty")
				row["last_backends_unavailable"] = jx.Int(stats, "last_backends_unavailable")
				var recent int
				err := u.DB.Pool.QueryRow(u.Ctx, `SELECT count(*) FROM source_autopilot_direct_results
					WHERE scope='public' AND topic_key=$1 AND published_at >= $2 AND assessed_at >= $2 AND policy_version=$3`,
					key, time.Now().Add(-autopilotDirectMaxAge).Unix(), TopicProfileVersion).Scan(&recent)
				if err != nil {
					return nil, err
				}
				row["direct_qualified_recent"] = recent
				var autoCandidates, autoProbation, autoActive int
				if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT
					count(*) FILTER(WHERE state='candidate'),
					count(*) FILTER(WHERE state='probation'),
					count(*) FILTER(WHERE state='active')
					FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`,
					autopilotAutoScope, key).Scan(&autoCandidates, &autoProbation, &autoActive); err != nil {
					return nil, err
				}
				row["auto_candidates"] = autoCandidates
				row["auto_sources_probation"] = autoProbation
				row["auto_sources_active"] = autoActive
				row["diagnosis"] = autopilotStatusReason(autopilotStatusInput{
					Paused: d.Paused, Enabled: enabled, Eligible: eligible,
					Running: lease > now(), Failure: failure,
					Available: available, Target: supply.Demand.Target,
					Deficit: supply.Demand.Deficit, Candidates: autoCandidates + autoProbation,
					ActiveSources: autoActive, FetchRemaining: jx.Int(row, "fetch_remaining"),
					ModelRemaining: jx.Int(row, "model_remaining"),
				})
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
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
	return jx.M{"topics": topics, "shadow_enabled": u.TBool("autopilot_shadow"), "auto_enabled": u.TBool("autopilot_auto"), "auto_available": true, "backends": backends}, nil
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
	d := autopilotDemandSettings{Topic: topic, Mode: mode, PostsPerDay: rate, HorizonDays: horizon}
	if err := saveAutopilotDemand(u.Ctx, u.DB.Pool, u.ID, d, now()); err != nil {
		if errors.Is(err, errAutopilotInvalidDemand) {
			return Bad("Проверьте тему, частоту и горизонт запаса.")
		}
		return err
	}
	return nil
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
