package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"posta/internal/jx"
	"posta/internal/llm"
	"testing"
)

// Exact saved public/control responses, not fabricated semantic judgments.
// A successful replay proves acceptance behavior; it is not a fresh model run.
func TestClassificationSavedLiveResponses(t *testing.T) {
	raw, err := os.ReadFile("../../tools/fixtures/classification_live_replay.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID, Caption, Description string
		Replies, Expected        []string
		PaidVerifier             bool
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				if calls >= len(c.Replies) {
					t.Error("unexpected extra model call")
					http.Error(w, "unexpected", 400)
					return
				}
				if c.PaidVerifier {
					var req jx.M
					json.NewDecoder(r.Body).Decode(&req)
					want := "nvidia/nemotron-3.5-super-vl:free"
					if calls > 0 {
						want = "qwen/qwen3.7-flash"
					}
					if jx.Str(req, "model") != want {
						t.Errorf("wrong route: got %s want %s", jx.Str(req, "model"), want)
					}
				}
				answer := c.Replies[calls]
				calls++
				json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
			}))
			defer srv.Close()
			u := taxonomyTestUser()
			u.Ctx = context.Background()
			u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
			u.LLM.LoadSettings = func() llm.Settings {
				s := llm.Preset("free")
				if c.PaidVerifier {
					s.DailyBudgetUSD = .1
					s.Roles[llm.RoleClassifyVerify] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "qwen/qwen3.7-flash"}}
				}
				return s
			}
			tags := u.classifyContent(c.Description, c.Caption, false)
			if (tags == nil) != (c.Expected == nil) || !jx.Equal(tags, c.Expected) {
				t.Fatalf("got %v want %v reason=%s", tags, c.Expected, u.classificationReason)
			}
			if calls != len(c.Replies) {
				t.Fatalf("used %d of %d saved responses", calls, len(c.Replies))
			}
		})
	}
}
