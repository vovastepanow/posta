package web

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/posta"
)

// POSTA_TEST_PG must refer to a disposable database.
func TestAdminPromptEndpointsPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS kv (key text primary key, value jsonb not null, expires_at bigint not null default 0, updated_at timestamptz not null default now())`); err != nil {
		t.Fatal(err)
	}
	a := &posta.App{DB: d}
	if err := a.KVSet(ctx, "admin:owners", []string{"test-owner"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `DELETE FROM kv WHERE key='admin:prompts'`); err != nil {
		t.Fatal(err)
	}
	c := &Ctx{A: a, U: a.User(ctx, "test-owner", nil), R: httptest.NewRequest("POST", "/", nil), In: jx.M{"id": "geo", "value": "TEST {{name}}"}}
	if _, err = routes["admin_prompt_save"].fn(c); err != nil {
		t.Fatal(err)
	}
	got, err := routes["admin_prompts"].fn(c)
	if err != nil {
		t.Fatal(err)
	}
	find := func(v any) posta.PromptTemplate {
		for _, p := range v.(jx.M)["prompts"].([]posta.PromptTemplate) {
			if p.ID == "geo" {
				return p
			}
		}
		t.Fatal("missing geo")
		return posta.PromptTemplate{}
	}
	if p := find(got); p.Value != "TEST {{name}}" {
		t.Fatalf("saved %q", p.Value)
	}
	c.In = jx.M{"id": "geo", "reset": true}
	if _, err = routes["admin_prompt_save"].fn(c); err != nil {
		t.Fatal(err)
	}
	got, err = routes["admin_prompts"].fn(c)
	if err != nil {
		t.Fatal(err)
	}
	if p := find(got); p.Value != p.Default {
		t.Fatalf("reset %q", p.Value)
	}
	c.U = a.User(ctx, "not-owner", nil)
	if _, err = routes["admin_prompt_save"].fn(c); err == nil {
		t.Fatal("non-owner could edit prompt")
	}
}
