package posta

import "testing"

func TestAutopilotSourcePolicy(t *testing.T) {
	p := sourcePolicy{MinEvaluated: 10, MinPasses: 3, MinFreshUnique: 3, PromoteRate: .8, DemoteRate: .4, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	for _, tc := range []struct {
		state        string
		e            sourceEvidence
		want, reason string
	}{
		{"probation", sourceEvidence{Evaluated: 3, Relevant: 3, FreshUnique: 3, SuccessfulPasses: 1}, "probation", "insufficient_evidence"},
		{"probation", sourceEvidence{Evaluated: 10, Relevant: 9, FreshUnique: 3, SuccessfulPasses: 3}, "active", "observed_useful"},
		{"probation", sourceEvidence{Evaluated: 10, Relevant: 9, FreshUnique: 0, SuccessfulPasses: 3}, "probation", "retain"},
		{"active", sourceEvidence{Evaluated: 10, Relevant: 2, FreshUnique: 1, SuccessfulPasses: 3}, "probation", "quality_drift"},
		{"active", sourceEvidence{Evaluated: 10, Relevant: 2, FreshUnique: 1, SuccessfulPasses: 3, TechnicalFailure: true}, "active", "access_unavailable"},
		{"probation", sourceEvidence{Evaluated: 10, Relevant: 2, FreshUnique: 1, SuccessfulPasses: 3}, "rejected", "observed_irrelevant"},
		{"probation", sourceEvidence{Evaluated: 10, Relevant: 9, FreshUnique: 3, SuccessfulPasses: 3, Advertising: 3}, "rejected", "observed_ad_or_duplicates"},
	} {
		got, reason := decideSourceState(tc.state, tc.e, p)
		if got != tc.want || reason != tc.reason {
			t.Errorf("%s %+v: got %s/%s, want %s/%s", tc.state, tc.e, got, reason, tc.want, tc.reason)
		}
	}
}

func TestProfessionalSourceYieldUsesEligiblePosts(t *testing.T) {
	if got := effectiveSourceRelevant(9, 2, true); got != 2 {
		t.Fatalf("professional eligible yield=%d, want 2", got)
	}
	if got := effectiveSourceRelevant(9, 2, false); got != 9 {
		t.Fatalf("general source relevance was changed: %d", got)
	}
	if got := effectiveSourceRelevant(3, 7, true); got != 3 {
		t.Fatalf("eligible count cannot exceed relevant evidence: %d", got)
	}
	p := sourcePolicy{MinEvaluated: 10, MinPasses: 3, MinFreshUnique: 3, PromoteRate: .8, DemoteRate: .4, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	e := sourceEvidence{Evaluated: 10, Relevant: effectiveSourceRelevant(9, 2, true), FreshUnique: 3, SuccessfulPasses: 3}
	if state, _ := decideSourceState("probation", e, p); state != "rejected" {
		t.Fatalf("professional source with mostly unusable posts became %s", state)
	}
}
