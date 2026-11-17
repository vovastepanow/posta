package posta

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotAutoDemandReconciliationPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const topic = "Проверка общей автоматической очереди"
	key := autopilotTopicKey(topic)
	for _, id := range []string{"autopilot-auto-a", "autopilot-auto-b"} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
	}
	_, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, autopilotAutoScope, key, autopilotAutoKind)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, autopilotAutoScope, key, autopilotAutoKind)
	})
	a := &App{DB: d}
	save := func(owner string, rate float64, mode string) {
		t.Helper()
		if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: mode, PostsPerDay: rate, HorizonDays: 3}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	save("autopilot-auto-a", 0, "auto")
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, autopilotAutoScope, key, autopilotAutoKind).Scan(&count); err != nil || count != 0 {
		t.Fatalf("zero-rate jobs=%d err=%v", count, err)
	}
	save("autopilot-auto-a", 5, "auto")
	save("autopilot-auto-b", 5, "auto")
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if err = a.User(ctx, "autopilot-auto-a", nil).SourceAutopilotPause(topic, true); err != nil {
		t.Fatal(err)
	}
	demands, err := listAutopilotDemands(ctx, d.Pool, "autopilot-auto-a")
	if err != nil || len(demands) != 1 || !demands[0].Paused {
		t.Fatalf("personal pause not persisted: %+v %v", demands, err)
	}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	if err = withAutopilotLease(ctx, d, job, func(_ pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("other subscriber lost lease: %v", err)
	}
	if err = a.User(ctx, "autopilot-auto-b", nil).SourceAutopilotPause(topic, true); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	if err = withAutopilotLease(ctx, d, job, func(_ pgx.Tx) error { return nil }); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("last subscriber did not revoke lease: %v", err)
	}
	job.Checkpoint = []byte(`{"query_turn":999}`)
	if err = saveAutopilotCheckpoint(ctx, d, job); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("revoked worker saved progress: %v", err)
	}
}

func TestAutopilotGlobalConcurrencyPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const scope = "test-global-concurrency"
	if _, err := d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope=$1`, scope); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope=$1`, scope)
	})
	for _, key := range []string{"a", "b", "c"} {
		if err := enqueueAutopilotJob(ctx, d.Pool, scope, key, autopilotAutoKind, key, "test", 1, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	jobs := make(chan *autopilotJob, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
			jobs <- job
			errs <- err
		}()
	}
	wg.Wait()
	close(jobs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	claimed := 0
	for job := range jobs {
		if job != nil {
			claimed++
		}
	}
	if claimed < 1 || claimed > 2 {
		t.Fatalf("claimed %d concurrent jobs", claimed)
	}
}

func TestAutopilotTopicOwnershipPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	a, b := &App{DB: d}, &App{DB: d}
	release, ok, err := a.trySourceTopicOwnership(ctx, "Тема владения")
	if err != nil || !ok {
		t.Fatalf("first owner: %v %v", ok, err)
	}
	if _, ok, err = b.trySourceTopicOwnership(ctx, "Тема владения"); err != nil || ok {
		t.Fatalf("second owner: %v %v", ok, err)
	}
	release()
	release, ok, err = b.trySourceTopicOwnership(ctx, "Тема владения")
	if err != nil || !ok {
		t.Fatalf("released owner: %v %v", ok, err)
	}
	release()
}

func TestAutopilotQueueFairAcrossModesPostgres(t *testing.T) {
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
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const scope = "test-fair-modes"
	if _, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope=$1`, scope); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope=$1`, scope)
	at := time.Now()
	if err = enqueueAutopilotJob(ctx, d.Pool, scope, "older", autopilotShadowKind, "older", "test", 0, at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = enqueueAutopilotJob(ctx, d.Pool, scope, "newer", autopilotAutoKind, "newer", "test", 10, at); err != nil {
		t.Fatal(err)
	}
	if job, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotAutoKind); err != nil || job != nil {
		t.Fatalf("newer task bypassed older: %+v %v", job, err)
	}
	if job, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotShadowKind); err != nil || job == nil || job.TopicKey != "older" {
		t.Fatalf("older task not claimed: %+v %v", job, err)
	}
	if job, err := claimAutopilotJobKind(ctx, d.Pool, at, time.Minute, autopilotAutoKind); err != nil || job == nil || job.TopicKey != "newer" {
		t.Fatalf("newer task did not follow: %+v %v", job, err)
	}
}

func TestAutopilotIntervalUsesPersonalObservedYieldPostgres(t *testing.T) {
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
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner, topic, feed = "autopilot-yield-test", "Тема проверки отдачи", "https://example.org/autopilot-yield-feed"
	key, source := autopilotTopicKey(topic), "rss:"+feed
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	for _, table := range []string{"source_autopilot_collection_passes", "source_autopilot_post_evidence", "source_autopilot_sources"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	remove := func(c *SourceDB) bool {
		var keep []jx.M
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") != feed {
				keep = append(keep, entry)
			}
		}
		c.RSS = keep
		return true
	}
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		remove(c)
		c.RSS = append(c.RSS, jx.M{"id": feed, "autopilot_managed": true, "autopilot_profiles": jx.M{key: jx.M{"topic": topic, "state": "active"}}})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	defer a.UpdateCatalog(context.Background(), remove)
	u := a.User(ctx, owner, nil)
	if err = u.SaveSettings(jx.M{"topic": topic, "active_sources_only": false}); err != nil {
		t.Fatal(err)
	}
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 1, HorizonDays: 3}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Unix()
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,policy_version,first_seen_at) VALUES('public',$1,$2,'active',1,$3)`, key, source, at); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising) SELECT 'public',$1,$2,i::text,'content-'||i::text,1,$3,$3,true,true,false FROM generate_series(1,14) i`, key, source, at); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count) VALUES('public',$1,$2,1,$3,1),('public',$1,$2,2,$4,1)`, key, source, at, at-86400); err != nil {
		t.Fatal(err)
	}
	job := &autopilotJob{Scope: autopilotAutoScope, Topic: topic, TopicKey: key, Checkpoint: []byte(`{}`)}
	interval, err := a.autopilotAutoInterval(ctx, job)
	if err != nil || interval != 12*time.Hour {
		t.Fatalf("covered interval %v: %v", interval, err)
	}
	if err = u.SourceOverrideSet(source, false); err != nil {
		t.Fatal(err)
	}
	interval, err = a.autopilotAutoInterval(ctx, job)
	if err != nil || interval != 6*time.Hour {
		t.Fatalf("excluded source influenced interval %v: %v", interval, err)
	}
}
