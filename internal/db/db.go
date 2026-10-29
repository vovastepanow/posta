// Package db — Postgres: пул соединений, миграции и короткие помощники для запросов.
package db

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Querier — общее у пула и транзакции: функции доступа к данным работают с любым из них.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type DB struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("адрес Postgres: %w", err)
	}
	if cfg.MaxConns < 16 {
		cfg.MaxConns = 16
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("Postgres недоступен: %w", err)
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// Migrate применяет новые файлы migrations/NNNN_*.sql по порядку, каждый — в своей транзакции.
func (d *DB) Migrate(ctx context.Context) (applied []string, err error) {
	if _, err = d.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, path := range names {
		name := strings.TrimPrefix(path, "migrations/")
		var exists bool
		if err = d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&exists); err != nil {
			return applied, err
		}
		if exists {
			continue
		}
		body, _ := migrations.ReadFile(path)
		err = d.Tx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("миграция %s: %w", name, err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return applied, err
		}
		applied = append(applied, name)
	}
	return applied, nil
}

// Tx — транзакция: fn → COMMIT, ошибка или паника → ROLLBACK.
func (d *DB) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.Pool, fn)
}

// One — одна строка в map (nil, если строк нет).
func One(ctx context.Context, q Querier, sql string, args ...any) (map[string]any, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

// All — все строки в map.
func All(ctx context.Context, q Querier, sql string, args ...any) ([]map[string]any, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToMap)
}

// Value — первое поле первой строки в dest; found=false, если строк нет.
func Value(ctx context.Context, q Querier, dest any, sql string, args ...any) (found bool, err error) {
	err = q.QueryRow(ctx, sql, args...).Scan(dest)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Exists — есть ли хотя бы одна строка.
func Exists(ctx context.Context, q Querier, sql string, args ...any) (bool, error) {
	var one int
	return Value(ctx, q, &one, sql, args...)
}

// Exec — число затронутых строк.
func Exec(ctx context.Context, q Querier, sql string, args ...any) (int64, error) {
	tag, err := q.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

// JSON — значение для столбца jsonb (без экранирования HTML и с неверным UTF-8, заменённым на U+FFFD).
func JSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return []byte(strings.ToValidUTF8(string(b), "�"))
}

// IsUnique — нарушение уникальности (код 23505).
func IsUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
