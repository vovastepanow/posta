package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestProfessionalAnalysisLowValue(t *testing.T) {
	tests := []struct {
		name     string
		analysis jx.M
		want     bool
	}{
		{"generic advice", jx.M{"interest": 10, "practical_value": 20, "specificity": 30}, true},
		{"useful instruction", jx.M{"interest": 20, "practical_value": 80, "specificity": 40}, false},
		{"specific case study", jx.M{"interest": 40, "practical_value": 25, "specificity": 90}, false},
		{"interesting news", jx.M{"interest": 90, "practical_value": 5, "specificity": 20}, false},
		{"borderline", jx.M{"interest": 35, "practical_value": 20, "specificity": 30}, false},
		{"legacy analysis", jx.M{"relevance": 95}, false},
		{"partial analysis", jx.M{"interest": 0, "practical_value": 0}, false},
		{"invalid value", jx.M{"interest": "low", "practical_value": 0, "specificity": 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := professionalAnalysisLowValue(tt.analysis); got != tt.want {
				t.Fatalf("professionalAnalysisLowValue(%v) = %v, want %v", tt.analysis, got, tt.want)
			}
		})
	}
}

func TestProfessionalValueTopic(t *testing.T) {
	u := taxonomyTestUser()
	for _, tc := range []struct {
		topic string
		want  bool
	}{
		{"Бизнес", true},
		{"Маркетинг и реклама", true},
		{"SMM и блогинг", true},
		{"Мемы", false},
	} {
		if got := u.professionalValueTopic(tc.topic); got != tc.want {
			t.Errorf("topic %q: got %v want %v", tc.topic, got, tc.want)
		}
	}
}

func TestContentAnalysisSchemaProfessionalSignals(t *testing.T) {
	schema := ContentAnalysisSchema("", false)
	properties := jx.Map(schema, "properties")
	for _, key := range []string{"interest", "practical_value", "specificity"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("missing optional score %s", key)
		}
	}
	for _, value := range jx.Strs(schema, "required") {
		if value == "interest" || value == "practical_value" || value == "specificity" {
			t.Fatalf("legacy analyses must remain compatible: %s required", value)
		}
	}
	humor := jx.Map(ContentAnalysisSchema("", true), "properties")
	if _, ok := humor["interest"]; ok {
		t.Fatal("meme analysis unexpectedly requires professional scores")
	}
}
