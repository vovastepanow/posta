package posta

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/db"
)

var errAutopilotSourceChanged = errors.New("autopilot source state changed")
var errAutopilotSourceOverride = errors.New("autopilot source has a manual override")
var errAutopilotProbationLimit = errors.New("autopilot probation limit reached")

func validAutopilotTransition(from, to, resume string) bool {
	if to == "cooldown" {
		return from == "candidate" || from == "probation" || from == "active" || from == "rejected"
	}
	if from == "cooldown" {
		return resume == to
	}
	switch from {
	case "candidate":
		return to == "probation" || to == "rejected"
	case "probation":
		return to == "active" || to == "rejected"
	case "active":
		return to == "probation"
	case "rejected":
		return to == "candidate"
	}
	return false
}

func lockAutopilotSource(ctx context.Context, tx pgx.Tx, scope, topic, source string) error {
	if scope == autopilotShadowScope {
		scope = "public"
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope+"\x1f"+topic+"\x1f"+source)
	return err
}

func ensureAutopilotShadowDemand(ctx context.Context, tx pgx.Tx, job *autopilotJob) error {
	mode := ""
	if job.Kind == autopilotShadowKind {
		mode = "shadow"
	}
	if job.Kind == autopilotAutoKind {
		mode = "auto"
	}
	if mode == "" {
		return nil
	}
	var owner string
	err := tx.QueryRow(ctx, `SELECT user_id FROM source_autopilot_demands WHERE topic_key=$1 AND mode=$2 AND NOT paused AND ($2<>'auto' OR posts_per_day>0) ORDER BY user_id LIMIT 1 FOR SHARE`, job.TopicKey, mode).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return errAutopilotLeaseLost
	}
	return err
}

// registerAutopilotCandidate is idempotent and cannot resurrect a rejected or
// excluded source. Only the current owner of the exact topic job may write it.
func registerAutopilotCandidate(ctx context.Context, database *db.DB, job *autopilotJob, sourceID string, at time.Time) error {
	return registerAutopilotCandidateWithOrigin(ctx, database, job, sourceID, "discovered", at)
}

// A legacy pending suggestion is never an approval. Record its origin as an
// auditable, idempotent candidate; the normal review worker makes the verdict.
func registerAutopilotCandidateWithOrigin(ctx context.Context, database *db.DB, job *autopilotJob, sourceID, origin string, at time.Time) error {
	return registerAutopilotCandidateWithEvidence(ctx, database, job, sourceID, origin, json.RawMessage(`{}`), at)
}

// Record bounded discovery metadata on the first insertion only. Rediscovery
// never changes the verdict, backoff, or evidence of an existing source.
func registerAutopilotCandidateWithEvidence(ctx context.Context, database *db.DB, job *autopilotJob, sourceID, origin string, evidence json.RawMessage, at time.Time) error {
	if job == nil || sourceID == "" || (origin != "discovered" && origin != "legacy_pending_import") || len(evidence) > 16384 || !json.Valid(evidence) {
		return errors.New("invalid source candidate")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, sourceID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH inserted AS (INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,evidence)
			SELECT $1,$2,$3,'candidate',$6,$4,$5,$7::jsonb WHERE NOT EXISTS (SELECT 1 FROM source_autopilot_overrides
			WHERE (scope=$1 OR ($1='public:shadow' AND scope='public')) AND owner_id='' AND topic_key=$2 AND source_id=$3 AND decision='excluded')
			ON CONFLICT DO NOTHING RETURNING scope,topic_key,source_id)
			INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at,evidence)
			SELECT scope,topic_key,source_id,'','candidate',$6,$4,$5,$7::jsonb FROM inserted`, job.Scope, job.TopicKey, sourceID, TopicProfileVersion, at.Unix(), origin, evidence)
		return err
	})
}

// setAutopilotSourceOverride serializes manual decisions with automatic
// decisions for this source. Empty decision clears the selected override.
func setAutopilotSourceOverride(ctx context.Context, database *db.DB, scope, owner, topic, source, decision string, at time.Time) error {
	if scope == "" || topic == "" || source == "" || (decision != "" && decision != "pinned" && decision != "excluded") {
		return errors.New("invalid source override")
	}
	return database.Tx(ctx, func(tx pgx.Tx) error {
		if err := lockAutopilotSource(ctx, tx, scope, topic, source); err != nil {
			return err
		}
		if decision == "" {
			_, err := tx.Exec(ctx, `DELETE FROM source_autopilot_overrides WHERE scope=$1 AND owner_id=$2 AND topic_key=$3 AND source_id=$4`, scope, owner, topic, source)
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_overrides(scope,owner_id,topic_key,source_id,decision,updated_at)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(scope,owner_id,topic_key,source_id) DO UPDATE SET decision=EXCLUDED.decision,updated_at=EXCLUDED.updated_at`, scope, owner, topic, source, decision, at.Unix())
		return err
	})
}

