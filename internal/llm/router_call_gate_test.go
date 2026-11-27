package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCallGateBlocksProviderAttempts(t *testing.T) {
	var calls atomic.Int32
	r := NewRouter(NewOllama("http://127.0.0.1:1"), NewOpenRouter("http://127.0.0.1:1", "test", "", ""))
	r.OnCall = func(Call) { calls.Add(1) }
	blocked := errors.New("budget exhausted")
	ctx := WithCallGate(context.Background(), func(context.Context, Target) error { return blocked })
	for _, fallback := range []bool{false, true} {
		_, err := r.try(ctx, &Request{Role: RoleSmart, Quiet: true}, Target{Provider: "ollama", Model: "test"}, fallback)
		if !errors.Is(err, blocked) {
			t.Fatalf("fallback=%v: %v", fallback, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("blocked calls reached provider: %d", calls.Load())
	}
}

type gateTestTransport func(*http.Request) (*http.Response, error)

func (f gateTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallGateChargesProviderRetry(t *testing.T) {
	var attempts, gates int
	or := NewOpenRouter("https://example.test", "test", "", "")
	or.client = &http.Client{Transport: gateTestTransport(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	r := NewRouter(NewOllama("http://127.0.0.1:1"), or)
	// This fixture intentionally tests a paid provider retry; authorize the
	// first attempt explicitly before exercising its independent call gate.
	r.LoadSettings = func() Settings {
		s := Preset("free")
		s.DailyBudgetUSD = 1
		return s
	}
	blocked := errors.New("budget exhausted")
	ctx := WithCallGate(context.Background(), func(context.Context, Target) error {
		gates++
		if gates > 1 {
			return blocked
		}
		return nil
	})
	_, err := r.try(ctx, &Request{Role: RoleSmart, Quiet: true, Logprobs: true}, Target{Provider: "openrouter", Model: "test"}, false)
	if !errors.Is(err, blocked) || attempts != 1 || gates != 2 {
		t.Fatalf("attempts=%d gates=%d err=%v", attempts, gates, err)
	}
}
