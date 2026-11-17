package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type classificationTransport func(*http.Request) (*http.Response, error)

func (f classificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenRouterErrorIncludesSafeProviderDetails(t *testing.T) {
	or := NewOpenRouter("https://example.invalid", "key", "", "")
	or.client.Transport = classificationTransport(func(*http.Request) (*http.Response, error) {
		body := `{"error":{"message":"Provider returned error","metadata":{"provider_name":"Baidu","provider_error_code":"tpm_rate_limit_exceeded","limit_source":"upstream_provider_shared_pool","raw":"private data"}}}`
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	_, err := or.Generate(context.Background(), "test:free", &Request{Role: RoleClassify})
	if err == nil || !strings.Contains(err.Error(), "code=tpm_rate_limit_exceeded") || strings.Contains(err.Error(), "private data") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClassificationBudgetFollowsActualTarget(t *testing.T) {
	free := Target{"openrouter", "example:free"}
	paid := Target{"openrouter", "example"}
	for _, tc := range []struct {
		name                        string
		primary, fallback           Target
		reserveOK                   bool
		wantCalls, wantReservations int
		wantError                   bool
	}{
		{"free ignores exhausted budget", free, Off, false, 1, 0, false},
		{"paid respects exhausted budget", paid, Off, false, 0, 1, true},
		{"paid falls back to free", paid, free, false, 1, 1, false},
		{"free failure cannot bypass paid budget", free, paid, false, 1, 1, true},
		{"paid reserves budget", paid, Off, true, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, reservations, settlements := 0, 0, 0
			or := NewOpenRouter("https://example.invalid", "key", "", "")
			or.client.Transport = classificationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				status, body := 200, `{"choices":[{"message":{"content":"ok"}}],"usage":{"cost":0.001}}`
				if tc.name == "free failure cannot bypass paid budget" {
					status, body = 403, `{"error":{"message":"denied"}}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			r := NewRouter(NewOllama(""), or)
			r.LoadSettings = func() Settings {
				s := Preset("free")
				s.DailyBudgetUSD = 1
				s.Roles[RoleClassify] = RoleConfig{Primary: tc.primary, Fallback: tc.fallback}
				return s
			}
			_, err := r.Generate(context.Background(), &Request{Role: RoleClassify, ReservePaid: func() (func(float64), bool) { reservations++; return func(float64) { settlements++ }, tc.reserveOK }})
			if (err != nil) != tc.wantError || calls != tc.wantCalls || reservations != tc.wantReservations {
				t.Fatalf("err=%v calls=%d reservations=%d", err, calls, reservations)
			}
			if tc.reserveOK && settlements != 1 {
				t.Fatalf("settlements=%d", settlements)
			}
		})
	}
}
