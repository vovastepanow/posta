package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestContentAnalysisSchemaRequiresMemeVerdictOnlyForHumor(t *testing.T) {
	humor := ContentAnalysisSchema("", true)
	if jx.Map(humor, "properties")["meme_kind"] == nil || jx.Map(humor, "properties")["joke"] == nil {
		t.Fatal("юморный анализ должен требовать тип мема и объяснение шутки")
	}
	plain := ContentAnalysisSchema("", false)
	if jx.Map(plain, "properties")["meme_kind"] != nil || jx.Map(plain, "properties")["joke"] != nil {
		t.Fatal("обычному анализу не нужны поля мемов")
	}
}

func TestQuoteIsFormNotMotivationTopic(t *testing.T) {
	if jx.Map(ContentAnalysisSchema("", false), "properties")["quote"] == nil {
		t.Fatal("vision analysis does not classify quote form")
	}
	if HashtagFormat(jx.M{"quote": true, "format": "text"}) != "цитата" {
		t.Fatal("quote form is not available to post hashtags")
	}
	for _, b := range TopicTree {
		if b.Key == "self" {
			for _, leaf := range b.Children {
				if leaf.Key == "self.motivation" && leaf.Title != "Мотивация" {
					t.Fatalf("quotes still mixed into motivation topic: %s", leaf.Title)
				}
			}
		}
	}
}

func TestMemeRelevanceCap(t *testing.T) {
	cases := []struct {
		kind, joke string
		want       int
	}{
		{"template_or_reaction", "", 30},
		{"not_meme", "", 30},
		{"finished_meme", "", 40},
		{"funny_situation", "поворот в конце", 90},
		{"", "", 90}, // старый ответ модели обрабатывается запасной проверкой
	}
	for _, tc := range cases {
		if got := memeRelevanceCap(90, tc.kind, tc.joke); got != tc.want {
			t.Errorf("%q / %q: want %d, got %d", tc.kind, tc.joke, tc.want, got)
		}
	}
}

func TestTextBatchSchemaRequiresMemeVerdictOnlyForHumor(t *testing.T) {
	humorArray := jx.Map(jx.Map(TextBatchSchema("", true), "properties"), "items")
	if jx.Map(jx.Map(humorArray, "items"), "properties")["meme_kind"] == nil {
		t.Fatal("текстовый анализ юмора должен требовать тип мема")
	}
	plainArray := jx.Map(jx.Map(TextBatchSchema("", false), "properties"), "items")
	if jx.Map(jx.Map(plainArray, "items"), "properties")["meme_kind"] != nil {
		t.Fatal("обычному текстовому анализу не нужен тип мема")
	}
}

func TestAnalysisSafetyWithoutTopicCatalog(t *testing.T) {
	for _, schema := range []jx.M{ContentAnalysisSchema("", false), jx.Map(jx.Map(jx.Map(TextBatchSchema("", false), "properties"), "items"), "items")} {
		props := jx.Map(schema, "properties")
		if props["tags"] == nil || props["safety"] == nil {
			t.Fatal("анализ должен запрашивать независимые метки и признаки безопасности")
		}
	}
	u := &U{}
	tags := u.analysisTags("", []string{"lewd_humor", "adult", "shock"})
	if len(tags) != 3 || tags[0] != ContentLewdHumorTag || tags[1] != "adult" || tags[2] != "shock" {
		t.Fatalf("признаки безопасности не преобразованы в метки фильтра: %v", tags)
	}
	if !u.ContentIsAdult(jx.M{"tags": tags}) {
		t.Fatal("фильтр должен распознавать признаки 18+")
	}
}

func TestAnalysisTagsDescribeContentAndPreserveSafety(t *testing.T) {
	u := taxonomyTestUser()
	got := u.contentAnalysisTags("Мемы", []string{"animals.cats", "memes.relatable"}, nil)
	if !jx.Equal(got, []string{"animals.cats", "memes.relatable"}) {
		t.Fatalf("content labels replaced with search topic: %v", got)
	}
	got = u.contentAnalysisTags("Мемы", []string{"animals.cats", "memes.relatable", "people.work"}, []string{"adult"})
	if !jx.Equal(got, []string{"animals.cats", "memes.relatable", "adult"}) {
		t.Fatalf("mandatory safety label lost: %v", got)
	}
}

func TestAnalysisDescriptionPreservesCompleteSentence(t *testing.T) {
	description := "Текстовый пост с анализом контент-стратегии застройщика в социальных сетях: частота публикаций, охваты, форматы видео и статичные изображения, а также результаты за три месяца."
	if got := analysisDescription(jx.M{"description": " " + description + " "}); got != description {
		t.Fatalf("description truncated: %q", got)
	}
}
