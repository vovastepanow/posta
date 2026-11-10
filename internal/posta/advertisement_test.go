package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestPostIsAdvertisement(t *testing.T) {
	for _, item := range []jx.M{
		{"url": "https://pikabu.ru/story/example?from=cpm&cpm_token=123", "text": "Когда есть смысл заводить вторую дебетовую карту"},
		{"text": "Оставили заявку на получение бесплатной карты?"},
		{"text": "Полезный пост\n#реклама"},
		{"text": "erid: abc"},
	} {
		if !PostIsAdvertisement(item) {
			t.Errorf("advertisement missed: %v", item)
		}
	}
	for _, item := range []jx.M{
		{"url": "https://pikabu.ru/story/example?from=feed", "text": "Банк изменил условия дебетовых карт"},
		{"url": "https://example.org/?from=cpm", "text": "Новости рекламы"},
		{"text": "Реклама лекарств стала темой исследования."},
	} {
		if PostIsAdvertisement(item) {
			t.Errorf("ordinary post rejected: %v", item)
		}
	}
}

func TestAdvertisingVerdictRequiredForAdmission(t *testing.T) {
	u := &U{}
	for _, analysis := range []jx.M{{}, {"advertising": true}, {"advertising": "false"}} {
		if reason := u.admissionReason(analysis, jx.M{"text": "Пост"}, "Мемы", "Пост"); reason != "реклама или не проверена на рекламу" {
			t.Errorf("unverified advertising verdict admitted: %v: %q", analysis, reason)
		}
	}
	textItems := jx.Map(jx.Map(jx.Map(TextBatchSchema("", false), "properties"), "items"), "items")
	for _, schema := range []jx.M{ContentAnalysisSchema("", false), textItems} {
		if jx.Map(jx.Map(schema, "properties"), "advertising") == nil {
			t.Errorf("advertising field missing from schema: %v", schema)
		}
	}
}
