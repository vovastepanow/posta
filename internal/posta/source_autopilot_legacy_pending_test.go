package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// The real manual "waiting for approval" shelf must stop displaying a source
// after it has safely entered the topic's autopilot queue. Re-running the
// transfer must not reset source state or duplicate history.
func TestLegacyPendingTransferredOutOfManualShelfPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires isolated autopilot PostgreSQL")
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

	const topic = "Тест переноса из ручной очереди"
	const owner = "autopilot-legacy-transfer-owner"
	const prefix = "https://example.org/legacy-autopilot-transfer-"
	source := SourceIDFor(PlatformRSS, prefix+"valid.xml")
	manualVote := SourceIDFor(PlatformRSS, prefix+"voted.xml")
	alreadyApproved := SourceIDFor(PlatformRSS, prefix+"approved.xml")
	differentTopic := SourceIDFor(PlatformRSS, prefix+"other.xml")
	key := autopilotTopicKey(topic)

	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at,settings)
		VALUES($1,0,jsonb_build_object('topic',$2))
		ON CONFLICT(id) DO UPDATE SET settings=jsonb_build_object('topic',$2)`, owner, topic); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 3,
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	defer saveAutopilotDemand(context.Background(), d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "off", PostsPerDay: 5, HorizonDays: 3,
	}, time.Now().Unix())
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, autopilotShadowScope, key); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, autopilotShadowScope, key)
		}
	}()

	a := &App{DB: d}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	entry := func(sid, atTopic string) jx.M {
		return jx.M{
			"source_id": sid, "platform": PlatformRSS, "id": sid[4:],
			"name": sid, "topic": atTopic, "reason": "legacy review",
			"score": 86, "timestamp": time.Now().Unix(),
		}
	}
	if _, err := a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		c.Suggestions = append(c.Suggestions, entry(source, topic), entry(source, topic),
			entry(manualVote, topic), entry(alreadyApproved, topic),
			entry(differentTopic, "Совсем другая тема"))
		c.Votes = append(c.Votes, jx.M{"source_id": manualVote, "topic": topic, "vote": "reject"})
		c.RSS = append(c.RSS, jx.M{"id": alreadyApproved[4:], "name": "Approved", "topics": []any{topic}})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
			filteredSuggestions := c.Suggestions[:0]
			for _, x := range c.Suggestions {
				if jx.Str(x, "topic") != topic && jx.Str(x, "source_id") != differentTopic {
					filteredSuggestions = append(filteredSuggestions, x)
				}
			}
			c.Suggestions = filteredSuggestions
			votes := c.Votes[:0]
			for _, x := range c.Votes {
				if jx.Str(x, "source_id") != manualVote {
					votes = append(votes, x)
				}
			}
			c.Votes = votes
			rss := c.RSS[:0]
			for _, x := range c.RSS {
				if jx.Str(x, "id") != alreadyApproved[4:] {
					rss = append(rss, x)
				}
			}
			c.RSS = rss
			return true
		})
	}()

	u := a.User(ctx, owner, nil)
	before, _, _, err := u.PendingSourcesPage("any", nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("fixture must be visible on manual pending shelf before transfer")
	}
	if got := a.User(ctx, "", nil).autopilotLegacyPendingCandidates(&autopilotJob{
		Scope: autopilotShadowScope, TopicKey: key, Topic: topic, Kind: autopilotShadowKind,
	}); len(got) != 1 || got[0] != source {
		t.Fatalf("unexpected eligible legacy suggestions: %v", got)
	}
	at := time.Now()
	if err := enqueueAutopilotJob(ctx, d.Pool, autopilotShadowScope, key,
		autopilotShadowKind, topic, "legacy_test", 1, at); err != nil {
		t.Fatal(err)
	}
	const token = "legacy-transfer-test-token"
	if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_jobs
		SET lease_token=$4,generation=1,lease_until=$5,demand_active=true,enabled=true
		WHERE scope=$1 AND topic_key=$2 AND kind=$3`,
		autopilotShadowScope, key, autopilotShadowKind, token, at.Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	job := &autopilotJob{
		Scope: autopilotShadowScope, Kind: autopilotShadowKind, Topic: topic,
		TopicKey: key, Token: token, Generation: 1,
	}
	added, err := a.autopilotImportLegacyPending(ctx, job, 60)
	if err != nil || added != 1 {
		t.Fatalf("first transfer added=%d err=%v", added, err)
	}
	added, err = a.autopilotImportLegacyPending(ctx, job, 60)
	if err != nil || added != 0 {
		t.Fatalf("repeated transfer added=%d err=%v", added, err)
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND reason='legacy_pending_import'`,
		autopilotShadowScope, key, source).Scan(&count); err != nil || count != 1 {
		t.Fatalf("transfer decision count=%d err=%v", count, err)
	}
	after, _, _, err := u.PendingSourcesPage("any", nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after {
		if jx.Str(item, "source_id") == source {
			t.Fatal("source still appears in manual approvals after autopilot transfer")
		}
	}
	var state string
	if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
		autopilotShadowScope, key, source).Scan(&state); err != nil || state != "candidate" {
		t.Fatalf("transferred source state=%q err=%v", state, err)
	}
}
