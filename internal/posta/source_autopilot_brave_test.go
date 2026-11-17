package posta

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestAutopilotBraveCooldownAvoidsNetwork(t *testing.T) {
	bravePause.Lock()
	old := bravePause.until
	bravePause.until = time.Now().Add(time.Hour)
	bravePause.Unlock()
	t.Cleanup(func() { bravePause.Lock(); bravePause.until = old; bravePause.Unlock() })
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("unexpected fetch") })
	u := (&App{}).User(ctx, "", nil)
	r := u.autopilotBraveSearch("site:youtube.com science")
	if r.Status != 429 || calls != 0 {
		t.Fatalf("status=%d requests=%d", r.Status, calls)
	}
}

func TestAutopilotBraveRateLimitHonorsRetryAfter(t *testing.T) {
	bravePause.Lock()
	old := bravePause.until
	bravePause.until = time.Time{}
	bravePause.Unlock()
	t.Cleanup(func() { bravePause.Lock(); bravePause.until = old; bravePause.Unlock() })
	u := (&App{}).User(context.Background(), "", nil)
	calls := 0
	fetch := func(string, int64, time.Duration, string) FetchResultURL {
		calls++
		return FetchResultURL{FetchResult: FetchResult{Status: 429, RetryAfter: "172800"}}
	}
	start := time.Now()
	u.autopilotBraveSearchWith("site:youtube.com cats", fetch)
	bravePause.Lock()
	until := bravePause.until
	bravePause.Unlock()
	if until.Before(start.Add(48 * time.Hour)) {
		t.Fatalf("Retry-After ignored: %v", until)
	}
	u.autopilotBraveSearchWith("site:vk.com cats", fetch)
	if calls != 1 {
		t.Fatalf("cooldown made %d requests", calls)
	}
}

func TestAutopilotBraveRetryDelay(t *testing.T) {
	at := time.Unix(1800000000, 0).UTC()
	if got := autopilotBraveRetryAt(at, ""); !got.Equal(at.Add(34 * time.Minute)) {
		t.Fatalf("default cooldown: %v", got)
	}
	until := at.Add(2 * time.Hour)
	if got := autopilotBraveRetryAt(at, until.Format(http.TimeFormat)); got.Before(until) {
		t.Fatalf("date Retry-After ignored: %v", got)
	}
}
