package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/db"
)

func TestAutopilotEvidenceDedupPostgres(t *testing.T) {
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
	for _, table := range []string{"source_autopilot_post_evidence", "source_autopilot_collection_passes", "source_autopilot_jobs"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope='test-evidence'`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='test-evidence'`) })
	}
	at := time.Now().Truncate(time.Second)
	if err = enqueueAutopilotJob(ctx, d.Pool, "test-evidence", "topic", "discovery", "topic", "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJob(ctx, d.Pool, at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim %+v %v", job, err)
	}
	e := autopilotPostEvidence{PostHash: "post-1", ContentHash: "same", PublishedAt: at.Add(-time.Hour).Unix(), ObservedAt: at.Unix(), Relevant: true, Eligible: true}
	if err = recordAutopilotPostEvidence(ctx, d, job, "source-a", e); err != nil {
		t.Fatal(err)
	}
	if err = recordAutopilotPostEvidence(ctx, d, job, "source-a", e); err != nil {
		t.Fatal(err)
	}
	e.PostHash = "post-2"
	e.ContentHash = "unique"
	if err = recordAutopilotPostEvidence(ctx, d, job, "source-a", e); err != nil {
		t.Fatal(err)
	}
	e.PostHash = "post-3"
	e.ContentHash = "ad"
	e.Relevant = false
	e.Eligible = false
	e.Advertising = true
	if err = recordAutopilotPostEvidence(ctx, d, job, "source-a", e); err != nil {
		t.Fatal(err)
	}
	e.PostHash = "post-4"
	e.ContentHash = "same"
	e.Relevant = true
	e.Eligible = true
	e.Advertising = false
	if err = recordAutopilotPostEvidence(ctx, d, job, "source-b", e); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []int64{1, 2} {
		if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count) VALUES('test-evidence','topic','source-a',$1,$2,0)`, slot, at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	s, err := autopilotEvidenceWindow(ctx, d.Pool, "test-evidence", "topic", "source-a", at.Add(-24*time.Hour), at.Add(time.Second))
	if err != nil || s.Evaluated != 3 || s.Relevant != 2 || s.Eligible != 2 || s.Advertising != 1 || s.Duplicates != 1 || s.FreshUnique != 1 || s.CuratedUnique != 1 || s.SuccessfulPasses != 2 {
		t.Fatalf("stats=%+v err=%v", s, err)
	}
	s, err = autopilotEvidenceWindow(ctx, d.Pool, "test-evidence", "topic", "source-b", at.Add(-24*time.Hour), at.Add(time.Second))
	if err != nil || s.Duplicates != 1 || s.FreshUnique != 0 || s.CuratedUnique != 1 {
		t.Fatalf("other source stats=%+v err=%v", s, err)
	}
}
