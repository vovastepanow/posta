package posta

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"posta/internal/db"
)

func TestLLMFreeQuotaConcurrentPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
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
	day := "test-" + randHex(8)
	defer d.Pool.Exec(ctx, "DELETE FROM kv WHERE key=$1", "llm_free_used:"+day)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := &App{DB: d}
			if _, err := a.reserveLLMFree(ctx, day, 5); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	a := &App{DB: d}
	if successes.Load() != 5 || KV(a, ctx, "llm_free_used:"+day, 0) != 5 {
		t.Fatalf("concurrent quota: successes=%d", successes.Load())
	}
	if _, err := a.reserveLLMFree(ctx, day, 5); err == nil {
		t.Fatal("restart bypassed durable quota")
	}
}
