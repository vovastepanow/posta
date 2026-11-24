package posta

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// Exercise the real review loop, including RSS parsing, the preliminary
// classifier gate, collection passes, evidence persistence, and promotion.
// Network/model results are deterministic fakes; PostgreSQL is real.
func TestAutopilotCandidateToActiveLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
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

	const topic, owner, source = "Тест жизненного цикла RSS", "autopilot-lifecycle-owner", "rss:https://example.org/lifecycle.xml"
	key := autopilotTopicKey(topic)
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(context.Background(), d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_post_evidence", "source_autopilot_collection_passes", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotShadowScope, key, autopilotShadowKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	if err := registerAutopilotCandidate(ctx, d, job, source, at); err != nil {
		t.Fatal(err)
	}
	var feed strings.Builder
	feed.WriteString("<rss><channel><title>Lifecycle</title>")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&feed, "<item><title>Case %d</title><link>https://example.org/p/%d</link><pubDate>%s</pubDate><description>Useful analysis of a real case %d with results and numbers.</description></item>", i, i, at.Format(time.RFC1123Z), i)
	}
	feed.WriteString("</channel></rss>")
	fetch := func(url string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: []byte(feed.String())}, URL: url}
	}
	preliminary := 0
	evaluate := func(_ jx.M, posts []jx.M, _ string) *sourceEvaluation {
		preliminary++
		if len(posts) < 10 {
			t.Errorf("expected at least ten parsed posts, got %d", len(posts))
		}
		return &sourceEvaluation{Score: 95}
	}
	assessCalls := 0
	assess := func(work context.Context, sourceID string, posts []jx.M) error {
		assessCalls++
		for i := range posts {
			evidence := autopilotPostEvidence{
				PostHash: fmt.Sprintf("post-%d", i), ContentHash: fmt.Sprintf("unique-content-%d", i),
				PublishedAt: at.Add(-time.Minute).Unix(), ObservedAt: time.Now().Unix(),
				Relevant: true, Eligible: true,
			}
			if err := recordAutopilotPostEvidence(work, d, job, sourceID, evidence); err != nil {
				return err
			}
		}
		return nil
	}
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	verifyState := func(expected string) {
		t.Helper()
		var state string
		if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&state); err != nil || state != expected {
			t.Fatalf("state=%q, want %q, err=%v", state, expected, err)
		}
	}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		if err := a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy); err != nil {
			return err
		}
		verifyState("probation")
		// Reproduce a second successful collection window without sleeping.
		// Adjust the old pass to the previous day, leaving the source due now.
		previous := at.Add(-24 * time.Hour).Unix()
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_collection_passes SET slot=$4,fetched_at=$5 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
			job.Scope, key, source, previous/int64(sharedCollectionInterval.Seconds()), previous); err != nil {
			return err
		}
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_sources SET next_check_at=0 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source); err != nil {
			return err
		}
		return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy)
	}); err != nil {
		t.Fatal(err)
	}
	verifyState("active")
	if preliminary != 1 || assessCalls != 2 {
		t.Fatalf("preliminary calls=%d assessments=%d; want 1 and 2", preliminary, assessCalls)
	}

	// Repeated checks of the same feed must not inflate evidence or collection
	// passes, revert the active source, or rerun preliminary classification.
	// Force the source due without advancing the real-world collection slot.
	for cycle := 0; cycle < 8; cycle++ {
		if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_sources SET next_check_at=0 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source); err != nil {
			t.Fatal(err)
		}
		if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
			return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy)
		}); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		verifyState("active")
		stats, err := autopilotEvidenceWindow(ctx, d.Pool, job.Scope, key, source, at.Add(-30*24*time.Hour), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if stats.Evaluated != 12 || stats.Eligible != 12 || stats.FreshUnique != 12 || stats.SuccessfulPasses != 2 {
			t.Fatalf("cycle %d inflated evidence or passes: %+v", cycle, stats)
		}
	}
	if preliminary != 1 || assessCalls != 10 {
		t.Fatalf("repeated cycles: preliminary=%d, assessments=%d; want 1 and 10", preliminary, assessCalls)
	}

	// The previously qualified publisher stops publishing. A successful
	// dated feed is evidence of inactivity, not thematic mismatch.
	old := at.Add(-60 * 24 * time.Hour)
	staleFeed := strings.ReplaceAll(feed.String(), at.Format(time.RFC1123Z), old.Format(time.RFC1123Z))
	if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_sources SET next_check_at=0 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source); err != nil {
		t.Fatal(err)
	}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, func(raw string) FetchResultURL {
			return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: []byte(staleFeed)}, URL: raw}
		}, evaluate, assess, policy)
	}); err != nil {
		t.Fatal(err)
	}
	verifyState("cooldown")
	var reason, resume string
	if err := d.Pool.QueryRow(ctx, `SELECT reason,resume_state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&reason, &resume); err != nil || reason != "no_recent_publications" || resume != "candidate" {
		t.Fatalf("stale publisher reason=%s resume=%s err=%v", reason, resume, err)
	}
}

// A mixed publisher must survive a low source score and graduate only after
// a genuinely new useful publication and another collection pass.
func TestAutopilotYieldMixedSourceLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
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

	const topic, owner, source = "Тест полезной отдачи смешанной ленты", "autopilot-yield-owner", "rss:https://example.org/yield.xml"
	key := autopilotTopicKey(topic)
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(context.Background(), d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_post_evidence", "source_autopilot_collection_passes", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public:shadow' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotShadowScope, key, autopilotShadowKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotShadowKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	if err := registerAutopilotCandidate(ctx, d, job, source, at); err != nil {
		t.Fatal(err)
	}
	var feed strings.Builder
	feed.WriteString("<rss><channel><title>Lifecycle</title>")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&feed, "<item><title>Case %d</title><link>https://example.org/p/%d</link><pubDate>%s</pubDate><description>Useful analysis of a real case %d with results and numbers.</description></item>", i, i, at.Format(time.RFC1123Z), i)
	}
	feed.WriteString("</channel></rss>")
	fetch := func(url string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: []byte(feed.String())}, URL: url}
	}
	preliminary := 0
	evaluate := func(_ jx.M, posts []jx.M, _ string) *sourceEvaluation {
		preliminary++
		if len(posts) < 10 {
			t.Errorf("expected at least ten parsed posts, got %d", len(posts))
		}
		return &sourceEvaluation{Score: 30}
	}
	assessCalls := 0
	assess := func(work context.Context, sourceID string, posts []jx.M) error {
		assessCalls++
		for i := range posts {
			published := at.Add(-time.Minute).Unix()
			if assessCalls == 2 && i == 0 {
				published = at.Add(-time.Second).Unix()
			}
			postID := fmt.Sprintf("post-%d", i)
			if assessCalls == 2 && i == 0 {
				postID = "live-new"
			}
			evidence := autopilotPostEvidence{
				PostHash: postID, ContentHash: postID,
				PublishedAt: published, ObservedAt: time.Now().Unix(),
				Relevant: i < 3, Eligible: i < 3,
			}
			if err := recordAutopilotPostEvidence(work, d, job, sourceID, evidence); err != nil {
				return err
			}
		}
		return nil
	}
	policy := sourcePolicy{YieldEnabled: true, YieldMinLive: 1, MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	verifyState := func(expected string) {
		t.Helper()
		var state string
		if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&state); err != nil || state != expected {
			t.Fatalf("state=%q, want %q, err=%v", state, expected, err)
		}
	}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		if err := a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy); err != nil {
			return err
		}
		verifyState("probation")
		// Reproduce a second successful collection window without sleeping.
		// Adjust the old pass to the previous day, leaving the source due now.
		previous := at.Add(-24 * time.Hour).Unix()
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_decisions SET at=$4 WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND old_state='candidate' AND new_state='probation'`, job.Scope, key, source, previous-1); err != nil {
			return err
		}
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_collection_passes SET slot=$4,fetched_at=$5 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
			job.Scope, key, source, previous/int64(sharedCollectionInterval.Seconds()), previous); err != nil {
			return err
		}
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_sources SET next_check_at=0 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source); err != nil {
			return err
		}
		return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy)
	}); err != nil {
		t.Fatal(err)
	}
	verifyState("active")
	if preliminary != 1 || assessCalls != 2 {
		t.Fatalf("preliminary calls=%d assessments=%d; want 1 and 2", preliminary, assessCalls)
	}

}

