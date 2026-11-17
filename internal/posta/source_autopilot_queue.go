package posta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"posta/internal/db"
	"posta/internal/jx"
)

var errAutopilotLeaseLost = errors.New("source autopilot lease lost")

type autopilotLeaseContextKey struct{}
type autopilotProjectionContextKey struct{}
type autopilotDeliveryContextKey struct{}

type autopilotDeliveryGuard struct {
	owner, source, topic string
	post, analysis       jx.M
}

func databaseAppCatalogEntryMatches(job *autopilotJob, entry jx.M) bool {
	if jx.Bool(entry, "autopilot_managed") && !jx.Bool(entry, "autopilot_manual") {
		profile := jx.Map(jx.Map(entry, "autopilot_profiles"), job.TopicKey)
		return jx.Str(profile, "state") == "probation" || jx.Str(profile, "state") == "active"
	}
	if q := strings.TrimSpace(jx.Str(entry, "query")); q != "" && !strings.EqualFold(q, TopicQueryOf(job.Topic)) {
		return false
	}
	for _, excluded := range jx.Strs(entry, "excluded_topics") {
		if TopicNorm(excluded) == TopicNorm(TopicBase(job.Topic)) {
			return false
		}
	}
	topics := jx.Strs(entry, "topics")
	if len(topics) == 0 {
		return true
	}
	for _, topic := range topics {
		if TopicNorm(topic) == TopicNorm(TopicBase(job.Topic)) {
			return true
		}
	}
	return false
}

func withAutopilotDelivery(ctx context.Context, owner, source, topic string, post, analysis jx.M) context.Context {
	return context.WithValue(ctx, autopilotDeliveryContextKey{}, autopilotDeliveryGuard{owner: owner, source: source, topic: topic, post: post, analysis: analysis})
}

type autopilotProjectionGuard struct {
	sourceID string
	remove   bool
	state    string
}

func withAutopilotProjection(ctx context.Context, sourceID, state string, remove bool) context.Context {
	return context.WithValue(ctx, autopilotProjectionContextKey{}, autopilotProjectionGuard{sourceID: sourceID, state: state, remove: remove})
}

func contextWithAutopilotLease(ctx context.Context, job *autopilotJob) context.Context {
	return context.WithValue(ctx, autopilotLeaseContextKey{}, job)
}

func autopilotCleanupContext(ctx, base context.Context) context.Context {
	if job, ok := ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); ok && job != nil {
		return contextWithAutopilotLease(base, job)
	}
	return base
}

