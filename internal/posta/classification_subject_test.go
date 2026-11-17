package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"posta/internal/jx"
	"posta/internal/llm"
	"strings"
	"testing"
)

func TestClassificationSeparatesRejectedContextFromUncertainty(t *testing.T) {
	// These are scripted verifier decisions, not a claim that a real model has
	// learned to reject industry context. Exercise the production acceptance path.
	text := "Операторы наружной рекламы подали судебные иски. Разобрана стратегия продвижения бренда."
	for _, tc := range []struct {
		name, basis, aspect, evidence string
		matches                       bool
		want                          []string
	}{
		{"reject_context_keep_subject", "context", "Отрасль стороны спора", "", false, []string{"business.legal"}},
		{"two_real_subjects", "subject", "Стратегия продвижения", "стратегия продвижения бренда", true, []string{"business.legal", "business.marketing"}},
		{"uncertain_is_pending", "uncertain", "Нельзя установить предмет", "", false, nil},
		{"legacy_boolean_is_not_semantic_proof", "", "", "Операторы наружной рекламы", true, nil},
		{"contradiction_is_pending", "context", "Отрасль стороны спора", "Операторы наружной рекламы", true, nil},
		{"missing_aspect_is_pending", "subject", "", "Операторы наружной рекламы", true, nil},
		{"same_aspect_for_two_labels_is_pending", "subject", "Судебные требования", "Операторы наружной рекламы", true, nil},
		{"same_evidence_for_two_labels_is_pending", "subject", "Рекламная отрасль", "подали судебные иски", true, nil},
		{"fabricated_evidence_is_pending", "subject", "Стратегия продвижения", "несуществующая цитата", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				calls++
				var req jx.M
				json.NewDecoder(r.Body).Decode(&req)
				messages := jx.Maps(req, "messages")
				prompt := jx.Str(jx.Maps(messages[len(messages)-1], "content")[0], "text")
				answer := jx.M{"sufficient": true, "selected": []jx.M{
					{"topic": "Право", "evidence": "подали судебные иски"},
					{"topic": "Маркетинг и реклама", "evidence": "Операторы наружной рекламы"},
				}}
				if strings.Contains(prompt, "\nТЕМЫ:\n") {
					if !strings.Contains(prompt, contentSubjectRule) || !strings.Contains(prompt, "судебные споры") {
						t.Error("verifier lost subject rule or full taxonomy boundary")
					}
					answer = jx.M{"verified": []jx.M{
						{"tag": "business.legal", "matches": true, "basis": "subject", "aspect": "Судебные требования", "evidence": "подали судебные иски"},
						{"tag": "business.marketing", "matches": tc.matches, "basis": tc.basis, "aspect": tc.aspect, "evidence": tc.evidence},
					}}
				}
				encoded, _ := json.Marshal(answer)
				json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": string(encoded)}}}})
			}))
			defer srv.Close()
			u := taxonomyTestUser()
			u.Ctx = context.Background()
			u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
			u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
			got := u.classifyContent("", text, false)
			if !jx.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("got %v, want %v; reason=%s", got, tc.want, u.classificationReason)
			}
			if calls > 3 {
				t.Fatalf("unexpected retries: %d", calls)
			}
		})
	}
}

func TestContentEvidenceCannotComeOnlyFromMetadata(t *testing.T) {
	for _, tc := range []struct {
		text, quote string
		want        bool
	}{
		{"Поведение поколений #маркетинг #продажи", "продажи", false},
		{"Поведение поколений (#продажи)", "продажи", false},
		{"Подробнее https://example.org/продажи", "продажи", false},
		{"Автор @продажи", "продажи", false},
		{"Как устроены продажи: этапы сделки. #продажи", "устроены продажи: этапы сделки", true},
		{"Как устроены продажи: этапы сделки. #продажи", "устроены продажи: этапы сделки. #продажи", true},
		{"Написал программу на C#", "программу на C#", true},
		{"Операторы наружной рекламы подали иски", "Операторы наружной рекламы", true}, // provenance is not semantic entailment
		{"Условия сделки", "несуществующий текст", false},
	} {
		if got := contentEvidenceGrounded(tc.text, tc.quote); got != tc.want {
			t.Errorf("%q / %q: %v", tc.text, tc.quote, got)
		}
	}
}
