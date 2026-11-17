package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotAdmissionPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated test database")
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
	for _, id := range []string{"autopilot-observation-a", "autopilot-observation-b"} {
		if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		if err := saveAutopilotDemand(ctx, d.Pool, id, autopilotDemandSettings{Topic: "Тема", Mode: "shadow", PostsPerDay: 5, HorizonDays: 2}, 1); err != nil {
			t.Fatal(err)
		}
	}
	item := jx.M{"url": "https://example.org/posts/1", "source_key": "rss:example"}
	for _, id := range []string{"autopilot-observation-a", "autopilot-observation-a", "autopilot-observation-b"} {
		if err := recordAutopilotAdmission(ctx, d.Pool, id, "Тема", item, true, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordAutopilotAdmission(ctx, d.Pool, "autopilot-observation-a", "Тема", item, false, 200); err != nil {
		t.Fatal(err)
	}
	if err := recordAutopilotAdmission(ctx, d.Pool, "autopilot-observation-a", "Тема", jx.M{"source_url": "https://example.org/feed", "source_key": "rss:example"}, true, 100); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, "autopilot-observation-b", autopilotDemandSettings{Topic: "Тема", Mode: "off", PostsPerDay: 5, HorizonDays: 2}, 2); err != nil {
		t.Fatal(err)
	}
	if err := recordAutopilotAdmission(ctx, d.Pool, "autopilot-observation-b", "Тема", jx.M{"url": "https://example.org/posts/2", "source_key": "rss:example"}, true, 200); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, "autopilot-observation-a", autopilotDemandSettings{Topic: "C++", Mode: "shadow", PostsPerDay: 1, HorizonDays: 1}, 1); err != nil {
		t.Fatal(err)
	}
	if err := recordAutopilotAdmission(ctx, d.Pool, "autopilot-observation-a", "C", item, true, 100); err != nil {
		t.Fatal(err)
	}
	if err := recordAutopilotAdmission(ctx, d.Pool, "autopilot-observation-a", "C++", item, true, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	var accepted bool
	var observed int64
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_admissions WHERE user_id LIKE 'autopilot-observation-%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("got %d rows", count)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT accepted,observed_at FROM source_autopilot_admissions WHERE user_id='autopilot-observation-a' AND topic_key=$1`, autopilotTopicKey("Тема")).Scan(&accepted, &observed); err != nil {
		t.Fatal(err)
	}
	if accepted || observed != 100 {
		t.Fatalf("accepted=%v observed=%d", accepted, observed)
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_admissions(user_id,topic_key,source_key,post_hash,policy_version,accepted,observed_at)
		SELECT user_id,topic_key,source_key,post_hash,policy_version+1,true,200 FROM source_autopilot_admissions
		WHERE user_id='autopilot-observation-a' AND topic_key=$1`, autopilotTopicKey("Тема"))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := autopilotAdmissionsWindow(ctx, d.Pool, "autopilot-observation-a", "Тема", "rss:example", 100, 150)
	if err != nil || stats.Evaluated != 1 || stats.Accepted != 1 {
		t.Fatalf("window: %+v %v", stats, err)
	}
	stats, err = autopilotAdmissionsWindow(ctx, d.Pool, "autopilot-observation-b", "Тема", "rss:example", 100, 150)
	if err != nil || stats.Evaluated != 1 || stats.Accepted != 1 {
		t.Fatalf("other user: %+v %v", stats, err)
	}
	stats, err = autopilotAdmissionsWindow(ctx, d.Pool, "autopilot-observation-a", "Тема", "rss:example", 150, 250)
	if err != nil || stats.Evaluated != 0 {
		t.Fatalf("reclassification moved window: %+v %v", stats, err)
	}
}
