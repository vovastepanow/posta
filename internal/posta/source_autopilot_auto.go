package posta

import (
	"context"
	"time"

	"posta/internal/jx"
)

// Evaluate each owner's usable supply independently. A shared post can satisfy
// several owners, so the most urgent owner controls the shared retry interval.
func (a *App) autopilotAutoInterval(ctx context.Context, job *autopilotJob) (time.Duration, error) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT user_id,topic,posts_per_day,horizon_days FROM source_autopilot_demands WHERE topic_key=$1 AND mode='auto' AND NOT paused AND posts_per_day>0 ORDER BY user_id`, job.TopicKey)
	if err != nil {
		return 0, err
	}
	type demand struct {
		owner, topic  string
		rate, horizon float64
	}
	var demands []demand
	for rows.Next() {
		var d demand
		if err := rows.Scan(&d.owner, &d.topic, &d.rate, &d.horizon); err != nil {
			rows.Close()
			return 0, err
		}
		demands = append(demands, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(demands) == 0 {
		return 0, errAutopilotLeaseLost
	}
	active, _, err := a.autopilotProjectedCounts(ctx, job.Topic)
	if err != nil {
		return 0, err
	}
	mode := "healthy"
	observed, err := a.autopilotObservedYield(ctx, job)
	if err != nil {
		return 0, err
	}
	minimumCoverage := -1.0
	for _, d := range demands {
		u := a.User(ctx, d.owner, nil)
		available, err := u.AutopilotAvailableSupply(d.topic)
		if err != nil {
			return 0, err
		}
		decision := decideAutopilotSupply(autopilotSupplyInput{Rate: d.rate, Horizon: d.horizon, Available: available, Capacity: u.TInt("pool_target"), ConfirmedSources: active, Now: time.Now()})
		if decision.Mode == "bootstrap" || (decision.Mode == "refill" && mode != "bootstrap") {
			mode = decision.Mode
		}
		rate := 0.0
		overrides := u.sourceOverridesForTopic(d.topic)
		for source, daily := range observed {
			if !sourceExcludedByOverride(overrides, source) {
				rate += daily
			}
		}
		coverage := rate / d.rate
		if minimumCoverage < 0 || coverage < minimumCoverage {
			minimumCoverage = coverage
		}
	}
	checkpoint := jx.Unmarshal(job.Checkpoint)
	if checkpoint == nil {
		checkpoint = jx.M{}
	}
	previous, since := jx.Str(checkpoint, "supply_mode"), jx.Int(checkpoint, "supply_mode_since")
	at := time.Now().Unix()
	if previous != mode && previous != "" && mode != "bootstrap" && since > 0 && at-since < int64(time.Hour.Seconds()) {
		mode = previous
	}
	if previous != mode || since <= 0 {
		checkpoint["supply_mode_since"] = at
	}
	checkpoint["supply_mode"] = mode
	if minimumCoverage >= 0 && len(observed) > 0 {
		previousCoverage := jx.Float(checkpoint, "expected_coverage")
		if !jx.Has(checkpoint, "expected_coverage") {
			previousCoverage = minimumCoverage
		}
		if minimumCoverage == 0 {
			checkpoint["expected_coverage"] = 0.0
		} else {
			checkpoint["expected_coverage"] = 0.7*previousCoverage + 0.3*minimumCoverage
		}
	}
	job.Checkpoint = jx.Marshal(checkpoint)
	return autopilotRetryInterval(mode, len(observed) > 0 && jx.Float(checkpoint, "expected_coverage") >= 1), nil
}

// Bootstrap retries more frequently so newly created topics are not left
// without discovery for hours. Refill is less urgent; healthy topics keep
// their existing daily cadence. Worker budgets and rate limits still apply.
func autopilotRetryInterval(mode string, coveredByObservedYield bool) time.Duration {
	switch mode {
	case "bootstrap":
		return time.Hour
	case "refill":
		if coveredByObservedYield {
			return 12 * time.Hour
		}
		return 2 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// Count only eligible, non-cloned content from active sources with at least
// two distinct days of successful collection; missing coverage is unknown.
func (a *App) autopilotObservedYield(ctx context.Context, job *autopilotJob) (map[string]float64, error) {
	since := time.Now().Add(-7 * 24 * time.Hour).Unix()
	rows, err := a.DB.Pool.Query(ctx, `SELECT s.source_id,
		(SELECT count(DISTINCT e.content_hash) FROM source_autopilot_post_evidence e WHERE e.scope=s.scope AND e.topic_key=s.topic_key AND e.source_id=s.source_id AND e.eligible AND e.content_hash<>'' AND e.published_at >= $3
		AND NOT EXISTS(SELECT 1 FROM source_autopilot_post_evidence other WHERE other.scope=e.scope AND other.topic_key=e.topic_key AND other.content_hash=e.content_hash AND other.source_id<>e.source_id AND other.published_at >= $3)),
		(SELECT count(DISTINCT (p.fetched_at/86400)) FROM source_autopilot_collection_passes p WHERE p.scope=s.scope AND p.topic_key=s.topic_key AND p.source_id=s.source_id AND p.fetched_at >= $3)
		FROM source_autopilot_sources s WHERE s.scope=$1 AND s.topic_key=$2 AND s.state='active'`, job.Scope, job.TopicKey, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]float64{}
	for rows.Next() {
		var source string
		var useful, days int64
		if err := rows.Scan(&source, &useful, &days); err != nil {
			return nil, err
		}
		if days >= 2 {
			result[source] = float64(useful) / 7
		}
	}
	return result, rows.Err()
}
