package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"posta/internal/jx"
	"posta/internal/llm"
	"testing"
)

func TestContentClassifierAcceptsOnlyGroundedMenuLabels(t *testing.T) {
	calls := 0
	reply := `{"selected":[{"topic":"Маркетинг и реклама","evidence":"запуске сайта и рекламы"},{"topic":"Программирование","evidence":"код на Python"},{"topic":"Несуществующая тема","evidence":"запуске сайта и рекламы"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "nvidia/nemotron-3.5-super-vl" {
			t.Errorf("wrong model: %v", req["model"])
		}
		if jx.Map(req, "provider")["max_price"] == nil {
			t.Error("price cap missing")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": reply}}}, "usage": map[string]any{"cost": 0.001}})
	}))
	defer srv.Close()
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	router.LoadSettings = func() llm.Settings { s := llm.Preset("cloud"); s.DailyBudgetUSD = 0.1; return s }
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = router
	got := u.classifyContent("", "Главная ошибка при запуске сайта и рекламы", false)
	if !jx.Equal(got, []string{"business.marketing"}) {
		t.Fatalf("unconfirmed labels survived: %v", got)
	}
	if got := u.classifyContent("", "Sensitive text", true); got != nil || calls != 1 {
		t.Fatalf("sensitive payload was sent: calls=%d tags=%v", calls, got)
	}
	reply = `not JSON`
	if got := u.classifyContent("", "Главная ошибка при запуске сайта и рекламы", false); got != nil {
		t.Fatalf("invalid response accepted: %v", got)
	}
}
