package posta

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

func TestAppendAutopilotSourceHint(t *testing.T) {
	hints := appendAutopilotSourceHint(nil, "telegram:marketing_channel")
	hints = appendAutopilotSourceHint(hints, "telegram:marketing_channel")
	if len(hints) != 1 {
		t.Fatalf("duplicated accepted source: %v", hints)
	}
	hints = appendAutopilotSourceHint(hints, "")
	if len(hints) != 1 {
		t.Fatalf("empty source was queued: %v", hints)
	}
	for i := 0; i < autopilotSourceHintMax+10; i++ {
		hints = appendAutopilotSourceHint(hints, fmt.Sprintf("rss:https://example.org/%d", i))
	}
	if len(hints) != autopilotSourceHintMax {
		t.Fatalf("queue length %d, want %d", len(hints), autopilotSourceHintMax)
	}
	if hints[len(hints)-1] == "" {
		t.Fatal("last accepted source dropped")
	}
}

func TestAutopilotSourceHintKey(t *testing.T) {
	key := autopilotSourceHintKey("SMM и блогинг")
	if key == "" || key != autopilotSourceHintKey("SMM и блогинг") {
		t.Fatalf("unstable hint key: %q", key)
	}
}

func TestAutopilotConsumesAcceptedSourceHintsPostgres(t *testing.T) {
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
	const topic = "Автопилот проверка подсказок авторов"
	const owner = "autopilot-hints-test-owner"
	key := autopilotTopicKey(topic)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		if _, err := d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
			_, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
		}
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_demands WHERE user_id=$1`, owner)
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	}()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 2, HorizonDays: 2}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("autopilot claim=%+v err=%v", job, err)
	}
	hint := "telegram:marketing_digest"
	if err := a.KVSet(ctx, autopilotSourceHintKey(topic), []string{hint, "invalid:source"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 5, 5, 5, 5, func(work context.Context) error {
		return a.autopilotConsumeSourceHints(work, job, 20)
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.Pool.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id=$2`, key, hint).Scan(&state); err != nil || state != "candidate" {
		t.Fatalf("hint source state=%q err=%v", state, err)
	}
	var remaining []string
	if !a.KVGet(ctx, autopilotSourceHintKey(topic), &remaining) || len(remaining) != 0 {
		t.Fatalf("hint queue not drained: %v", remaining)
	}
}
