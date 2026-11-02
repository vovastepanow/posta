package posta

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
)

// ---- kv: настоящее состояние приложения (флаги админки, квоты, задания). Значение — сам JSON. ----

// KVRaw — значение ключа или nil (нет или просрочен).
func (a *App) KVRaw(ctx context.Context, key string) json.RawMessage {
	var raw []byte
	found, err := db.Value(ctx, a.DB.Pool, &raw, `SELECT value FROM kv WHERE key = $1 AND (expires_at = 0 OR expires_at > $2)`, key, now())
	if err != nil || !found {
		return nil
	}
	return raw
}

// KVGet — значение ключа в dest; false — ключа нет.
func (a *App) KVGet(ctx context.Context, key string, dest any) bool {
	raw := a.KVRaw(ctx, key)
	return raw != nil && json.Unmarshal(raw, dest) == nil
}

// KV — значение ключа или def.
func KV[T any](a *App, ctx context.Context, key string, def T) T {
	var v T
	if a.KVGet(ctx, key, &v) {
		return v
	}
	return def
}

// KVSet — записать значение; ttl > 0 — ключ временный.
func (a *App) KVSet(ctx context.Context, key string, value any, ttl time.Duration) error {
	exp := int64(0)
	if ttl > 0 {
		exp = time.Now().Add(ttl).Unix()
	}
	_, err := a.DB.Pool.Exec(ctx, `INSERT INTO kv (key, value, expires_at, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at, updated_at = now()`, key, db.JSON(value), exp)
	return err
}

func (a *App) KVDelete(ctx context.Context, key string) error {
	_, err := a.DB.Pool.Exec(ctx, `DELETE FROM kv WHERE key = $1`, key)
	return err
}

func (a *App) KVDeletePrefix(ctx context.Context, prefix string) error {
	_, err := a.DB.Pool.Exec(ctx, `DELETE FROM kv WHERE key LIKE $1`, strings.ReplaceAll(prefix, "%", `\%`)+"%")
	return err
}

// KVUpdate — изменить значение под блокировкой строки: fn получает текущее (nil — нет) и возвращает новое; nil — не менять.
func KVUpdate[T any](a *App, ctx context.Context, key string, ttl time.Duration, fn func(cur T, exists bool) *T) (T, error) {
	var result T
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		var raw []byte
		var exp int64
		err := tx.QueryRow(ctx, `SELECT value, expires_at FROM kv WHERE key = $1 FOR UPDATE`, key).Scan(&raw, &exp)
		exists := err == nil && (exp == 0 || exp > now())
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		var cur T
		if exists {
			json.Unmarshal(raw, &cur)
		}
		next := fn(cur, exists)
		if next == nil {
			result = cur
			return nil
		}
		result = *next
		expAt := int64(0)
		if ttl > 0 {
			expAt = time.Now().Add(ttl).Unix()
		}
		_, err = tx.Exec(ctx, `INSERT INTO kv (key, value, expires_at, updated_at) VALUES ($1, $2, $3, now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at, updated_at = now()`, key, db.JSON(next), expAt)
		return err
	})
	return result, err
}

// ---- cache: дорогие кэши, переживающие перезапуск (переводы, статьи, оценки тредов). ----

func (a *App) CacheGet(ctx context.Context, key string, dest any) bool {
	var raw []byte
	found, err := db.Value(ctx, a.DB.Pool, &raw, `SELECT value FROM cache WHERE key = $1 AND expires_at > $2`, key, now())
	return err == nil && found && json.Unmarshal(raw, dest) == nil
}

func (a *App) CacheSet(ctx context.Context, key string, value any, ttl time.Duration) {
	a.DB.Pool.Exec(ctx, `INSERT INTO cache (key, value, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at`, key, db.JSON(value), time.Now().Add(ttl).Unix())
}

// CacheGC — уборка просроченного kv и cache (раз в час).
func (a *App) CacheGC(ctx context.Context) {
	t := now()
	a.DB.Pool.Exec(ctx, `DELETE FROM cache WHERE expires_at <= $1`, t)
	a.DB.Pool.Exec(ctx, `DELETE FROM kv WHERE expires_at > 0 AND expires_at <= $1`, t)
}

// ---- MemCache: сырые ответы площадок и прочее на минуты — только в памяти процесса. ----

type memEntry struct {
	val any
	exp time.Time
}

type MemCache struct {
	mu sync.Mutex
	m  map[string]memEntry
	// limit — сколько ключей держать; при переполнении удаляются просроченные, затем самые старые по сроку
	limit int
}

func NewMemCache(limit int) *MemCache { return &MemCache{m: map[string]memEntry{}, limit: limit} }

func (c *MemCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.val, true
}

func (c *MemCache) Set(key string, val any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.limit {
		c.evictLocked()
	}
	c.m[key] = memEntry{val, time.Now().Add(ttl)}
}

func (c *MemCache) Delete(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}

func (c *MemCache) evictLocked() {
	t := time.Now()
	for k, e := range c.m {
		if t.After(e.exp) {
			delete(c.m, k)
		}
	}
	for len(c.m) >= c.limit {
		var oldest string
		var at time.Time
		for k, e := range c.m {
			if oldest == "" || e.exp.Before(at) {
				oldest, at = k, e.exp
			}
		}
		delete(c.m, oldest)
	}
}

// Mem — типизированное чтение из MemCache.
func Mem[T any](c *MemCache, key string) (T, bool) {
	v, ok := c.Get(key)
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}
