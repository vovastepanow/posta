package posta

import (
	"encoding/json"
	"os"
	"testing"

	"posta/internal/config"
	"posta/internal/jx"
)

func TestCollectorTextEvidenceGrounding(t *testing.T) {
	text := "Компания раскрыла цифры: 40 новых клиентов. Тестирование ещё продолжается."
	verdict := jx.M{"advertising": false, "relevance": 95}
	for _, quote := range []string{"40 новых клиентов", "Тестирование еще продолжается", "КОМПАНИЯ РАСКРЫЛА ЦИФРЫ"} {
		if !collectorTextEvidenceValid(jx.Merge(verdict, jx.M{"evidence": quote}), text) {
			t.Fatalf("actual quotation rejected: %q", quote)
		}
	}
	for _, quote := range []string{"400 новых клиентов", "Компания закрылась", "Вчера запущен робот", ""} {
		if collectorTextEvidenceValid(jx.Merge(verdict, jx.M{"evidence": quote}), text) {
			t.Fatalf("unsupported high verdict accepted: %q", quote)
		}
	}
	if collectorTextEvidenceValid(jx.M{"relevance": 0, "advertising": false, "evidence": "wrong post"}, text) {
		t.Fatal("unsupported negative verdict poisoned the cache")
	}
	if !collectorTextEvidenceValid(jx.M{"relevance": config.QualityNoEvidenceCap, "advertising": false}, text) {
		t.Fatal("honest absence of evidence is not a malformed verdict")
	}
}

func TestSavedRealTextAuditEvidence(t *testing.T) {
	// Archived real model responses, not a new live inference benchmark.
	raw, err := os.ReadFile("testdata/real_post_audit_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []realPostAuditCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	for _, c := range cases {
		texts[c.ID] = c.Text
	}
	raw, err = os.ReadFile("../../reports/real-post-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	report := jx.Unmarshal(raw)
	checked := 0
	for _, row := range jx.Maps(report, "cases") {
		text, ok := texts[jx.Str(row, "id")]
		if !ok || !jx.Bool(row, "expected_accept") {
			continue
		}
		a := jx.Map(row, "final")
		if !collectorTextEvidenceValid(a, text) {
			t.Errorf("previously reviewed positive case %s failed quote check", jx.Str(row, "id"))
		}
		checked++
	}
	if checked != 28 {
		t.Fatalf("expected all 28 archived positive text decisions, got %d", checked)
	}
}
