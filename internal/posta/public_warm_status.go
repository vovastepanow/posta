package posta

import "posta/internal/jx"

// Never disclose owner-specific queries, even if they later become eligible
// for private prewarming. Only the server's published starter queries appear.
func (u *U) publicWarmStatus() []jx.M {
	keys := make([]string, len(publicWarmSeedQueries))
	for i, query := range publicWarmSeedQueries {
		keys[i] = autopilotTopicKey(query)
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT p.topic_key,p.target,p.enabled,p.checked_at,p.ready_count,p.duplicate_count,p.missing_media_count,p.diagnosis,
		COALESCE(j.next_run_at,0),COALESCE(j.lease_until,0),COALESCE(j.enabled,false),COALESCE(j.demand_active,false),COALESCE(j.last_error,'')
		FROM public_warm_profiles p LEFT JOIN source_autopilot_jobs j
		ON j.scope='public' AND j.kind='auto_discovery' AND j.topic_key=p.topic_key
		WHERE p.topic_key=ANY($1::text[])`, keys)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byKey := map[string]jx.M{}
	for rows.Next() {
		var key, diagnosis string
		var enabled bool
		var target, ready, duplicates, media int
		var checked, next, lease int64
		var jobEnabled, eligible bool
		var failure string
		if err := rows.Scan(&key, &target, &enabled, &checked, &ready, &duplicates, &media, &diagnosis, &next, &lease, &jobEnabled, &eligible, &failure); err != nil {
			return nil
		}
		switch diagnosis {
		case "ready", "missing_media", "duplicates", "needs_review", "no_sources", "needs_content", "not_checked":
		default:
			diagnosis = "not_checked"
		}
		if failure != "warmup_budget_exhausted" {
			failure = autopilotFailureKind(failure)
		}
		byKey[key] = jx.M{"target": target, "enabled": enabled, "checked_at": checked, "ready_count": ready,
			"duplicate_count": duplicates, "missing_media_count": media, "diagnosis": diagnosis,
			"next_run_at": next, "running": jobEnabled && eligible && lease > now() && autopilotWorkerAlive(u.App.autopilotWorkerHealth("auto"), now()),
			"failure": failure, "stale": checked > 0 && now()-checked > 12*3600}
	}
	if rows.Err() != nil {
		return nil
	}
	out := make([]jx.M, 0, len(keys))
	for i, key := range keys {
		row := byKey[key]
		if row == nil {
			row = jx.M{"target": 100, "enabled": false, "checked_at": 0, "diagnosis": "not_checked"}
		}
		row["query"] = publicWarmSeedQueries[i]
		out = append(out, row)
	}
	return out
}
