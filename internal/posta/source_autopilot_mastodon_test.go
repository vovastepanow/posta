package posta

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotMastodonRequiresExplicitPublicSafety(t *testing.T) {
	feed := "https://mastodon.social/@cats.rss"
	if name, ok := autopilotMastodonFeed(feed); !ok || name != "cats" {
		t.Fatal("valid public feed rejected")
	}
	for _, bad := range []string{"https://mastodon.social.evil.test/@cats.rss", feed + "?token=secret", "https://mastodon.social/@../cats.rss"} {
		if _, ok := autopilotMastodonFeed(bad); ok {
			t.Fatalf("invalid feed accepted: %s", bad)
		}
	}
	data := []byte(`[{"content":"A public post about friendly cats today","created_at":"2026-11-15T12:00:00Z","url":"https://mastodon.social/@cats/1","sensitive":false,"visibility":"public","account":{"id":"123","url":"https://mastodon.social/@cats"}},{"content":"Sensitive post about friendly cats today","created_at":"2026-11-15T12:00:00Z","url":"https://mastodon.social/@cats/2","sensitive":true,"visibility":"public","account":{"id":"123","url":"https://mastodon.social/@cats"}},{"content":"Unknown safety about friendly cats today","created_at":"2026-11-15T12:00:00Z","url":"https://mastodon.social/@cats/3","visibility":"public","account":{"id":"123","url":"https://mastodon.social/@cats"}},{"content":"Private status about friendly cats today","created_at":"2026-11-15T12:00:00Z","url":"https://mastodon.social/@cats/4","sensitive":false,"visibility":"private","account":{"id":"123","url":"https://mastodon.social/@cats"}},{"content":"Another account about friendly cats today","created_at":"2026-11-15T12:00:00Z","url":"https://mastodon.social/@other/1","sensitive":false,"visibility":"public","account":{"id":"124","url":"https://mastodon.social/@other"}}]`)
	posts, err := autopilotMastodonSafeStatuses(data, feed)
	if err != nil || len(posts) != 1 {
		t.Fatalf("safe posts=%v err=%v", posts, err)
	}
	searchPosts, err := autopilotMastodonSafeStatuses(data, "")
	if err != nil || len(searchPosts) != 2 {
		t.Fatalf("public search must reject private, sensitive and unknown posts: %v %v", searchPosts, err)
	}
	if searchBackendCacheKey("Mastodon", "cats", 1, false) == searchBackendCacheKey("Mastodon", "cats", 1, true) {
		t.Fatal("public autopilot and personal search share a cache key")
	}
	c := PostSearchAuthor(posts[0])
	if c == nil || c.ID != feed {
		t.Fatalf("unexpected source: %#v", c)
	}
	if _, ok := autopilotCandidateID(*c); !ok {
		t.Fatal("Mastodon feed cannot enter pipeline")
	}
	if _, err = autopilotMastodonSafeStatuses([]byte(`{}`), feed); err == nil {
		t.Fatal("invalid status response accepted")
	}
}

func TestAutopilotMastodonReviewAndProjectionPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner, topic, feed = "autopilot-mastodon-test", "Тема теста Mastodon", "https://mastodon.social/@autopilottest.rss"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	key := autopilotTopicKey(topic)
	for _, table := range []string{"source_autopilot_collection_passes", "source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
		var keep []jx.M
		for _, e := range c.RSS {
			if jx.Str(e, "id") != feed {
				keep = append(keep, e)
			}
		}
		c.RSS = keep
		return true
	})
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%v err=%v", job, err)
	}
	job.MaxProbation = 1
	source := SourceIDFor(PlatformRSS, feed)
	if err = a.autopilotShadowSearchWith(ctx, job, func(context.Context) ([]DiscoveryCandidate, error) {
		return []DiscoveryCandidate{{Platform: PlatformRSS, ID: feed}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var rows []string
	for i := 0; i < 3; i++ {
		rows = append(rows, fmt.Sprintf(`{"content":"Public post about interesting cats %d today","created_at":%q,"url":"https://mastodon.social/@autopilottest/%d","sensitive":false,"visibility":"public","account":{"id":"123","url":"https://mastodon.social/@autopilottest"}}`, i, time.Now().UTC().Format(time.RFC3339), i))
	}
	rows = append(rows, fmt.Sprintf(`{"content":"Sensitive post about interesting cats today","created_at":%q,"url":"https://mastodon.social/@autopilottest/9","sensitive":true,"visibility":"public","account":{"id":"123","url":"https://mastodon.social/@autopilottest"}}`, time.Now().UTC().Format(time.RFC3339)))
	body := []byte("[" + strings.Join(rows, ",") + "]")
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	if err = a.autopilotReviewShadowCandidatesWith(ctx, job, 1, 85, func(string) FetchResultURL {
		return FetchResultURL{URL: "https://mastodon.social/api/v1/accounts/123/statuses", FetchResult: FetchResult{OK: true, Bytes: body}}
	}, func(jx.M, []jx.M, string) *sourceEvaluation { return &sourceEvaluation{Score: 90} }, func(_ context.Context, got string, posts []jx.M) error {
		if got != source || len(posts) != 3 {
			t.Fatalf("source=%s safe posts=%d", got, len(posts))
		}
		return nil
	}, policy); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotProjections(ctx, job); err != nil {
		t.Fatal(err)
	}
	active, probation, err := a.autopilotProjectedCounts(ctx, topic)
	if err != nil || active != 0 || probation != 1 {
		t.Fatalf("counts %d/%d err=%v", active, probation, err)
	}
}

func TestAutopilotMastodonSearchAndLookupRequireBudget(t *testing.T) {
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("budget exhausted") })
	u := (&App{}).User(ctx, "", nil)
	if result := u.autopilotMastodonListing("https://mastodon.social/@cats.rss"); result.OK || calls != 1 {
		t.Fatalf("lookup=%+v calls=%d", result, calls)
	}
	status := ""
	u.discoverySearchObservation = func(_, _, value string) { status = value }
	if got := u.autopilotFindMastodon("cats"); len(got) != 0 || calls != 2 || status != "unavailable" {
		t.Fatalf("search=%v calls=%d status=%s", got, calls, status)
	}
}
