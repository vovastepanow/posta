package posta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/llm"
)

func TestAutopilotBudgetPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated test database")
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
	_, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_reservations WHERE scope='budget-test'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Pool.Exec(ctx, `DELETE FROM source_autopilot_budget WHERE scope='budget-test'`)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var successes, failures atomic.Int32
	at := time.Now()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := reserveAutopilotBudget(ctx, d, fmt.Sprintf("budget-test-%d", i), "budget-test", "search", at, 1, 5)
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, errAutopilotBudgetExhausted) {
				failures.Add(1)
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 5 || failures.Load() != 15 {
		t.Fatalf("successes=%d failures=%d", successes.Load(), failures.Load())
	}
	var used, reservations int
	if err := d.Pool.QueryRow(ctx, `SELECT used FROM source_autopilot_budget WHERE scope='budget-test'`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_reservations WHERE scope='budget-test'`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if used != 5 || reservations != 5 {
		t.Fatalf("used=%d reservations=%d", used, reservations)
	}
	var id string
	if err := d.Pool.QueryRow(ctx, `SELECT reservation_id FROM source_autopilot_reservations WHERE scope='budget-test' LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := reserveAutopilotBudget(ctx, d, id, "budget-test", "search", at, 1, 5); !errors.Is(err, errAutopilotReservationExists) {
		t.Fatalf("replaying a charged reservation must not authorize another call: %v", err)
	}
}

func TestAutopilotModelBudgetPostgres(t *testing.T) {
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
	for _, query := range []string{`DELETE FROM source_autopilot_reservations WHERE resource='llm_attempt'`, `DELETE FROM source_autopilot_budget WHERE resource='llm_attempt'`, `DELETE FROM source_autopilot_jobs WHERE scope='test-model'`} {
		if _, err := d.Pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope='test-model'`)
	})
	at := time.Now()
	if err := enqueueAutopilotJob(ctx, d.Pool, "test-model", "topic", "discovery", "topic", "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJob(ctx, d.Pool, at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"cost":0.000001}}`))
	}))
	defer server.Close()
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(server.URL, "test", "", ""))
	router.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.Roles[llm.RoleSmart] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "test:free"}, Fallback: llm.Off}
		return s
	}
	modelCtx := autopilotModelBudget(ctx, d, job, 3, 2)
	for i := 0; i < 2; i++ {
		if _, err := router.Generate(modelCtx, &llm.Request{Role: llm.RoleSmart, Prompt: "test", Quiet: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := router.Generate(modelCtx, &llm.Request{Role: llm.RoleSmart, Prompt: "test", Quiet: true}); !errors.Is(err, errAutopilotBudgetExhausted) {
		t.Fatalf("expected exhaustion: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	var used int
	if err := d.Pool.QueryRow(ctx, `SELECT used FROM source_autopilot_budget WHERE scope='server' AND resource='llm_attempt'`).Scan(&used); err != nil || used != 2 {
		t.Fatalf("server budget=%d err=%v", used, err)
	}
}

func TestAutopilotNetworkBudgetAtomicPostgres(t *testing.T) {
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
	for _, query := range []string{
		`DELETE FROM source_autopilot_reservations WHERE resource='public_fetch'`,
		`DELETE FROM source_autopilot_budget WHERE resource='public_fetch'`,
		`DELETE FROM source_autopilot_jobs WHERE scope='test-network'`,
	} {
		if _, err := d.Pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_jobs WHERE scope='test-network'`)
	})
	at := time.Now()
	if err := enqueueAutopilotJob(ctx, d.Pool, "test-network", "topic", "discovery", "topic", "test", 1, at); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJob(ctx, d.Pool, at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	gateCtx := autopilotNetworkBudget(ctx, d, job, 3, 2)
	gate := gateCtx.Value(autopilotNetworkGateKey{}).(func(context.Context) error)
	for i := 0; i < 2; i++ {
		if err := gate(gateCtx); err != nil {
			t.Fatal(err)
		}
	}
	if err := gate(gateCtx); !errors.Is(err, errAutopilotBudgetExhausted) {
		t.Fatalf("expected topic limit: %v", err)
	}
	var serverUsed, topicUsed int
	if err := d.Pool.QueryRow(ctx, `SELECT used FROM source_autopilot_budget WHERE scope='server' AND resource='public_fetch'`).Scan(&serverUsed); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT used FROM source_autopilot_budget WHERE scope='test-network:topic' AND resource='public_fetch'`).Scan(&topicUsed); err != nil {
		t.Fatal(err)
	}
	if serverUsed != 2 || topicUsed != 2 {
		t.Fatalf("partial reservation: server=%d topic=%d", serverUsed, topicUsed)
	}
}


func TestAutopilotSearchFetchCap(t *testing.T) {
	var charged atomic.Int32
	ctx := context.WithValue(context.Background(), autopilotNetworkGateKey{}, func(context.Context) error {
		charged.Add(1)
		return nil
	})
	searchCtx := withAutopilotSearchFetchCap(ctx, 3)
	gate := searchCtx.Value(autopilotNetworkGateKey{}).(func(context.Context) error)
	for i := 0; i < 3; i++ {
		if err := gate(searchCtx); err != nil {
			t.Fatal(err)
		}
	}
	if autopilotSearchFetchCapReached(searchCtx) {
		t.Fatal("cap reached before an extra request")
	}
	if err := gate(searchCtx); !errors.Is(err, errAutopilotSearchFetchCap) {
		t.Fatalf("unexpected cap error: %v", err)
	}
	if !autopilotSearchFetchCapReached(searchCtx) || charged.Load() != 3 {
		t.Fatalf("cap=%v charged=%d", autopilotSearchFetchCapReached(searchCtx), charged.Load())
	}
	// Candidate checks use the original context, not the search sub-budget.
	original := ctx.Value(autopilotNetworkGateKey{}).(func(context.Context) error)
	if err := original(ctx); err != nil {
		t.Fatal(err)
	}
	if charged.Load() != 4 {
		t.Fatalf("candidate review budget was blocked: %d", charged.Load())
	}
}

func TestAutopilotPaidDailyThreshold(t *testing.T) {
	if got := autopilotPaidLimitMicro(withAutopilotPaidLimit(context.Background(), 2)); got != 2000000 {
		t.Fatalf("paid threshold: got %d; want 2000000", got)
	}
	if got := autopilotPaidLimitMicro(withAutopilotPaidLimit(context.Background(), 0)); got != 0 {
		t.Fatalf("disabled paid calls should have zero threshold: %d", got)
	}
	if got := autopilotPaidLimitMicro(context.Background()); got != -1 {
		t.Fatalf("unset legacy context should not block: %d", got)
	}
}
