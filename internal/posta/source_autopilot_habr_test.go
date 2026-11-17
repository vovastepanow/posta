package posta

import (
	"context"
	"errors"
	"testing"
)

func TestAutopilotHabrPublicFeeds(t *testing.T) {
	page := `<a href="/ru/hubs/python/">Python</a><a href="https://evil.example/ru/hubs/python/">Python</a><a href="/ru/hubs/java/">Java</a>`
	got := autopilotHabrCandidates(page, "python")
	if len(got) != 1 || got[0].ID != "https://habr.com/ru/rss/hubs/python/articles/all/" {
		t.Fatalf("unexpected public feeds: %#v", got)
	}
	if SourceIDFor(PlatformRSS, got[0].ID) != "rss:"+got[0].ID {
		t.Fatal("feed cannot be projected through legacy catalog IDs")
	}
}

func TestAutopilotHabrStopsWithoutNetworkBudget(t *testing.T) {
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error {
		calls++
		return errors.New("budget exhausted")
	})
	u := (&App{}).User(ctx, "", nil)
	var statuses []string
	u.discoverySearchObservation = func(_, _, result string) { statuses = append(statuses, result) }
	if got := u.autopilotFindHabr("python", 0); len(got) != 0 || calls == 0 || len(statuses) == 0 || statuses[0] != "unavailable" {
		t.Fatalf("results=%v budget calls=%d statuses=%v", got, calls, statuses)
	}
}
