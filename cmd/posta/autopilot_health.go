package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

// autopilot-health is intentionally read-only. It never calls a model,
// searches external sites, claims jobs, or modifies the production database.
func autopilotHealth(ctx context.Context, cfg config.Server, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("использование: posta autopilot-health [часть названия темы]")
	}
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer d.Close()

	rows, err := d.Pool.Query(ctx, `SELECT topic,enabled,demand_active,next_run_at,lease_until,
		last_error,checkpoint::text
		FROM source_autopilot_jobs
		WHERE scope='public' AND kind='auto_discovery'
		ORDER BY topic`)
	if err != nil {
		return fmt.Errorf("запрос состояния автопилота: %w (примените миграции перед проверкой)", err)
	}
	defer rows.Close()
	filter := ""
	if len(args) > 0 {
		filter = strings.ToLower(strings.TrimSpace(args[0]))
	}
	found := 0
	for rows.Next() {
		var topic, lastError, checkpoint string
		var enabled, active bool
		var next, lease int64
		if err := rows.Scan(&topic, &enabled, &active, &next, &lease, &lastError, &checkpoint); err != nil {
			return err
		}
		if filter != "" && !strings.Contains(strings.ToLower(topic), filter) {
			continue
		}
		found++
		fmt.Println(formatAutopilotHealthRow(topic, enabled, active, next, lease, lastError, jx.Unmarshal([]byte(checkpoint)), time.Now().Unix()))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found == 0 {
		fmt.Println("Автоматических тем не найдено. Проверьте, включён ли режим «Автоматический» в Posta.")
	}
	return nil
}

func formatAutopilotHealthRow(topic string, enabled, active bool, next, lease int64, lastError string, checkpoint jx.M, at int64) string {
	status := "ожидает"
	switch {
	case !enabled || !active:
		status = "выключен"
	case lease > at:
		status = "работает"
	case lastError != "":
		// Do not print untrusted backend errors: URLs may include credentials.
		status = "последний цикл завершился ошибкой"
	}
	nextLabel := "не запланирован"
	if next > 0 {
		nextLabel = time.Unix(next, 0).UTC().Format("2006-01-02 15:04 UTC")
	}
	outcome := jx.Str(checkpoint, "last_search_outcome")
	if outcome == "" {
		outcome = "ещё не проверялся"
	}
	return fmt.Sprintf("Тема: %s | %s | следующий запуск: %s | поиск: %s | кандидаты: %d, авторы из постов: %d | AI принято: %d, отклонено: %d",
		topic, status, nextLabel, outcome,
		jx.Int(checkpoint, "last_source_candidates"),
		jx.Int(checkpoint, "last_direct_sources"),
		jx.Int(checkpoint, "direct_accepted_total"),
		jx.Int(checkpoint, "direct_rejected_total"))
}