// transitionAutopilotSource records the reason and evidence atomically with
// the state. Callers must derive evidence from persisted observations.
func transitionAutopilotSource(ctx context.Context, database *db.DB, job *autopilotJob, source, expected, next, reason string, evidence json.RawMessage, at, nextCheck time.Time) error {
	if job == nil || source == "" || reason == "" || len(evidence) > 16384 || !json.Valid(evidence) {
		return errors.New("invalid source transition")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, source); err != nil {
			return err
		}
		var current, resume string
		if err := tx.QueryRow(ctx, `SELECT state,resume_state FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3 FOR UPDATE`, job.Scope, job.TopicKey, source).Scan(&current, &resume); err != nil {
			return err
		}
		if current != expected || !validAutopilotTransition(current, next, resume) {
			return errAutopilotSourceChanged
		}
		if validAutopilotPublicJob(job) && (current == "candidate" || current == "cooldown") && next == "probation" {
			if job.MaxProbation < 1 {
				return errors.New("invalid probation limit")
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND state='probation'`, job.Scope, job.TopicKey).Scan(&count); err != nil {
				return err
			}
			if count >= job.MaxProbation {
				return errAutopilotProbationLimit
			}
		}
		var override string
		err := tx.QueryRow(ctx, `SELECT decision FROM source_autopilot_overrides WHERE (scope=$1 OR ($1='public:shadow' AND scope='public')) AND owner_id='' AND topic_key=$2 AND source_id=$3 ORDER BY (decision='excluded') DESC LIMIT 1`, job.Scope, job.TopicKey, source).Scan(&override)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if override == "excluded" && (next == "candidate" || next == "probation" || next == "active") {
			return errAutopilotSourceOverride
		}
		if override == "pinned" && current == "active" && next != "cooldown" {
			return errAutopilotSourceOverride
		}
		newResume := ""
		if next == "cooldown" {
			newResume = current
			if reason == "youtube_stream_unavailable" && current == "probation" {
				newResume = "candidate"
			}
		}
		nextAt := int64(0)
		if !nextCheck.IsZero() {
			nextAt = nextCheck.Unix()
		}
		_, err = tx.Exec(ctx, `UPDATE source_autopilot_sources SET state=$4,resume_state=$5,reason=$6,evidence=source_autopilot_sources.evidence || $7::jsonb,policy_version=$8,checked_at=$9,next_check_at=$10
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, source, next, newResume, reason, evidence, TopicProfileVersion, at.Unix(), nextAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, job.Scope, job.TopicKey, source, current, next, reason, evidence, TopicProfileVersion, at.Unix())
		return err
	})
}

// recordAutopilotSourceCheck saves uncertainty without changing the state.
func recordAutopilotSourceCheck(ctx context.Context, database *db.DB, job *autopilotJob, source, expected, reason string, evidence json.RawMessage, at, nextCheck time.Time) error {
	if job == nil || source == "" || (expected != "candidate" && expected != "probation" && expected != "active" && expected != "cooldown") || reason == "" || len(evidence) > 16384 || !json.Valid(evidence) {
		return errors.New("invalid candidate check")
	}
	return withAutopilotLease(ctx, database, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, source); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE source_autopilot_sources SET reason=$4,evidence=source_autopilot_sources.evidence || $5::jsonb,policy_version=$6,checked_at=$7,next_check_at=$8
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND state=$9`, job.Scope, job.TopicKey, source, reason, evidence, TopicProfileVersion, at.Unix(), nextCheck.Unix(), expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errAutopilotSourceChanged
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
			VALUES($1,$2,$3,$8,$8,$4,$5,$6,$7)`, job.Scope, job.TopicKey, source, reason, evidence, TopicProfileVersion, at.Unix(), expected)
		return err
	})
}
