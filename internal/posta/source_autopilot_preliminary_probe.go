package posta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

// Run a bounded counterfactual on already fetched, public text-only inputs.
// No results, state, post evidence, or delivery are changed by this probe.
// A positive verdict disproves the claim that this source has no useful posts,
// not the old classifier's accuracy on its different input and objective.
const autopilotPreliminaryProbeLimit = 30

func (a *App) autopilotProbePreliminaryRejection(ctx context.Context, job *autopilotJob, source string, posts []jx.M) (jx.M, bool, error) {
	if job.Scope != autopilotAutoScope || a.LLM == nil || autopilotBudgetError(ctx) != nil {
		return nil, false, nil
	}
	var already, scheduled bool
	var reserved, unreserved, pending int
	if err := a.DB.Pool.QueryRow(ctx, `SELECT
		COALESCE(bool_or(source_id=$3 AND evidence ? 'preliminary_post_probe'),false),
		COALESCE(bool_or(source_id=$3 AND evidence ? 'preliminary_probe_scheduled'),false),
		count(*) FILTER (WHERE evidence ? 'preliminary_post_probe' OR evidence ? 'preliminary_probe_scheduled'),
		count(*) FILTER (WHERE evidence ? 'preliminary_post_probe' AND NOT (evidence ? 'preliminary_probe_scheduled')),
		count(*) FILTER (WHERE state IN ('candidate','rejected') AND next_check_at <= $4
			AND evidence ? 'preliminary_probe_scheduled' AND NOT (evidence ? 'preliminary_post_probe')
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE o.scope=$1 AND o.topic_key=$2
				AND o.owner_id='' AND o.source_id=source_autopilot_sources.source_id AND o.decision='excluded'))
		FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`,
		job.Scope, job.TopicKey, source, time.Now().Unix()).Scan(&already, &scheduled, &reserved, &unreserved, &pending); err != nil {
		return nil, false, err
	}
	// Leave room for historic rejected sources, and do not let an ordinary
	// candidate consume the only probe slot while a scheduled one is due.
	if already || (!scheduled && (reserved >= autopilotPreliminaryProbeLimit || unreserved >= 10 || pending > 0)) {
		return nil, false, nil
	}
	out, err := a.autopilotProbeTextSample(ctx, job, source, posts)
	return out, err == nil, err
}

// Assess public text without changing source state or delivery. Reused by
// the historical rejection probe and the independent publisher control panel.
func (a *App) autopilotProbeTextSample(ctx context.Context, job *autopilotJob, source string, posts []jx.M) (jx.M, error) {
	u := a.User(ctx, "", nil)
	var inputs []jx.M
	var texts []string
	mediaExcluded, localExcluded := 0, 0
	for _, post := range posts {
		if alternative := autopilotTextArticleAlternative(source, post, u.TopicsAcceptText([]string{job.Topic})); alternative != nil {
			post = alternative
		}
		if PostItemHasVideo(post) || ContentImageKey(post) != "" {
			mediaExcluded++
			continue
		}
		txt := AnalysisText(post)
		if txt == "" || !autopilotPublicPostURL(jx.Str(post, "url"), false) ||
			jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") || len(jx.ToStrs(post["safety"])) > 0 || a.ContentIsAdult(post) ||
			PostIsAdvertisement(post) || IsSpamContent(CleanPostAttribution(txt, post), "") {
			localExcluded++
			continue
		}
		if len(inputs) < 6 {
			inputs = append(inputs, post)
			texts = append(texts, txt)
		}
	}
	out := jx.M{"at": time.Now().Unix(), "policy_version": TopicProfileVersion, "fresh_sample": len(posts), "media_excluded": mediaExcluded,
		"local_excluded": localExcluded, "submitted": len(inputs), "has_eligible": false}
	if len(inputs) == 0 {
		out["status"] = "no_text_only_input"
		return out, nil
	}
	analyses, err := u.AnalyzeTextBatch(texts, job.Topic, EmptyHistory())
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
		return nil, budgetErr
	}
	if err != nil {
		out["status"] = "provider_failure"
		out["cause"] = autopilotAssessmentCause(err)
		return out, nil
	}
	traceCtx, trace := newAutopilotPostAudit(ctx, inputs)
	completed, eligibleCount := 0, 0
	for i, post := range inputs {
		entry := autopilotPostAuditEntry(traceCtx, post)
		entry["sent_text_runes"] = len([]rune(textAnalysisRecord(texts[i])))
		entry["outcome"] = "text_model_pending"
		if i >= len(analyses) || analyses[i] == nil || jx.Bool(analyses[i], "tags_classification_pending") {
			continue
		}
		analysis := analyses[i]
		ad, ok := analysis["advertising"].(bool)
		if !ok {
			continue
		}
		threshold := u.LLM.ThresholdForModel(jx.Str(analysis, "model"), u.TInt("relevance_threshold"))
		relevant := int(jx.Int(analysis, "relevance")) >= threshold && analysis["topic_match"] != false
		reason := u.contentAdmissionReason(analysis, post, job.Topic, texts[i])
		eligible := reason == ""
		autopilotPostAuditVerdict(entry, analysis, relevant, eligible, ad, threshold, reason)
		completed++
		if eligible {
			eligibleCount++
		}
	}
	out["status"] = "completed_or_partial"
	out["completed"], out["eligible"] = completed, eligibleCount
	out["has_eligible"] = eligibleCount > 0
	autopilotAttachPostAudit(out, trace, time.Now())
	return out, nil
}

