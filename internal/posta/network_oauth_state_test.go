package posta

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestNetworkOAuthStateSingleUsePostgres(t *testing.T) {
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
	state, err := a.NetworkOAuthStateCreate(ctx, uid, "instagram", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	defer a.KVDelete(ctx, "netoauth:"+state)
	if got := a.NetworkOAuthStateTake(ctx, state, "wrong-profile"); got != nil {
		t.Fatalf("wrong profile consumed OAuth state")
	}
	var consumed atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got := a.NetworkOAuthStateTake(ctx, state, uid)
			if got != nil {
				consumed.Add(1)
				if jx.Str(got, "verifier") != "verifier" || jx.Str(got, "platform") != "instagram" {
					t.Error("incorrect state returned")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if consumed.Load() != 1 {
		t.Fatalf("OAuth state consumed %d times", consumed.Load())
	}
	if a.NetworkOAuthStateTake(ctx, state, uid) != nil {
		t.Fatal("OAuth state replay accepted")
	}
}
