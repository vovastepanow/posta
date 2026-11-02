package posta

import (
	"testing"

	"posta/internal/jx"
)

// Воронка прохода: источники (ukraine_sources) в суммы не входят, нулевые причины не попадают.
func TestPassFunnel(t *testing.T) {
	skipped := queueSkip{"ukraine": 4, "format": 0, "duplicate": 2, "ukraine_sources": 3}
	f := passFunnel(120, 50, skipped, map[string]int{"rejected": 7, "duplicates": 1}, 12, 3)
	if f["filtered"] != 6 {
		t.Fatalf("отсеяно %v, ждали 6 (4 ПК-фильтр + 2 дубля, без источников и нулей)", f["filtered"])
	}
	by := f["filtered_by"].(jx.M)
	if _, ok := by["ukraine_sources"]; ok {
		t.Fatalf("ukraine_sources попал в причины: %v", by)
	}
	if _, ok := by["format"]; ok {
		t.Fatalf("нулевая причина попала в карту: %v", by)
	}
	if by["ukraine"] != 4 {
		t.Fatalf("ПК-фильтр: %v, ждали 4", by["ukraine"])
	}
	if f["collected"] != 120 || f["candidates"] != 50 || f["prescreen_rejected"] != 7 || f["duplicates"] != 1 || f["analyzed"] != 12 || f["added"] != 3 {
		t.Fatalf("счётчики: %v", f)
	}
}
