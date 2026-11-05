package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

// Use the actual shipped tree and an in-memory registry, without a database or model.
func taxonomyTestUser() *U {
	var topics []*Topic
	for _, b := range TopicTree {
		parent := &Topic{ID: b.Key, Key: b.Key, Title: b.Title, About: b.About, Aliases: b.Aliases, Scope: "broad", Format: &b.Format}
		topics = append(topics, parent)
		for _, leaf := range b.Children {
			parentID := b.Key
			topics = append(topics, &Topic{ID: leaf.Key, Key: leaf.Key, Title: leaf.Title, About: leaf.About, Aliases: leaf.Aliases, Scope: "narrow", Parent: &parentID, Format: leaf.Format})
		}
	}
	return &U{App: &App{catalog: &Catalog{idx: buildTopicIndex(topics)}}}
}

func TestTaxonomyNamesAndFormatsForRequestedTopics(t *testing.T) {
	u := taxonomyTestUser()
	cases := []struct{ input, key string }{
		{"Бизнес", "business"}, {"предпринимательство", "business.owners"},
		{"города", "travel.cities"}, {"спорт", "sport"},
		{"криптовалюта", "finance.crypto"}, {"биткоин", "finance.crypto"},
		{"кулинария", "food"}, {"рецепты", "food.recipes"},
	}
	for _, tc := range cases {
		e := u.TopicLookup(tc.input)
		if e == nil || e.Key != tc.key {
			t.Errorf("%q resolved to %v, want %s", tc.input, e, tc.key)
		}
	}
	for _, topic := range []string{"Бизнес", "Города и архитектура ? Тюмень", "Спорт", "Криптовалюта"} {
		f := u.TopicFormatOf(topic)
		if f.Media != "optional" || f.Text != "optional" || f.Judge != "both" {
			t.Errorf("%q should accept text and visual content, got %+v", topic, f)
		}
		prompt := u.ContentAnalysisPrompt(topic, "", EmptyHistory(), nil, "")
		if strings.Contains(prompt, "не про тему или нет шутки") || strings.Contains(prompt, "шутка очевидна и узнаваема") {
			t.Errorf("%q is evaluated using humor anchors", topic)
		}
		if strings.Contains(prompt, "meme_kind:") {
			t.Errorf("%q incorrectly treated as humor", topic)
		}
	}
}

func TestTopicWithQueryKeepsBaseTopicContext(t *testing.T) {
	u := taxonomyTestUser()
	key := "Города и архитектура ? Тюмень"
	prompt := u.ContentAnalysisPrompt(key, "Прогулка по Тюмени", EmptyHistory(), nil, "")
	for _, required := range []string{"Жизнь городов и регионов", "местные новости", "Пользователь ищет: «Тюмень»", "Примета: в подписи упомянуто «Тюмень»", "одновременно соответствовать нескольким подтемам"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt missing %q", required)
		}
	}
	if !u.TopicIsHumor("Мемы ? Тюмень") {
		t.Fatal("query must not turn off the base topic's humor rules")
	}
	if !u.hasSpecificTopic(key) {
		t.Fatal("query must preserve the specific base topic")
	}
}

func TestQueryTyumenMatchesVariantsWithoutAcceptingOtherCities(t *testing.T) {
	plan := queryPlanFallback("Тюмень")
	for _, text := range []string{"Новости Тюмени", "Тюменский городской парк", "Street views in Tyumen", "вид на Тюмень"} {
		if queryLex(jx.M{"text": text}, plan) == 0 {
			t.Errorf("should match %q", text)
		}
	}
	for _, text := range []string{"Новости Томска", "Красивые города России"} {
		if queryLex(jx.M{"text": text}, plan) != 0 {
			t.Errorf("should not match %q", text)
		}
	}
}

func TestTopicIndexResolvesTitlesAndRejectsAmbiguousAliases(t *testing.T) {
	a := &Topic{ID: "a", Title: "Общая", Aliases: []string{"Точная", "неясная"}}
	b := &Topic{ID: "b", Title: "Точная", Aliases: []string{"неясная", "понятная"}}
	for _, list := range [][]*Topic{{a, b}, {b, a}} {
		idx := buildTopicIndex(list)
		if idx.byNorm[TopicNorm("Точная")] != b {
			t.Fatal("exact title must beat an alias")
		}
		if idx.byNorm[TopicNorm("неясная")] != nil {
			t.Fatal("ambiguous alias must not silently resolve")
		}
		if idx.byNorm[TopicNorm("понятная")] != b {
			t.Fatal("unique alias should resolve")
		}
	}
}

func TestOverlappingTagsSurviveAndDoNotDependOnOneWinner(t *testing.T) {
	u := taxonomyTestUser()
	for _, raw := range [][]string{{"food.recipes", "food.baking"}, {"memes.relatable", "memes.topical", "people.work"}} {
		got := u.ContentTagsClean(raw)
		if len(got) != len(raw) {
			t.Fatalf("overlapping tags lost: %v -> %v", raw, got)
		}
	}
	rules := ContentTagsPromptRules()
	if !strings.Contains(rules, "food.recipes и food.baking") || !strings.Contains(rules, "memes.relatable и people.work") {
		t.Fatal("missing overlapping examples")
	}
	for _, topic := range []string{"Рецепты", "Выпечка и десерты", "Жиза", "Мемы на злобу дня"} {
		rule := u.subtopicRelevanceRule(topic)
		if !strings.Contains(rule, "одновременно соответствовать нескольким подтемам") {
			t.Errorf("%s still uses exclusive categories: %q", topic, rule)
		}
	}
}

