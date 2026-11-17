package posta

import (
	"context"
	"errors"
	"strings"

	"posta/internal/db"
	"posta/internal/jx"
)

// Personal outcomes include personal filters, so they must never be reused as
// public relevance evidence. A post without a stable identity is not counted.
func recordAutopilotAdmission(ctx context.Context, q db.Querier, userID, topic string, item jx.M, accepted bool, observedAt int64) error {
	// source_url can identify a feed rather than a post; treating it as a post
	// collapses every item in the feed into a single observation.
	if jx.Str(item, "url") == "" && (jx.Str(item, "board") == "" || jx.Str(item, "thread_id") == "" || jx.Str(item, "post_id") == "") {
		return nil
	}
	post := CandidatePostKey(item)
	source := ContentSourceKey(item)
	if userID == "" || strings.TrimSpace(topic) == "" || post == "" || source == "source:" {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO source_autopilot_admissions
		(user_id,topic_key,source_key,post_hash,policy_version,accepted,observed_at)
		SELECT $1,$2,$3,$4,$5,$6,COALESCE((SELECT min(observed_at) FROM source_autopilot_admissions
			WHERE user_id=$1 AND topic_key=$2 AND source_key=$3 AND post_hash=$4),$7)
		WHERE EXISTS (SELECT 1 FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$8 AND mode <> 'off')
		ON CONFLICT (user_id,topic_key,source_key,post_hash,policy_version)
		DO UPDATE SET accepted=EXCLUDED.accepted`,
		userID, autopilotTopicKey(topic), source, repeatDigest(post), TopicProfileVersion, accepted, observedAt, autopilotTopicKey(topic))
	return err
}

type autopilotAdmissionStats struct {
	Evaluated int
	Accepted  int
}

// Each post contributes at most once even when it has been reclassified under
// several policy versions. The most recent policy version wins; the window is
// based on its original observation time, not the time of a cache replay.
func autopilotAdmissionsWindow(ctx context.Context, q db.Querier, userID, topic, source string, since, until int64) (autopilotAdmissionStats, error) {
	var stats autopilotAdmissionStats
	if userID == "" || strings.TrimSpace(topic) == "" || source == "" || since >= until {
		return stats, errors.New("invalid admission window")
	}
	err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE accepted) FROM (
		SELECT DISTINCT ON (post_hash) accepted, first_observed FROM (
			SELECT post_hash, policy_version, accepted, min(observed_at) OVER (PARTITION BY post_hash) AS first_observed
			FROM source_autopilot_admissions WHERE user_id=$1 AND topic_key=$2 AND source_key=$3
		) versions ORDER BY post_hash, policy_version DESC
	) latest WHERE first_observed >= $4 AND first_observed < $5`, userID, autopilotTopicKey(topic), source, since, until).Scan(&stats.Evaluated, &stats.Accepted)
	return stats, err
}
