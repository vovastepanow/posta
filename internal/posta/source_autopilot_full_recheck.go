package posta

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The parameters $1/$2/$3 always identify scope, topic and source. A new
// assessment generation does not change the topic profile version or delete
// already delivered content. Old source observations are not new evidence.
const autopilotFullRecheckEpochSQL = `COALESCE((SELECT (s.evidence->>'full_recheck_at')::bigint
	FROM source_autopilot_sources s WHERE s.scope=$1 AND s.topic_key=$2 AND s.source_id=$3),0)`

// One explicit full restart per existing public topic. The schedule and
// decision history are committed together under the topic lease. Do not
// reset the queue again when the application restarts or cron redeploys.
// Recovered aliases and global manual exclusions are not live candidates.
func (a *App) autopilotQueueFullRecheck(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) {
		return errors.New("invalid full recheck job")
	}
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		at := time.Now().Unix()
		_, err := tx.Exec(ctx, `WITH targets AS (
			SELECT s.source_id,s.state,s.reason,s.evidence,
				EXISTS(SELECT 1 FROM source_autopilot_overrides o
					WHERE (o.scope=$1 OR ($1='public:shadow' AND o.scope='public')) AND o.topic_key=$2
					AND o.source_id=s.source_id AND o.owner_id='' AND o.decision='pinned') AS pinned
			FROM source_autopilot_sources s
			WHERE s.scope=$1 AND s.topic_key=$2 AND s.reason<>'source_identity_recovered'
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_decisions d WHERE d.scope=$1 AND d.topic_key=$2
				AND d.reason='full_recheck_v4_scheduled')
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o
				WHERE (o.scope=$1 OR ($1='public:shadow' AND o.scope='public')) AND o.topic_key=$2
				AND o.source_id=s.source_id AND o.owner_id='' AND o.decision='excluded')
			FOR UPDATE OF s
		), reset AS (
			UPDATE source_autopilot_sources s SET
				state=CASE WHEN t.pinned OR (t.state='cooldown' AND t.reason='rate_limited') THEN t.state ELSE 'candidate' END,
				resume_state=CASE WHEN t.state='cooldown' AND t.reason='rate_limited' AND NOT t.pinned THEN 'candidate'
					WHEN t.pinned THEN s.resume_state ELSE '' END,
				reason=CASE WHEN t.reason='rate_limited' THEN t.reason ELSE 'full_recheck_v4_scheduled' END,
				checked_at=0,
				next_check_at=CASE WHEN t.reason='rate_limited' THEN s.next_check_at ELSE $3 END,
				evidence=(s.evidence - ARRAY['review_policy_v3_checked','preliminary_post_probe','preliminary_probe_scheduled',
					'yield_pilot','yield_comparison','post_audit','post_audit_at','post_audit_outcomes','post_audit_kinds','post_audit_total'])
					|| jsonb_build_object('full_recheck_at',$3::bigint,'review_policy_v3_scheduled',true)
			FROM targets t WHERE s.scope=$1 AND s.topic_key=$2 AND s.source_id=t.source_id
			RETURNING s.source_id,s.state,t.state AS old_state,t.reason AS old_reason
		)
		INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
		SELECT $1,$2,source_id,old_state,state,'full_recheck_v4_scheduled',
			jsonb_build_object('previous_reason',old_reason,'full_recheck_at',$3::bigint),$4,$3 FROM reset`,
			job.Scope, job.TopicKey, at, TopicProfileVersion)
		return err
	})
}
