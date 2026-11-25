package posta

import (
	"math"
	"testing"

	"posta/internal/jx"
)

func TestEditorialRankingDistinguishesQualityFromRelevance(t *testing.T) {
	at := int64(2000000000)
	weak := jx.M{"relevance": 95, "interest": 36, "practical_value": 38, "specificity": 36, "posted_at": at, "source_key": "rss:weak"}
	useful := jx.M{"relevance": 90, "interest": 40, "practical_value": 90, "specificity": 85, "posted_at": at - 300, "source_key": "rss:useful"}
	news := jx.M{"relevance": 90, "interest": 95, "practical_value": 5, "specificity": 75}
	if poolEditorialBonus(useful, true) <= poolEditorialBonus(weak, true) || poolEditorialBonus(news, true) <= 0 {
		t.Fatal("useful instructions or substantive news did not rank higher")
	}
	got := poolOrderWithPreference([]jx.M{weak, useful}, func(item jx.M) int64 { return poolEditorialBonus(item, true) })
	if jx.Str(got[0], "source_key") != "rss:useful" {
		t.Fatal("editorial value did not influence nearby records")
	}
	useful["postponed_at"] = at
	got = poolOrderWithPreference([]jx.M{weak, useful}, func(item jx.M) int64 { return poolEditorialBonus(item, true) })
	if jx.Str(got[0], "source_key") != "rss:weak" {
		t.Fatal("quality overrode an explicit postponement")
	}
	if poolEditorialBonus(jx.M{"relevance": 85}, true) != 0 || poolEditorialBonus(jx.M{"relevance": 85, "interest": 0}, true) != 0 {
		t.Fatal("unknown quality was treated as poor quality")
	}
	if poolEditorialBonus(jx.M{"relevance": 85, "interest": 100, "specificity": 100, "practical_value": 100}, false) != 0 {
		t.Fatal("professional criteria leaked to unrelated topics")
	}
}

func TestProfessionalScoreValidationAndHistory(t *testing.T) {
	low := jx.M{"interest": "10", "practical_value": 20, "specificity": 30}
	if !professionalAnalysisLowValue(low) {
		t.Fatal("valid numeric string bypassed low-value filter")
	}
	for _, v := range []any{nil, true, -1, 101, math.NaN(), "NaN", "unknown"} {
		item := jx.Merge(low, jx.M{"interest": v})
		if professionalAnalysisLowValue(item) {
			t.Fatalf("invalid score became a negative verdict: %v", v)
		}
	}
	u := taxonomyTestUser()
	h := u.ContentHistoryRecord(jx.Merge(low, jx.M{"evidence": "exact evidence", "analysis_model": "model-a"}), nil)
	for _, k := range []string{"interest", "practical_value", "specificity", "evidence", "analysis_model"} {
		if h[k] == nil {
			t.Fatalf("history lost quality signal %s", k)
		}
	}
	if QualityViewsRatio(jx.M{"engagement": jx.M{"neighbors_median_views": 100}}) != nil {
		t.Fatal("missing view count became zero")
	}
}
