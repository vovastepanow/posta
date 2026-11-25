-- Repair only current public trials created by the overly broad readable-feed
-- gate. Preserve all saved posts, evidence, history and explicit overrides.
WITH last_transition AS (
  SELECT DISTINCT ON (scope,topic_key,source_id) scope,topic_key,source_id,old_state,new_state,reason,evidence
  FROM source_autopilot_decisions
  WHERE scope='public' AND old_state<>new_state
  ORDER BY scope,topic_key,source_id,id DESC
), repair AS (
  SELECT s.scope,s.topic_key,s.source_id,s.state,
    CASE WHEN d.reason='yield_confirmation_required' AND d.old_state='active' THEN 'active' ELSE 'candidate' END AS next_state
  FROM source_autopilot_sources s JOIN last_transition d USING(scope,topic_key,source_id)
  WHERE s.scope='public' AND s.state='probation' AND d.new_state='probation'
  AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE o.scope=s.scope AND o.topic_key=s.topic_key AND o.source_id=s.source_id AND o.owner_id='')
  AND (
    (d.old_state='active' AND d.reason='yield_confirmation_required' AND s.reason='yield_confirmation_required')
    OR (d.old_state='candidate' AND d.reason='yield_readable_trial'
      AND NOT EXISTS (SELECT 1 FROM source_autopilot_content_results r WHERE r.scope=s.scope AND r.topic_key=s.topic_key AND r.source_id=s.source_id AND r.policy_version=s.policy_version AND r.published_at >= extract(epoch from now())::bigint-2592000)
      AND NOT EXISTS (SELECT 1 FROM source_autopilot_direct_results r WHERE r.scope=s.scope AND r.topic_key=s.topic_key AND r.source_id=s.source_id AND r.policy_version=s.policy_version AND r.published_at >= extract(epoch from now())::bigint-172800)
      AND NOT COALESCE(d.evidence->'preliminary_post_probe' @> '{"has_eligible":true}'::jsonb,false)
      AND NOT COALESCE(
        CASE WHEN jsonb_typeof(d.evidence->'preliminary_score')='number' AND jsonb_typeof(d.evidence->'threshold')='number' AND jsonb_typeof(d.evidence->'reviewed_samples')='number'
        THEN (d.evidence->>'reviewed_samples')::numeric >= 2 AND (d.evidence->>'preliminary_score')::numeric >=
          LEAST(100,(d.evidence->>'threshold')::numeric+CASE WHEN (d.evidence->>'reviewed_samples')::numeric=2 THEN 10 ELSE 0 END)
        ELSE false END,false)
    )
  )
), changed AS (
  UPDATE source_autopilot_sources s SET state=r.next_state,resume_state='',
    reason=CASE WHEN r.next_state='active' THEN 'yield_legacy_approval_restored' ELSE 'yield_admission_unverified' END,
    next_check_at=GREATEST(s.next_check_at,extract(epoch from now())::bigint+3600),
    evidence=s.evidence || '{"yield_admission_repaired":true}'::jsonb
  FROM repair r WHERE s.scope=r.scope AND s.topic_key=r.topic_key AND s.source_id=r.source_id AND s.state=r.state
  RETURNING s.scope,s.topic_key,s.source_id,s.state,s.reason,s.policy_version
)
INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
SELECT scope,topic_key,source_id,'probation',state,reason,'{"yield_admission_repaired":true}'::jsonb,policy_version,extract(epoch from now())::bigint FROM changed;
