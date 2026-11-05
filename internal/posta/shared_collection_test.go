package posta

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/secret"
)

func TestSharedCollectionAllowsOnlyPublicSources(t *testing.T) {
	cases := []struct {
		platform, id, query string
		want                bool
	}{
		{PlatformTelegram, "publicchannel", "", true},
		{PlatformRSS, "https://spacey.space/@spaceflight.rss", "", true},
		{PlatformRSS, "https://example.org/feed?token=secret", "", false},
		{PlatformRSS, "https://name:secret@example.org/feed", "", false},
		{PlatformRSS, "https://discord.com/channels/123456/123456", "", false},
		{PlatformRSS, "https://www.instagram.com/example/", "", false},
		{PlatformRSS, "https://example.org/feed", "private search", false},
		{PlatformVK, "public123", "", true},
		{PlatformOK, "1234567890", "", true},
		{PlatformTikTok, "example", "", true},
	}
	for _, tc := range cases {
		if got := sharedSourceEligible(tc.platform, jx.M{"id": tc.id, "query": tc.query}); got != tc.want {
			t.Errorf("%s %s: got %v", tc.platform, tc.id, got)
		}
	}
}

func TestPersonalSourceCacheIsSeparated(t *testing.T) {
	a := personalSourcePollKey("user-a", PlatformVK, "public1")
	b := personalSourcePollKey("user-b", PlatformVK, "public1")
	if a == b || a == sharedSourceKey(PlatformVK, "public1") {
		t.Fatal("personal source caches must not be shared between users")
	}
}

func TestVKTokenKeyIsUserScoped(t *testing.T) {
	a := context.WithValue(context.Background(), vkUserContextKey{}, strings.Repeat("a", 32))
	b := context.WithValue(context.Background(), vkUserContextKey{}, strings.Repeat("b", 32))
	if vkTokenKey(a) == "" || vkTokenKey(a) == vkTokenKey(b) || vkTokenKey(context.Background()) != "" {
		t.Fatal("VK tokens must be bound to one user")
	}
}

func TestSharedCollectionQueuePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
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
	a := &App{DB: d, Box: secret.FromKey([32]byte{1})}
	ctxA := a.User(ctx, strings.Repeat("a", 32), nil).Ctx
	ctxB := a.User(ctx, strings.Repeat("b", 32), nil).Ctx
	a.SaveVKTokens(ctxA, jx.M{"access_token": "first", "expires_at": now() + 3600})
	a.SaveVKTokens(ctxB, jx.M{"access_token": "second", "expires_at": now() + 3600})
	if a.VKAccessToken(ctxA) != "first" || a.VKAccessToken(ctxB) != "second" || a.VKAccessToken(ctx) != "" {
		t.Fatal("VK accounts leaked across users")
	}
	a.catalog = &Catalog{app: a}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	topic := a.TopicRegistry()[0].Title
	key := TopicNorm(topic)
	if _, err = d.Pool.Exec(ctx, `DELETE FROM topic_collection_queue`); err != nil {
		t.Fatal(err)
	}
	a.RequestSharedCollection(ctx, topic, 1)
	a.RequestSharedCollection(ctx, topic, 10)
	var n, priority int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*),max(priority) FROM topic_collection_queue WHERE topic_key=$1`, key).Scan(&n, &priority); err != nil || n != 1 || priority != 10 {
		t.Fatalf("queue count=%d priority=%d err=%v", n, priority, err)
	}
	// A crashed worker's expired lease is eligible again, and repeated requests
	// for an in-flight topic remain coalesced.
	_, err = d.Pool.Exec(ctx, `UPDATE topic_collection_queue SET lease_until=$2,lease_id='crashed' WHERE topic_key=$1`, key, now()-1)
	if err != nil {
		t.Fatal(err)
	}
	// Avoid network traffic while exercising the claim and completion path.
	a.catalog.mu.Lock()
	a.catalog.cur.Telegram = nil
	a.catalog.cur.RSS = nil
	a.catalog.cur.VK = nil
	a.catalog.cur.OK = nil
	a.catalog.cur.TikTok = nil
	a.catalog.cur.Boards = nil
	a.catalog.cur.DvachTopicBoards = nil
	a.catalog.mu.Unlock()
	a.collectNextSharedTopic(ctx)
	var requested, done, lease int64
	if err = d.Pool.QueryRow(ctx, `SELECT requested_at,last_done,lease_until FROM topic_collection_queue WHERE topic_key=$1`, key).Scan(&requested, &done, &lease); err != nil || requested != 0 || done == 0 || lease != 0 {
		t.Fatalf("completion requested=%d done=%d lease=%d err=%v", requested, done, lease, err)
	}
	a.RequestSharedCollection(ctx, topic, 10)
	if err = d.Pool.QueryRow(ctx, `SELECT requested_at FROM topic_collection_queue WHERE topic_key=$1`, key).Scan(&requested); err != nil || requested != 0 {
		t.Fatalf("fresh job requeued: %d %v", requested, err)
	}
	_, err = d.Pool.Exec(ctx, `UPDATE topic_collection_queue SET last_done=$2 WHERE topic_key=$1`, key, now()-int64((sharedCollectionInterval+time.Second).Seconds()))
	if err != nil {
		t.Fatal(err)
	}
	a.RequestSharedCollection(ctx, topic, 10)
	if err = d.Pool.QueryRow(ctx, `SELECT requested_at FROM topic_collection_queue WHERE topic_key=$1`, key).Scan(&requested); err != nil || requested == 0 {
		t.Fatalf("stale job not queued: %d %v", requested, err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); a.collectNextSharedTopic(ctx) }()
	}
	workers.Wait()
	var attempts int
	if err = d.Pool.QueryRow(ctx, `SELECT attempts FROM topic_collection_queue WHERE topic_key=$1`, key).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("job claimed more than once: attempts=%d err=%v", attempts, err)
	}
}
