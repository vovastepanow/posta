package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotDemandProfilesPostgres(t *testing.T) {
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
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	userID := "autopilot-demand-test"
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	for _, topic := range []string{"C", "C++", "Автомобили в Екатеринбурге"} {
		if err := saveAutopilotDemand(ctx, d.Pool, userID, autopilotDemandSettings{Topic: topic, Mode: "shadow", PostsPerDay: 5, HorizonDays: 2}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveAutopilotDemand(ctx, d.Pool, userID, autopilotDemandSettings{Topic: "C++", Mode: "off", PostsPerDay: 0, HorizonDays: 2}, 2); err != nil {
		t.Fatal(err)
	}
	list, err := listAutopilotDemands(ctx, d.Pool, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d profiles", len(list))
	}
	for _, demand := range list {
		if demand.Topic == "C++" && (demand.Mode != "off" || demand.PostsPerDay != 0) {
			t.Fatalf("incorrect saved demand: %+v", demand)
		}
	}
}

func TestAutopilotAPIIsUserScopedPostgres(t *testing.T) {
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
	for _, id := range []string{"autopilot-api-a", "autopilot-api-b"} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	ua := a.User(ctx, "autopilot-api-a", nil)
	ub := a.User(ctx, "autopilot-api-b", nil)
	if err = ua.SourceAutopilotDemandSave("Тема A", "shadow", 5, 3); err != nil {
		t.Fatal(err)
	}
	if err = ub.SourceAutopilotDemandSave("Тема B", "shadow", 5, 3); err != nil {
		t.Fatal(err)
	}
	if err = ua.SourceAutopilotDemandSave("Тема A", "auto", 5, 3); err != nil {
		t.Fatal(err)
	}
	status, err := ua.SourceAutopilotStatus()
	if err != nil {
		t.Fatal(err)
	}
	topics, ok := status["topics"].([]jx.M)
	if !ok || len(topics) != 1 || jx.Str(topics[0], "topic") != "Тема A" || jx.Str(topics[0], "mode") != "auto" {
		t.Fatalf("leaked another owner's demands: %+v", status)
	}
	if _, err = ua.SourceAutopilotHistory("Тема B", "auto", 0, 1); err == nil {
		t.Fatal("history of another owner's profile was exposed")
	}
	key := autopilotTopicKey("Тема A")
	at := now()
	readyBefore, delayedBefore := jx.Int(topics[0], "queue_ready"), jx.Int(topics[0], "queue_delayed")
	testPrefix := "rss:https://queue-" + randHex(8) + ".example.org/"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,checked_at,next_check_at)
		VALUES ('public',$1,$2||'retry','candidate','feed_unavailable',1,$3,$3-7200,$3+7200),
		('public',$1,$2||'waiting','candidate','assessment_unavailable',1,$3,$3,$3+7200),
		('public',$1,$2||'ready','candidate','discovered',1,$3,$3,$3-1),
		('public',$1,$2||'probation','candidate','probation_full',1,$3,$3,$3+7200)`, key, testPrefix, at); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id LIKE $2||'%'`, key, testPrefix)
	})
	status, err = ua.SourceAutopilotStatus()
	if err != nil {
		t.Fatal(err)
	}
	row := status["topics"].([]jx.M)[0]
	if jx.Int(row, "queue_ready") != readyBefore+3 || jx.Int(row, "queue_delayed") != delayedBefore+1 {
		t.Fatalf("ready and delayed candidates overlap: %+v", row)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at) VALUES ('public',$1,'rss:https://example.org/a','','candidate','test',1,1),('public',$1,'rss:https://example.org/b','','candidate','test',1,2)`, key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_decisions WHERE scope='public' AND topic_key=$1`, key)
	})
	page, err := ua.SourceAutopilotHistory("Тема A", "auto", 0, 1)
	if err != nil || jx.Int(page, "next_before") == 0 {
		t.Fatalf("history first page: %+v %v", page, err)
	}
	page, err = ua.SourceAutopilotHistory("Тема A", "auto", jx.Int(page, "next_before"), 1)
	if err != nil || len(page["decisions"].([]jx.M)) != 1 || jx.Int(page, "next_before") != 0 {
		t.Fatalf("history next page: %+v %v", page, err)
	}
}
