package posta

import (
	"strings"
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

func TestProfessionalValueSchemasRequiredForBusiness(t *testing.T) {
	u := taxonomyTestUser()
	for _, topic := range []string{"Бизнес", "SMM и блогинг", "Маркетинг и реклама"} {
		schema := u.contentAnalysisSchemaForTopic("", topic)
		for _, key := range []string{"interest", "practical_value", "specificity"} {
			if !contains(jx.Strs(schema, "required"), key) {
				t.Errorf("vision %q missing required score %q", topic, key)
			}
		}
		batch := professionalTextBatchSchema("", false, u.professionalValueTopic(topic))
		item := jx.Map(jx.Map(jx.Map(batch, "properties"), "items"), "items")
		for _, key := range []string{"interest", "practical_value", "specificity"} {
			if !contains(jx.Strs(item, "required"), key) || jx.Map(item, "properties")[key] == nil {
				t.Errorf("text %q missing score %q", topic, key)
			}
		}
	}
	meme := u.contentAnalysisSchemaForTopic("", "Мемы")
	if contains(jx.Strs(meme, "required"), "interest") {
		t.Fatal("professional scores unexpectedly required for memes")
	}
}

func TestAnalysisExplanationIsCompleteAndSeparateFromQuotedEvidence(t *testing.T) {
	quote := "Ask every new message where they saw you. Paste the link to that post next to th"
	explanation := "Публикация объясняет, как системно узнавать у новых клиентов источник обращения и оценивать эффективность постов."
	analysis := jx.M{"description": "Практическая инструкция по отслеживанию клиентов.", "why_relevant": analysisExplanation(jx.M{"explanation": explanation}, quote),
		"evidence": quote, "relevance": 94, "format": "text"}
	if jx.Str(analysis, "why_relevant") != explanation {
		t.Fatalf("model explanation replaced or truncated: %+v", analysis)
	}
	record := analysisRecord(analysis, jx.M{"image": "test", "text": "Полный исходный текст, не только цитата."}, "SMM и блогинг", "text")
	if jx.Str(record, "why_relevant") != explanation || jx.Str(record, "evidence") != quote ||
		jx.Str(record, "text") != "Полный исходный текст, не только цитата." {
		t.Fatalf("reason, quoted evidence and original must stay separate: %+v", record)
	}
	if fallback := analysisExplanation(jx.M{}, quote); !strings.HasPrefix(fallback, "Фрагмент публикации:") {
		t.Fatalf("legacy quote must be labeled as excerpt, not explanation: %q", fallback)
	}
	vision := ContentAnalysisSchema("", false)
	textItem := jx.Map(jx.Map(jx.Map(TextBatchSchema("", false), "properties"), "items"), "items")
	if jx.Map(vision, "properties")["explanation"] == nil ||
		jx.Map(textItem, "properties")["explanation"] == nil ||
		!contains(jx.Strs(vision, "required"), "explanation") ||
		!contains(jx.Strs(textItem, "required"), "explanation") {
		t.Fatal("analysis schemas must request a distinct complete explanation")
	}
}

func TestArticleAnalysisDoesNotSilentlyCutBodyTo800Characters(t *testing.T) {
	body := strings.Repeat("Практический пример контент-маркетинга. ", 30) + "Значимый вывод находится в конце."
	item := jx.M{"article": jx.M{"title": "SMM статья"}, "text": body}
	got := articleAnalysisText(item)
	if !strings.Contains(got, "Значимый вывод находится в конце.") {
		t.Fatalf("article's important final argument was lost: %q", got)
	}
}
