package posta

import "posta/internal/jx"

// Never disclose owner-specific queries, even if they later become eligible
// for private prewarming. Only the server's published starter queries appear.
func (u *U) publicWarmStatus() []jx.M {
	keys := make([]string, len(publicWarmSeedQueries))
	for i, query := range publicWarmSeedQueries {
		keys[i] = autopilotTopicKey(query)
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT topic_key,target,enabled,checked_at,ready_count,duplicate_count,missing_media_count,diagnosis
		FROM public_warm_profiles WHERE topic_key=ANY($1::text[])`, keys)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byKey := map[string]jx.M{}
	for rows.Next() {
		var key, diagnosis string
		var enabled bool
		var target, ready, duplicates, media int
		var checked int64
		if err := rows.Scan(&key, &target, &enabled, &checked, &ready, &duplicates, &media, &diagnosis); err != nil {
			return nil
		}
		switch diagnosis {
		case "ready", "missing_media", "duplicates", "needs_review", "no_sources", "needs_content", "not_checked":
		default:
			diagnosis = "not_checked"
		}
		byKey[key] = jx.M{"target": target, "enabled": enabled, "checked_at": checked, "ready_count": ready,
			"duplicate_count": duplicates, "missing_media_count": media, "diagnosis": diagnosis}
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
