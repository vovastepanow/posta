package posta

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
)

var errAutopilotProjectionConflict = errors.New("source conflicts with a manual source or a case-sensitive alias")

func (a *App) autopilotProjectionPossible(job *autopilotJob, source string) error {
	platform, feed, kind, ok := autopilotSourceParts(source)
	if !ok {
		return errAutopilotProjectionConflict
	}
	d := a.Catalog()
	for _, vote := range d.Votes {
		if sourceIDMatchesLegacy(jx.Str(vote, "source_id"), source) && autopilotTopicKey(jx.Str(vote, "topic")) == job.TopicKey && jx.Str(vote, "vote") == "reject" {
			return errAutopilotSourceOverride
		}
	}
	for _, entry := range *d.List(kind) {
		existing := jx.Str(entry, "id")
		if !sameCatalogSource(platform, existing, feed) {
			continue
		}
		if id, ok := autopilotCandidateID(DiscoveryCandidate{Platform: platform, ID: existing}); !ok || id != source {
			return errAutopilotProjectionConflict
		}
		if (!jx.Bool(entry, "autopilot_managed") || jx.Bool(entry, "autopilot_manual")) && !a.CatalogEntryMatchesTopic(entry, job.Topic, false) {
			return errAutopilotProjectionConflict
		}
	}
	return nil
}

