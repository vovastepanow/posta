package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

// Exercise every shipped non-humor branch and leaf in both normalization paths.
func TestNonHumorTopicsDoNotRequireJokesOrRejectQuotedConditionals(t *testing.T) {
	u := taxonomyTestUser()
	for _, topic := range u.TopicRegistry() {
		if u.TopicIsHumor(topic.Title) {
			continue
		}
		t.Run(topic.Key, func(t *testing.T) {
			for _, quote := range []string{"Если изменить условия, результат изменится", "Новый метод может улучшить результат", "Возможно применение в этой области"} {
				raw := jx.M{"relevance": 90, "evidence": quote, "description": quote, "advertising": false, "format": "photo"}
				vision := u.finishAnalysis(raw, topic.Title, "", false, nil, false, "test-model")
				raw["format"] = "text"
				text := u.finishTextAnalysis(raw, topic.Title, "test-model")
				for _, result := range []Analysis{vision, text} {
					if jx.Int(result, "relevance") != 90 || jx.Str(result, "model") != "test-model" {
						t.Fatalf("on-topic result corrupted: %v", result)
					}
					if reason := u.QualityReasons(result, nil, topic.Title, DefaultQualityOpts()); reason != "" {
						t.Fatalf("relevant non-humor post rejected: %s", reason)
					}
				}
			}
			schema := jx.Map(jx.Map(jx.Map(TextBatchSchema("", false), "properties"), "items"), "items")
			if jx.Map(schema, "properties")["joke"] != nil {
				t.Fatal("non-humor schema still asks for a joke")
			}
			prompt := u.TextBatchPrompt(topic.Title, []string{"Материал по теме"}, EmptyHistory())
			if strings.Contains(prompt, "joke:") || strings.Contains(prompt, "meme_kind:") || !strings.Contains(prompt, u.relevanceRule(topic.Title)) {
				t.Fatal("text analysis does not use the topic relevance rules")
			}
		})
	}
}

func TestTopicAdmissionStillRejectsWeakAndUnsubstantiatedMatches(t *testing.T) {
	u := taxonomyTestUser()
	for _, tc := range []struct {
		name, topic, evidence string
		relevance, queryMatch int
	}{
		{"off-topic", "Бизнес", "кот играет с мячом", 15, 0},
		{"weak connection", "Бизнес", "частичное совпадение", 65, 0},
		{"no evidence", "Бизнес", "", 95, 0},
		{"wrong city", "Города и архитектура ? Тюмень", "открытие парка в Томске", 95, 10},
		{"missing query verdict", "Спорт ? Спартак", "футбольный матч", 95, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := jx.M{"relevance": tc.relevance, "query_match": tc.queryMatch, "evidence": tc.evidence, "advertising": false, "format": "photo"}
			vision := u.finishAnalysis(raw, tc.topic, TopicQueryOf(tc.topic), false, nil, false, "test")
			raw["format"] = "text"
			text := u.finishTextAnalysis(raw, tc.topic, "test")
			for _, result := range []Analysis{vision, text} {
				if reason := u.QualityReasons(result, nil, tc.topic, DefaultQualityOpts()); reason == "" {
					t.Fatalf("unsuitable post passed: %v", result)
				}
			}
		})
	}
}

func TestTextLeafRulesAndCompoundHumor(t *testing.T) {
	u := taxonomyTestUser()
	for _, title := range []string{"Рецепты", "Выпечка и десерты", "Криптовалюта"} {
		rule := u.subtopicRelevanceRule(title)
		if rule == "" || !strings.Contains(u.TextBatchPrompt(title, []string{"Текст"}, EmptyHistory()), rule) {
			t.Errorf("leaf-topic constraints missing from text analysis: %s", title)
		}
	}
	for _, title := range []string{"Мемы + Животные", "Животные + Юмор ? коты"} {
		if !u.TopicIsHumor(title) || !strings.Contains(u.TextBatchPrompt(title, []string{"Текст"}, EmptyHistory()), "meme_kind:") {
			t.Errorf("compound topic lost humor requirement: %s", title)
		}
	}
	if u.TopicIsHumor("Бизнес + Спорт") {
		t.Fatal("ordinary compound topic treated as humor")
	}
}