// The recorded positive probe enters the limited pilot without the global switch.
func TestAutopilotYieldPilotLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
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

	const topic, owner, source = "Тест ограниченного пилота отдачи", "autopilot-yield-pilot-owner", "rss:https://example.org/yield-pilot.xml"
	key := autopilotTopicKey(topic)
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(context.Background(), d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3}, 2)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_post_evidence", "source_autopilot_collection_passes", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Second)
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotAutoScope, key, autopilotAutoKind, topic, "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	job.MaxProbation = 3
	// Persist a positive counterfactual result from a completed post
	// assessment. The ordinary strict source policy remains selected.
	probe := jx.Marshal(jx.M{"preliminary_post_probe": jx.M{"has_eligible": true, "policy_version": TopicProfileVersion}})
	if err := registerAutopilotCandidateWithEvidence(ctx, d, job, source, "discovered", probe, at); err != nil {
		t.Fatal(err)
	}
	var feed strings.Builder
	feed.WriteString("<rss><channel><title>Lifecycle</title>")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&feed, "<item><title>Case %d</title><link>https://example.org/p/%d</link><pubDate>%s</pubDate><description>Useful analysis of a real case %d with results and numbers.</description></item>", i, i, at.Format(time.RFC1123Z), i)
	}
	feed.WriteString("</channel></rss>")
	fetch := func(url string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: []byte(feed.String())}, URL: url}
	}
	preliminary := 0
	evaluate := func(_ jx.M, posts []jx.M, _ string) *sourceEvaluation {
		preliminary++
		if len(posts) < 10 {
			t.Errorf("expected at least ten parsed posts, got %d", len(posts))
		}
		return &sourceEvaluation{Score: 30}
	}
	assessCalls := 0
	assess := func(work context.Context, sourceID string, posts []jx.M) error {
		assessCalls++
		for i := range posts {
			published := at.Add(-time.Minute).Unix()
			if assessCalls == 2 && i == 0 {
				published = at.Add(-time.Second).Unix()
			}
			postID := fmt.Sprintf("post-%d", i)
			if assessCalls == 2 && i == 0 {
				postID = "live-new"
			}
			evidence := autopilotPostEvidence{
				PostHash: postID, ContentHash: postID,
				PublishedAt: published, ObservedAt: time.Now().Unix(),
				Relevant: i < 3, Eligible: i < 3,
			}
			if err := recordAutopilotPostEvidence(work, d, job, sourceID, evidence); err != nil {
				return err
			}
		}
		return nil
	}
	policy := sourcePolicy{YieldPilotEnabled: true, YieldMinLive: 1, MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	verifyState := func(expected string) {
		t.Helper()
		var state string
		if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&state); err != nil || state != expected {
			t.Fatalf("state=%q, want %q, err=%v", state, expected, err)
		}
	}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
		if err := a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy); err != nil {
			return err
		}
		verifyState("probation")
		// Reproduce a second successful collection window without sleeping.
		// Adjust the old pass to the previous day, leaving the source due now.
		previous := at.Add(-24 * time.Hour).Unix()
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_decisions SET at=$4 WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND old_state='candidate' AND new_state='probation'`, job.Scope, key, source, previous-1); err != nil {
			return err
		}
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_collection_passes SET slot=$4,fetched_at=$5 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
			job.Scope, key, source, previous/int64(sharedCollectionInterval.Seconds()), previous); err != nil {
			return err
		}
		if _, err := d.Pool.Exec(work, `UPDATE source_autopilot_sources SET next_check_at=0 WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source); err != nil {
			return err
		}
		return a.autopilotReviewShadowCandidatesWith(work, job, 1, 85, fetch, evaluate, assess, policy)
	}); err != nil {
		t.Fatal(err)
	}
	verifyState("active")
	var pilot bool
	if err := d.Pool.QueryRow(ctx, `SELECT evidence @> '{"yield_pilot":true}'::jsonb FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, key, source).Scan(&pilot); err != nil || !pilot {
		t.Fatalf("pilot marker=%v err=%v", pilot, err)
	}
	if preliminary != 1 || assessCalls != 2 {
		t.Fatalf("preliminary calls=%d assessments=%d; want 1 and 2", preliminary, assessCalls)
	}

}
