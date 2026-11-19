package llm

import (
	"context"
	"encoding/json"
	"errors"
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
				w.Header().Set("Content-Type", "application/json")
				// OpenRouter may inspect the model catalog before the next
				// completion. Its GET is independently budget-gated.
				if req.Method == http.MethodGet && req.URL.Path == "/models" {
					_, _ = w.Write([]byte(`{"data":[]}`))
					return
				}
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
			// A catalog lookup gets its own gate charge in addition to
			// the completion calls. Never allow an unmetered model request.
			if len(models) != tc.wantCalls || gates < tc.wantCalls || gates > tc.wantCalls+1 {
				t.Fatalf("models=%v gates=%d want calls=%d (+ possible catalog gate)", models, gates, tc.wantCalls)
			}
			if tc.wantSuccess && models[1] != "openrouter/free" {
				t.Fatalf("free router not used: %v", models)
			}
		})
	}
}

func TestSourceAutopilotPaidNemotronFailoverIsScopedAndBudgetGated(t *testing.T) {
	for _, tc := range []struct {
		name string
		freeStatus int
		optIn bool
		freeOnly bool
		blockPaid bool
		wantPaid bool
	}{
		{"free 403 can use paid Nemotron", http.StatusForbidden, true, false, false, true},
		{"free 429 can use paid Nemotron", http.StatusTooManyRequests, true, false, false, true},
		{"ordinary requests cannot spend", http.StatusForbidden, false, false, false, false},
		{"free-only requests cannot spend", http.StatusForbidden, true, true, false, false},
		{"invalid credentials cannot trigger paid reserve", http.StatusUnauthorized, true, false, false, false},
		{"database gate rejects exhausted paid reserve", http.StatusForbidden, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/chat/completions" {
					http.NotFound(w, req)
					return
				}
				var body struct{ Model string `json:"model"` }
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Errorf("decode: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				models = append(models, body.Model)
				w.Header().Set("Content-Type", "application/json")
				if body.Model == "nvidia/nemotron-3.5-super-vl:free" {
					w.WriteHeader(tc.freeStatus)
					_, _ = w.Write([]byte(`{"error":{"message":"free-models-per-day limit reached"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":90,\"reason\":\"on-topic\"}"}}],"usage":{"prompt_tokens":30,"completion_tokens":12,"cost":0.00001}}`))
			}))
			defer server.Close()
			r := NewRouter(NewOllama("http://127.0.0.1:1"), NewOpenRouter(server.URL, "test", "", ""))
			r.LoadSettings = func() Settings {
				s := Preset("free")
				s.Roles[RoleSmart] = RoleConfig{Primary: FreeVision, Fallback: Off}
				return s // default $0 setting must not silently bypass the scoped gate
			}
			ctx := context.Background()
			if tc.optIn {
				ctx = WithSourceAutopilotPaidNemotronFallback(ctx)
			}
			ctx = WithCallGate(ctx, func(_ context.Context, target Target) error {
				if tc.blockPaid && !target.Free() {
					return errors.New("source autopilot budget exhausted")
				}
				return nil
			})
			response, err := r.Generate(ctx, &Request{Role: RoleSmart, Prompt: "Judge source", FreeOnly: tc.freeOnly})
			if tc.wantPaid && (err != nil || response == nil || response.Model != "nvidia/nemotron-3.5-super-vl") {
				t.Fatalf("paid reserve response=%+v err=%v", response, err)
			}
			if !tc.wantPaid && err == nil {
				t.Fatalf("unexpected paid reserve response=%+v", response)
			}
			paidCalls := 0
			for _, model := range models {
				if model == "nvidia/nemotron-3.5-super-vl" {
					paidCalls++
				}
			}
			if tc.wantPaid && paidCalls != 1 || !tc.wantPaid && paidCalls != 0 {
				t.Fatalf("paid model calls=%d; models=%v", paidCalls, models)
			}
		})
	}
}
