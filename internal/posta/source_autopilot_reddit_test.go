package posta

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAutopilotRedditIdentityAndSafety(t *testing.T) {
	feed := "https://www.reddit.com/r/cats/.rss"
	if id, ok := autopilotPublicRSSID(feed); !ok || id != "rss:"+feed {
		t.Fatalf("public Reddit feed rejected: %q %v", id, ok)
	}
	for _, raw := range []string{feed + "?token=secret", "https://www.reddit.com/r/Cats/.rss", "https://www.reddit.com/r/cats/"} {
		if _, ok := autopilotPublicRSSID(raw); ok {
			t.Errorf("unsafe or noncanonical feed accepted: %s", raw)
		}
	}
	if autopilotPublicRedditFeed("https://www.reddit.com.evil.test/r/cats/.rss") {
		t.Fatal("lookalike host accepted as Reddit")
	}
	if sharedSourceEligible(PlatformRSS, map[string]any{"id": feed}) {
		t.Fatal("Reddit must not enter shared cache")
	}
	if !autopilotPinnablePublicRSS(map[string]any{"id": feed}) || autopilotPinnablePublicRSS(map[string]any{"id": feed, "query": "private"}) {
		t.Fatal("public pin validation failed")
	}
	data := []byte(`{"data":{"children":[{"data":{"display_name":"Cats","subreddit_type":"public","over18":false}},{"data":{"display_name":"Adult","subreddit_type":"public","over18":true}},{"data":{"display_name":"Unknown","subreddit_type":"public"}},{"data":{"display_name":"Null","subreddit_type":"public","over18":null}}]}}`)
	clean, err := autopilotRedditSafeListing(data, "over18")
	if err != nil {
		t.Fatal(err)
	}
	got, err := discoveryRedditCommunities(clean)
	if err != nil || len(got) != 1 || got[0].ID != feed {
		t.Fatalf("communities=%v err=%v", got, err)
	}
	if _, err = autopilotRedditSafeListing([]byte(`{"data":{}}`), "over18"); err == nil {
		t.Fatal("malformed listing accepted")
	}
}

func TestAutopilotRedditPostSafetyAndRedirect(t *testing.T) {
	feed := "https://www.reddit.com/r/cats/.rss"
	listingURL := "https://www.reddit.com/r/cats/new.json?limit=25"
	body := strings.Repeat("A sufficiently long public cat post. ", 5)
	item := func(id, flag string) string {
		return fmt.Sprintf(`{"data":{"title":"Cat","selftext":%q,"permalink":"/r/cats/comments/%s/post/","subreddit":"cats","created_utc":%d%s}}`, body, id, time.Now().Unix(), flag)
	}
	data := []byte(`{"data":{"children":[` + item("one", `,"over_18":false`) + `,` + item("two", `,"over_18":true`) + `,` + item("three", "") + `]}}`)
	posts, err := autopilotRedditListing(data, feed, listingURL)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%v err=%v", posts, err)
	}
	if _, err = autopilotRedditListing(data, feed, "https://example.org/r/cats/new.json"); err == nil {
		t.Fatal("unexpected redirect accepted")
	}
}

func TestAutopilotRedditRequiresNetworkBudget(t *testing.T) {
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("budget exhausted") })
	u := (&App{}).User(ctx, "", nil)
	status := ""
	u.discoverySearchObservation = func(_, _, value string) { status = value }
	if got := u.autopilotFindReddit("cats"); len(got) != 0 || calls != 1 || status != "unavailable" {
		t.Fatalf("candidates=%v calls=%d status=%q", got, calls, status)
	}
}
