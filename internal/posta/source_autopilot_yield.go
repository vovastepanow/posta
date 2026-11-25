package posta

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// Yield compares useful unique publications in a fixed publication-time
// window, not thematic purity. Shared content receives curation credit,
// but the same content hash is counted only once within a source.
type sourceYieldEvidence struct {
	Evidence                              sourceEvidence
	UsefulUnique, LiveUnique, TrialPasses int
	TrialStartedAt                        int64
	CompleteSample                        bool
}

func autopilotYieldEvidenceWindow(ctx context.Context, q db.Querier, scope, key, source string, since, until time.Time) (sourceYieldEvidence, error) {
	var e sourceYieldEvidence
	if scope == "" || key == "" || source == "" || !since.Before(until) {
		return e, fmt.Errorf("invalid yield evidence window")
	}
	// A cooldown resume is not a new trial. A real re-entry from candidate
	// or demotion from active requires renewed evidence.
	err := q.QueryRow(ctx, `WITH trial AS (
		SELECT COALESCE(max(at),0) AS started FROM source_autopilot_decisions
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND old_state IN ('candidate','active')
		AND new_state='probation' AND at < $5 AND at >= `+autopilotFullRecheckEpochSQL+`
	), latest AS (
		SELECT DISTINCT ON (post_hash) post_hash,content_hash,published_at,observed_at,eligible
		FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND observed_at < $5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`
		ORDER BY post_hash,policy_version DESC
	), recent AS (
		SELECT * FROM latest WHERE published_at >= $4 AND published_at < $5
		AND eligible AND content_hash<>''
	)
	SELECT (SELECT count(DISTINCT content_hash) FROM recent),
		(SELECT count(DISTINCT r.content_hash) FROM recent r WHERE trial.started>0 AND r.published_at>trial.started AND r.observed_at>=trial.started
			AND NOT EXISTS (SELECT 1 FROM latest prior WHERE prior.content_hash=r.content_hash
				AND (prior.published_at<=trial.started OR prior.observed_at<trial.started))),
		(SELECT count(*) FROM source_autopilot_collection_passes WHERE scope=$1 AND topic_key=$2 AND source_id=$3
			AND trial.started>0 AND fetched_at>=GREATEST(trial.started,$4) AND fetched_at<$5),trial.started
	FROM trial`, scope, key, source, since.Unix(), until.Unix()).Scan(&e.UsefulUnique, &e.LiveUnique, &e.TrialPasses, &e.TrialStartedAt)
	return e, err
}

// The initial candidate parameters reuse existing minimum useful count,
// pass count and ad/repeat limits. They are provisional until an independent
// benchmark calibrates them. Zero useful content is a negative only after a
// complete current sample and enough real post evaluations. Unknown input
// never becomes a negative label. A positive trial needs a newly published
// eligible item, not just repeated reads of its archive.
func autopilotLiveTrialConfirmed(y sourceYieldEvidence, p sourcePolicy) bool {
	return p.MinPasses >= 2 && p.MinFreshUnique > 0 && p.YieldMinLive > 0 &&
		y.TrialStartedAt > 0 && y.TrialPasses >= p.MinPasses &&
		y.UsefulUnique >= p.MinFreshUnique && y.LiveUnique >= p.YieldMinLive &&
		y.LiveUnique <= y.UsefulUnique && y.TrialPasses <= y.Evidence.SuccessfulPasses &&
		y.UsefulUnique <= y.Evidence.Eligible
}

// New approvals need live evidence. Existing approvals are not revoked
// merely because the policy changed; observed deterioration is separate.
func autopilotGuardLiveTrial(state, next, reason string, legacyActive bool, y sourceYieldEvidence, p sourcePolicy) (string, string) {
	if !p.RequireLiveTrial || next != "active" || autopilotLiveTrialConfirmed(y, p) {
		return next, reason
	}
	if state == "probation" {
		return "probation", "yield_confirmation_required"
	}
	return next, reason
}

func decideSourceYieldState(state string, y sourceYieldEvidence, p sourcePolicy) (string, string) {
	e := y.Evidence
	if e.TechnicalFailure {
		return state, "access_unavailable"
	}
	if p.MinEvaluated < 1 || p.MinPasses < 1 || p.MinFreshUnique < 1 || p.YieldMinLive < 1 ||
		math.IsNaN(p.MaxAdvertisingRate) || p.MaxAdvertisingRate < 0 || p.MaxAdvertisingRate > 1 ||
		math.IsNaN(p.MaxDuplicateRate) || p.MaxDuplicateRate < 0 || p.MaxDuplicateRate > 1 ||
		e.Evaluated < 0 || e.Eligible < 0 || e.Eligible > e.Evaluated || e.Relevant < e.Eligible || e.Relevant > e.Evaluated ||
		e.Advertising < 0 || e.Advertising > e.Evaluated || e.SameSourceDuplicates < 0 || e.SameSourceDuplicates > e.Evaluated ||
		e.SuccessfulPasses < 0 || y.UsefulUnique < 0 || y.UsefulUnique > e.Eligible || y.LiveUnique < 0 || y.LiveUnique > y.UsefulUnique ||
		y.TrialPasses < 0 || y.TrialPasses > e.SuccessfulPasses || y.TrialStartedAt < 0 {
		return state, "invalid_yield_evidence_or_policy"
	}
	excess := false
	if e.Evaluated > 0 {
		excess = float64(e.Advertising)/float64(e.Evaluated) > p.MaxAdvertisingRate ||
			float64(e.SameSourceDuplicates)/float64(e.Evaluated) > p.MaxDuplicateRate
	}
	negativeConfirmed := y.CompleteSample && e.Evaluated >= p.MinEvaluated && e.SuccessfulPasses >= p.MinPasses
	switch state {
	case "probation":
		if !excess && autopilotLiveTrialConfirmed(y, p) {
			return "active", "yield_confirmed"
		}
		if negativeConfirmed && excess {
			return "rejected", "yield_ad_or_self_repeats"
		}
		if negativeConfirmed && y.UsefulUnique == 0 {
			return "rejected", "yield_no_useful_posts"
		}
		return state, "yield_observing"
	case "active":
		if negativeConfirmed && (excess || y.UsefulUnique == 0) {
			return "probation", "yield_declined"
		}
	}
	return state, "yield_retain"
}

func autopilotPostAuditComplete(trace *autopilotPostAudit) bool {
	if trace == nil || len(trace.entries) == 0 {
		return false
	}
	for _, entry := range trace.entries {
		switch jx.Str(entry, "outcome") {
		case "already_assessed", "local_ad_or_spam", "verdict_completed":
		default:
			return false
		}
	}
	return true
}

// Adapt healthy, fully reviewed feeds to observed posting intervals. The
// caller retains ordinary retry cadence while unassessed posts remain.
func autopilotYieldPollInterval(posts []jx.M, fallback time.Duration) time.Duration {
	seen := map[int64]bool{}
	var times []int64
	for _, p := range posts {
		if at := jx.Int(p, "posted_at"); at > 0 && !seen[at] {
			seen[at] = true
			times = append(times, at)
		}
	}
	if len(times) < 2 {
		return fallback
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	intervals := make([]int64, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		intervals = append(intervals, times[i]-times[i-1])
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i] < intervals[j] })
	median := intervals[len(intervals)/2]
	// Bound seconds before multiplying into a time.Duration.
	seconds := min(median, int64((7*24*time.Hour)/time.Second))
	return max(3*time.Hour, min(7*24*time.Hour, time.Duration(seconds)*time.Second*3/2))
}
