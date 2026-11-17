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

func TestClassificationUsesTailInSelectionAndVerification(t *testing.T) {
	caption := strings.Repeat("Вводный текст. ", 240) + "Главная ошибка при запуске сайта и рекламы"
	description := "Описание в конце также должно сохраниться."
	wantText := caption + "\n" + description
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		var req jx.M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		messages := jx.Maps(req, "messages")
		parts := jx.Maps(messages[len(messages)-1], "content")
		prompt := jx.Str(parts[0], "text")
		if !strings.HasSuffix(prompt, wantText) {
			t.Errorf("call %d lost full content", calls)
		}
		answer := `{"sufficient":true,"selected":[{"topic":"Маркетинг и реклама","evidence":"запуске сайта и рекламы"}]}`
		if strings.HasPrefix(prompt, "Проверь каждую") {
			answer = `{"verified":[{"tag":"business.marketing","matches":true,"basis":"subject","aspect":"Публикация раскрывает действие из определения темы","evidence":"запуске сайта и рекламы"}]}`
		}
		json.NewEncoder(w).Encode(jx.M{"choices": []any{jx.M{"message": jx.M{"content": answer}}}})
	}))
	defer srv.Close()
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
	if got := u.classifyContent(description, caption, false); !jx.Equal(got, []string{"business.marketing"}) {
		t.Fatalf("tail evidence was not accepted: %v (%s)", got, u.classificationReason)
	}
	if calls != 2 {
		t.Fatalf("want selection and verification, got %d calls", calls)
	}
}

func TestOversizedClassificationPreservesPendingWithoutModel(t *testing.T) {
	u := taxonomyTestUser() // nil LLM: an attempted model call would fail the test
	text := strings.Repeat("я", contentClassificationMaxPromptBytes/2+1)
	result := jx.M{"relevance": 92, "tags": []string{"finance.taxes"}}
	u.applyContentClassification(result, "", text, false)
	if !jx.Bool(result, "tags_classification_pending") || jx.Str(result, "tags_classification_reason") != "input_too_large" || jx.Int(result, "relevance") != 92 {
		t.Fatal(result)
	}
	if len(jx.ToStrs(result["tags"])) != 0 {
		t.Fatal("oversized input received a topic verdict")
	}
	saved := analysisRecord(result, jx.M{}, "Налоги", "text")
	if !jx.Bool(saved, "tags_classification_pending") || jx.Str(saved, "tags_classification_reason") != "input_too_large" {
		t.Fatal("pending reason lost on save", saved)
	}
}

func TestVerificationRejectsOversizedPromptIncludingDefinitions(t *testing.T) {
	for _, retry := range []bool{false, true} {
		u := taxonomyTestUser()
		labels := []contentLabel{{"business.marketing", "Маркетинг", strings.Repeat("я", 25000)}}
		if got := u.verifySelectedLabelsAttempt("Короткий текст", labels, []string{"business.marketing"}, retry); got != nil || u.classificationReason != "input_too_large" {
			t.Fatalf("retry=%v: tags=%v reason=%s", retry, got, u.classificationReason)
		}
	}
}
