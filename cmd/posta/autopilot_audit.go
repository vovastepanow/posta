package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/posta"
)

// The audit is local, read-only, and deliberately does not call external
// providers or upload production data. Resolve exact topic keys from jobs.
func autopilotAudit(ctx context.Context, cfg config.Server, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("использование: posta autopilot-audit [часть названия темы]")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer d.Close()
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	filter := ""
	if len(args) == 1 {
		filter = strings.ToLower(strings.TrimSpace(args[0]))
	}
	jobs, err := db.All(ctx, tx, `SELECT DISTINCT topic,topic_key,scope FROM source_autopilot_jobs
		WHERE scope IN ('public','public:shadow') AND kind IN ('auto_discovery','shadow_discovery')
		ORDER BY topic,scope,topic_key`)
	if err != nil {
		return err
	}
	at := time.Now()
	out := make([]jx.M, 0)
	for _, job := range jobs {
		topic, key, scope := jx.Str(job, "topic"), jx.Str(job, "topic_key"), jx.Str(job, "scope")
		if filter != "" && !strings.Contains(strings.ToLower(topic), filter) {
			continue
		}
		funnel, err := posta.AutopilotAuditFunnel(ctx, tx, scope, key, at)
		if err != nil {
			return err
		}
		funnel["topic"], funnel["topic_key"] = topic, key
		out = append(out, funnel)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
