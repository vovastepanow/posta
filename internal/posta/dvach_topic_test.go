package posta

import (
	"testing"

	"posta/internal/jx"
)

// Треды Двача: «не по теме» выкидываются, остальные идут по убыванию оценки, без оценки — 50.
func TestDvachByTopicOrder(t *testing.T) {
	pool := []jx.M{
		{"num": "1", "subject": "мусор"},
		{"num": "2", "subject": "мемы"},
		{"num": "3", "subject": "неизвестно"},
		{"num": "4", "subject": "лучшее"},
	}
	score := func(v int) *int { return &v }
	gates := map[string]*int{"1": score(0), "2": score(60), "4": score(90)}
	got, _ := DvachByTopic(pool, gates)
	var ids []string
	for _, t := range got {
		ids = append(ids, jx.Str(t, "num"))
	}
	want := []string{"4", "2", "3"}
	if len(ids) != len(want) {
		t.Fatalf("порядок: получили %v, ждали %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("порядок: получили %v, ждали %v", ids, want)
		}
	}
}
