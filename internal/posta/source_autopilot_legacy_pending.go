package posta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
)

// Every legacy suggestion visible to the user must get a definite route:
// supported candidates enter normal assessment, while unsupported/malformed
 // ones get an auditable terminal review outcome. Neither is an approval.
type autopilotLegacyPendingRoute struct {
 source string
 reason string // empty means eligible for the normal candidate worker
}

// Use the same topic equivalence as the manual shelf. User votes, existing
// approvals and source blocks remain final. Never mutate the catalog here.
func (u *U) autopilotLegacyPendingRoutes(job *autopilotJob) []autopilotLegacyPendingRoute {
 if job == nil || !validAutopilotPublicJob(job) {
  return nil
 }
 d := u.Catalog()
 decided := u.decidedSourceKeys(d.Votes, job.Topic)
 approved := map[string]bool{}
 for _, kind := range CatalogKinds {
  list := d.List(kind.Kind)
  if list == nil {
   continue
  }
  for _, entry := range *list {
   if u.CatalogEntryMatchesTopic(entry, job.Topic, true) {
    approved[recommendationSourceKey(SourceIDFor(kind.Platform, jx.Str(entry, "id")))] = true
   }
  }
 }
 var routes []autopilotLegacyPendingRoute
 seen := map[string]bool{}
 for _, item := range d.Suggestions {
  topic := jx.Str(item, "topic")
  if topic == "" || !u.TopicsAreEquivalent(topic, job.Topic) {
   continue
  }
  raw := jx.Str(item, "source_id")
  if raw == "" || len(raw) > 4096 {
   continue
  }
  identity := recommendationSourceKey(raw)
  if seen[identity] || recommendationExcluded(decided, identity) || approved[identity] ||
    u.UASourceBlocked(raw, jx.M{"title": jx.Str(item, "name")}) != "" {
   continue
  }
  // An untrusted catalog identity must not be accepted as a valid source.
  // Keep its original ID for the audit record and manual-shelf handoff.
  candidate := DiscoveryCandidate{Platform: jx.Str(item, "platform"), ID: jx.Str(item, "id")}
  sid, valid := autopilotCandidateID(candidate)
  reason := ""
  if !valid {
   reason = "unsupported_source_format"
  } else if sid != raw || recommendationSourceKey(sid) != identity {
   reason = "invalid_source_identity"
  }
  seen[identity] = true
  routes = append(routes, autopilotLegacyPendingRoute{source: raw, reason: reason})
 }
 return routes
}

// Keep the original eligible-candidate interface for the leased importer.
func (u *U) autopilotLegacyPendingCandidates(job *autopilotJob) []string {
 var out []string
 for _, route := range u.autopilotLegacyPendingRoutes(job) {
  if route.reason == "" {
   out = append(out, route.source)
  }
 }
 return out
}

// Transfer up to limit new suggestions per run. Previously imported sources
// are never reset, and unavailable/invalid/unsupported sources remain on the
// old shelf rather than disappearing without an auditable decision.
func (a *App) autopilotImportLegacyPending(ctx context.Context, job *autopilotJob, limit int) (int, error) {
	if limit < 1 || !validAutopilotPublicJob(job) {
		return 0, nil
	}
	u := a.User(ctx, "", nil)
	sources := u.autopilotLegacyPendingCandidates(job)
	if len(sources) == 0 {
		return 0, nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=ANY($3)`, job.Scope, job.TopicKey, sources)
	if err != nil {
		return 0, err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		existing[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	added := 0
	for _, source := range sources {
		if existing[source] {
			continue
		}
		if err := registerAutopilotCandidateWithOrigin(ctx, a.DB, job, source, "legacy_pending_import", time.Now()); err != nil {
			return added, err
		}
		added++
		if added >= limit {
			break
		}
	}
	return added, nil
}

// Explain any remaining item directly in the manual queue, rather than
// silently leaving it there when migration prerequisites are unmet.
// This method only reads the user's own configuration; it never starts work.
func (u *U) pendingSourceAutopilotStatus(topic string, item jx.M, mode string, paused bool) string {
	if mode != "shadow" && mode != "auto" {
		return "autopilot_off"
	}
	if paused {
		return "autopilot_paused"
	}
	if mode == "shadow" && !u.TBool("autopilot_shadow") ||
		mode == "auto" && !u.TBool("autopilot_auto") {
		return "autopilot_server_disabled"
	}
	source, ok := autopilotCandidateID(DiscoveryCandidate{
		Platform: jx.Str(item, "platform"), ID: jx.Str(item, "id"),
	})
	if !ok {
		return "unsupported_source_format"
	}
	if jx.Str(item, "source_id") == "" ||
		recommendationSourceKey(source) != recommendationSourceKey(jx.Str(item, "source_id")) {
		return "invalid_source_identity"
	}
	if !u.TopicsAreEquivalent(jx.Str(item, "suggestion_topic"), topic) {
		return "topic_mismatch"
	}
	return "awaiting_transfer"
}

// excludeAutopilotTransferredPending keeps the historical suggestion rows
// intact for audit, while the original manual approval shelf displays only
// sources not already handed over to a public autopilot topic queue.
func (u *U) excludeAutopilotTransferredPending(candidates []jx.M, topic string) ([]jx.M, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	ids := make([]string, 0, len(candidates))
	for _, item := range candidates {
		if id := jx.Str(item, "source_id"); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return candidates, nil
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT DISTINCT source_id FROM source_autopilot_sources
		WHERE topic_key=$1 AND scope IN ('public','public:shadow') AND source_id=ANY($2)`,
		autopilotTopicKey(topic), ids)
	if err != nil {
		return nil, err
	}
	transferred := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		transferred[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]jx.M, 0, len(candidates))
	for _, item := range candidates {
		if !transferred[jx.Str(item, "source_id")] {
			out = append(out, item)
		}
	}
	return out, nil
}

