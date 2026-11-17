package posta

import (
	"context"
	"errors"
	"testing"
)

func TestAutopilotTumblrCandidates(t *testing.T) {
	page := `https://Cats.tumblr.com/post/1 https://cats.tumblr.com/post/2 https://www.tumblr.com/ https://-bad.tumblr.com/ https://dogs.tumblr.com/post/1`
	got := autopilotTumblrCandidates(page, "животные")
	if len(got) != 2 || got[0].ID != "https://cats.tumblr.com/rss" || got[1].ID != "https://dogs.tumblr.com/rss" {
		t.Fatalf("unexpected blogs: %#v", got)
	}
	for _, c := range got {
		if _, ok := autopilotCandidateID(c); !ok {
			t.Fatalf("feed cannot enter pipeline: %s", c.ID)
		}
	}
}

func TestAutopilotTumblrRequiresBudget(t *testing.T) {
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("budget exhausted") })
	u := (&App{}).User(ctx, "", nil)
	status := ""
	u.discoverySearchObservation = func(_, _, result string) { status = result }
	if got := u.autopilotFindTumblr("животные"); len(got) != 0 || calls != 1 || status != "unavailable" {
		t.Fatalf("candidates=%v calls=%d status=%s", got, calls, status)
	}
}
