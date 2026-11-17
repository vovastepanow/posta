package posta

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
)

// A cache hit is not a new pass. This records public fetch coverage only when
// at least one demand for this exact profile is enabled.
func recordAutopilotPublicFetch(ctx context.Context, q db.Querier, topic, source string, at int64, count int) error {
	if topic == "" || source == "" || at <= 0 || count < 0 {
		return errors.New("invalid collection observation")
	}
	key := autopilotTopicKey(topic)
	_, err := q.Exec(ctx, `INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count)
		SELECT 'public',$1,$2,$3,$4,$5 WHERE EXISTS (SELECT 1 FROM source_autopilot_demands WHERE topic_key=$1 AND mode IN ('shadow','auto'))
		ON CONFLICT(scope,topic_key,source_id,slot) DO NOTHING`, key, source, at/int64(sharedCollectionInterval.Seconds()), at, count)
	return err
}

func recordAutopilotShadowFetch(ctx context.Context, database *db.DB, job *autopilotJob, source string, at int64, count int) error {
	if !validAutopilotPublicJob(job) || source == "" || at <= 0 || count < 0 {
		return errors.New("invalid shadow collection observation")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(scope,topic_key,source_id,slot) DO NOTHING`, job.Scope, job.TopicKey, source, at/int64(sharedCollectionInterval.Seconds()), at, count)
		return err
	})
}

type autopilotFetchStats struct {
	SuccessfulPasses int
	FetchedItems     int
	LastSuccess      int64
}

// The total is fetched items, not unique or useful posts. Classification and
// deduplication must provide separate evidence for promotion.
func autopilotPublicFetchWindow(ctx context.Context, q db.Querier, topic, source string, since, until int64) (autopilotFetchStats, error) {
	var stats autopilotFetchStats
	if topic == "" || source == "" || since >= until {
		return stats, errors.New("invalid collection window")
	}
	err := q.QueryRow(ctx, `SELECT count(*),COALESCE(sum(fetched_count),0),COALESCE(max(fetched_at),0)
		FROM source_autopilot_collection_passes WHERE scope='public' AND topic_key=$1 AND source_id=$2 AND fetched_at >= $3 AND fetched_at < $4`,
		autopilotTopicKey(topic), source, since, until).Scan(&stats.SuccessfulPasses, &stats.FetchedItems, &stats.LastSuccess)
	return stats, err
}
