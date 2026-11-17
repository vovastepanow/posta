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

func TestAutopilotProjectionRespectsStateAndOverridePostgres(t *testing.T) {
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
	const owner = "autopilot-projection-test"
	const topic = "Тест проекции источника"
	const feed = "https://autopilottest.tumblr.com/rss"
	const conflictFeed = "https://example.org/autopilot-manual-conflict"
	key := autopilotTopicKey(topic)
	source := "rss:" + feed
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner) })
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, 1); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_overrides", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, autopilotAutoScope, key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, autopilotAutoScope, key)
		})
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
			var out []jx.M
			for _, e := range c.RSS {
				if jx.Str(e, "id") != feed && jx.Str(e, "id") != conflictFeed && jx.Str(e, "id") != "https://example.org/two" && jx.Str(e, "id") != "https://example.org/three" {
					out = append(out, e)
				}
			}
			c.RSS = out
			return true
		})
	})
	at := time.Now()
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim %+v %v", job, err)
	}
	job.MaxProbation = 3
	if err = a.autopilotShadowSearchWith(ctx, job, func(context.Context) ([]DiscoveryCandidate, error) {
		return []DiscoveryCandidate{{Platform: PlatformRSS, ID: feed}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.projectAutopilotSource(ctx, job, source, "probation"); !errors.Is(err, errAutopilotSourceChanged) {
		t.Fatalf("unadmitted candidate projected: %v", err)
	}
	date := at.UTC().Format(time.RFC1123Z)
	var items string
	for i := 0; i < 3; i++ {
		items += fmt.Sprintf(`<item><title>Post %d</title><link>https://example.org/post/%d</link><pubDate>%s</pubDate><description>Post body</description></item>`, i, i, date)
	}
	feedBytes := []byte(`<rss><channel><title>Test</title>` + items + `</channel></rss>`)
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	if err = a.autopilotReviewShadowCandidatesWith(ctx, job, 3, 85, func(string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{OK: true, Bytes: feedBytes}, URL: feed}
	}, func(jx.M, []jx.M, string) *sourceEvaluation { return &sourceEvaluation{Score: 90} }, func(context.Context, string, []jx.M) error { return nil }, policy); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotProjections(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = a.projectAutopilotSource(ctx, job, source, "probation"); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range a.Catalog().RSS {
		if jx.Str(entry, "id") == feed {
			count++
			if !a.CatalogEntryMatchesTopic(entry, topic, false) || a.CatalogEntryMatchesTopic(entry, topic+" уточнение", false) {
				t.Fatal("projection lost topic isolation")
			}
		}
	}
	if count != 1 {
		t.Fatalf("projection count=%d", count)
	}
	active, probation, err := a.autopilotProjectedCounts(ctx, topic)
	if err != nil || active != 0 || probation != 1 {
		t.Fatalf("projected source counts: active=%d probation=%d err=%v", active, probation, err)
	}
	for _, other := range []string{"rss:https://example.org/two", "rss:https://example.org/three", "rss:https://example.org/four"} {
		if err = registerAutopilotCandidate(ctx, d, job, other, at); err != nil {
			t.Fatal(err)
		}
		err = transitionAutopilotSource(ctx, d, job, other, "candidate", "probation", "preliminary", []byte(`{}`), at, at)
		if strings.HasSuffix(other, "four") {
			if !errors.Is(err, errAutopilotProbationLimit) {
				t.Fatalf("probation cap: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if err = setAutopilotSourceOverride(ctx, d, autopilotAutoScope, "", key, source, "excluded", at); err != nil {
		t.Fatal(err)
	}
	if err = a.projectAutopilotSource(ctx, job, source, "probation"); !errors.Is(err, errAutopilotSourceOverride) {
		t.Fatalf("override ignored: %v", err)
	}
	if err = setAutopilotSourceOverride(ctx, d, autopilotAutoScope, "", key, source, "", at); err != nil {
		t.Fatal(err)
	}
	if err = a.unprojectAutopilotSource(ctx, job, source); !errors.Is(err, errAutopilotSourceChanged) {
		t.Fatalf("removed non-rejected source: %v", err)
	}
	if err = transitionAutopilotSource(ctx, d, job, source, "probation", "rejected", "poor_quality", []byte(`{}`), at, at); err != nil {
		t.Fatal(err)
	}
	if err = setAutopilotSourceOverride(ctx, d, autopilotAutoScope, owner, key, source, "pinned", at); err != nil {
		t.Fatal(err)
	}
	if err = a.unprojectAutopilotSource(ctx, job, source); !errors.Is(err, errAutopilotSourceOverride) {
		t.Fatalf("removed pinned source: %v", err)
	}
	if err = setAutopilotSourceOverride(ctx, d, autopilotAutoScope, owner, key, source, "", at); err != nil {
		t.Fatal(err)
	}
	const otherTopic = "Другая тема источника"
	otherKey := autopilotTopicKey(otherTopic)
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") == feed {
				profiles := jx.Map(entry, "autopilot_profiles")
				profiles[otherKey] = jx.M{"topic": otherTopic, "state": "active"}
				entry["topics"] = autopilotProjectionTopics(profiles)
				return true
			}
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.unprojectAutopilotSource(ctx, job, source); err != nil {
		t.Fatal(err)
	}
	if err = a.unprojectAutopilotSource(ctx, job, source); err != nil {
		t.Fatalf("repeated removal: %v", err)
	}
	conflict := "rss:" + conflictFeed
	if err = registerAutopilotCandidate(ctx, d, job, conflict, at); err != nil {
		t.Fatal(err)
	}
	if err = transitionAutopilotSource(ctx, d, job, conflict, "candidate", "probation", "preliminary", []byte(`{}`), at, at); err != nil {
		t.Fatal(err)
	}
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		c.RSS = append(c.RSS, jx.M{"id": conflictFeed, "topics": []any{"Другая тема"}})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotProjections(ctx, job); err != nil {
		t.Fatal(err)
	}
	var conflictState, resume string
	if err = d.Pool.QueryRow(ctx, `SELECT state,resume_state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, autopilotAutoScope, key, conflict).Scan(&conflictState, &resume); err != nil || conflictState != "cooldown" || resume != "probation" {
		t.Fatalf("conflict did not free trial slot: %s/%s %v", conflictState, resume, err)
	}
	for _, entry := range a.Catalog().RSS {
		if jx.Str(entry, "id") == feed {
			if a.CatalogEntryMatchesTopic(entry, topic, false) || !a.CatalogEntryMatchesTopic(entry, otherTopic, false) {
				t.Fatal("removal must preserve only the other profile")
			}
			return
		}
	}
	t.Fatal("removed another profile's source")
}