// Reopen a small, stable cohort of historical thematic rejections, not
// technical failures or excluded publishers. Schedule rather than approve.
// While the strict policy is selected this enables counterfactual diagnosis
// and, with the limited pilot enabled, a normal trial on a positive probe.
func (a *App) autopilotSchedulePreliminaryProbe(ctx context.Context, job *autopilotJob, yield bool) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope || a.LLM == nil {
		return nil
	}
	at := time.Now().UTC()
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH quota AS (
			SELECT LEAST(5,GREATEST(0,30-count(*)::int)) AS available
			FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2
			AND (evidence ? 'preliminary_post_probe' OR evidence ? 'preliminary_probe_scheduled')
		), daily AS (
			SELECT GREATEST(0,5-count(*)::int) AS available FROM source_autopilot_decisions
			WHERE scope=$1 AND topic_key=$2 AND reason='preliminary_probe_scheduled' AND at >= $4
		), candidates AS (
			SELECT s.source_id FROM source_autopilot_sources s
			WHERE s.scope=$1 AND s.topic_key=$2 AND s.state='rejected'
			AND (s.reason='preliminary_topic_mismatch' OR ($6 AND s.reason IN ('observed_irrelevant','observed_low_yield')))
			AND NOT (s.evidence ? 'preliminary_post_probe') AND NOT (s.evidence ? 'preliminary_probe_scheduled')
			AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o
				WHERE o.scope=s.scope AND o.topic_key=s.topic_key AND o.source_id=s.source_id
				AND o.owner_id='' AND o.decision='excluded')
			ORDER BY md5(s.source_id),s.source_id
			LIMIT (SELECT LEAST(quota.available,daily.available) FROM quota,daily)
			FOR UPDATE OF s SKIP LOCKED
		), scheduled AS (
			UPDATE source_autopilot_sources s SET next_check_at=$3,
			evidence=s.evidence || '{"preliminary_probe_scheduled":true}'::jsonb
			FROM candidates c WHERE s.scope=$1 AND s.topic_key=$2 AND s.source_id=c.source_id
			RETURNING s.source_id
		)
		INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
		SELECT $1,$2,source_id,'rejected','rejected','preliminary_probe_scheduled',
		'{"preliminary_probe_scheduled":true}'::jsonb,$5,$3 FROM scheduled`,
			job.Scope, job.TopicKey, at.Unix(), at.Truncate(24*time.Hour).Unix(), TopicProfileVersion, yield)
		return err
	})
}