func TestPersonalComplaintsDoNotShareAnalysisCache(t *testing.T) {
	u := taxonomyTestUser()
	item := jx.M{"image": "image.jpg"}
	first := History{"complaints": []jx.M{{"topic": "Бизнес", "complaint_reason": "match_error", "description": "реклама курсов", "timestamp": int64(1)}}}
	second := History{"complaints": []jx.M{{"topic": "Бизнес", "complaint_reason": "match_error", "description": "котировки акций", "timestamp": int64(2)}}}
	u.ID = "one"
	k1 := u.analysisCacheKey(item, "Бизнес", first)
	if k1 == u.analysisCacheKey(item, "Бизнес", second) || k1 == u.analysisCacheKey(item, "Бизнес", EmptyHistory()) {
		t.Fatal("changed personal complaints reused a cache entry")
	}
	u.ID = "two"
	if k1 == u.analysisCacheKey(item, "Бизнес", first) {
		t.Fatal("personal result shared across users")
	}
	prompt := u.ContentAnalysisPrompt("Бизнес", "", first, nil, "")
	if !strings.Contains(prompt, "реклама курсов") || strings.Contains(prompt, "котировки акций") {
		t.Fatal("personal feedback missing or mixed")
	}
	textPrompt := u.TextBatchPrompt("Бизнес", []string{"текст"}, first)
	if !strings.Contains(textPrompt, "реклама курсов") || strings.Contains(textPrompt, "котировки акций") {
		t.Fatal("personal feedback missing or mixed in text analysis")
	}
}

func TestTyumenSearchFiltersUnrelatedCities(t *testing.T) {
	plan := queryPlanIncludeOriginal("Тюмень", QueryPlan{RU: []string{"города России", "тюменские новости"}, Must: []string{"тюмень"}})
	if len(plan.RU) == 0 || plan.RU[0] != "Тюмень" {
		t.Fatalf("original query lost: %v", plan)
	}
	// The broad alternative may admit another city for later model review;
	// with the exact fallback plan, unrelated cities must not enter at all.
	plan = queryPlanFallback("Тюмень")
	mk := func(src, text string) jx.M {
		return jx.M{"source_key": src, "text": text, "image": src + ".jpg", "url": "https://example.org/" + src, "posted_at": now()}
	}
	result := rankQuerySearch(map[string][]jx.M{PlatformTelegram: {
		mk("one", "В Тюмени открыли новый парк"),
		mk("two", "Tyumen city center"),
		mk("three", "В Томске открыли новый парк"),
		mk("four", "Новости других городов"),
	}}, plan, 30, noSkip)
	if len(result[PlatformTelegram]) != 2 {
		t.Fatalf("unexpected city search results: %v", result)
	}
}

func TestAmbiguousAliasDoesNotMatchSources(t *testing.T) {
	first := &Topic{ID: "a", Title: "Первая", Scope: "broad", Aliases: []string{"общее слово"}}
	second := &Topic{ID: "b", Title: "Вторая", Scope: "broad", Aliases: []string{"общее слово"}}
	a := &App{catalog: &Catalog{idx: buildTopicIndex([]*Topic{first, second})}}
	if a.TopicLookup("общее слово") != nil || a.TopicsAreEquivalent("общее слово", "Первая") || a.TopicsAreEquivalent("общее слово", "Вторая") {
		t.Fatal("ambiguous alias must not assign a source to either topic")
	}
}

func TestFinalAnalysisPreservesRelevantOverlappingTopics(t *testing.T) {
	u := taxonomyTestUser()
	cases := []struct {
		topic, query, evidence string
		tags                   []string
		queryMatch             int
		want                   int64
	}{
		{"Рецепты", "", "рецепт торта", []string{"food.baking"}, 0, 90},
		{"Выпечка и десерты", "", "торт и ингредиенты", []string{"food.recipes"}, 0, 90},
		{"Бизнес", "", "открытие магазина", []string{"business.owners"}, 0, 90},
		{"Спорт", "", "результат матча", []string{"sport.football"}, 0, 90},
		{"Криптовалюта", "", "биткоин и блокчейн", []string{"finance.invest"}, 0, 90},
		{"Города и архитектура ? Тюмень", "Тюмень", "новый парк в Тюмени", []string{"news.russia"}, 88, 88},
		{"Города и архитектура ? Тюмень", "Тюмень", "улица в Томске", []string{"travel.cities"}, 5, 5},
	}
	for _, tc := range cases {
		raw := jx.M{"relevance": 90, "query_match": tc.queryMatch, "description": tc.evidence, "evidence": tc.evidence, "tags": tc.tags, "format": "photo"}
		got := u.finishAnalysis(raw, tc.topic, tc.query, false, nil, false, "test-model")
		if jx.Int(got, "relevance") != tc.want {
			t.Errorf("%s: relevance %d, want %d", tc.topic, jx.Int(got, "relevance"), tc.want)
		}
	}
}
