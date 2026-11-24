package posta

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"fmt"
	"os"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/llm"
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

// A real RSS response passes through the production review function and
// PostgreSQL evidence tables. Assessment is deterministic because no model
// credentials are installed in this isolated CI environment.
func TestAutopilotLiveRSSReviewLifecycle(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" || os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires isolated PostgreSQL and POSTA_AUTOPILOT_LIVE=1")
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
	const owner, topic = "autopilot-live-rss-review", "Technology live source review"
	const feed = "https://hnrss.org/frontpage"
	source := "rss:" + feed
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
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileAutopilotShadowEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), 2*time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	u := a.User(ctx, "", nil)
	live := u.RSSGet(feed, rssMaxBytes, 15*time.Second, HTTPUA)
	if !live.OK || !rssIsFeed(rssLoad(live.Bytes)) {
		t.Fatalf("live RSS fetch failed: HTTP %d: %v", live.Status, live.Error)
	}
	parsed, err := RSSParse(live.Bytes, 20, "", feed, true, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	fresh := autopilotFreshRSSSamples(parsed, time.Now().Unix())
	if len(fresh) < 10 {
		t.Fatalf("live RSS provides only %d fresh items; need 10 for lifecycle", len(fresh))
	}
	if err := registerAutopilotCandidate(ctx, d, job, source, time.Now()); err != nil {
		t.Fatal(err)
	}
	fetch := func(raw string) FetchResultURL {
		if raw != feed {
			t.Errorf("unexpected source %q", raw)
		}
		return live
	}
	classified, assessments := 0, 0
	evaluate := func(_ jx.M, posts []jx.M, _ string) *sourceEvaluation {
		classified++
		return &sourceEvaluation{Score: 95}
	}
	assess := func(work context.Context, sourceID string, posts []jx.M) error {
		assessments++
		for i, post := range posts {
			key := CandidatePostKey(post)
			if key == "" {
				return fmt.Errorf("empty post key at index %d", i)
			}
			e := autopilotPostEvidence{
				PostHash: repeatDigest(key), ContentHash: repeatDigest(key),
				PublishedAt: jx.Int(post, "posted_at"), ObservedAt: time.Now().Unix(),
				Relevant: true, Eligible: true,
			}
			if err := recordAutopilotPostEvidence(work, d, job, sourceID, e); err != nil {
				return err
			}
		}
		return nil
	}
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	verify := func(want string) error {
		var got string
		if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&got); err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("state=%s want=%s", got, want)
		}
		return nil
	}
	err = runAutopilotLeased(ctx, d, job, 2*time.Minute, 20*time.Second, 50, 50, 50, 50, func(work context.Context) error {
		if err := a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy); err != nil {
			return err
		}
		if err := verify("probation"); err != nil {
			return err
		}
		at := time.Now().Add(-24 * time.Hour).Unix()
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_collection_passes SET slot=$4,fetched_at=$5 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
			job.Scope, key, source, at/int64(sharedCollectionInterval.Seconds()), at); err != nil {
			return err
		}
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_sources SET next_check_at=0 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source); err != nil {
			return err
		}
		return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := verify("active"); err != nil {
		t.Fatal(err)
	}
	stats, err := autopilotEvidenceWindow(ctx, d.Pool, job.Scope, key, source, time.Now().Add(-30*24*time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Evaluated < 10 || stats.SuccessfulPasses != 2 || classified != 1 || assessments != 2 {
		t.Fatalf("unexpected evidence: %+v; classified=%d assessments=%d", stats, classified, assessments)
	}
	t.Logf("LIVE LIFECYCLE: feed=%s fresh=%d evaluated=%d passes=%d state=active", strings.TrimPrefix(source, "rss:"), len(fresh), stats.Evaluated, stats.SuccessfulPasses)
}

// Deliver one real public RSS article through the protected auto-mode pool
// transaction. The classification is fixed because CI has no AI credentials;
// everything after admission uses production PostgreSQL and catalog logic.
func TestAutopilotLiveRSSAutoDelivery(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" || os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires isolated PostgreSQL and POSTA_AUTOPILOT_LIVE=1")
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
	const topic, owner, feed = "Live technology dispatch", "autopilot-live-delivery", "https://hnrss.org/frontpage"
	key, source := autopilotTopicKey(topic), "rss:"+feed
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", ""))
	a := &App{DB: d, LLM: router, Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
		var keep []jx.M
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") != feed {
				keep = append(keep, entry)
			}
		}
		c.RSS = keep
		return true
	})
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := a.User(ctx, owner, nil).SaveSettings(jx.M{
		"topic": "Other topic", "active_sources_only": false, "text_posts": true,
		"topic_formats": jx.M{TopicNorm(topic): jx.M{"media": "optional", "text": "optional"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "auto", PostsPerDay: 3, HorizonDays: 2,
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), 2*time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("auto job=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	live := a.User(ctx, "", nil).RSSGet(feed, rssMaxBytes, 15*time.Second, HTTPUA)
	if !live.OK || !rssIsFeed(rssLoad(live.Bytes)) {
		t.Fatalf("live RSS HTTP %d: %v", live.Status, live.Error)
	}
	posts, err := RSSParse(live.Bytes, 20, "", feed, true, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	var article jx.M
	for _, post := range autopilotFreshRSSSamples(posts, time.Now().Unix()) {
		if CandidatePostKey(post) != "" && AnalysisText(post) != "" &&
			autopilotPublicPostURL(jx.Str(post, "url"), false) && ContentImageKey(post) == "" && !PostItemHasVideo(post) {
			article = post
			break
		}
	}
	if article == nil {
		t.Fatal("live RSS had no eligible text-only article")
	}
	analysis := jx.M{
		"advertising": false, "relevance": 95, "topic_match": true,
		"model": "deterministic-test", "description": "Technology article",
		"why_relevant": "Technology topic", "tags": []any{},
	}
	err = runAutopilotLeased(ctx, d, job, 2*time.Minute, 20*time.Second, 50, 50, 50, 50, func(work context.Context) error {
		if err := registerAutopilotCandidate(work, d, job, source, time.Now()); err != nil {
			return err
		}
		if err := transitionAutopilotSource(work, d, job, source, "candidate", "probation", "live_test_preliminary", []byte(`{}`), time.Now(), time.Now()); err != nil {
			return err
		}
		if err := transitionAutopilotSource(work, d, job, source, "probation", "active", "live_test_verified", []byte(`{}`), time.Now(), time.Now()); err != nil {
			return err
		}
		if err := a.reconcileAutopilotProjections(work, job); err != nil {
			return err
		}
		if err := a.saveAutopilotContentResult(work, job, source, article, analysis); err != nil {
			return err
		}
		if err := a.deliverAutopilotTopicContent(work, job); err != nil {
			return err
		}
		return a.deliverAutopilotTopicContent(work, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	var results, pool, admissions int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_content_results WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&results); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1`, owner).Scan(&pool); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_admissions WHERE user_id=$1 AND topic_key=$2 AND accepted`, owner, key).Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE DELIVERY: source=%s results=%d pool=%d admissions=%d repeated_delivery=2", source, results, pool, admissions)
	if results != 1 || pool != 1 || admissions != 1 {
		t.Fatalf("live delivery stalled: results=%d pool=%d admissions=%d", results, pool, admissions)
	}
}

// Compare both extractors on exactly the same live HTML. This is a bounded
// public connectivity and parser check, NOT a semantic precision benchmark:
// no search provider key or AI credentials are used in this workflow.
func TestAutopilotLiveSearchLinkComparison(t *testing.T) {
	if os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	u := (&App{}).User(ctx, "", nil)
	cases := []struct {
		query, platform string
	}{
		{"site:t.me продвижение telegram каналов", PlatformTelegram},
		{"site:vk.com уход за домашними кошками", PlatformVK},
		{"site:youtube.com/channel astrophotography tutorials", PlatformRSS},
	}
	available, found := 0, 0
	for _, tc := range cases {
		if ctx.Err() != nil {
			break
		}
		res := u.autopilotPublisherSearch(tc.query, tc.platform)
		if !autopilotPublisherSearchResponseValid(res) {
			t.Logf("LIVE SEARCH: query=%q unavailable HTTP=%d", tc.query, res.Status)
			continue
		}
		p, _ := url.Parse(res.URL)
		available++
		old := map[string]bool{}
		for _, candidate := range SourceLinkedSources(string(res.Bytes)) {
			if candidate.Platform == tc.platform {
				old[candidate.ID] = true
			}
		}
		newIDs := []string{}
		for _, candidate := range autopilotSearchLinkedCandidates(res, tc.platform) {
			newIDs = append(newIDs, candidate.ID)
		}
		found += len(newIDs)
		t.Logf("LIVE SEARCH: query=%q provider=%s legacy=%d direct=%d ids=%v", tc.query, p.Host, len(old), len(newIDs), newIDs[:min(len(newIDs), 8)])
	}
	if available == 0 {
		t.Skip("anonymous search provider unavailable; no live comparison can be claimed")
	}
	if found == 0 {
		t.Fatal("search returned accessible documents but no supported direct publisher hyperlinks; inspect live provider markup")
	}
}

// Exercise actual anonymous native endpoints without relying on any
// external search index. A failure is not a negative topic assessment.
func TestAutopilotLiveNativePublisherSearch(t *testing.T) {
	if os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	u := (&App{}).User(ctx, "", nil)
	t.Run("YouTube", func(t *testing.T) {
		got, err := u.autopilotNativeYouTubeSearch("astrophotography tutorials")
		t.Logf("NATIVE YOUTUBE: candidates=%d error=%v", len(got), err)
		if err != nil || len(got) == 0 {
			t.Skip("public native search unavailable or empty in this environment")
		}
		for _, c := range got {
			if _, ok := canonicalYouTubeFeed(c.ID); !ok {
				t.Fatalf("invalid channel candidate: %q", c.ID)
			}
		}
	})
	t.Run("Bluesky", func(t *testing.T) {
		got, err := u.autopilotSearchBlueskyPosts("photography")
		t.Logf("NATIVE BLUESKY: assessable text posts=%d error=%v", len(got), err)
		if err != nil || len(got) == 0 {
			t.Skip("public post search unavailable or no assessable text in this environment")
		}
		for _, post := range got {
			source := jx.Str(post, "source_key")
			if !autopilotPostMatchesSource(source, post) {
				t.Fatalf("invalid Bluesky author attribution")
			}
		}
	})
	t.Run("Rutube", func(t *testing.T) {
		got := u.autopilotFindRutube("астрофотография")
		t.Logf("NATIVE RUTUBE: candidates=%d", len(got))
		if len(got) == 0 {
			t.Skip("native search unavailable or empty in this environment")
		}
		for _, c := range got {
			if RutubeChannelID(c.ID) == "" {
				t.Fatalf("invalid Rutube candidate %q", c.ID)
			}
		}
	})
}

// Inspect live search evidence and real feed accessibility separately from
// source admission. Public titles help inspect search quality, but do not
// substitute for the production multimodal classifier or ground truth.
func TestAutopilotLiveNativeYouTubeTopicEvidence(t *testing.T) {
	if os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	u := (&App{}).User(ctx, "", nil)
	searches, feeds, usable := 0, 0, 0
	for _, query := range []string{"social media marketing case studies", "astrophotography tutorials", "C# programming"} {
		if ctx.Err() != nil {
			break
		}
		got, err := u.autopilotNativeYouTubeSearch(query)
		t.Logf("YT TOPIC: query=%q candidates=%d error=%v", query, len(got), err)
		if err != nil || len(got) == 0 {
			continue
		}
		searches++
		for _, c := range got[:min(len(got), 5)] {
			t.Logf("YT EVIDENCE: query=%q channel=%q name=%q video=%q matching=%d", query, c.ID, jx.Str(c.Info, "title"), jx.Str(c.Info, "search_video_title"), jx.Int(c.Info, "matching_videos"))
		}
		for _, c := range got[:min(len(got), 2)] {
			if ctx.Err() != nil {
				break
			}
			res := u.RSSGet(c.ID, rssMaxBytes, 8*time.Second, HTTPUA)
			feeds++
			if !res.OK || !rssIsFeed(rssLoad(res.Bytes)) {
				t.Logf("YT FEED: channel=%q status=%d unavailable", c.ID, res.Status)
				if res.Status == 429 {
					break
				}
				continue
			}
			posts, err := RSSParse(res.Bytes, 20, "", res.URL, true, true, 0)
			if err != nil {
				t.Logf("YT FEED: channel=%q parse_failed", c.ID)
				continue
			}
			samples := autopilotSourceQualitySamples(posts, time.Now().Unix())
			if len(samples) > 0 {
				usable++
			}
			var titles []string
			for _, post := range samples[:min(len(samples), 3)] {
				titles = append(titles, trimRunes(strings.SplitN(AnalysisText(post), "\n", 2)[0], 120))
			}
			t.Logf("YT FEED: channel=%q samples=%d titles=%q", c.ID, len(samples), titles)
		}
	}
	t.Logf("YT SUMMARY: successful_searches=%d attempted_feeds=%d usable_feeds=%d", searches, feeds, usable)
	if searches == 0 {
		t.Skip("native YouTube unavailable")
	}
	if usable == 0 {
		t.Skip("search works, but no inspected YouTube feed returned usable samples in this environment")
	}
}

// Exercise the production Telegram adapter using an unambiguous search
// intent rather than an acronym. Check independent public channel pages.
func TestAutopilotLiveTelegramIndex(t *testing.T) {
	if os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	u := (&App{}).User(ctx, "", nil)
	got, err := u.autopilotTelegramIndexRequest("продвижение в социальных сетях")
	t.Logf("TELEGRAM INDEX: candidates=%d error=%v", len(got), err)
	if err != nil || len(got) == 0 {
		t.Skip("directory unavailable or no public channels for this query")
	}
	for _, c := range got {
		t.Logf("TELEGRAM CANDIDATE: id=%q title=%q", c.ID, jx.Str(c.Info, "title"))
	}
	usable, recent := 0, 0
	for _, c := range got {
		if ctx.Err() != nil {
			break
		}
		HostThrottle("t.me", 1500*time.Millisecond)
		r := u.RSSGet("https://t.me/s/"+c.ID, rssMaxBytes, 10*time.Second, BrowserUA)
		if !r.OK {
			t.Logf("TELEGRAM FEED: id=%q status=%d", c.ID, r.Status)
			continue
		}
		posts, err := autopilotPublicSocialPosts(PlatformTelegram, c.ID, r)
		t.Logf("TELEGRAM FEED: id=%q posts=%d error=%v", c.ID, len(posts), err)
		for _, post := range posts[:min(3,len(posts))] {
			t.Logf("TELEGRAM SAMPLE: id=%q url=%q posted=%d media=%t text=%q",c.ID,jx.Str(post,"url"),jx.Int(post,"posted_at"),PostItemHasMedia(post),trimRunes(AnalysisText(post),180))
		}
		if err == nil && len(posts) > 0 {
			usable++
			if len(autopilotFreshRSSSamples(posts,time.Now().Unix()))>0 {
				recent++
			}
		}
	}
	t.Logf("TELEGRAM SUMMARY: candidates=%d usable_feeds=%d fresh_feeds=%d",len(got),usable,recent)
	if usable == 0 {
		t.Skip("search works, but sampled channel pages unavailable")
	}
}


func TestAutopilotLiveBlueskyAuthorFallback(t *testing.T) {
	if os.Getenv("POSTA_AUTOPILOT_LIVE") != "1" {
		t.Skip("requires POSTA_AUTOPILOT_LIVE=1")
	}
	ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second)
	defer cancel()
	u:=(&App{}).User(ctx,"",nil)
	_,globalErr:=u.autopilotBlueskySearchData("software engineering")
	t.Logf("BLUESKY GLOBAL: error=%v",globalErr)
	posts,err:=autopilotBlueskyAuthorFallback("software engineering",time.Now().Unix(),func(raw string)([]byte,error){
		p,_:=url.Parse(raw)
		body,err:=u.searchGetJSON(raw)
		t.Logf("BLUESKY AUTHOR API: endpoint=%s bytes=%d error=%v",p.Path,len(body),err)
		return body,err
	})
	t.Logf("BLUESKY FALLBACK: safe_recent_text_posts=%d error=%v",len(posts),err)
	if err!=nil || len(posts)==0 {
		t.Skip("author fallback could not produce independently assessable recent text in this environment")
	}
	for _,post:=range posts{
		if !autopilotPostMatchesSource(jx.Str(post,"source_key"),post){
			t.Fatal("foreign author returned from fallback")
		}
	}
}
