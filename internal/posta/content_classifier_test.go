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

func TestContentClassifierAcceptsOnlyGroundedMenuLabels(t *testing.T) {
	calls := 0
	reply := `{"sufficient":true,"selected":[{"topic":"Маркетинг и реклама","evidence":"запуске сайта и рекламы"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "nvidia/nemotron-3.5-super-vl:free" {
			t.Errorf("wrong model: %v", req["model"])
		}
		if jx.Map(req, "provider")["max_price"] == nil {
			t.Error("price cap missing")
		}
		messages := jx.Maps(req, "messages")
		parts := jx.Maps(messages[len(messages)-1], "content")
		answer := reply
		if len(parts) > 0 && strings.HasPrefix(jx.Str(parts[0], "text"), "Проверь каждую") {
			answer = `{"verified":[{"tag":"business.marketing","matches":true,"basis":"subject","aspect":"Публикация раскрывает действие из определения темы","evidence":"запуске сайта и рекламы"}]}`
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}, "usage": map[string]any{"cost": 0.001}})
	}))
	defer srv.Close()
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	router.LoadSettings = func() llm.Settings { s := llm.Preset("free"); s.DailyBudgetUSD = 0.1; return s }
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = router
	got := u.classifyContent("", "Главная ошибка при запуске сайта и рекламы", false)
	if !jx.Equal(got, []string{"business.marketing"}) {
		t.Fatalf("unconfirmed labels survived: %v", got)
	}
	if got := u.classifyContent("", "Sensitive text", true); got != nil || calls != 2 {
		t.Fatalf("sensitive payload was sent: calls=%d tags=%v", calls, got)
	}
	reply = `not JSON`
	if got := u.classifyContent("", "Главная ошибка при запуске сайта и рекламы", false); got != nil {
		t.Fatalf("invalid response accepted: %v", got)
	}
}
