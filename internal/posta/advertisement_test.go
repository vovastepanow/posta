package posta

import (
	"encoding/json"
	"os"
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

func TestAdvertisingDiscussionIsNotAdvertising(t *testing.T) {
	for _, item := range []jx.M{
		{"text": "Как рекламная интеграция повлияла на удержание аудитории"},
		{"text": "Что такое партнёрский материал и как его маркировать"},
		{"text": "Кейс SMM: скидка по промокоду не увеличила продажи"},
		{"text": "Исследование: как устроены реферальные ссылки t.me/+example и ?start=test"},
		{"text": "Новая функция бесплатного тарифа @examplebot #smm #marketing #analytics #case"},
		{"text": "Финансовый обзор: оформить кредит стало сложнее"},
		{"text": "Полезный разбор", "description": "Рекламная интеграция"},
		{"text": "В статье объясняется, зачем нужен тег #реклама в соцсетях"},
		{"text": "Маркетолог объяснил, почему не стоит подписываться на наш канал в качестве цели"},
	} {
		if PostIsAdvertisement(item) || PostSearchIsAd(item) {
			t.Errorf("editorial material was mistaken for an advertisement: %v", item)
		}
	}
	for _, item := range []jx.M{
		{"text": "Предложение магазина\nПартнёрский материал"},
		{"text": "Новый товар\nerid: abc123"},
		{"text": "Полезный пост #реклама #shop"},
		{"advertising": true, "text": "Товар"},
	} {
		if !PostIsAdvertisement(item) || !PostSearchIsAd(item) {
			t.Errorf("explicit disclosure missed: %v", item)
		}
	}
}

func TestArchivedPositivePostsSurviveHardAdvertisingFilter(t *testing.T) {
	raw, err := os.ReadFile("testdata/real_post_audit_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []realPostAuditCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, c := range cases {
		if !c.Want {
			continue
		}
		item := jx.M{"text": c.Text, "url": c.URL}
		if PostSearchIsAd(item) {
			t.Errorf("reviewed positive post %s rejected as advertising", c.ID)
		}
		checked++
	}
	if checked != 28 {
		t.Fatalf("expected 28 reviewed positive text decisions, got %d", checked)
	}
}

func TestQuerySearchDoesNotDropMarketingVocabulary(t *testing.T) {
	posts := []jx.M{
		{"text": "SMM: как измерить эффективность промокодов и скидок", "text_post": true, "url": "https://example.org/case", "posted_at": now(), "source_key": "rss:case"},
		{"text": "SMM: покупайте услуги\n#реклама", "text_post": true, "url": "https://example.org/ad", "posted_at": now(), "source_key": "rss:ad"},
	}
	out := rankQuerySearch(map[string][]jx.M{PlatformRSS: posts}, QueryPlan{Must: []string{"smm"}}, 30, noSkip)
	if len(out[PlatformRSS]) != 1 || jx.Str(out[PlatformRSS][0], "url") != "https://example.org/case" {
		t.Fatalf("search lost the case study or admitted a disclosed ad: %v", out)
	}
}