// reconcileLegacyPendingWithoutLease runs during source-autopilot scheduling,
// even when the job itself is backed off or waiting for an AI budget.
// This only inserts new, unreviewed candidates; all actual assessments still
// require a normal fenced worker lease and resource budget.
func (a *App) reconcileLegacyPendingWithoutLease(ctx context.Context, mode, scope, kind string, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT j.topic_key,j.topic FROM source_autopilot_jobs j
		WHERE j.scope=$1 AND j.kind=$2 AND j.enabled AND j.demand_active
		AND EXISTS (SELECT 1 FROM source_autopilot_demands d WHERE d.topic_key=j.topic_key AND d.mode=$3 AND NOT d.paused)
		ORDER BY j.topic_key LIMIT 100`, scope, kind, mode)
	if err != nil {
		return 0, err
	}
	type target struct{ key, topic string }
	var jobs []target
	for rows.Next() {
		var j target
		if err := rows.Scan(&j.key, &j.topic); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	user := a.User(ctx, "", nil)
	added := 0
	for _, target := range jobs {
		job := &autopilotJob{Scope: scope, Kind: kind, TopicKey: target.key, Topic: target.topic}
		for _, route := range user.autopilotLegacyPendingRoutes(job) {
			if ctx.Err() != nil {
				return added, ctx.Err()
			}
			// Unreviewable sources must not enter the candidate loop: that loop
			// intentionally skips unsupported platforms and would stall forever.
			// Record a durable rejection (format/identity, not content quality)
			// and preserve a visible audit trail in the autopilot explorer.
			state, reason := "candidate", "legacy_pending_import"
			var checkedAt, nextCheckAt int64
			if route.reason != "" {
				state, reason = "rejected", route.reason
				checkedAt = time.Now().Unix()
				nextCheckAt = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
			}
			var inserted bool
			err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
				if err := lockAutopilotSource(ctx, tx, scope, target.key, route.source); err != nil {
					return err
				}
				now := time.Now().Unix()
				err := tx.QueryRow(ctx, `WITH fresh AS (
					INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,checked_at,next_check_at)
					SELECT $1,$2,$3,$8,$9,$6,$7,$10,$11
					WHERE EXISTS (SELECT 1 FROM source_autopilot_jobs j WHERE j.scope=$1 AND j.topic_key=$2 AND j.kind=$4 AND j.enabled AND j.demand_active)
					AND EXISTS (SELECT 1 FROM source_autopilot_demands d WHERE d.topic_key=$2 AND d.mode=$5 AND NOT d.paused AND ($5<>'auto' OR d.posts_per_day>0))
					AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o
						WHERE (o.scope=$1 OR ($1='public:shadow' AND o.scope='public')) AND o.owner_id='' AND o.topic_key=$2 AND o.source_id=$3 AND o.decision='excluded')
					ON CONFLICT DO NOTHING RETURNING scope,topic_key,source_id,state,reason
				), audit AS (
					INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at)
					SELECT scope,topic_key,source_id,'',state,reason,$6,$7 FROM fresh
					RETURNING id
				)
				SELECT EXISTS(SELECT 1 FROM audit)`,
					scope, target.key, route.source, kind, mode, TopicProfileVersion, now, state, reason, checkedAt, nextCheckAt).Scan(&inserted)
				if err != nil || !inserted {
					return err
				}
				// Only runnable candidates advance the next check; a rejected
				// unsupported source must not create a hot retry loop.
				if state == "candidate" {
					_, err = tx.Exec(ctx, `UPDATE source_autopilot_jobs
						SET next_run_at=LEAST(next_run_at,$4)
						WHERE scope=$1 AND topic_key=$2 AND kind=$3 AND enabled AND demand_active
						AND last_error=''`, scope, target.key, kind, now+5)
				}
				return err
			})
			if err != nil {
				return added, err
			}
			if inserted {
				added++
			}
			if added >= limit {
				return added, nil
			}
		}
	}
	return added, nil
}
