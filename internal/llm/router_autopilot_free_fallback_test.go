package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceAutopilotFreeFailoverOnModel403(t *testing.T) {
	for _, tc := range []struct {
		name string
		optIn bool
		primaryStatus int
		wantCalls int
		wantSuccess bool
	}{
		{"explicit public autopilot uses free router", true, http.StatusForbidden, 2, true},
		{"ordinary requests keep configured model policy", false, http.StatusForbidden, 1, false},
		{"invalid API credential must not trigger failover", true, http.StatusUnauthorized, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body struct{ Model string `json:"model"` }
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				models = append(models, body.Model)
				w.Header().Set("Content-Type", "application/json")
				if body.Model != "openrouter/free" {
					w.WriteHeader(tc.primaryStatus)
					_, _ = w.Write([]byte(`{"error":{"message":"model access denied"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":95,\"reason\":\"fits\"}"}}],"usage":{"prompt_tokens":3,"completion_tokens":6}}`))
			}))
			defer server.Close()
			r := NewRouter(NewOllama("http://127.0.0.1:1"), NewOpenRouter(server.URL, "test", "", ""))
			r.LoadSettings = func() Settings {
				cfg := Preset("free")
				cfg.Roles[RoleSmart] = RoleConfig{Primary: Target{"openrouter", "blocked-model:free"}, Fallback: Off}
				return cfg
			}
			gates := 0
			ctx := WithCallGate(context.Background(), func(context.Context, Target) error {
				gates++
				return nil
			})
			if tc.optIn {
				ctx = WithSourceAutopilotFreeFallback(ctx)
			}
			response, err := r.Generate(ctx, &Request{Role: RoleSmart, Prompt: "Source quality", MaxTokens: 80})
			if tc.wantSuccess && (err != nil || response == nil || response.Model != "openrouter/free") {
				t.Fatalf("free failover response=%+v err=%v", response, err)
			}
			if !tc.wantSuccess && err == nil {
				t.Fatalf("unexpected success: %+v", response)
			}
			if len(models) != tc.wantCalls || gates != tc.wantCalls {
				t.Fatalf("models=%v gates=%d want calls=%d", models, gates, tc.wantCalls)
			}
			if tc.wantSuccess && models[1] != "openrouter/free" {
				t.Fatalf("free router not used: %v", models)
			}
		})
	}
}
