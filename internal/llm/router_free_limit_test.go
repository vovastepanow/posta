package llm

import (
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
