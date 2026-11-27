package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
)

func TestAutopilotSharedListingReusesOnlySuccessfulBoundedPublicRead(t *testing.T) {
	a := &App{autopilotListings: NewMemCache(2)}
	source := "telegram:marketing_test"
	res := FetchResultURL{FetchResult: FetchResult{OK: true, Status: 200, Bytes: []byte("original")}}
	calls := 0
	fetch := func(string) FetchResultURL { calls++; return res }
	if _, reused := a.autopilotReviewListing(source, source, true, fetch); reused || calls != 1 {
		t.Fatal("cold lookup should fetch")
	}
	a.rememberAutopilotListing(source, res, 2)
	got, reused := a.autopilotReviewListing(source, source, true, fetch)
	if !reused || calls != 1 {
		t.Fatal("cross-topic listing was not reused")
	}
	got.Bytes[0] = 'X'
	got, _ = a.autopilotReviewListing(source, source, true, fetch)
	if string(got.Bytes) != "original" {
		t.Fatal("shared listing mutated")
	}
	if _, reused = a.autopilotReviewListing(source, source, false, fetch); reused || calls != 2 {
		t.Fatal("explicit recheck must fetch")
	}
	a.rememberAutopilotListing("telegram:empty_test", res, 0)
	if _, reused = a.autopilotReviewListing("telegram:empty_test", "", true, fetch); reused {
		t.Fatal("empty result cached")
	}
	a.rememberAutopilotListing("telegram:large_test", FetchResultURL{FetchResult: FetchResult{OK: true, Status: 200, Bytes: make([]byte, autopilotListingMaxBytes+1)}}, 2)
	if _, reused = a.autopilotReviewListing("telegram:large_test", "", true, fetch); reused {
		t.Fatal("oversized result cached")
	}
}

func TestAutopilotRegistryIdentityPreservesVerdictsPostgres(t *testing.T) {
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
	source := "telegram:registry_" + randHex(6)
	defer d.Pool.Exec(ctx, "DELETE FROM source_registry WHERE source_id=$1", source)
	at := time.Now().Unix()
	remember := func() error {
		return d.Tx(ctx, func(tx pgx.Tx) error {
			return rememberAutopilotIdentity(ctx, tx, source, at)
		})
	}
	if err := remember(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `UPDATE source_registry SET status='owner_no',name='Manual',topics='{"smm":{"score":0,"reason":"off topic"}}' WHERE source_id=$1`, source); err != nil {
		t.Fatal(err)
	}
	if err := remember(); err != nil {
		t.Fatal(err)
	}
	var status, name string
	var verdictPreserved bool
	if err := d.Pool.QueryRow(ctx, `SELECT status,name,topics ? 'smm' FROM source_registry WHERE source_id=$1`, source).Scan(&status, &name, &verdictPreserved); err != nil {
		t.Fatal(err)
	}
	if status != "owner_no" || name != "Manual" || !verdictPreserved {
		t.Fatal("rediscovery overwrote a saved verdict")
	}
}
