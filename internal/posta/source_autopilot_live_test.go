package posta

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

// Run the production Bluesky parser against a live public API response,
// then use the real leased discovery writer and verify persisted rows.
// This test deliberately does not pretend to validate AI or production delivery.
func TestAutopilotLiveDiscoveryPersistence(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" || os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1 and an isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner = "autopilot-live-bluesky"
	const topic = "Posta live tech discovery"
	key := autopilotTopicKey(topic)
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "shadow", PostsPerDay: 2, HorizonDays: 2,
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err := a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), 2*time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	job.SearchQuery = "technology"
	endpoint := "https://public.api.bsky.app/xrpc/app.bsky.actor.searchActors?q=" + url.QueryEscape(job.SearchQuery) + "&limit=25"
	client := &http.Client{Timeout: 20 * time.Second}
	var status, hits, valid int
	err = runAutopilotLeased(ctx, d, job, 2*time.Minute, 20*time.Second, 50, 50, 50, 50, func(work context.Context) error {
		return a.autopilotShadowSearchWith(work, job, func(inner context.Context) ([]DiscoveryCandidate, error) {
			req, err := http.NewRequestWithContext(inner, http.MethodGet, endpoint, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", "Posta-Live-Smoke/1.0")
			response, err := client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("Bluesky live search: %w", err)
			}
			defer response.Body.Close()
			status = response.StatusCode
			if status != http.StatusOK {
				return nil, fmt.Errorf("Bluesky live search HTTP %d", status)
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
			if err != nil {
				return nil, err
			}
			var responseMeta struct {
				Actors []json.RawMessage `json:"actors"`
			}
			if err := json.Unmarshal(body, &responseMeta); err != nil {
				return nil, err
			}
			hits = len(responseMeta.Actors)
			candidates, err := discoveryBlueskyActors(body)
			if err != nil {
				return nil, err
			}
			for _, candidate := range candidates {
				if _, ok := autopilotCandidateID(candidate); ok {
					valid++
				}
			}
			return candidates, nil
		})
	})
	if err != nil {
		t.Fatalf("live discovery failed (HTTP %d): %v", status, err)
	}
	var stored, decisions, attributed int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND state='candidate'`, job.Scope, key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions WHERE scope=$1 AND topic_key=$2 AND reason='discovered'`, job.Scope, key).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_query_sources WHERE scope=$1 AND topic_key=$2 AND query=$3`, job.Scope, key, job.SearchQuery).Scan(&attributed); err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE DISCOVERY: Bluesky HTTP=%d search_hits=%d valid=%d stored=%d decisions=%d attributed=%d", status, hits, valid, stored, decisions, attributed)
	if hits == 0 || valid == 0 || stored == 0 || stored != decisions || stored != attributed {
		t.Fatalf("discovery funnel stalled: hits=%d valid=%d stored=%d decisions=%d attributed=%d", hits, valid, stored, decisions, attributed)
	}
}
