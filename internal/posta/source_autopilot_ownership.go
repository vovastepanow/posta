package posta

import (
	"context"
	"time"
)

// A session advisory lock coordinates the old discovery path and autopilot
// across server processes without holding a database transaction over I/O.
func (a *App) trySourceTopicOwnership(ctx context.Context, topic string) (func(), bool, error) {
	a.discoveryOwnershipOnce.Do(func() { a.discoveryOwnershipSlots = make(chan struct{}, 4) })
	select {
	case a.discoveryOwnershipSlots <- struct{}{}:
	default:
		return nil, false, nil
	}
	conn, err := a.DB.Pool.Acquire(ctx)
	if err != nil {
		<-a.discoveryOwnershipSlots
		return nil, false, err
	}
	key := "source-discovery-topic:" + autopilotTopicKey(topic)
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(cleanup)
		<-a.discoveryOwnershipSlots
		return nil, false, err
	}
	if !locked {
		conn.Release()
		<-a.discoveryOwnershipSlots
		return nil, false, nil
	}
	release := func() {
		defer func() { <-a.discoveryOwnershipSlots }()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&unlocked); err != nil || !unlocked {
			// A pooled connection must never retain a session lock.
			_ = conn.Hijack().Close(cleanup)
			return
		}
		conn.Release()
	}
	return release, true, nil
}
