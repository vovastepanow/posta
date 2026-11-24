package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// AutopilotAuditFunnel is a read-only snapshot of retained evidence, not
// independent ground truth. Stages are mutually exclusive per source; all
// other counts are observations with explicitly different denominators.
// No URL, post text, model reply, owner identifier or secret is returned.
func AutopilotAuditFunnel(ctx context.Context, q db.Querier, scope, key string, at time.Time) (jx.M, error) {
	if (scope != autopilotAutoScope && scope != autopilotShadowScope) || key == "" || at.IsZero() {
		return nil, fmt.Errorf("invalid public audit scope, topic or time")
	}
	var raw []byte
	err := q.QueryRow(ctx, `WITH sources AS (
		SELECT source_id,state,reason,checked_at,first_seen_at,
			CASE WHEN evidence->>'discovery_origin' IN ('verified_post','post_nomination','directory','citation')
				THEN evidence->>'discovery_origin' ELSE 'unknown' END AS discovery_origin
		FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2
	), passes AS (
		SELECT source_id,count(*) AS n,sum(fetched_count) AS items
		FROM source_autopilot_collection_passes WHERE scope=$1 AND topic_key=$2 AND fetched_at <= $3 GROUP BY source_id
	), latest AS (
		SELECT DISTINCT ON (source_id,post_hash) source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising
		FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND observed_at <= $3
		ORDER BY source_id,post_hash,policy_version DESC
	), verdicts AS (
		SELECT e.source_id,count(*) AS n,count(*) FILTER (WHERE relevant) AS relevant,
			count(*) FILTER (WHERE eligible) AS eligible,count(*) FILTER (WHERE advertising) AS ads,
			count(*) FILTER (WHERE eligible AND NOT EXISTS (
				SELECT 1 FROM source_autopilot_content_results c WHERE c.scope=$1 AND c.topic_key=$2
				AND c.source_id=e.source_id AND c.post_hash=e.post_hash AND c.policy_version=e.policy_version AND c.assessed_at <= $3
			)) AS eligible_without_matching_result,
			count(DISTINCT content_hash) FILTER (WHERE eligible AND content_hash<>'' AND published_at >= $4 AND published_at <= $3) AS useful_unique_30d,
			count(*) FILTER (WHERE published_at >= $4 AND published_at <= $3) AS evaluated_30d
		FROM latest e GROUP BY e.source_id
	), results AS (
		SELECT source_id,count(*) AS n FROM source_autopilot_content_results
		WHERE scope=$1 AND topic_key=$2 AND assessed_at <= $3 GROUP BY source_id
	), history AS (
		SELECT source_id,count(*) AS n,
			count(*) FILTER (WHERE old_state<>'' AND old_state<>new_state) AS changes,
			bool_or(new_state IN ('probation','active') OR old_state IN ('probation','active')) AS trial
		FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2 AND at <= $3 GROUP BY source_id
	), per_source AS (
		SELECT s.*,COALESCE(p.n,0) AS passes,COALESCE(p.items,0) AS fetched_items,
			COALESCE(v.n,0) AS evaluated,COALESCE(v.relevant,0) AS relevant,COALESCE(v.eligible,0) AS eligible,
			COALESCE(v.ads,0) AS ads,COALESCE(v.eligible_without_matching_result,0) AS missing,
			COALESCE(v.useful_unique_30d,0) AS useful_unique_30d,COALESCE(v.evaluated_30d,0) AS evaluated_30d,
			COALESCE(r.n,0) AS saved,COALESCE(h.n,0) AS decisions,COALESCE(h.changes,0) AS changes,
			(s.state IN ('probation','active') OR COALESCE(h.trial,false)) AS trial,
			CASE WHEN COALESCE(r.n,0)>0 THEN 'saved_result'
				WHEN COALESCE(v.eligible,0)>0 AND $1='public:shadow' THEN 'eligible_shadow_not_delivered'
				WHEN COALESCE(v.eligible,0)>0 THEN 'eligible_no_saved_result'
				WHEN COALESCE(v.n,0)>0 THEN 'assessed_no_eligible'
				WHEN COALESCE(p.n,0)>0 THEN 'fetched_no_post_verdict'
				WHEN s.checked_at>0 THEN 'checked_no_recorded_fetch_or_verdict'
				ELSE 'unchecked_no_recorded_evidence' END AS stage
		FROM sources s LEFT JOIN passes p USING(source_id) LEFT JOIN verdicts v USING(source_id)
		LEFT JOIN results r USING(source_id) LEFT JOIN history h USING(source_id)
	), summary AS (
		SELECT count(*) AS sources,count(*) FILTER (WHERE checked_at>0) AS checked_sources,
			count(*) FILTER (WHERE passes>0) AS sources_with_fetch,
			count(*) FILTER (WHERE trial) AS sources_with_recorded_trial,
			count(*) FILTER (WHERE evaluated>0) AS sources_with_post_verdict,
			count(*) FILTER (WHERE eligible>0) AS sources_with_eligible,
			count(*) FILTER (WHERE saved>0) AS sources_with_saved_result,
			COALESCE(sum(passes),0) AS successful_passes,COALESCE(sum(fetched_items),0) AS fetched_items_including_repeats,
			COALESCE(sum(evaluated),0) AS distinct_source_post_verdicts,COALESCE(sum(relevant),0) AS relevant_verdicts,
			COALESCE(sum(eligible),0) AS eligible_verdicts,COALESCE(sum(ads),0) AS advertising_verdicts,
			COALESCE(sum(missing),0) AS eligible_without_matching_saved_version,
			COALESCE(sum(saved),0) AS saved_results,COALESCE(sum(decisions),0) AS decision_records,
			COALESCE(sum(changes),0) AS actual_state_changes,COALESCE(sum(evaluated_30d),0) AS evaluated_posts_published_30d,
			COALESCE(sum(useful_unique_30d),0) AS useful_unique_per_source_30d
		FROM per_source
	)
	SELECT to_jsonb(summary) || jsonb_build_object(
		'stages',COALESCE((SELECT jsonb_object_agg(stage,n) FROM (SELECT stage,count(*) AS n FROM per_source GROUP BY stage) a),'{}'::jsonb),
		'stage_state_reason',COALESCE((SELECT jsonb_agg(to_jsonb(b) ORDER BY stage,state,reason) FROM
			(SELECT stage,state,reason,count(*) AS sources FROM per_source GROUP BY stage,state,reason) b),'[]'::jsonb),
		'discovery_paths',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY discovery_origin) FROM (
			SELECT discovery_origin,count(*) AS sources,
				count(*) FILTER (WHERE passes>0) AS readable,
				count(*) FILTER (WHERE evaluated>0) AS assessed,
				count(*) FILTER (WHERE eligible>0) AS with_eligible,
				count(*) FILTER (WHERE state='active') AS active
			FROM per_source GROUP BY discovery_origin) p),'[]'::jsonb),
		'direct_results', (SELECT count(*) FROM source_autopilot_direct_results WHERE scope=$1 AND topic_key=$2 AND assessed_at <= $3),
		'yield_pilot_states',COALESCE((SELECT jsonb_object_agg(state,n) FROM (
			SELECT state,count(*) AS n FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2
			AND evidence @> '{"yield_pilot":true}'::jsonb GROUP BY state) p),'{}'::jsonb),
		'preliminary_post_probes', (SELECT jsonb_build_object(
			'sources',count(*),
			'sources_with_eligible',count(*) FILTER (WHERE evidence->'preliminary_post_probe' @> '{"has_eligible":true}'::jsonb),
			'no_text_only_input',count(*) FILTER (WHERE evidence->'preliminary_post_probe' @> '{"status":"no_text_only_input"}'::jsonb),
			'complete_or_partial',count(*) FILTER (WHERE evidence->'preliminary_post_probe' @> '{"status":"completed_or_partial"}'::jsonb),
			'provider_failure',count(*) FILTER (WHERE evidence->'preliminary_post_probe' @> '{"status":"provider_failure"}'::jsonb))
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND evidence ? 'preliminary_post_probe'),
		'recent_review_outcomes',COALESCE((SELECT jsonb_object_agg(outcome,n) FROM (
			SELECT t.key AS outcome,sum(CASE WHEN jsonb_typeof(t.value)='number' THEN (t.value::text)::numeric ELSE 0 END) AS n
			FROM source_autopilot_sources s CROSS JOIN LATERAL jsonb_each(
				CASE WHEN jsonb_typeof(s.evidence->'post_audit_outcomes')='object'
				THEN s.evidence->'post_audit_outcomes' ELSE '{}'::jsonb END) t
			WHERE s.scope=$1 AND s.topic_key=$2 AND
				CASE WHEN jsonb_typeof(s.evidence->'post_audit_at')='number'
				THEN (s.evidence->>'post_audit_at')::numeric BETWEEN $4 AND $3 ELSE false END
			GROUP BY t.key) o),'{}'::jsonb),
		'policy_comparison',COALESCE((SELECT jsonb_agg(to_jsonb(v) ORDER BY strict_next,yield_next) FROM (
			SELECT evidence->'yield_comparison'->>'strict_next' AS strict_next,
				evidence->'yield_comparison'->>'yield_next' AS yield_next,count(*) AS sources
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2
			AND jsonb_typeof(evidence->'yield_comparison')='object'
			AND CASE WHEN jsonb_typeof(evidence->'yield_comparison'->'at')='number'
			THEN (evidence->'yield_comparison'->>'at')::numeric BETWEEN $4 AND $3 ELSE false END
			GROUP BY 1,2) v),'[]'::jsonb)
	) FROM summary`, scope, key, at.Unix(), at.Add(-30*24*time.Hour).Unix()).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out jx.M
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["as_of"] = at.UTC().Format(time.RFC3339)
	out["evidence_scope"] = scope
	out["saved_results_applicable"] = scope == autopilotAutoScope
	out["retention_limited"] = true
	out["independent_quality_measured"] = false
	return out, nil
}