func autopilotProjectionTopics(profiles jx.M) []any {
	seen := make(map[string]bool)
	for _, value := range profiles {
		profile, ok := value.(map[string]any)
		if !ok {
			continue
		}
		topic := TopicBase(jx.Str(profile, "topic"))
		if topic != "" {
			seen[topic] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for topic := range seen {
		keys = append(keys, topic)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, topic := range keys {
		out = append(out, topic)
	}
	return out
}

func (a *App) autopilotProjectedCounts(ctx context.Context, topic string) (int, int, error) {
	visible := map[string]bool{}
	for _, kind := range CatalogKinds {
		for _, entry := range *a.Catalog().List(kind.Kind) {
			if a.CatalogEntryMatchesTopic(entry, topic, false) {
				visible[SourceIDFor(kind.Platform, jx.Str(entry, "id"))] = true
			}
		}
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id,state FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND state IN ('probation','active')`, autopilotTopicKey(topic))
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	active, probation := 0, 0
	for rows.Next() {
		var source, state string
		if err := rows.Scan(&source, &state); err != nil {
			return 0, 0, err
		}
		if visible[source] {
			if state == "active" {
				active++
			} else {
				probation++
			}
		}
	}
	return active, probation, rows.Err()
}

// projectAutopilotSource connects an already admitted public RSS source. The
// source record remains authoritative; rerunning repairs a missing projection.
func (a *App) projectAutopilotSource(ctx context.Context, job *autopilotJob, source, state string) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope || (state != "probation" && state != "active") {
		return errors.New("invalid source projection")
	}
	platform, feed, kind, ok := autopilotSourceParts(source)
	if !ok {
		return errors.New("invalid public source")
	}
	ctx = withAutopilotProjection(contextWithAutopilotLease(ctx, job), source, state, false)
	var conflict error
	_, err := a.UpdateCatalog(ctx, func(d *SourceDB) bool {
		for _, vote := range d.Votes {
			if sourceIDMatchesLegacy(jx.Str(vote, "source_id"), source) && autopilotTopicKey(jx.Str(vote, "topic")) == job.TopicKey && jx.Str(vote, "vote") == "reject" {
				conflict = errAutopilotSourceOverride
				return false
			}
		}
		list := d.List(kind)
		for _, entry := range *list {
			existing := jx.Str(entry, "id")
			if !sameCatalogSource(platform, existing, feed) {
				continue
			}
			if existingID, ok := autopilotCandidateID(DiscoveryCandidate{Platform: platform, ID: existing}); !ok || existingID != source {
				conflict = errAutopilotProjectionConflict
				return false
			}
			if !jx.Bool(entry, "autopilot_managed") || jx.Bool(entry, "autopilot_manual") {
				if !a.CatalogEntryMatchesTopic(entry, job.Topic, false) {
					conflict = errAutopilotProjectionConflict
				}
				return false
			}
			profiles := jx.Map(entry, "autopilot_profiles")
			if profiles == nil {
				profiles = jx.M{}
			}
			current := jx.Map(profiles, job.TopicKey)
			if jx.Str(current, "state") == state && jx.Str(current, "topic") == job.Topic {
				return false
			}
			profiles[job.TopicKey] = jx.M{"topic": job.Topic, "state": state}
			entry["autopilot_profiles"] = profiles
			entry["topics"] = autopilotProjectionTopics(profiles)
			return true
		}
		name := feed
		if platform == PlatformRSS {
			name = hostOf(feed)
		}
		*list = append(*list, jx.M{"id": feed, "name": name, "topics": []any{TopicBase(job.Topic)}, "auto": true,
			"autopilot_managed": true, "autopilot_profiles": jx.M{job.TopicKey: jx.M{"topic": job.Topic, "state": state}}})
		return true
	})
	if err != nil {
		return err
	}
	return conflict
}

// unprojectAutopilotSource removes only the rejected profile and never removes
// a manually adopted source or another profile's projection.
func (a *App) unprojectAutopilotSource(ctx context.Context, job *autopilotJob, source string) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope {
		return errors.New("invalid source projection")
	}
	_, feed, kind, ok := autopilotSourceParts(source)
	if !ok {
		return errors.New("invalid public source")
	}
	ctx = withAutopilotProjection(contextWithAutopilotLease(ctx, job), source, "rejected", true)
	_, err := a.UpdateCatalog(ctx, func(d *SourceDB) bool {
		list := d.List(kind)
		for i, entry := range *list {
			if jx.Str(entry, "id") != feed || !jx.Bool(entry, "autopilot_managed") || jx.Bool(entry, "autopilot_manual") {
				continue
			}
			for _, vote := range d.Votes {
				if sourceIDMatchesLegacy(jx.Str(vote, "source_id"), source) && (jx.Str(vote, "vote") == "approve" || jx.Str(vote, "vote") == "maybe") {
					entry["autopilot_manual"] = true
					return true
				}
			}
			profiles := jx.Map(entry, "autopilot_profiles")
			if _, exists := profiles[job.TopicKey]; !exists {
				return false
			}
			delete(profiles, job.TopicKey)
			if len(profiles) == 0 {
				*list = append((*list)[:i], (*list)[i+1:]...)
			} else {
				entry["autopilot_profiles"] = profiles
				entry["topics"] = autopilotProjectionTopics(profiles)
			}
			return true
		}
		return false
	})
	return err
}

// reconcileAutopilotProjections also repairs a process crash between recording
// a source decision and updating the catalog.
// Old OK off-topic rejections used to count an unreadable mobile-page
// title as definitive evidence. Reopen these historic decisions exactly once
// for the corrected multi-post review. Existing user exclusions still win
// during due-source selection; this grants no source an approval.
func (a *App) autopilotRescheduleOldOKRejections(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope ||
		autopilotCanonicalTopic(job.Topic) != "SMM и блогинг" {
		return nil
	}
	_, err := a.DB.Pool.Exec(ctx, `UPDATE source_autopilot_sources
		SET next_check_at=LEAST(next_check_at,$3),
			evidence=evidence || '{"ok_topic_reaudit":true}'::jsonb
		WHERE scope=$1 AND topic_key=$2 AND state='rejected'
		AND reason='off_topic_group' AND source_id LIKE 'ok:%'
		AND NOT (evidence ? 'ok_topic_reaudit')`,
		job.Scope, job.TopicKey, time.Now().Unix())
	return err
}

// Historical suggestions rejected for their ID/format were never given a
// content verdict. Requeue only URLs that the current public-source parser
// can safely validate; never reconstruct a case-sensitive YouTube channel ID.
func (a *App) autopilotReconsiderTechnicalRejects(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope {
		return nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND state='rejected'
		AND reason IN ('invalid_source_identity','unsupported_source_format')
		AND NOT (evidence ? 'technical_reaudit_v1')
		ORDER BY source_id LIMIT 100`, job.Scope, job.TopicKey)
	if err != nil {
		return err
	}
	var sources []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, old := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		canonical := ""
		if len(old) > 4 && old[:4] == "rss:" {
			if normalized, ok := autopilotPublicRSSID(old[4:]); ok &&
				SourceIDFor(PlatformRSS, normalized[4:]) == normalized {
				canonical = normalized
			}
		} else if _, _, _, ok := autopilotSourceParts(old); ok {
			canonical = old
		}
		at := time.Now()
		if canonical == old {
			// This is only a request to retry, not an approval or a bypass
			// of manual exclusions or the normal model gates.
			err := transitionAutopilotSource(ctx, a.DB, job, old, "rejected", "candidate",
				"technical_reaudit", []byte(`{"technical_reaudit_v1":true}`), at, at)
			if err != nil && !errors.Is(err, errAutopilotSourceChanged) &&
				!errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
		} else if canonical != "" {
			if err := registerAutopilotCandidate(ctx, a.DB, job, canonical, at); err != nil {
				return err
			}
		}
		// Keep the original, possibly irrecoverable entry auditable. In
		// particular, lowercase legacy YouTube IDs cannot safely be guessed.
		if _, err := a.DB.Pool.Exec(ctx, `UPDATE source_autopilot_sources
			SET evidence=evidence || '{"technical_reaudit_v1":true}'::jsonb
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND state='rejected'`,
			job.Scope, job.TopicKey, old); err != nil {
			return err
		}
	}
	return nil
}

// A previous 160-rune source prescreen could reject a useful publisher before
// the actual point of a long post. Revisit those decisions at most 20/day,
// preserving the old rejection until the normal review runs. Every source is
// scheduled only once, even if the new assessment rejects it again.
func (a *App) autopilotScheduleFullContextRechecks(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope ||
		autopilotCanonicalTopic(job.Topic) != "SMM и блогинг" {
		return nil
	}
	at := time.Now().UTC()
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH quota AS (
			SELECT GREATEST(0,20-count(*)::int) AS available
			FROM source_autopilot_decisions
			WHERE scope=$1 AND topic_key=$2 AND reason='full_context_reaudit_scheduled' AND at >= $4
		), candidates AS (
			SELECT s.source_id FROM source_autopilot_sources s
			WHERE s.scope=$1 AND s.topic_key=$2 AND s.state='rejected'
			AND s.reason='preliminary_topic_mismatch'
			AND s.next_check_at>$3
			AND NOT EXISTS (
				SELECT 1 FROM source_autopilot_decisions d WHERE d.scope=$1
				AND d.topic_key=$2 AND d.source_id=s.source_id
				AND d.reason='full_context_reaudit_scheduled')
			AND NOT EXISTS (
				SELECT 1 FROM source_autopilot_overrides o WHERE o.scope=$1
				AND o.owner_id='' AND o.topic_key=$2 AND o.source_id=s.source_id
				AND o.decision='excluded')
			ORDER BY s.checked_at,s.source_id
			LIMIT (SELECT available FROM quota)
			FOR UPDATE OF s SKIP LOCKED
		), scheduled AS (
			UPDATE source_autopilot_sources s
			SET next_check_at=$3
			FROM candidates c
			WHERE s.scope=$1 AND s.topic_key=$2 AND s.source_id=c.source_id
			RETURNING s.source_id
		)
		INSERT INTO source_autopilot_decisions
			(scope,topic_key,source_id,old_state,new_state,reason,evidence,policy_version,at)
		SELECT $1,$2,source_id,'rejected','rejected','full_context_reaudit_scheduled',
			'{"explanation":"recheck_with_fuller_captions"}'::jsonb,$5,$3 FROM scheduled`,
			job.Scope, job.TopicKey, at.Unix(), at.Truncate(24*time.Hour).Unix(), TopicProfileVersion)
		return err
	})
}

func (a *App) reconcileAutopilotProjections(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope {
		return errors.New("invalid projection job")
	}
	if err := a.autopilotRescheduleOldOKRejections(ctx, job); err != nil {
		return err
	}
	if err := a.autopilotReconsiderTechnicalRejects(ctx, job); err != nil {
		return err
	}
	if err := a.autopilotScheduleFullContextRechecks(ctx, job); err != nil {
		return err
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id,state FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND state IN ('probation','active','rejected') ORDER BY source_id`, job.Scope, job.TopicKey)
	if err != nil {
		return err
	}
	type projection struct{ source, state string }
	var projections []projection
	for rows.Next() {
		var p projection
		if err := rows.Scan(&p.source, &p.state); err != nil {
			rows.Close()
			return err
		}
		projections = append(projections, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range projections {
		// Legacy manual suggestions with unsupported identities are now
		// retained as rejected, auditable rows. They cannot have a valid
		// catalog projection. Attempting to unproject them previously
		// returned "invalid public source" on EVERY job run, stopping the
		// entire auto worker at prepare before any candidate reviews.
		if _, _, _, valid := autopilotSourceParts(p.source); !valid {
			if p.state == "rejected" {
				continue
			}
			// Old malformed trials also must not prevent healthy sources from
			// reaching review. Quarantine rather than activating or deleting.
			at := time.Now()
			err = transitionAutopilotSource(ctx, a.DB, job, p.source, p.state,
				"cooldown", "unsupported_source_format", []byte(`{}`), at,
				time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil && !errors.Is(err, errAutopilotSourceChanged) &&
				!errors.Is(err, errAutopilotSourceOverride) {
				return err
			}
			continue
		}
		if p.state == "rejected" {
			err = a.unprojectAutopilotSource(ctx, job, p.source)
		} else {
			err = a.projectAutopilotSource(ctx, job, p.source, p.state)
		}
		if errors.Is(err, errAutopilotProjectionConflict) && p.state != "rejected" {
			at := time.Now()
			transitionErr := transitionAutopilotSource(ctx, a.DB, job, p.source, p.state, "cooldown", "projection_conflict", []byte(`{}`), at, at.Add(7*24*time.Hour))
			if transitionErr != nil && !errors.Is(transitionErr, errAutopilotSourceChanged) && !errors.Is(transitionErr, errAutopilotSourceOverride) {
				return transitionErr
			}
			continue
		}
		if err != nil && !errors.Is(err, errAutopilotSourceChanged) && !errors.Is(err, errAutopilotSourceOverride) && !errors.Is(err, errAutopilotProjectionConflict) {
			return err
		}
	}
	return nil
}
