package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotPersonalPinSurvivesPublicRemovalPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	const topic = "Тема личного закрепления"
	const feed = "https://example.org/test-personal-pin-feed"
	for _, id := range []string{"autopilot-pin-a", "autopilot-pin-b"} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		if err = a.User(ctx, id, nil).SaveSettings(jx.M{"topic": topic}); err != nil {
			t.Fatal(err)
		}
	}
	remove := func(c *SourceDB) bool {
		var keep []jx.M
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") != feed {
				keep = append(keep, entry)
			}
		}
		c.RSS = keep
		return true
	}
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		remove(c)
		c.RSS = append(c.RSS, jx.M{"id": feed, "topics": []any{topic}, "autopilot_managed": true, "autopilot_profiles": jx.M{autopilotTopicKey(topic): jx.M{"topic": topic, "state": "probation"}}})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.UpdateCatalog(context.Background(), remove) })
	first, second := a.User(ctx, "autopilot-pin-a", nil), a.User(ctx, "autopilot-pin-b", nil)
	if err = first.RemoveTopicSource(PlatformRSS, feed, "test"); err != nil {
		t.Fatal(err)
	}
	if !sourceExcludedByOverride(first.sourceOverridesForTopic(topic), "rss:"+feed) || sourceExcludedByOverride(second.sourceOverridesForTopic(topic), "rss:"+feed) || len(a.Catalog().RSS) == 0 {
		t.Fatal("personal removal affected shared source")
	}
	if err = first.SourceOverrideSet("rss:"+feed, true); err != nil {
		t.Fatal(err)
	}
	if err = first.SourceVote("reject", jx.M{"platform": PlatformRSS, "id": feed, "source_id": "rss:" + feed, "name": "test"}, "other"); err != nil {
		t.Fatal(err)
	}
	if !sourceExcludedByOverride(first.sourceOverridesForTopic(topic), "rss:"+feed) || sourceExcludedByOverride(second.sourceOverridesForTopic(topic), "rss:"+feed) {
		t.Fatal("vote was not confined to its owner")
	}
	if err = first.SourceOverrideSet("rss:"+feed, true); err != nil {
		t.Fatal(err)
	}
	if err = first.SourcePersonalPin("rss:"+feed, true); err != nil {
		t.Fatal(err)
	}
	if _, err = a.UpdateCatalog(ctx, remove); err != nil {
		t.Fatal(err)
	}
	has := func(u *U, topic string) bool {
		for _, entry := range u.catalogWithPersonalPins(topic).RSS {
			if jx.Str(entry, "id") == feed && u.CatalogEntryMatchesTopic(entry, topic, false) {
				return true
			}
		}
		return false
	}
	if !has(first, topic) || has(second, topic) || has(first, topic+" уточнение") {
		t.Fatal("pin was not isolated to owner and topic")
	}
	if err = first.SourcePersonalPin("rss:"+feed, false); err != nil {
		t.Fatal(err)
	}
	if has(first, topic) {
		t.Fatal("unpinned source remains in personal catalog")
	}
}
