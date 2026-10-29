package posta

import (
	"testing"

	"posta/internal/jx"
)

// Один пост с источника: подсчёт записей темы из источника и отсев кандидатов взятых источников.
func TestSourcePoolCount(t *testing.T) {
	pool := []jx.M{
		{"topic": "Мемы", "source_key": "tg:a", "source": "A"},
		{"topic": " мемы ", "source_key": "tg:a", "source": "A"},
		{"topic": "Мемы", "source_key": "tg:b", "source": "B"},
		{"topic": "Города", "source_key": "tg:a", "source": "A"},
	}
	if n := sourcePoolCount(pool, "мемы", "tg:a"); n != 2 {
		t.Fatalf("тема «мемы», источник tg:a: want 2, got %d", n)
	}
	if n := sourcePoolCount(pool, "Мемы", "tg:b"); n != 1 {
		t.Fatalf("источник tg:b: want 1, got %d", n)
	}
	if n := sourcePoolCount(pool, "Мемы", "tg:c"); n != 0 {
		t.Fatalf("чужой источник: want 0, got %d", n)
	}
}

func TestDropTakenSources(t *testing.T) {
	items := []jx.M{{"source_key": "tg:a", "image": "1"}, {"source_key": "tg:b", "image": "2"}, {"source_key": "tg:a", "image": "3"}}
	out := dropTakenSources(items, map[string]bool{"tg:a": true})
	if len(out) != 1 || jx.Str(out[0], "image") != "2" {
		t.Fatalf("после отсева остаётся только tg:b, got %v", out)
	}
	if got := dropTakenSources(items, nil); len(got) != 3 {
		t.Fatalf("без взятых источников список не меняется, got %d", len(got))
	}
}
