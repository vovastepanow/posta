package posta

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

// Simulate independent scheduler invocations rather than repeated calls on a
// single lease: checkpoints must survive completion, and an interrupted worker
// must not write once a replacement has claimed the job.
func TestAutopilotIndependentRestartAndLeaseRecoveryPostgres(t *testing.T) {
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
	const scope, topicKey, kind = "test-restart", "independent-restart", "independent-restart"
	if _, err := d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, topicKey, kind); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, topicKey, kind)
	if err := enqueueAutopilotJob(ctx, d.Pool, scope, topicKey, kind, "test", "test", 1, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	var previous *autopilotJob
	for cycle := 1; cycle <= 3; cycle++ {
		job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, kind)
		if err != nil || job == nil {
			t.Fatalf("cycle %d claim=%+v err=%v", cycle, job, err)
		}
		if previous != nil {
			if job.Token == previous.Token || job.Generation <= previous.Generation {
				t.Fatalf("cycle %d reused token/generation: previous=%+v next=%+v", cycle, previous, job)
			}
			if got := jx.Int(jx.Unmarshal(job.Checkpoint), "completed_cycles"); got != int64(cycle-1) {
				t.Fatalf("cycle %d lost persisted checkpoint: %d", cycle, got)
			}
			if err := saveAutopilotCheckpoint(ctx, d, previous); !errors.Is(err, errAutopilotLeaseLost) {
				t.Fatalf("cycle %d stale worker changed checkpoint: %v", cycle, err)
			}
		}
		job.Checkpoint = jx.Marshal(jx.M{"completed_cycles": cycle})
		if err := runAutopilotLeased(ctx, d, job, time.Minute, 10*time.Second, 100, 100, 100, 100, func(work context.Context) error {
			return saveAutopilotCheckpoint(work, d, job)
		}); err != nil {
			t.Fatalf("cycle %d work: %v", cycle, err)
		}
		if err := finishAutopilotJob(ctx, d, job, time.Now().Add(-time.Second), job.Checkpoint, ""); err != nil {
			t.Fatalf("cycle %d finish: %v", cycle, err)
		}
		previous = job
	}

	// Model an abrupt process death: the old lease expires while its worker
	// has not called finish. A replacement must pick up persisted progress.
	lost, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, kind)
	if err != nil || lost == nil {
		t.Fatalf("interrupted claim=%+v err=%v", lost, err)
	}
	lost.Checkpoint = jx.Marshal(jx.M{"completed_cycles": 3, "phase": "review"})
	if err := saveAutopilotCheckpoint(ctx, d, lost); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_jobs SET lease_until=0 WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, topicKey, kind); err != nil {
		t.Fatal(err)
	}
	if err := finishAutopilotJob(ctx, d, lost, time.Now(), lost.Checkpoint, ""); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("expired worker finished job: %v", err)
	}
	replacement, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, kind)
	if err != nil || replacement == nil {
		t.Fatalf("replacement claim=%+v err=%v", replacement, err)
	}
	if got := jx.Str(jx.Unmarshal(replacement.Checkpoint), "phase"); got != "review" {
		t.Fatalf("checkpoint after restart phase=%q, want review", got)
	}
	if replacement.Generation <= lost.Generation || replacement.Token == lost.Token {
		t.Fatalf("restarted worker did not fence old lease")
	}
	if err := renewAutopilotLease(ctx, d.Pool, lost, time.Minute); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("expired worker renewed lease: %v", err)
	}
	if err := saveAutopilotCheckpoint(ctx, d, lost); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("expired worker saved checkpoint: %v", err)
	}
	replacement.Checkpoint = jx.Marshal(jx.M{"completed_cycles": 4, "phase": "complete"})
	if err := finishAutopilotJob(ctx, d, replacement, time.Now().Add(time.Hour), replacement.Checkpoint, ""); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var lastError string
	if err := d.Pool.QueryRow(ctx, `SELECT attempts,last_error FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, topicKey, kind).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 5 || lastError != "" {
		t.Fatal(fmt.Sprintf("restarts=%d last_error=%q, want five claims and no error", attempts, lastError))
	}
}
