package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPersonalBlogTypeIndependentFromTopic(t *testing.T) {
	for _, tc := range []struct {
		mode, kind string
		allowed    bool
	}{
		{personalBlogAny, personalBlogUnknown, true},
		{personalBlogAny, personalBlogYes, true},
		{personalBlogOnly, personalBlogYes, true},
		{personalBlogOnly, personalBlogNo, false},
		{personalBlogOnly, personalBlogUnknown, false},
		{personalBlogExclude, personalBlogYes, false},
		{personalBlogExclude, personalBlogNo, true},
		{personalBlogExclude, personalBlogUnknown, false},
	} {
		if got := personalBlogAllowed(tc.mode, tc.kind); got != tc.allowed {
			t.Errorf("mode=%q type=%q: allowed=%t, want %t", tc.mode, tc.kind, got, tc.allowed)
		}
	}
	for _, value := range []string{"yes", "no", "unknown", "", "person"} {
		if value == "yes" || value == "no" {
			if personalBlogLabelFromReview(value) != value {
				t.Fatalf("failed to preserve valid type %q", value)
			}
		} else if personalBlogLabelFromReview(value) != personalBlogUnknown {
			t.Fatalf("unknown type %q must never be treated as not-personal", value)
		}
	}
}

func TestCombinedReviewParsesBlogClassification(t *testing.T) {
	response := `{"text_score":91,"image_score":87,"reason":"relevant","personal_blog":"yes"}`
	e := parseCombinedSourceReview(response, false)
	if e == nil || e.PersonalBlog != personalBlogYes || e.Score != 87 {
		t.Fatalf("personal blog was not detected in existing source model response: %+v", e)
	}
	e = parseCombinedSourceReview(`{"text_score":91,"image_score":87,"reason":"relevant"}`, false)
	if e == nil || e.PersonalBlog != personalBlogUnknown {
		t.Fatalf("old source model responses must remain valid with unknown type: %+v", e)
	}
}

func TestPersonalBlogPreferencePersistsByTopicPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
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
	const owner = "autopilot-personal-blog-filter-test"
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	app := &App{DB: d}
	if err := app.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	u := app.User(ctx, owner, nil)
	if err := u.SavePersonalBlogMode("SMM и блогинг", personalBlogOnly); err != nil {
		t.Fatal(err)
	}
	if err := u.SavePersonalBlogMode("Единоборства", personalBlogExclude); err != nil {
		t.Fatal(err)
	}
	if u.personalBlogMode("smm") != personalBlogOnly || u.personalBlogMode("Единоборства") != personalBlogExclude {
		t.Fatal("personal blog preferences were not stored independently by topic")
	}
	if err := u.SavePersonalBlogMode("smm", personalBlogAny); err != nil {
		t.Fatal(err)
	}
	if u.personalBlogMode("SMM и блогинг") != personalBlogAny ||
		u.personalBlogMode("Единоборства") != personalBlogExclude {
		t.Fatal("removing one topic's restriction changed another topic")
	}
	// Existing saved items use exactly the same filter as new admissions.
	key := autopilotTopicKey("Единоборства")
	for _, source := range []string{"rss:https://personal-blog-filter.example/yes", "rss:https://personal-blog-filter.example/no"} {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,policy_version,first_seen_at,evidence)
			VALUES('public',$1,$2,'probation',$3,1,jsonb_build_object('personal_blog',$4::text))`,
			key, source, TopicProfileVersion, source[len(source)-2:]); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(ctx, `DELETE FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id=$2`, key, source)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_sources SET evidence='{"personal_blog":"yes"}' WHERE scope='public' AND topic_key=$1 AND source_id='rss:https://personal-blog-filter.example/yes'`, key); err != nil {
		t.Fatal(err)
	}
	filter, err := u.poolPersonalBlogFilter("Единоборства")
	if err != nil {
		t.Fatal(err)
	}
	if filter(jx.M{"source_key": "rss:https://personal-blog-filter.example/yes"}) ||
		!filter(jx.M{"source_key": "rss:https://personal-blog-filter.example/no"}) ||
		filter(jx.M{"source_key": "unknown"}) {
		t.Fatal("saved pool personal-blog exclusion not enforced")
	}
	if err := u.SavePersonalBlogMode("Единоборства", personalBlogAny); err != nil {
		t.Fatal(err)
	}
	filter, err = u.poolPersonalBlogFilter("Единоборства")
	if err != nil || !filter(jx.M{"source_key": "rss:https://personal-blog-filter.example/yes"}) {
		t.Fatal("re-enabling blogs should show retained posts")
	}
	if err := u.SavePersonalBlogMode("SMM и блогинг", "invalid"); err == nil {
		t.Fatal("invalid filter mode was accepted")
	}
}
