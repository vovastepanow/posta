package posta

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestSettingsConcurrentIndependentChangesPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set; requires disposable database")
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
	a := &App{DB: d, Mem: NewMemCache(100)}
	uid := randHex(16)
	defer d.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, uid)
	if err := a.User(ctx, uid, nil).SaveSettings(jx.M{"topic": "Мемы"}); err != nil {
		t.Fatal(err)
	}
	const workers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- a.User(ctx, uid, nil).SaveSettings(jx.M{fmt.Sprintf("test_patch_%d", i): i + 1})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := a.User(ctx, uid, nil).Settings()
	for i := 0; i < workers; i++ {
		if jx.Int(s, fmt.Sprintf("test_patch_%d", i)) != int64(i+1) {
			t.Fatalf("concurrent patch %d was lost: %v", i, s)
		}
	}
	if jx.Str(s, "topic") != "Мемы" {
		t.Fatal("unrelated topic setting was overwritten")
	}
}
