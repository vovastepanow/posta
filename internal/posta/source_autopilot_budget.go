package posta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/db"
	"posta/internal/llm"
)

var errAutopilotBudgetExhausted = errors.New("source autopilot budget exhausted")
var errAutopilotReservationExists = errors.New("source autopilot reservation already charged")

type autopilotNetworkGateKey struct{}
type autopilotBudgetTrackerKey struct{}
type autopilotBudgetTracker struct {
	mu  sync.Mutex
	err error
}

func autopilotBudgetTopicScope(job *autopilotJob) string {
	scope := job.Scope
	if scope == autopilotShadowScope {
		scope = autopilotAutoScope
	}
	return scope + ":" + job.TopicKey
}

func withAutopilotBudgetTracker(ctx context.Context) context.Context {
	return context.WithValue(ctx, autopilotBudgetTrackerKey{}, &autopilotBudgetTracker{})
}

func noteAutopilotBudgetError(ctx context.Context, err error) error {
	if err != nil {
		if tracker, ok := ctx.Value(autopilotBudgetTrackerKey{}).(*autopilotBudgetTracker); ok {
			tracker.mu.Lock()
			if tracker.err == nil {
				tracker.err = err
			}
			tracker.mu.Unlock()
		}
	}
	return err
}

func autopilotBudgetError(ctx context.Context) error {
	if tracker, ok := ctx.Value(autopilotBudgetTrackerKey{}).(*autopilotBudgetTracker); ok {
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		return tracker.err
	}
	return nil
}

// The gate is invoked before every fetch, including each redirect hop.
func autopilotNetworkBudget(ctx context.Context, database *db.DB, job *autopilotJob, serverLimit, topicLimit int64) context.Context {
	return context.WithValue(ctx, autopilotNetworkGateKey{}, func(ctx context.Context) error {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return noteAutopilotBudgetError(ctx, err)
		}
		id := hex.EncodeToString(nonce[:])
		err := withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
			if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
				return err
			}
			var at time.Time
			if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
				return err
			}
			if err := reserveAutopilotBudgetTx(ctx, tx, id+":server", "server", "public_fetch", at, 1, serverLimit); err != nil {
				return err
			}
			return reserveAutopilotBudgetTx(ctx, tx, id+":topic", autopilotBudgetTopicScope(job), "public_fetch", at, 1, topicLimit)
		})
		return noteAutopilotBudgetError(ctx, err)
	})
}

// autopilotModelBudget gates every actual router attempt, including fallback.
// Server and topic limits are reserved atomically under the same lease.
func autopilotModelBudget(ctx context.Context, database *db.DB, job *autopilotJob, serverLimit, topicLimit int64) context.Context {
	return llm.WithCallGate(ctx, func(ctx context.Context, _ llm.Target) error {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return noteAutopilotBudgetError(ctx, err)
		}
		id := hex.EncodeToString(nonce[:])
		err := withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
			if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
				return err
			}
			var at time.Time
			if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
				return err
			}
			if err := reserveAutopilotBudgetTx(ctx, tx, id+":server", "server", "llm_attempt", at, 1, serverLimit); err != nil {
				return err
			}
			return reserveAutopilotBudgetTx(ctx, tx, id+":topic", autopilotBudgetTopicScope(job), "llm_attempt", at, 1, topicLimit)
		})
		return noteAutopilotBudgetError(ctx, err)
	})
}

// reserveAutopilotBudget charges a UTC calendar day before the external call.
// IDs must identify a single logical call and are never reused for retries.
func reserveAutopilotBudget(ctx context.Context, database *db.DB, id, scope, resource string, at time.Time, amount, limit int64) error {
	return database.Tx(ctx, func(tx pgx.Tx) error {
		return reserveAutopilotBudgetTx(ctx, tx, id, scope, resource, at, amount, limit)
	})
}

// A worker must reserve using the same fenced transaction that verifies its
// lease, otherwise a stale worker could consume budget after ownership changed.
func reserveAutopilotJobBudget(ctx context.Context, database *db.DB, job *autopilotJob, id, scope, resource string, at time.Time, amount, limit int64) error {
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		return reserveAutopilotBudgetTx(ctx, tx, id, scope, resource, at, amount, limit)
	})
}

func reserveAutopilotBudgetTx(ctx context.Context, tx pgx.Tx, id, scope, resource string, at time.Time, amount, limit int64) error {
	if id == "" || scope == "" || resource == "" || amount <= 0 || limit < amount {
		return errAutopilotBudgetExhausted
	}
	day := at.UTC().Format("2006-01-02")
	tag, err := tx.Exec(ctx, `INSERT INTO source_autopilot_reservations(reservation_id,scope,resource,day,amount)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, scope, resource, day, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var same bool
		err = tx.QueryRow(ctx, `SELECT scope=$2 AND resource=$3 AND day=$4::date AND amount=$5
				FROM source_autopilot_reservations WHERE reservation_id=$1`, id, scope, resource, day, amount).Scan(&same)
		if err != nil {
			return err
		}
		if !same {
			return errors.New("reservation ID reused for a different call")
		}
		// The prior process may have made the external call before crashing.
		// Success here would authorize an uncharged second call.
		return errAutopilotReservationExists
	}
	tag, err = tx.Exec(ctx, `INSERT INTO source_autopilot_budget(scope,resource,day,used) VALUES($1,$2,$3,$4)
			ON CONFLICT(scope,resource,day) DO UPDATE SET used=source_autopilot_budget.used+EXCLUDED.used
			WHERE source_autopilot_budget.used <= $5-EXCLUDED.used`, scope, resource, day, amount, limit)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errAutopilotBudgetExhausted
	}
	return nil
}
