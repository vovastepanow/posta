package main

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestFormatAutopilotHealthRow(t *testing.T) {
	stats := jx.M{
		"last_search_outcome": "empty",
		"last_source_candidates": 3,
		"last_direct_sources": 2,
		"direct_accepted_total": 7,
		"direct_rejected_total": 5,
	}
	got := formatAutopilotHealthRow("Маркетинг", true, true, 100, 0, "", stats, 50)
	for _, want := range []string{"Маркетинг", "empty", "кандидаты: 3", "авторы из постов: 2", "AI принято: 7, отклонено: 5"} {
		if !strings.Contains(got, want) {
			t.Errorf("health summary missing %q: %s", want, got)
		}
	}
	const raw = "http://secret.example/path?token=PRIVATE_CREDENTIAL"
	broken := formatAutopilotHealthRow("SMM", true, true, 100, 0, raw, stats, 50)
	if strings.Contains(broken, raw) || strings.Contains(broken, "PRIVATE_CREDENTIAL") {
		t.Fatalf("raw backend failure leaked to diagnostics: %s", broken)
	}
	if !strings.Contains(broken, "ошибкой") {
		t.Fatalf("missing failure indicator: %s", broken)
	}
	running := formatAutopilotHealthRow("SMM", true, true, 100, 200, "", stats, 50)
	if !strings.Contains(running, "работает") {
		t.Fatalf("running job not recognized: %s", running)
	}
	off := formatAutopilotHealthRow("SMM", false, true, 100, 200, "", stats, 50)
	if !strings.Contains(off, "выключен") {
		t.Fatalf("disabled job was reported as running: %s", off)
	}
}
