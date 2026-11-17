package posta

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAutopilotFetchFailsClosedWithoutBudget(t *testing.T) {
	base := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: "public"})
	for _, tc := range []struct {
		ctx  context.Context
		want string
	}{
		{base, "budget unavailable"},
		{context.WithValue(base, autopilotNetworkGateKey{}, func(context.Context) error { return errors.New("budget exhausted") }), "budget exhausted"},
	} {
		u := &U{App: &App{}, Ctx: tc.ctx}
		result := u.RSSGet("https://example.test/feed", 1024, time.Second, "")
		if result.OK || !strings.Contains(result.Error, tc.want) {
			t.Fatalf("unexpected fetch result: %+v", result)
		}
		if _, err := u.FetchPublicImage("https://example.test/image.jpg"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("image fetch bypassed budget: %v", err)
		}
		if _, err := u.searchGetJSON("https://example.test/search"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("JSON search bypassed budget: %v", err)
		}
	}
}

func TestPublicFetchHonorsCanceledUserContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u := &U{App: &App{}, Ctx: ctx}
	_, err := u.FetchPublic("https://example.test/feed", FetchOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
