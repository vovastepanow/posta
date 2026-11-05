package posta

import (
	"context"
	"log"
	"time"
)

// Discover independently of the post collector, for recently active profiles.
// One topic is processed at a time; discovery's existing topic lock also protects
// against overlap with a manually started or collector-driven pass.
func (a *App) runSourceDiscoveryWorker(ctx context.Context) {
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	for {
		a.runScheduledSourceDiscovery(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) runScheduledSourceDiscovery(ctx context.Context) {
	rows, err := a.DB.Pool.Query(ctx, `SELECT id FROM users WHERE last_seen > $1 ORDER BY last_seen DESC`, now()-30*86400)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("автоподбор источников: %v", err)
		}
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		log.Printf("автоподбор источников: %v", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		u := a.User(ctx, id, func(line string) { log.Printf("фоновый подбор: %s", line) })
		topic := u.CurrentTopic()
		if a.ParserRunning(id) || !u.SourceDiscoveryDue(u.Catalog(), topic) {
			continue
		}
		log.Printf("фоновый подбор источников: запуск по теме %q", topic)
		u.RunSourceDiscovery(topic, 0)
		log.Printf("фоновый подбор источников: проход по теме %q завершён", topic)
	}
}
