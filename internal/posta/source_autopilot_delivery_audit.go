package posta

import (
	"context"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Diagnostic counters are owner-specific, never part of the public evidence.
// The delivery loop is sequential: only its leased worker writes this audit.
type autopilotDeliveryAuditKey struct{}

type autopilotDeliveryAudit struct {
	owners map[string]map[string]int
}

func autopilotDeliveryAuditCount(ctx context.Context, owner, reason string, count int) {
	audit, ok := ctx.Value(autopilotDeliveryAuditKey{}).(*autopilotDeliveryAudit)
	if !ok || audit == nil || owner == "" || count < 0 {
		return
	}
	if audit.owners[owner] == nil {
		audit.owners[owner] = map[string]int{}
	}
	audit.owners[owner][reason] += count
}

func autopilotDeliveryAuditCacheKey(owner, topicKey string, direct bool) string {
	mode := "all"
	if direct {
		mode = "direct"
	}
	return "autopilot_delivery_audit:" + mode + ":" + sha1Short(owner+"\x00"+topicKey, 40)
}

func (a *App) saveAutopilotDeliveryAudit(ctx context.Context, job *autopilotJob, direct bool, audit *autopilotDeliveryAudit, completed bool) {
	ctx = contextWithAutopilotLease(ctx, job)
	for owner, counters := range audit.owners {
		a.CacheSet(ctx, autopilotDeliveryAuditCacheKey(owner, job.TopicKey, direct),
			jx.M{"at": time.Now().Unix(), "completed": completed, "counters": counters}, 48*time.Hour)
	}
}

// Inventory describes saved results, not the owner's usable supply. Distinguish
// inactive source states, stale assessment generations, and delivery filters
// without publishing another owner's settings, account ID or post contents.
func (u *U) autopilotDeliveryStatus(topic string) jx.M {
	key := autopilotTopicKey(topic)
	out := jx.M{}
	var audit jx.M
	if u.CacheGet(u.Ctx, autopilotDeliveryAuditCacheKey(u.ID, key, false), &audit) {
		out["last_all_attempt"] = audit
	}
	audit = nil
	if u.CacheGet(u.Ctx, autopilotDeliveryAuditCacheKey(u.ID, key, true), &audit) {
		out["last_direct_attempt"] = audit
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT COALESCE(s.state,'missing'),count(*),
		count(*) FILTER (WHERE r.published_at >= $3 AND r.policy_version=$4
			AND r.assessed_at >= COALESCE((s.evidence->>'full_recheck_at')::bigint,0))
		FROM source_autopilot_content_results r
		LEFT JOIN source_autopilot_sources s ON s.scope=r.scope AND s.topic_key=r.topic_key AND s.source_id=r.source_id
		WHERE r.scope=$1 AND r.topic_key=$2 GROUP BY s.state`,
		autopilotAutoScope, key, time.Now().Add(-time.Duration(config.ContentMaxAgeDays)*24*time.Hour).Unix(), TopicProfileVersion)
	if err != nil {
		out["inventory_unavailable"] = true
		return out
	}
	defer rows.Close()
	saved, current := map[string]int{}, map[string]int{}
	ready := 0
	for rows.Next() {
		var state string
		var all, fresh int
		if err := rows.Scan(&state, &all, &fresh); err != nil {
			out["inventory_unavailable"] = true
			return out
		}
		saved[state], current[state] = all, fresh
		if state == "probation" || state == "active" {
			ready += fresh
		}
	}
	if rows.Err() != nil {
		out["inventory_unavailable"] = true
		return out
	}
	out["saved_by_source_state"] = saved
	out["current_by_source_state"] = current
	out["ready_source_results"] = ready
	var direct int
	cutoff := time.Now().Add(-autopilotDirectMaxAge).Unix()
	if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT count(*) FROM source_autopilot_direct_results
		WHERE scope=$1 AND topic_key=$2 AND published_at >= $3 AND assessed_at >= $3 AND policy_version=$4`,
		autopilotAutoScope, key, cutoff, TopicProfileVersion).Scan(&direct); err == nil {
		out["ready_direct_results"] = direct
	}
	return out
}
