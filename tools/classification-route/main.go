// Controlled classifier fallback rollout. Never changes content labels.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"posta/internal/config"
	"posta/internal/db"
	"time"
)

const fallback = `{"provider":"openrouter","model":"nvidia/nemotron-3.5-super-vl:free"}`
const requestedRoute = `{"primary":{"provider":"openrouter","model":"nvidia/nemotron-3.5-super-vl:free"},"fallback":{"provider":"openrouter","model":"qwen/qwen3.7-flash"}}`
const requestedVerifier = `{"primary":{"provider":"openrouter","model":"nvidia/nemotron-3.5-super-vl:free"},"fallback":{"provider":"openrouter","model":"qwen/qwen3.7-flash"}}`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: classification-route snapshot|configure|enable|restore|verify directory")
	}
	mode, dir := os.Args[1], os.Args[2]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := config.LoadServer()
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer d.Close()
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if mode == "snapshot" {
		tx, e := d.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		queries := map[string]string{
			"settings-before.json":      `SELECT value FROM kv WHERE key='admin:llm'`,
			"pool-labels-before.json":   `SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'user_id',user_id,'image_key',image_key,'tags',data->'tags') ORDER BY id),'[]') FROM pool_items`,
			"public-labels-before.json": `SELECT coalesce(jsonb_agg(jsonb_build_object('source',source_key,'key',post_key,'tag',topic_key) ORDER BY source_key,post_key,topic_key),'[]') FROM public_post_topics`,
		}
		for name, q := range queries {
			var raw []byte
			if e = tx.QueryRow(ctx, q).Scan(&raw); e != nil {
				return e
			}
			f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return e
			}
			_, e = f.Write(raw)
			f.Close()
			if e != nil {
				return e
			}
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		fmt.Println("Snapshot saved; no database writes")
		return nil
	}
	before, e := os.ReadFile(filepath.Join(dir, "settings-before.json"))
	if e != nil {
		return e
	}
	if mode == "configure" {
		tag, e := d.Pool.Exec(ctx, `UPDATE kv SET value=jsonb_set(jsonb_set(value,'{roles,classify}',$2::jsonb),'{roles,classify_verify}',$3::jsonb),updated_at=now() WHERE key='admin:llm' AND value=$1::jsonb`, before, requestedRoute, requestedVerifier)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("settings changed concurrently; not updated")
		}
		fmt.Println("Configured Nemotron for proposal and verification; Qwen is fallback; budgets unchanged")
		return nil
	}
	if mode == "enable" {
		tag, e := d.Pool.Exec(ctx, `UPDATE kv SET value=jsonb_set(value,'{roles,classify,fallback}',$2::jsonb),updated_at=now() WHERE key='admin:llm' AND value=$1::jsonb`, before, fallback)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("settings changed concurrently; not updated")
		}
		fmt.Println("Enabled free classifier fallback; other settings unchanged")
		return nil
	}
	if mode == "restore" {
		tag, e := d.Pool.Exec(ctx, `UPDATE kv SET value=jsonb_set(value,'{roles,classify,fallback}',($1::jsonb)#>'{roles,classify,fallback}'),updated_at=now() WHERE key='admin:llm' AND value#>'{roles,classify,fallback}'=$2::jsonb`, before, fallback)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("fallback changed concurrently; not restored")
		}
		fmt.Println("Restored previous classifier fallback only")
		return nil
	}
	if mode == "verify" {
		var current []byte
		if e = d.Pool.QueryRow(ctx, `SELECT value FROM kv WHERE key='admin:llm'`).Scan(&current); e != nil {
			return e
		}
		var settings map[string]any
		json.Unmarshal(current, &settings)
		roles, _ := settings["roles"].(map[string]any)
		r, _ := json.Marshal(roles["classify"])
		fmt.Println("Classifier route:", string(r))
		v, _ := json.Marshal(roles["classify_verify"])
		fmt.Println("Verifier route:", string(v))
		pool, e := os.ReadFile(filepath.Join(dir, "pool-labels-before.json"))
		if e != nil {
			return e
		}
		pub, e := os.ReadFile(filepath.Join(dir, "public-labels-before.json"))
		if e != nil {
			return e
		}
		var changed, removed int
		if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM jsonb_array_elements($1::jsonb) old LEFT JOIN pool_items p ON ((old ? 'image_key' AND old->>'image_key' <> '' AND p.user_id=old->>'user_id' AND p.image_key=old->>'image_key') OR ((NOT old ? 'image_key' OR old->>'image_key' = '') AND p.id=(old->>'id')::bigint)) WHERE p.id IS NULL OR coalesce(p.data->'tags','null'::jsonb) IS DISTINCT FROM old->'tags'`, pool).Scan(&changed); e != nil {
			return e
		}
		if e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM jsonb_array_elements($1::jsonb) old WHERE NOT EXISTS (SELECT 1 FROM public_post_topics t WHERE t.source_key=old->>'source' AND t.post_key=old->>'key' AND t.topic_key=old->>'tag')`, pub).Scan(&removed); e != nil {
			return e
		}
		if changed != 0 {
			rows, err := db.All(ctx, d.Pool, `SELECT old->>'id' AS id, old->'tags' AS before_tags, p.data->'tags' AS after_tags, p.id IS NULL AS record_absent FROM jsonb_array_elements($1::jsonb) old LEFT JOIN pool_items p ON ((old ? 'image_key' AND old->>'image_key' <> '' AND p.user_id=old->>'user_id' AND p.image_key=old->>'image_key') OR ((NOT old ? 'image_key' OR old->>'image_key' = '') AND p.id=(old->>'id')::bigint)) WHERE p.id IS NULL OR coalesce(p.data->'tags','null'::jsonb) IS DISTINCT FROM old->'tags'`, pool)
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(rows)
			fmt.Println("Label differences:", string(raw))
		}
		fmt.Printf("Existing pool label sets changed: %d; existing public labels removed: %d\n", changed, removed)
		if changed != 0 || removed != 0 {
			return fmt.Errorf("label snapshot differs; inspect without overwriting concurrent changes")
		}
		return nil
	}
	return fmt.Errorf("unknown mode")
}
