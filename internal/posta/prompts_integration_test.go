package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"posta/internal/db"
	"posta/internal/llm"
)

// POSTA_TEST_PG must point to a disposable database, never the live Posta database.
func TestPromptSaveResetReachesModel(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS kv (key text primary key, value jsonb not null, expires_at bigint not null default 0, updated_at timestamptz not null default now())`); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `DELETE FROM kv WHERE key='admin:prompts'`); err != nil {
		t.Fatal(err)
	}
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		received = body.Messages[0].Content[0].Text
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"lat\":55.75,\"lon\":37.62}"}}]}`)
	}))
	defer srv.Close()
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	router.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.Roles[llm.RoleText] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "test:free"}}
		return s
	}
	a := &App{DB: d, LLM: router}
	u := a.User(ctx, "test", nil)
	edited := "TEST-PROMPT: {{name}}"
	if err := a.PromptSave(ctx, "geo", edited, false); err != nil {
		t.Fatal(err)
	}
	if got := u.prompt("geo", map[string]string{"name": "Москва"}); got != "TEST-PROMPT: Москва" {
		t.Fatalf("saved prompt: %q", got)
	}
	if _, ok := u.geoAsk("Москва"); !ok {
		t.Fatal("model request failed")
	}
	if received != "TEST-PROMPT: Москва" {
		t.Fatalf("model received %q", received)
	}
	if err := a.PromptSave(ctx, "geo", "", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := u.geoAsk("Москва"); !ok {
		t.Fatal("model request after reset failed")
	}
	if strings.Contains(received, "TEST-PROMPT") || !strings.Contains(received, "Москва") || !strings.Contains(received, "широту") {
		t.Fatalf("reset prompt sent to model: %q", received)
	}
}
