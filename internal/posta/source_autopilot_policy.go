package posta

import "math"

// sourceEvidence contains only completed content evaluations. Network and
// model failures are tracked separately and never enter the denominator.
type sourceEvidence struct {
	Evaluated, Relevant, FreshUnique, CuratedUnique, SuccessfulPasses int
	Advertising, Duplicates, SameSourceDuplicates                      int
	TechnicalFailure                                                  bool
	Eligible                                                          int
	RequireEligible                                                   bool
}

type sourcePolicy struct {
	MinEvaluated, MinPasses, MinFreshUnique int
	PromoteRate, DemoteRate                 float64
	MaxAdvertisingRate, MaxDuplicateRate    float64
	YieldEnabled, YieldPilotEnabled         bool
	YieldMinLive                            int
}

func (a *App) autopilotSourcePolicy() sourcePolicy {
	return sourcePolicy{MinEvaluated: a.TInt("autopilot_min_evaluations"), MinPasses: a.TInt("autopilot_min_passes"), MinFreshUnique: a.TInt("autopilot_min_unique"),
		PromoteRate: float64(a.TInt("autopilot_promote_percent")) / 100, DemoteRate: float64(a.TInt("autopilot_demote_percent")) / 100,
		MaxAdvertisingRate: float64(a.TInt("autopilot_max_ads_percent")) / 100, MaxDuplicateRate: float64(a.TInt("autopilot_max_duplicates_percent")) / 100,
		YieldEnabled: a.TBool("autopilot_yield_policy"), YieldPilotEnabled: a.TBool("autopilot_yield_pilot"), YieldMinLive: a.TInt("autopilot_yield_min_live")}
}

// Professional sources must produce posts that survive the full admission
// pipeline, not just posts containing the topic's keywords. Other topics
// retain their existing relevance-based policy.
func effectiveSourceRelevant(relevant, eligible int, professional bool) int {
	if !professional {
		return relevant
	}
	return min(relevant, max(0, eligible))
}

// decideSourceState uses different promotion and demotion thresholds. Unknown
// or insufficient evidence retains the current state.
func decideSourceState(state string, e sourceEvidence, p sourcePolicy) (string, string) {
	if e.TechnicalFailure {
		return state, "access_unavailable"
	}
	if p.MinEvaluated <= 0 || p.MinPasses <= 0 || p.MinFreshUnique <= 0 || math.IsNaN(p.PromoteRate) || math.IsNaN(p.DemoteRate) || math.IsNaN(p.MaxAdvertisingRate) || math.IsNaN(p.MaxDuplicateRate) || p.DemoteRate < 0 || p.PromoteRate > 1 || p.DemoteRate >= p.PromoteRate || p.MaxAdvertisingRate < 0 || p.MaxAdvertisingRate > 1 || p.MaxDuplicateRate < 0 || p.MaxDuplicateRate > 1 || e.Evaluated < 0 || e.Relevant < 0 || e.Relevant > e.Evaluated || e.FreshUnique < 0 || e.FreshUnique > e.Evaluated || e.CuratedUnique < 0 || e.CuratedUnique > e.Evaluated || e.SuccessfulPasses < 0 || e.Advertising < 0 || e.Advertising > e.Evaluated || e.Duplicates < 0 || e.Duplicates > e.Evaluated || e.SameSourceDuplicates < 0 || e.SameSourceDuplicates > e.Duplicates {
		return state, "invalid_evidence_or_policy"
	}
	if e.Evaluated < p.MinEvaluated || e.SuccessfulPasses < p.MinPasses {
		return state, "insufficient_evidence"
	}
	if e.RequireEligible && (e.Eligible < 0 || e.Eligible > e.Evaluated) {
		return state, "invalid_evidence_or_policy"
	}
	topicRate := float64(e.Relevant) / float64(e.Evaluated)
	rate := float64(effectiveSourceRelevant(e.Relevant, e.Eligible, e.RequireEligible)) / float64(e.Evaluated)
	excessAds := float64(e.Advertising)/float64(e.Evaluated) > p.MaxAdvertisingRate
	// Sharing a valuable post is not evidence of authorship. Yet a publisher
	// consistently selecting diverse, independently assessed useful posts
	// is a valuable curator. Require the same relevance, minimum sample,
	// multi-pass observation and advertising protections as original sources.
	// A single viral repost repeated many times earns no additional credit.
	valuableCuration := e.CuratedUnique >= p.MinFreshUnique &&
		rate >= p.PromoteRate && !excessAds
	// Curation credit never excuses a publisher repeating its own posts.
	excessSelfRepeats := float64(e.SameSourceDuplicates)/float64(e.Evaluated) > p.MaxDuplicateRate
	poorQuality := excessAds || excessSelfRepeats || (float64(e.Duplicates)/float64(e.Evaluated) > p.MaxDuplicateRate && !valuableCuration)
	switch state {
	case "probation":
		if !poorQuality && rate >= p.PromoteRate && e.FreshUnique >= p.MinFreshUnique {
			return "active", "observed_useful"
		}
		if !poorQuality && valuableCuration {
			return "active", "observed_useful_curation"
		}
		if poorQuality {
			return "rejected", "observed_ad_or_duplicates"
		}
		if topicRate < p.DemoteRate {
			return "rejected", "observed_irrelevant"
		}
		if rate < p.DemoteRate {
			return "rejected", "observed_low_yield"
		}
	case "active":
		if rate < p.DemoteRate || poorQuality {
			return "probation", "quality_drift"
		}
	}
	return state, "retain"
}
