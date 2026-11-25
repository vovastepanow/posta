package posta

import (
	"testing"

	"posta/internal/jx"
)

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

func TestCollectorSourceVerdictUsesActualAdmissionAndCompleteEvidence(t *testing.T) {
	a := jx.M{"relevance": 95, "advertising": false}
	for _, reason := range []string{"реклама или не проверена на рекламу", "спам", "не подтверждены обязательные признаки темы", "нет конкретной пользы, фактов или интересного вывода", "обычное фото, а тема — мемы"} {
		if passed, known := collectorSourceVerdict(a, reason, "text"); passed || !known {
			t.Fatalf("rejected high-scoring post counted as a source success: %s", reason)
		}
	}
	for _, reason := range []string{"18+ и чувствительный контент выключены", "украинский контент", "тема поиска сменилась"} {
		if _, known := collectorSourceVerdict(a, reason, "text"); known {
			t.Fatalf("user filter or cancelled run became source evidence: %s", reason)
		}
	}
	for _, evidence := range []string{"poster_only", "album_partial", "caption_only", ""} {
		if _, known := collectorSourceVerdict(a, "порог 80: оценка 10", evidence); known {
			t.Fatalf("incomplete evidence affected source rating: %s", evidence)
		}
	}
	if passed, known := collectorSourceVerdict(a, "", "storyboard"); !passed || !known {
		t.Fatal("accepted video did not count")
	}
	if _, known := collectorSourceVerdict(nil, "спам", "image"); known {
		t.Fatal("invalid model result affected source rating")
	}
}
