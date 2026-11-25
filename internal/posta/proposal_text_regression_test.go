package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestShortTextIdentityAndArticleHistory(t *testing.T) {
	a := &App{}
	text := jx.M{"text_post": true, "text": "SMM"}
	if key := ContentItemKey(text); key == "" || key == ContentItemKey(jx.M{"text_post": true, "text": "SEO"}) {
		t.Fatal("short text has no distinct identity")
	}
	article := jx.M{"text_post": true, "text": "Article", "url": "https://example.org/article", "article": jx.M{"title": "Title", "lead": "Lead"}}
	h := a.ContentHistoryRecord(article, nil)
	if !jx.Equal(h["article"], article["article"]) || ContentItemKey(h) != ContentItemKey(article) {
		t.Fatal("article metadata or key lost in history")
	}
}

func TestTextProposalAndMarkPostedPostgres(t *testing.T) {
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
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	uid := randHex(16)
	u := (&App{DB: d, Mem: NewMemCache(100)}).User(ctx, uid, nil)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	first := jx.M{"text_post": true, "text": "SMM", "pooled_at": now()}
	second := jx.M{"text_post": true, "text": "SEO", "pooled_at": now()}
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{first, second} }); err != nil {
		t.Fatal(err)
	}
	key := ContentItemKey(first)
	constraint := "test_mark_" + uid
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE history_items ADD CONSTRAINT `+constraint+` CHECK (user_id <> '`+uid+`')`); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(ctx, `ALTER TABLE history_items DROP CONSTRAINT IF EXISTS `+constraint)
	if u.ContentMarkPosted(key) != "" || len(u.Pool()) != 2 {
		t.Fatal("failed mark lost pool item")
	}
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE history_items DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	if u.ContentMarkPosted(key) != "liked" || len(u.Pool()) != 1 || len(u.History()["liked"]) != 1 {
		t.Fatal("mark did not commit")
	}
	if u.ContentMarkPosted(key) != "already" {
		t.Fatal("second mark should be idempotent")
	}
	if _, _, _, _, err := u.VotePool(ContentItemKey(second), "like", nil); err != nil {
		t.Fatal(err)
	}
	if len(u.History()["liked"]) != 2 || len(u.proposalPending()) != 0 {
		t.Fatal("distinct text records collided or entered media backfill")
	}
	if err := u.ProposalHide(ContentItemKey(second)); err != nil {
		t.Fatal(err)
	}
	for _, r := range u.History()["liked"] {
		if !jx.Bool(r, "proposal_hidden") {
			t.Fatal("hide did not persist")
		}
	}
}
