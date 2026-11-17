package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPersonalSourceOverridePersistsAndIsIsolatedPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"autopilot-personal-a", "autopilot-personal-b"} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		if err = a.User(ctx, id, nil).SaveSettings(jx.M{"topic": "Тема проверки"}); err != nil {
			t.Fatal(err)
		}
	}
	first := a.User(ctx, "autopilot-personal-a", nil)
	if err = first.SourceOverrideSet("rss:https://example.org/feed", false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"autopilot-personal-a", true}, {"autopilot-personal-b", false}} {
		fresh := a.User(ctx, tc.id, nil)
		if got := sourceExcludedByOverride(fresh.sourceOverridesForTopic("Тема проверки"), "rss:https://example.org/feed"); got != tc.want {
			t.Fatalf("user %s: got %v", tc.id, got)
		}
	}
	if err = first.SourceOverrideSet("rss:https://example.org/feed", true); err != nil {
		t.Fatal(err)
	}
	if sourceExcludedByOverride(first.sourceOverridesForTopic("Тема проверки"), "rss:https://example.org/feed") {
		t.Fatal("source remains excluded after enabling")
	}
	if err = first.SaveSettings(jx.M{"topic": "C++"}); err != nil {
		t.Fatal(err)
	}
	if err = first.SourceOverrideSet("rss:https://example.org/another", false); err != nil {
		t.Fatal(err)
	}
	if !sourceExcludedByOverride(first.sourceOverridesForTopic("C++"), "rss:https://example.org/another") || sourceExcludedByOverride(first.sourceOverridesForTopic("C"), "rss:https://example.org/another") {
		t.Fatal("personal overrides merged distinct topic profiles")
	}
}
