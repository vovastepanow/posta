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
	unsupported := SourceIDFor(PlatformRSS, "http://example.org/unsupported-feed.xml")
	invalidIdentity := SourceIDFor(PlatformRSS, prefix+"identity-mismatch.xml")
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
			entry(differentTopic, "Совсем другая тема"),
			entry(unsupported, topic), entry(invalidIdentity, topic))
		// A valid RSS URL with an inconsistent persisted ID must never
		// become an eligible candidate, even if the suggestion is visible.
		c.Suggestions[len(c.Suggestions)-1]["id"] = prefix + "different-feed.xml"
		c.Votes = append(c.Votes,
			jx.M{"source_id": manualVote, "topic": topic, "vote": "reject"},
			// A decision on a different topic must not prevent this
			// source from entering the active topic's autopilot.
			jx.M{"source_id": source, "topic": "Математика", "vote": "reject"})
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
				if jx.Str(x, "source_id") != manualVote && jx.Str(x, "source_id") != source {
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
	// Regression: a shadow job with pending candidates must never call
	// the auto-only projection, which previously returned "invalid projection job".
	if err := a.autopilotFinishBacklog(ctx, job); err != nil {
		t.Fatalf("shadow backlog completion failed: %v", err)
	}
	// This must transfer without executing the scheduled AI/source-review
	// worker, including when the review job is not yet due.
	added, err := a.reconcileLegacyPendingWithoutLease(ctx, "shadow", autopilotShadowScope, autopilotShadowKind, 60)
	if err != nil || added != 3 {
		t.Fatalf("scheduler transfer added=%d err=%v", added, err)
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
		id := jx.Str(item, "source_id")
		if id == source || id == unsupported || id == invalidIdentity {
			t.Fatalf("source %s still appears in manual approvals after autopilot transfer", id)
		}
	}
	for _, tc := range []struct{ id, wantState, wantReason string }{
		{source, "candidate", "legacy_pending_import"},
		{unsupported, "rejected", "unsupported_source_format"},
		{invalidIdentity, "rejected", "invalid_source_identity"},
	} {
		var state, reason string
		if err := d.Pool.QueryRow(ctx, `SELECT state,reason FROM source_autopilot_sources
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
			autopilotShadowScope, key, tc.id).Scan(&state, &reason); err != nil ||
			state != tc.wantState || reason != tc.wantReason {
			t.Fatalf("source %q state=%q reason=%q err=%v", tc.id, state, reason, err)
		}
		var audits int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND new_state=$4 AND reason=$5`,
			autopilotShadowScope, key, tc.id, tc.wantState, tc.wantReason).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("source %q audit rows=%d err=%v", tc.id, audits, err)
		}
	}
}
