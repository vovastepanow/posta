package posta

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// A persistent, atomic quota reservation shared by every OpenRouter route.
// Use the existing UTC counter key so upgrading does not reset today's usage.
func (a *App) reserveLLMFree(ctx context.Context, day string, limit int) (int, error) {
	var count int
	err := a.DB.Pool.QueryRow(ctx, `INSERT INTO kv(key,value,expires_at,updated_at)
		VALUES($1,'1'::jsonb,$2,now())
		ON CONFLICT(key) DO UPDATE SET value=to_jsonb((kv.value::text)::integer+1),
		expires_at=EXCLUDED.expires_at,updated_at=now()
		WHERE $3 <= 0 OR (kv.value::text)::integer < $3
		RETURNING (value::text)::integer`, "llm_free_used:"+day, time.Now().Add(3*24*time.Hour).Unix(), limit).Scan(&count)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("достигнут заданный лимит бесплатных запросов")
	}
	if err != nil {
		return 0, fmt.Errorf("не удалось зарезервировать квоту ИИ: %w", err)
	}
	return count, nil
}
