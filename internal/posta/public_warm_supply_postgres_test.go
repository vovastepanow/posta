package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestPublicWarmSupplyPostgres(t *testing.T) {
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
	a := &App{DB: d, LLM: llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", "")),
		Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	if err := a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	topic := "Warm supply test " + randHex(8)
	key := autopilotTopicKey(topic)
	at := time.Now()
	source := "rss:https://example.org/feed/" + randHex(8)
	defer d.Pool.Exec(ctx, "DELETE FROM public_warm_profiles WHERE topic_key=$1", key)
	for _, table := range []string{"source_autopilot_jobs", "source_autopilot_sources", "source_autopilot_content_results", "source_autopilot_overrides"} {
		defer d.Pool.Exec(ctx, "DELETE FROM "+table+" WHERE scope='public' AND topic_key=$1", key)
	}
	if _, err := d.Pool.Exec(ctx, "INSERT INTO public_warm_profiles(topic_key,query,target) VALUES($1,$2,100)", key, topic); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	// Make the test independent of other integration tests' scheduled jobs.
	if _, err := d.Pool.Exec(ctx, "UPDATE source_autopilot_jobs SET next_run_at=0 WHERE scope='public' AND topic_key=$1", key); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, at.Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("warmup claim=%+v err=%v", job, err)
	}
	if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,policy_version,first_seen_at)
		VALUES('public',$1,$2,'active',$3,$4)`, key, source, TopicProfileVersion, at.Unix()); err != nil {
		t.Fatal(err)
	}
	imageLocal, err := a.MediaWrite(MediaCacheName("warm-image", "jpg"), []byte("verified image placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	post := jx.M{"url": "https://example.org/post/ready", "image": "https://example.org/image.jpg", "image_local": imageLocal,
		"text": "Detailed example of a relevant publication.", "posted_at": at.Unix()}
	analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test",
		"description": "Useful example", "why_relevant": "A relevant publication", "evidence": "Detailed example", "tags": []any{}}
	for _, entry := range []struct {
		id string
		post, analysis jx.M
	}{
		{"ready", post, analysis},
		{"same-content", post, analysis},
		{"missing-media", jx.Merge(post, jx.M{"url": "https://example.org/post/missing", "image": "https://example.org/missing.jpg", "image_local": ""}), analysis},
		{"advertising", jx.Merge(post, jx.M{"url": "https://example.org/post/ad"}), jx.Merge(analysis, jx.M{"advertising": true})},
	} {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_content_results(scope,topic_key,source_id,post_hash,post,analysis,published_at,assessed_at,policy_version)
			VALUES('public',$1,$2,$3,$4,$5,$6,$6,$7)`, key, source, entry.id, db.JSON(entry.post), db.JSON(entry.analysis), at.Unix(), TopicProfileVersion); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := a.refreshPublicWarmSupply(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Ready != 1 || stats.Duplicates != 1 || stats.MissingMedia != 1 || stats.Target != 100 {
		t.Fatalf("warm supply inflated by duplicate, advertising or missing media: %+v", stats)
	}
	var saved int
	if err := d.Pool.QueryRow(ctx, "SELECT ready_count FROM public_warm_profiles WHERE topic_key=$1", key).Scan(&saved); err != nil || saved != 1 {
		t.Fatalf("stored readiness=%d err=%v", saved, err)
	}
}
