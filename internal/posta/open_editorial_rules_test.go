package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestOpenEditorialRuleAndSafetyLabels(t *testing.T) {
	for _, term := range []string{"topic_match", "исключения", "доказательства", "не является мемом", "не является инструкцией"} {
		if !strings.Contains(openEditorialRule, term) {
			t.Fatalf("missing requirement %q", term)
		}
	}
	a := jx.M{"relevance": 94, "topic_match": true, "advertising": false, "tags_classification_pending": true, "tags_classification_reason": "model_unavailable"}
	finishOpenContentLabels(a, []string{"adult"})
	if jx.Int(a, "relevance") != 94 || jx.Bool(a, "tags_classification_pending") || a["tags_classification_reason"] != nil || !contains(jx.ToStrs(a["tags"]), "adult") {
		t.Fatalf("editorial or safety decision lost: %v", a)
	}
}
