package posta

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/db"
)

func TestAutopilotCleanupRetainsLease(t *testing.T) {
	job := &autopilotJob{Token: "lease"}
	ctx, cancel := context.WithCancel(contextWithAutopilotLease(context.Background(), job))
	cancel()
	cleanup := autopilotCleanupContext(ctx, context.Background())
	if cleanup.Err() != nil || cleanup.Value(autopilotLeaseContextKey{}) != job {
		t.Fatal("cleanup must survive cancellation while preserving fencing")
	}
}

// POSTA_AUTOPILOT_TEST_DSN must name an isolated, disposable database.
func TestAutopilotQueuePostgres(t *testing.T) {
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
	if _, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_jobs WHERE scope='test-queue'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope='test-queue'`)
	})
	at := time.Now().Truncate(time.Second)
	if err = enqueueAutopilotJob(ctx, d.Pool, "test-queue", "topic", "discovery", "Topic", "test", 1, at); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	jobs := make(chan *autopilotJob, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := claimAutopilotJob(ctx, d.Pool, at, time.Minute)
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
	var first *autopilotJob
	count := 0
	for job := range jobs {
		if job != nil {
			first = job
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claimed %d jobs", count)
	}
	panicErr := runAutopilotLeased(ctx, d, first, time.Minute, 10*time.Second, 10, 10, 10, 10, func(context.Context) error { panic("test worker panic") })
	if panicErr == nil || !strings.Contains(panicErr.Error(), "test worker panic") {
		t.Fatalf("worker panic was not contained: %v", panicErr)
	}
	if err := enqueueAutopilotJob(ctx, d.Pool, "test-queue", "topic", "discovery", "Topic", "manual", 10, at); err != nil {
		t.Fatal(err)
	}
	if early, err := claimAutopilotJob(ctx, d.Pool, at.Add(24*time.Hour), time.Minute); err != nil || early != nil {
		t.Fatalf("clock skew stole a live lease: %+v %v", early, err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE source_autopilot_jobs SET lease_until=0 WHERE scope='test-queue'`); err != nil {
		t.Fatal(err)
	}
	second, err := claimAutopilotJob(ctx, d.Pool, at.Add(2*time.Minute), time.Minute)
	if err != nil || second == nil {
		t.Fatalf("reclaim: %v, %v", second, err)
	}
	workStopped := false
	err = runAutopilotLeased(ctx, d, first, time.Minute, 10*time.Millisecond, 100, 10, 100, 10, func(ctx context.Context) error {
		<-ctx.Done()
		workStopped = true
		return ctx.Err()
	})
	if !errors.Is(err, errAutopilotLeaseLost) || !workStopped {
		t.Fatalf("lost lease did not cancel worker: %v", err)
	}
	err = withAutopilotLease(ctx, d, first, func(pgx.Tx) error { t.Error("stale worker ran effect"); return nil })
	if !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale worker: %v", err)
	}
	app := &App{DB: d}
	staleCtx := contextWithAutopilotLease(ctx, first)
	if err := app.KVSet(staleCtx, "autopilot-test-fenced-kv", "stale", 0); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale KVSet: %v", err)
	}
	app.CacheSet(staleCtx, "autopilot-stale-cache", "stale", time.Hour)
	var cacheRows int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM cache WHERE key='autopilot-stale-cache'`).Scan(&cacheRows); err != nil || cacheRows != 0 {
		t.Fatalf("stale cache write: %d %v", cacheRows, err)
	}
	canceledCtx, cancel := context.WithCancel(staleCtx)
	cancel()
	cleanupCtx := autopilotCleanupContext(canceledCtx, context.Background())
	if err := app.KVSet(cleanupCtx, "autopilot-test-fenced-kv", "stale-cleanup", 0); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale cleanup KVSet: %v", err)
	}
	u := &U{App: app, Ctx: staleCtx}
	if err := u.registryUpsert(registryWrite{SourceID: "test:stale-worker", Status: RegStatusRejected}); !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale registry write: %v", err)
	}
	if err := app.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = app.UpdateCatalog(staleCtx, func(d *SourceDB) bool { d.Discovery["autopilot_stale_test"] = true; return true })
	if !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale catalog update: %v", err)
	}
	currentCtx := contextWithAutopilotLease(ctx, second)
	if err := app.KVSet(currentCtx, "autopilot-test-fenced-kv", "current", 0); err != nil {
		t.Fatalf("current KVSet: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM kv WHERE key='autopilot-test-fenced-kv'`)
	})
	err = reserveAutopilotJobBudget(ctx, d, first, "stale-worker-test", "test-queue", "search", at, 1, 10)
	if !errors.Is(err, errAutopilotLeaseLost) {
		t.Fatalf("stale reservation: %v", err)
	}
	var countReservations int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_reservations WHERE reservation_id='stale-worker-test'`).Scan(&countReservations); err != nil || countReservations != 0 {
		t.Fatalf("stale reservation persisted: %d, %v", countReservations, err)
	}
	if err = finishAutopilotJob(ctx, d, second, time.Now().Add(time.Hour), json.RawMessage(`{"cursor":1}`), ""); err != nil {
		t.Fatal(err)
	}
	var checkpoint json.RawMessage
	if err = d.Pool.QueryRow(ctx, `SELECT checkpoint FROM source_autopilot_jobs WHERE scope='test-queue'`).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if string(checkpoint) != `{"cursor": 1}` {
		t.Fatalf("checkpoint %s", checkpoint)
	}
	if ok, err := setAutopilotJobEnabled(ctx, d.Pool, "test-queue", "topic", "discovery", false); err != nil || !ok {
		t.Fatalf("disable: %v %v", ok, err)
	}
	if err := enqueueAutopilotJob(ctx, d.Pool, "test-queue", "topic", "discovery", "Topic", "manual", 20, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJob(ctx, d.Pool, at.Add(3*time.Hour), time.Minute)
	if err != nil || job != nil {
		t.Fatalf("disabled job claimed: %v %v", job, err)
	}
	if ok, err := setAutopilotJobEnabled(ctx, d.Pool, "test-queue", "topic", "discovery", true); err != nil || !ok {
		t.Fatalf("enable: %v %v", ok, err)
	}
	job, err = claimAutopilotJob(ctx, d.Pool, at.Add(3*time.Hour), time.Minute)
	if err != nil || job == nil || string(job.Checkpoint) != `{"cursor": 1}` {
		t.Fatalf("resume: %+v %v", job, err)
	}
}
