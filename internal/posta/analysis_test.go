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

func TestMemeRelevanceCap(t *testing.T) {
	cases := []struct {
		kind, joke string
		want       int
	}{
		{"template_or_reaction", "", 30},
		{"not_meme", "", 30},
		{"finished_meme", "", 40},
		{"funny_situation", "поворот в конце", 90},
		{"", "", 90}, // запасной judge всё ещё проверит шутку старого ответа модели
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
