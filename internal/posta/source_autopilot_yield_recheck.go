package posta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Reopen legacy thematic decisions in a bounded cohort. Never reset the
// whole queue, clear previous post assessments, or bypass an exclusion.
// Previously active sources are checked first; other sources are scheduled
// at most 20 per UTC day and five per cycle. No new model or fetch call here.
func (a *App) autopilotScheduleYieldRecheck(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope {
		return nil
	}
	at := time.Now().UTC()
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH daily AS (
			SELECT GREATEST(0,20-count(*)::int) AS available
			FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2
			AND reason='yield_recheck_scheduled' AND at >= $4
		), eligible AS (
			SELECT s.source_id,s.state,CASE
				WHEN s.state='active' THEN 0
				WHEN EXISTS (SELECT 1 FROM source_autopilot_content_results r WHERE r.scope=s.scope AND r.topic_key=s.topic_key AND r.source_id=s.source_id) THEN 1
				WHEN s.state='candidate' THEN 2
				WHEN s.reason='sparse_publisher' THEN 3
				ELSE 4 END AS priority
			FROM source_autopilot_sources s
			WHERE s.scope=$1 AND s.topic_key=$2
			AND NOT (s.evidence ? 'yield_recheck_scheduled')
			AND (
				(s.state='active' AND COALESCE(s.evidence->>'source_decision_policy','') NOT IN ('yield_v2','yield_pilot','strict_v1_live_guard'))
				OR (s.state='candidate' AND s.reason='assessment_unavailable')
				OR (s.state='rejected' AND s.reason IN ('preliminary_topic_mismatch','observed_irrelevant','observed_low_yield'))
				OR (s.state='cooldown' AND s.reason IN ('sparse_publisher','preliminary_match_uncertain'))
			)
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE o.scope=s.scope AND o.topic_key=s.topic_key AND o.source_id=s.source_id AND o.owner_id='' AND o.decision='excluded')
			ORDER BY priority,s.checked_at,s.source_id
			LIMIT (SELECT LEAST(5,available) FROM daily)
			FOR UPDATE OF s SKIP LOCKED
		), scheduled AS (
			UPDATE source_autopilot_sources s SET next_check_at=LEAST(s.next_check_at,$3),
			evidence=s.evidence || '{"yield_recheck_scheduled":true}'::jsonb
			FROM eligible e WHERE s.scope=$1 AND s.topic_key=$2 AND s.source_id=e.source_id
			RETURNING s.source_id,s.state
		)
		INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
		SELECT $1,$2,source_id,state,state,'yield_recheck_scheduled','{"yield_recheck_scheduled":true}'::jsonb,$5,$3 FROM scheduled`,
			job.Scope, job.TopicKey, at.Unix(), at.Truncate(24*time.Hour).Unix(), TopicProfileVersion)
		return err
	})
}
