package posta

import (
	"context"
	"time"

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
	var already bool
	var count int
	if err := a.DB.Pool.QueryRow(ctx, `SELECT
		COALESCE(bool_or(source_id=$3),false),count(*)
		FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND evidence ? 'preliminary_post_probe'`,
		job.Scope, job.TopicKey, source).Scan(&already, &count); err != nil {
		return nil, false, err
	}
	if already || count >= autopilotPreliminaryProbeLimit {
		return nil, false, nil
	}
	u := a.User(ctx, "", nil)
	var inputs []jx.M
	var texts []string
	mediaExcluded, localExcluded := 0, 0
	for _, post := range posts {
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
	out := jx.M{"at": time.Now().Unix(), "fresh_sample": len(posts), "media_excluded": mediaExcluded,
		"local_excluded": localExcluded, "submitted": len(inputs), "has_eligible": false}
	if len(inputs) == 0 {
		out["status"] = "no_text_only_input"
		return out, true, nil
	}
	analyses, err := u.AnalyzeTextBatch(texts, job.Topic, EmptyHistory())
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
		return nil, false, budgetErr
	}
	if err != nil {
		out["status"] = "provider_failure"
		out["cause"] = autopilotAssessmentCause(err)
		return out, true, nil
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
	return out, true, nil
}
