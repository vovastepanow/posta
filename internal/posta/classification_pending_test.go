package posta

import (
	"context"
	"posta/internal/jx"
	"posta/internal/llm"
	"testing"
)

func TestUnavailableLabelsPreserveRelevanceAndMandatorySafety(t *testing.T) {
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("http://127.0.0.1:1", "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.Roles[llm.RoleClassify] = llm.RoleConfig{Primary: llm.Off, Fallback: llm.Off}
		return s
	}
	result := jx.M{"relevance": 92, "description": "Публикация о налогах", "tags": []string{"finance.taxes"}}
	u.applyContentClassification(result, "", "Как оформить налоговый вычет", false)
	if !jx.Bool(result, "tags_classification_pending") || jx.Int(result, "relevance") != 92 {
		t.Fatal(result)
	}
	if len(jx.ToStrs(result["tags"])) != 0 {
		t.Fatal("accepted unconfirmed topic", result)
	}
	saved := analysisRecord(result, jx.M{}, "Налоги", "text")
	if !jx.Bool(saved, "tags_classification_pending") || jx.Str(saved, "tags_classification_reason") == "" {
		t.Fatal("lost pending state", saved)
	}
	result["tags"] = []string{"finance.taxes", "adult"}
	u.applyContentClassification(result, "", "sensitive text", false)
	if !jx.Equal(result["tags"], []string{"adult"}) || !u.ContentIsAdult(result) {
		t.Fatal("lost sensitive safety label", result)
	}
}

func TestPendingFillNeverReplacesExistingOrReviewedTopics(t *testing.T) {
	for _, tc := range []struct {
		name          string
		data          jx.M
		old, proposed []string
		want          bool
	}{
		{"pending empty", jx.M{"tags_classification_pending": true}, nil, []string{"finance.taxes"}, true},
		{"pending safety", jx.M{"tags_classification_pending": true}, []string{"adult"}, []string{"adult", "finance.taxes"}, true},
		{"existing topic", jx.M{"tags_classification_pending": true}, []string{"business.logistics"}, []string{"style.tattoo"}, false},
		{"reviewed empty", jx.M{"tags_classification_pending": true, "tags_reviewed": true}, nil, []string{"finance.taxes"}, false},
		{"not pending", jx.M{}, nil, []string{"finance.taxes"}, false},
		{"unavailable", jx.M{"tags_classification_pending": true}, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canFillPendingLabels(tc.data, tc.old, tc.proposed); got != tc.want {
				t.Fatal(got)
			}
		})
	}
}
