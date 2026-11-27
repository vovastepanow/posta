package posta

import (
	"context"
	"time"
)

// Reserve a conservative upper bound before each request. Failed requests keep
// the reservation; successful requests settle against the provider's real cost.
func (u *U) reserveClassification() (func(float64), bool) {
	const reserve = 0.025
	if u.DB == nil {
		return func(float64) {}, true
	} // isolated tests use their own router budget
	key := "classification_budget:" + time.Now().UTC().Format("2006-01-02")
	var value float64
	err := u.DB.Pool.QueryRow(u.Ctx, `INSERT INTO kv(key,value,expires_at,updated_at) VALUES($1,'0.025'::jsonb,$2,now()) ON CONFLICT(key) DO UPDATE SET value=to_jsonb((kv.value::text)::numeric+0.025),updated_at=now() WHERE (kv.value::text)::numeric+0.025<=0.10 RETURNING (value::text)::float8`, key, time.Now().Add(7*24*time.Hour).Unix()).Scan(&value)
	if err != nil {
		return nil, false
	}
	return func(cost float64) {
		if cost <= 0 || cost > reserve {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		u.DB.Pool.Exec(ctx, `UPDATE kv SET value=to_jsonb(GREATEST(0,(value::text)::numeric-$2::numeric)),updated_at=now() WHERE key=$1`, key, reserve-cost)
	}, true
}
