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

func TestClassifierUsesFreeVerifierWithExhaustedPaidBudget(t *testing.T) {
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req jx.M
		json.NewDecoder(r.Body).Decode(&req)
		models = append(models, jx.Str(req, "model"))
		answer := `{"sufficient":true,"selected":[{"topic":"Право","evidence":"подали иск в суд"}]}`
		messages := jx.Maps(req, "messages")
		prompt := jx.Str(jx.Maps(messages[len(messages)-1], "content")[0], "text")
		if strings.HasPrefix(prompt, "Проверь каждую") {
			answer = `{"verified":[{"tag":"business.legal","matches":true,"basis":"subject","aspect":"Судебный иск по долгу","evidence":"подали иск в суд"}]}`
		}
		json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
	}))
	defer srv.Close()
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.DailyBudgetUSD = 0
		s.Roles[llm.RoleClassifyVerify] = llm.RoleConfig{Primary: llm.FreeVision, Fallback: llm.Target{Provider: "openrouter", Model: "qwen/qwen3.7-flash"}}
		return s
	}
	tags := u.classifyContent("", "Рекламные агентства подали иск в суд о взыскании долга.", false)
	if !jx.Equal(tags, []string{"business.legal"}) || len(models) != 2 || models[0] != llm.FreeVision.Model || models[1] != llm.FreeVision.Model {
		t.Fatalf("tags=%v models=%v reason=%s", tags, models, u.classificationReason)
	}
}

func TestClassifierUsesConfiguredPaidVerifierAndRespectsBudget(t *testing.T) {
	for _, budget := range []float64{0, .1} {
		t.Run(map[bool]string{true: "allowed", false: "blocked"}[budget > 0], func(t *testing.T) {
			var models []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				var req jx.M
				json.NewDecoder(r.Body).Decode(&req)
				model := jx.Str(req, "model")
				models = append(models, model)
				answer := `{"sufficient":true,"selected":[{"topic":"Право","evidence":"подали иск в суд"},{"topic":"Маркетинг и реклама","evidence":"Рекламные агентства"}]}`
				if model == "qwen/qwen3.7-flash" {
					if !jx.Equal(jx.Map(req, "provider")["order"], []string{"alibaba/us-east-1"}) || jx.Map(req, "provider")["max_price"] == nil {
						t.Error("lost provider preference or price cap")
					}
					answer = `{"verified":[{"tag":"business.legal","matches":true,"basis":"subject","aspect":"Судебный иск по долгу","evidence":"подали иск в суд"},{"tag":"business.marketing","matches":false,"basis":"context","aspect":"Отрасль истца","evidence":""}]}`
				}
				json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
			}))
			defer srv.Close()
			u := taxonomyTestUser()
			u.Ctx = context.Background()
			u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
			u.LLM.LoadSettings = func() llm.Settings {
				s := llm.Preset("free")
				s.DailyBudgetUSD = budget
				s.Roles[llm.RoleClassifyVerify] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "qwen/qwen3.7-flash"}, ProviderOrder: []string{"alibaba/us-east-1"}}
				return s
			}
			tags := u.classifyContent("", "Рекламные агентства подали иск в суд о взыскании долга.", false)
			if budget > 0 {
				if !jx.Equal(tags, []string{"business.legal"}) || len(models) != 2 || models[1] != "qwen/qwen3.7-flash" {
					t.Fatalf("tags=%v models=%v", tags, models)
				}
			} else {
				if tags != nil || len(models) != 1 {
					t.Fatalf("paid call escaped budget: tags=%v models=%v", tags, models)
				}
			}
			before := len(models)
			if u.classifyContent("", "Sensitive publication", true) != nil || len(models) != before {
				t.Fatal("sensitive content sent externally")
			}
		})
	}
}
