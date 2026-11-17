package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"posta/internal/llm"
	"testing"
)

func TestClassifierUncertaintyNeverBecomesNegative(t *testing.T) {
	cases := []struct {
		name    string
		replies []string
	}{
		{"invalid_verification_response", []string{`{"sufficient":true,"selected":[{"topic":"Маркетинг и реклама","evidence":"запуске сайта и рекламы"}]}`, `{"sufficient":true,"selected":[]}`}},
		{"missing_data", []string{`{"sufficient":false,"selected":[]}`}},
		{"malformed_array", []string{`{"sufficient":true,"selected":null}`}},
		{"invalid_evidence", []string{`{"sufficient":true,"selected":[{"topic":"Маркетинг и реклама","evidence":"выдуманная цитата"}]}`, `{"verified":null}`}},
		{"broken_row", []string{`{"sufficient":true,"selected":[null]}`}},
		{"unknown_menu_label", []string{`{"sufficient":true,"selected":[{"topic":"Неизвестная тема","evidence":"запуске сайта и рекламы"}]}`}},
		{"missing_status", []string{`{"selected":[]}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
					return
				}
				if calls >= len(tc.replies) {
					t.Error("unexpected retry")
					http.Error(w, "unexpected", 500)
					return
				}
				answer := tc.replies[calls]
				calls++
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
			}))
			defer server.Close()
			router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(server.URL, "test", "", ""))
			router.LoadSettings = func() llm.Settings { return llm.Preset("free") }
			u := taxonomyTestUser()
			u.Ctx = context.Background()
			u.LLM = router
			if tags := u.classifyContent("", "Главная ошибка при запуске сайта и рекламы", false); tags != nil {
				t.Fatalf("uncertainty became a verdict: %v", tags)
			}
		})
	}
}