// Existing callers keep their normal transaction semantics. Discovery workers
// carrying a lease obtain a row lock and validate the token in the transaction
// that performs each effect.
func autopilotEffectTx(ctx context.Context, database *db.DB, fn func(pgx.Tx) error) error {
	if job, ok := ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); ok && job != nil {
		return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
			if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
				return err
			}
			if guard, ok := ctx.Value(autopilotProjectionContextKey{}).(autopilotProjectionGuard); ok {
				if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, guard.sourceID); err != nil {
					return err
				}
				var state string
				if err := tx.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, guard.sourceID).Scan(&state); err != nil {
					return err
				}
				if state != guard.state || (!guard.remove && state != "probation" && state != "active") || (guard.remove && state != "rejected") {
					return errAutopilotSourceChanged
				}
				var blocked bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_overrides WHERE scope='public' AND topic_key=$1 AND source_id=$2 AND ((owner_id='' AND decision='excluded' AND NOT $3) OR (decision='pinned' AND $3)))`, job.TopicKey, guard.sourceID, guard.remove).Scan(&blocked); err != nil {
					return err
				}
				if blocked {
					return errAutopilotSourceOverride
				}
			}
			if guard, ok := ctx.Value(autopilotDeliveryContextKey{}).(autopilotDeliveryGuard); ok {
				if job.Kind != autopilotAutoKind || job.Scope != autopilotAutoScope || guard.owner == "" || guard.source == "" {
					return errAutopilotLeaseLost
				}
				if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, guard.source); err != nil {
					return err
				}
				var state string
				if err := tx.QueryRow(ctx, `SELECT state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, guard.source).Scan(&state); err != nil {
					return err
				}
				if state != "probation" && state != "active" {
					return errAutopilotSourceChanged
				}
				var globallyExcluded bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_overrides WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND owner_id='' AND decision='excluded')`, job.Scope, job.TopicKey, guard.source).Scan(&globallyExcluded); err != nil {
					return err
				}
				if globallyExcluded {
					return errAutopilotSourceOverride
				}
				// Serialize with catalog votes/removals, then require the source
				// still to be projected for this exact profile.
				var catalogRevision int64
				if err := tx.QueryRow(ctx, `SELECT version FROM catalog_revision WHERE id=true FOR SHARE`).Scan(&catalogRevision); err != nil {
					return err
				}
				var catalogRaw []byte
				_, id, kind, valid := autopilotSourceParts(guard.source)
				if !valid {
					return errAutopilotSourceOverride
				}
				alternateID := id
				if _, youtube := canonicalYouTubeFeed(id); youtube {
					alternateID = strings.Replace(id, "https://www.youtube.com/", "https://youtube.com/", 1)
				}
				if err := tx.QueryRow(ctx, `SELECT data FROM catalog_sources WHERE kind=$1 AND (source_id=$2 OR source_id=$3) ORDER BY (source_id=$2) DESC LIMIT 1`, kind, id, alternateID).Scan(&catalogRaw); err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						return errAutopilotSourceOverride
					}
					return err
				}
				entry := jx.Unmarshal(catalogRaw)
				if entry == nil || !databaseAppCatalogEntryMatches(job, entry) {
					return errAutopilotSourceOverride
				}
				oldID := legacyYouTubeSourceKey(guard.source)
				oldShortHost := strings.Replace(oldID, "rss:https://www.youtube.com/", "rss:https://youtube.com/", 1)
				voteRows, err := tx.Query(ctx, `SELECT data FROM source_votes WHERE source_id=$1 OR source_id=$2 OR source_id=$3`, guard.source, oldID, oldShortHost)
				if err != nil {
					return err
				}
				for voteRows.Next() {
					var raw []byte
					if err := voteRows.Scan(&raw); err != nil {
						voteRows.Close()
						return err
					}
					vote := jx.Unmarshal(raw)
					if jx.Str(vote, "vote") == "reject" && autopilotTopicKey(jx.Str(vote, "topic")) == job.TopicKey {
						voteRows.Close()
						return errAutopilotSourceOverride
					}
				}
				err = voteRows.Err()
				voteRows.Close()
				if err != nil {
					return err
				}
				var active bool
				if err := tx.QueryRow(ctx, `SELECT mode='auto' AND NOT paused AND posts_per_day>0 FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2 FOR SHARE`, guard.owner, job.TopicKey).Scan(&active); err != nil {
					return err
				}
				if !active {
					return errAutopilotLeaseLost
				}
				var settingsRaw []byte
				if err := tx.QueryRow(ctx, `SELECT settings FROM users WHERE id=$1 FOR SHARE`, guard.owner).Scan(&settingsRaw); err != nil {
					return err
				}
				settings := jx.Unmarshal(settingsRaw)
				legacy := jx.Map(jx.Map(settings, "source_overrides"), TopicNorm(job.Topic))
				exact := jx.Map(jx.Map(settings, "source_overrides_v2"), job.TopicKey)
				combined := jx.M{}
				for key, value := range legacy {
					combined[key] = value
				}
				for key, value := range exact {
					combined[key] = value
				}
				if sourceExcludedByOverride(combined, guard.source) {
					return errAutopilotSourceOverride
				}
			}
			return fn(tx)
		})
	}
	return database.Tx(ctx, fn)
}

type autopilotJob struct {
	Scope, TopicKey, Kind, Topic, Token string
	Generation                          int64
	Checkpoint                          json.RawMessage
	MaxProbation                        int
	SearchQuery                         string
}

// Legacy discovery writes into a shared catalog. It may only be used for a
// public job without a user account; private jobs require their own store and
// an independent public verification before touching the shared catalog.
func autopilotMayUseSharedDiscovery(job *autopilotJob, userID string) bool {
	return job != nil && job.Scope == "public" && userID == ""
}

// Enqueue only raises priority for existing jobs; it cannot bypass backoff or
// reset progress. A separate explicit resume operation is needed for disabled jobs.
func enqueueAutopilotJob(ctx context.Context, q db.Querier, scope, topicKey, kind, topic, reason string, priority int, at time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO source_autopilot_jobs
		(scope,topic_key,kind,topic,reason,priority,next_run_at,requested_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
		ON CONFLICT (scope,topic_key,kind) DO UPDATE SET
		priority=GREATEST(source_autopilot_jobs.priority,EXCLUDED.priority)`,
		scope, topicKey, kind, topic, reason, priority, at.Unix())
	return err
}

