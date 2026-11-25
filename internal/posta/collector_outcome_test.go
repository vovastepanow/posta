package posta

import "testing"

func TestQuickScreenScoreRejectsMalformedAnswers(t *testing.T) {
	for _, input := range []string{`{"score":"unknown"}`, `{"score":true}`, `{"score":101}`, `{"score":-10}`, `{"other":90}`, "Оценка от 0 до 100", "101", "-90", "90 или 10"} {
		if got := parseQuickScore(input); got != nil {
			t.Errorf("malformed input became verdict: %q -> %d", input, *got)
		}
	}
	for _, input := range []string{`{"score":90}`, `{"score":"90"}`, "90", "90%", "Оценка: 90%"} {
		if got := parseQuickScore(input); got == nil || *got != 90 {
			t.Errorf("valid score lost: %q -> %v", input, got)
		}
	}
}

func TestCollectorOutcomeCountersAreSeparateAndSnapshotSafe(t *testing.T) {
	var counts map[string]int
	for _, reason := range []string{"", "порог 80: оценка 75", "порог 80: оценка 10", "нет конкретной пользы, фактов или интересного вывода", "тема поиска сменилась"} {
		incrementCollectorCount(&counts, collectorAdmissionCode(reason))
	}
	snapshot := collectorCountSnapshot(counts)
	incrementCollectorCount(&counts, "accepted")
	if snapshot["accepted"] != 1 || counts["accepted"] != 2 || counts["relevance"] != 2 || counts["low_editorial_value"] != 1 || counts["topic_changed"] != 1 {
		t.Fatalf("unexpected counters: %v snapshot %v", counts, snapshot)
	}
}
