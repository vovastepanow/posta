package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOpenRouterDailyLimitResponse(t *testing.T) {
	r := &Router{}
	first := Target{"openrouter", "one:free"}
	other := Target{"openrouter", "two:free"}
	r.countFree(first, errors.New("OpenRouter: Rate limit exceeded: free-models-per-day (HTTP 429)"))
	if reason := r.blocked(other, &Request{}, Settings{}); !strings.Contains(reason, "суточного лимита") {
		t.Fatalf("daily limit did not block other free model: %q", reason)
	}
	r.freeDailyBlockedUntil = time.Now().Add(-time.Second)
	if reason := r.blocked(other, &Request{}, Settings{FreeDailyLimit: 1}); reason != "" {
		t.Fatalf("model remained blocked after retry time: %q", reason)
	}
}

func TestOpenRouterMinuteLimitDoesNotBlockOtherModels(t *testing.T) {
	r := &Router{}
	r.countFree(Target{"openrouter", "one:free"}, errors.New("429 free-models-per-min"))
	if reason := r.blocked(Target{"openrouter", "two:free"}, &Request{}, Settings{}); reason != "" {
		t.Fatalf("unrelated model blocked: %q", reason)
	}
}

func TestUpstreamTokenLimitUsesShortCooldown(t *testing.T) {
	r := &Router{}
	model := Target{"openrouter", "test:free"}
	r.countFree(model, errors.New("HTTP 429; code=tpm_rate_limit_exceeded; limit=upstream_provider_shared_pool"))
	remaining := time.Until(r.freeBlocked[model.Model])
	if remaining < time.Minute || remaining > 3*time.Minute {
		t.Fatalf("unexpected upstream cooldown: %s", remaining)
	}
}

func TestFreeQuotaReservedBeforeCall(t *testing.T) {
	r := &Router{}
	r.SetFreeUsed(1)
	if err := r.reserveFreeAttempt(context.Background(), FreeVision, 2); err != nil {
		t.Fatal(err)
	}
	if r.FreeUsed() != 2 || r.reserveFreeAttempt(context.Background(), FreeVision, 2) == nil {
		t.Fatal("configured cap was exceeded")
	}
	r.countFree(FreeVision, errors.New("HTTP 429"))
	if r.FreeUsed() != 2 {
		t.Fatal("attempt counted twice")
	}
	r.SetFreeUsed(0)
	r.ReserveFree = func(context.Context, string, int) (int, error) {
		return 0, errors.New("database unavailable")
	}
	if r.reserveFreeAttempt(context.Background(), FreeVision, 2) == nil || r.FreeUsed() != 0 {
		t.Fatal("unreserved provider call allowed")
	}
}