// setAutopilotJobEnabled affects exactly one scoped job. Revocation changes
// the generation and clears its token atomically, including when a worker is
// already running; a future resume retains its checkpoint and backoff.
func setAutopilotJobEnabled(ctx context.Context, q db.Querier, scope, topicKey, kind string, enabled bool) (bool, error) {
	if scope == "" || topicKey == "" || kind == "" {
		return false, errors.New("invalid job key")
	}
	tag, err := q.Exec(ctx, `UPDATE source_autopilot_jobs SET enabled=$4,
		generation=CASE WHEN NOT $4 THEN generation+1 ELSE generation END,
		lease_token=CASE WHEN NOT $4 THEN '' ELSE lease_token END,
		lease_until=CASE WHEN NOT $4 THEN 0 ELSE lease_until END
		WHERE scope=$1 AND topic_key=$2 AND kind=$3`, scope, topicKey, kind, enabled)
	return tag.RowsAffected() == 1, err
}

func claimAutopilotJob(ctx context.Context, q *pgxpool.Pool, at time.Time, duration time.Duration) (*autopilotJob, error) {
	return claimAutopilotJobKind(ctx, q, at, duration, "")
}

func claimAutopilotJobKind(ctx context.Context, q *pgxpool.Pool, at time.Time, duration time.Duration, kind string) (*autopilotJob, error) {
	if duration < time.Second {
		return nil, errors.New("lease duration must be at least one second")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	job := &autopilotJob{Token: hex.EncodeToString(b[:])}
	tx, err := q.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// A separate statement after acquiring the lock gets a fresh READ COMMITTED
	// snapshot; counting in the locking statement can race on an older snapshot.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('source-autopilot-global-claim',0))`); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `UPDATE source_autopilot_jobs SET lease_token=$1,lease_until=extract(epoch FROM clock_timestamp())::bigint+$2,
		generation=generation+1,attempts=attempts+1
		WHERE (scope,topic_key,kind)=(SELECT j.scope,j.topic_key,j.kind FROM source_autopilot_jobs j
		WHERE j.enabled AND j.demand_active AND ($4='' OR j.kind=$4) AND j.next_run_at <= $3 AND j.lease_until <= extract(epoch FROM clock_timestamp())::bigint
		AND (j.kind NOT IN ('auto_discovery','shadow_discovery') OR
			(SELECT count(*) FROM source_autopilot_jobs running WHERE running.kind IN ('auto_discovery','shadow_discovery') AND running.enabled AND running.demand_active AND running.lease_until > extract(epoch FROM clock_timestamp())::bigint) < 2)
		AND (j.kind NOT IN ('auto_discovery','shadow_discovery') OR NOT EXISTS
			(SELECT 1 FROM source_autopilot_jobs older WHERE older.kind IN ('auto_discovery','shadow_discovery') AND older.kind<>j.kind
			AND older.enabled AND older.demand_active AND older.next_run_at <= $3 AND older.lease_until <= extract(epoch FROM clock_timestamp())::bigint
			AND (older.next_run_at,older.requested_at) < (j.next_run_at,j.requested_at)))
		ORDER BY j.next_run_at, j.requested_at, j.priority DESC, j.scope, j.topic_key, j.kind
		FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING scope,topic_key,kind,topic,generation,checkpoint`, job.Token, int64(duration/time.Second), at.Unix(), kind).
		Scan(&job.Scope, &job.TopicKey, &job.Kind, &job.Topic, &job.Generation, &job.Checkpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

// withAutopilotLease locks and validates the job in the same transaction as
// the effect. Callers must not perform network operations inside fn.
func withAutopilotLease(ctx context.Context, database *db.DB, job *autopilotJob, fn func(pgx.Tx) error) error {
	return database.Tx(ctx, func(tx pgx.Tx) error {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT enabled AND demand_active AND lease_until > extract(epoch FROM clock_timestamp())::bigint
			FROM source_autopilot_jobs WHERE scope=$1 AND topic_key=$2 AND kind=$3
			AND lease_token=$4 AND generation=$5 FOR UPDATE`, job.Scope, job.TopicKey, job.Kind, job.Token, job.Generation).Scan(&valid)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !valid) {
			return errAutopilotLeaseLost
		}
		if err != nil {
			return err
		}
		return fn(tx)
	})
}

