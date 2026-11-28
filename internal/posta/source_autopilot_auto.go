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
		job.WarmOnly = true
		supply, err := a.refreshPublicWarmSupply(ctx, job)
		if err != nil {
			return 0, err
		}
		job.WarmReady = supply.Ready >= supply.Target
		return publicWarmInterval(supply.Ready, supply.Target), nil
	}
	active, _, err := a.autopilotProjectedCounts(ctx, job.Topic)
	if err != nil {
		return 0, err
	}
	mode := "healthy"
	observed, err := a.autopilotObservedContent(ctx, job)
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
		overrides := u.sourceOverridesForTopic(d.topic)
		rate, covered := autopilotObservedOwnerRate(observed, func(source string) bool {
			return sourceExcludedByOverride(overrides, source)
		})
		if !covered {
			// Unobserved supply must not inherit another owner's estimate.
			minimumCoverage = 0
			continue
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

// Retain hashes by source so the owner-level estimate can count their
// union after applying that owner's exclusions. A shared useful post still
// supplies one item, not zero or one item per publisher.
func (a *App) autopilotObservedContent(ctx context.Context, job *autopilotJob) (map[string][]string, error) {
	since := time.Now().Add(-7 * 24 * time.Hour).Unix()
	rows, err := a.DB.Pool.Query(ctx, `WITH latest AS (
		SELECT DISTINCT ON (source_id,post_hash) source_id,post_hash,content_hash,published_at,eligible
		FROM source_autopilot_post_evidence e WHERE scope=$1 AND topic_key=$2
		AND e.observed_at >= COALESCE((SELECT (s.evidence->>'full_recheck_at')::bigint
			FROM source_autopilot_sources s WHERE s.scope=e.scope AND s.topic_key=e.topic_key AND s.source_id=e.source_id),0)
		ORDER BY source_id,post_hash,policy_version DESC
	), covered AS (
		SELECT s.source_id FROM source_autopilot_sources s
		JOIN source_autopilot_collection_passes p ON p.scope=s.scope AND p.topic_key=s.topic_key AND p.source_id=s.source_id
		WHERE s.scope=$1 AND s.topic_key=$2 AND s.state='active' AND p.fetched_at >= GREATEST($3,COALESCE((s.evidence->>'full_recheck_at')::bigint,0))
		GROUP BY s.source_id HAVING count(DISTINCT (p.fetched_at/86400)) >= 2
	), useful AS (
		SELECT DISTINCT source_id,content_hash FROM latest
		WHERE published_at >= $3 AND eligible AND content_hash<>''
	)
	SELECT s.source_id,COALESCE(e.content_hash,'') FROM covered s
	LEFT JOIN useful e ON e.source_id=s.source_id`, job.Scope, job.TopicKey, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]string{}
	for rows.Next() {
		var source, hash string
		if err := rows.Scan(&source, &hash); err != nil {
			return nil, err
		}
		if _, ok := result[source]; !ok {
			result[source] = nil
		}
		if hash != "" {
			result[source] = append(result[source], hash)
		}
	}
	return result, rows.Err()
}

func autopilotObservedOwnerRate(observed map[string][]string, excluded func(string) bool) (float64, bool) {
	unique := map[string]bool{}
	covered := false
	for source, hashes := range observed {
		if excluded != nil && excluded(source) {
			continue
		}
		covered = true
		for _, hash := range hashes {
			if hash != "" {
				unique[hash] = true
			}
		}
	}
	return float64(len(unique)) / 7, covered
}
