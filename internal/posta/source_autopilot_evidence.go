package posta

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/db"
)

type autopilotPostEvidence struct {
	PostHash, ContentHash           string
	PublishedAt, ObservedAt         int64
	Relevant, Eligible, Advertising bool
}

// Only completed assessments are written; transport and model failures have
// no negative verdict. Replaying a post cannot add another event.
func recordAutopilotPostEvidence(ctx context.Context, database *db.DB, job *autopilotJob, source string, e autopilotPostEvidence) error {
	if job == nil || source == "" || e.PostHash == "" || e.PublishedAt <= 0 || e.ObservedAt <= 0 || e.PublishedAt > e.ObservedAt+300 || (e.Eligible && (!e.Relevant || e.Advertising)) {
		return errors.New("invalid post evidence")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising)
			VALUES($1,$2,$3,$4,$5,$6,
			COALESCE((SELECT min(published_at) FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4),$7),
			CASE WHEN `+autopilotFullRecheckEpochSQL+`>0 THEN $8 ELSE
			COALESCE((SELECT min(observed_at) FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4),$8) END,$9,$10,$11)
			ON CONFLICT(scope,topic_key,source_id,post_hash,policy_version) DO UPDATE SET
			content_hash=EXCLUDED.content_hash,observed_at=EXCLUDED.observed_at,relevant=EXCLUDED.relevant,
			eligible=EXCLUDED.eligible,advertising=EXCLUDED.advertising
			WHERE source_autopilot_post_evidence.observed_at < `+autopilotFullRecheckEpochSQL+` AND EXCLUDED.observed_at >= `+autopilotFullRecheckEpochSQL+``,
			job.Scope, job.TopicKey, source, e.PostHash, e.ContentHash, TopicProfileVersion, e.PublishedAt, e.ObservedAt, e.Relevant, e.Eligible, e.Advertising)
		return err
	})
}

type autopilotEvidenceStats struct {
	Evaluated, Relevant, Eligible, Advertising, Duplicates, FreshUnique, CuratedUnique, SuccessfulPasses int
	SameSourceDuplicates int
}

// A matching content hash from multiple sources marks every copy as duplicate;
// fixed source traversal order never awards permanent originality to one.
func autopilotEvidenceWindow(ctx context.Context, q db.Querier, scope, topicKey, source string, since, until time.Time) (autopilotEvidenceStats, error) {
	var s autopilotEvidenceStats
	if scope == "" || topicKey == "" || source == "" || !since.Before(until) {
		return s, errors.New("invalid evidence window")
	}
	err := q.QueryRow(ctx, `WITH latest AS (
		SELECT DISTINCT ON (source_id,post_hash) source_id,post_hash,content_hash,published_at,relevant,eligible,advertising
		FROM source_autopilot_post_evidence e WHERE scope=$1 AND topic_key=$2
		AND e.observed_at >= COALESCE((SELECT (s.evidence->>'full_recheck_at')::bigint
			FROM source_autopilot_sources s WHERE s.scope=e.scope AND s.topic_key=e.topic_key AND s.source_id=e.source_id),0)
		ORDER BY source_id,post_hash,policy_version DESC
	), windowed AS (
		SELECT *, row_number() OVER (PARTITION BY source_id,content_hash ORDER BY published_at,post_hash) AS content_copy
		FROM latest WHERE published_at >= $4 AND published_at < $5
	),
	shared AS (SELECT content_hash FROM windowed WHERE content_hash<>'' GROUP BY content_hash HAVING count(DISTINCT source_id)>1)
	SELECT count(*),count(*) FILTER(WHERE relevant),count(*) FILTER(WHERE eligible),count(*) FILTER(WHERE advertising),
	count(*) FILTER(WHERE content_hash<>'' AND (content_copy>1 OR content_hash IN (SELECT content_hash FROM shared))),
	count(*) FILTER(WHERE content_hash<>'' AND content_copy>1),
	count(DISTINCT content_hash) FILTER(WHERE eligible AND content_hash<>'' AND content_hash NOT IN (SELECT content_hash FROM shared)),
	-- The same useful material can be selected by multiple publishers.
	-- Preserve its value as curation, not falsely as original authorship.
	count(DISTINCT content_hash) FILTER(WHERE eligible AND relevant AND NOT advertising AND content_hash<>'' AND content_hash IN (SELECT content_hash FROM shared)),
	(SELECT count(*) FROM source_autopilot_collection_passes WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND fetched_at >= GREATEST($4,`+autopilotFullRecheckEpochSQL+`) AND fetched_at < $5)
	FROM windowed WHERE source_id=$3`, scope, topicKey, source, since.Unix(), until.Unix()).
		Scan(&s.Evaluated, &s.Relevant, &s.Eligible, &s.Advertising, &s.Duplicates, &s.SameSourceDuplicates, &s.FreshUnique, &s.CuratedUnique, &s.SuccessfulPasses)
	return s, err
}