func renewAutopilotLease(ctx context.Context, q db.Querier, job *autopilotJob, duration time.Duration) error {
	if duration < time.Second {
		return errors.New("lease duration must be at least one second")
	}
	tag, err := q.Exec(ctx, `UPDATE source_autopilot_jobs SET lease_until=extract(epoch FROM clock_timestamp())::bigint+$6
		WHERE scope=$1 AND topic_key=$2 AND kind=$3 AND lease_token=$4 AND generation=$5
		AND enabled AND demand_active AND lease_until > extract(epoch FROM clock_timestamp())::bigint`,
		job.Scope, job.TopicKey, job.Kind, job.Token, job.Generation, int64(duration/time.Second))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errAutopilotLeaseLost
	}
	return nil
}

// runAutopilotLeased cancels work as soon as renewal fails. Any final writes
// still require fencing, since cancellation cannot recall an in-flight call.
func runAutopilotLeased(ctx context.Context, database *db.DB, job *autopilotJob, lease, heartbeat time.Duration, serverModelLimit, topicModelLimit, serverFetchLimit, topicFetchLimit int64, work func(context.Context) error) error {
	if database == nil || database.Pool == nil || job == nil || work == nil {
		return errors.New("invalid leased job")
	}
	if heartbeat <= 0 || lease < time.Second || heartbeat >= lease {
		return errors.New("invalid heartbeat interval")
	}
	if serverModelLimit <= 0 || topicModelLimit <= 0 {
		return errors.New("invalid model call limit")
	}
	if serverFetchLimit <= 0 || topicFetchLimit <= 0 {
		return errors.New("invalid fetch limit")
	}
	ctx = withAutopilotBudgetTracker(ctx)
	ctx = autopilotModelBudget(ctx, database, job, serverModelLimit, topicModelLimit)
	ctx = autopilotNetworkBudget(ctx, database, job, serverFetchLimit, topicFetchLimit)
	workCtx, cancel := context.WithCancel(contextWithAutopilotLease(ctx, job))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("autopilot worker panic: %v", recovered)
			}
		}()
		done <- work(workCtx)
	}()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			cancel()
			<-done
			return ctx.Err()
		case <-ticker.C:
			if err := renewAutopilotLease(workCtx, database.Pool, job, lease); err != nil {
				cancel()
				<-done
				return err
			}
		}
	}
}

func finishAutopilotJob(ctx context.Context, database *db.DB, job *autopilotJob, next time.Time, checkpoint json.RawMessage, lastError string) error {
	if !json.Valid(checkpoint) {
		return errors.New("invalid checkpoint")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs SET next_run_at=$4,requested_at=$4,
			lease_until=0,lease_token='',priority=0,checkpoint=$5,last_error=$6
			WHERE scope=$1 AND topic_key=$2 AND kind=$3`, job.Scope, job.TopicKey, job.Kind, next.Unix(), checkpoint, lastError)
		return err
	})
}

func saveAutopilotCheckpoint(ctx context.Context, database *db.DB, job *autopilotJob) error {
	if !json.Valid(job.Checkpoint) {
		return errors.New("invalid checkpoint")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE source_autopilot_jobs SET checkpoint=$4 WHERE scope=$1 AND topic_key=$2 AND kind=$3`, job.Scope, job.TopicKey, job.Kind, job.Checkpoint)
		return err
	})
}
