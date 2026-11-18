package posta

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// Explicit opt-in; this reaches one public search endpoint and never calls a
// model or changes the production catalog.
func TestAutopilotLivePublicRSS(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" || os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1 and an isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner, topic = "autopilot-live-smoke", "Технологии"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 1, HorizonDays: 1}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	err = runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 1, 1, 4, 4, func(work context.Context) error {
		// Independent public publishers, not private accounts. Failure of
		// one feed does not masquerade as a systemic search outage.
		feeds := []string{
			"https://hnrss.org/frontpage",
			"https://feeds.bbci.co.uk/news/technology/rss.xml",
			"https://www.bing.com/news/search?q=technology&format=rss",
		}
		u := a.User(work, "", nil)
		var observations []string
		for _, feed := range feeds {
			if err := work.Err(); err != nil {
				return err
			}
			result := u.RSSGet(feed, rssMaxBytes, 15*time.Second, HTTPUA)
			if !result.OK || !rssIsFeed(rssLoad(result.Bytes)) {
				observations = append(observations, fmt.Sprintf("%s: status=%d error=%s", feed, result.Status, result.Error))
				continue
			}
			items := rssItems(rssLoad(result.Bytes))
			posts, parseErr := RSSParse(result.Bytes, 20, "", feed, true, true, 0)
			if parseErr != nil {
				observations = append(observations, fmt.Sprintf("%s: RSS parse error: %v", feed, parseErr))
				continue
			}
			valid := 0
			for _, post := range posts {
				if CandidatePostKey(post) != "" && jx.Str(post, "url") != "" && AnalysisText(post) != "" {
					valid++
				}
			}
			t.Logf("Public feed %s: HTTP %d; raw items=%d; Posta posts=%d; valid=%d",
				feed, result.Status, len(items), len(posts), valid)
			if valid > 0 {
				// Validate not just XML availability, but translation into
				// usable, deduplicatable Posta candidate items.
				return nil
			}
			observations = append(observations, fmt.Sprintf("%s: parsed feed has no usable Posta posts", feed))
		}
		return fmt.Errorf("no public RSS endpoint returned posts: %v", observations)
	})
	if err != nil {
		t.Fatal(err)
	}
}
