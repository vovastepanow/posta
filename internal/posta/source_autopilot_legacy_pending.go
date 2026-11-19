package posta

import (
	"context"
	"time"

	"posta/internal/jx"
)

// The legacy source suggestion shelf and the source autopilot used to be
// disconnected. Select only genuinely undecided, public source suggestions
// for the exact canonical topic of a running job. User votes remain final;
// migrating a suggestion never counts as a manual approval.
func (u *U) autopilotLegacyPendingCandidates(job *autopilotJob) []string {
	if job == nil || !validAutopilotPublicJob(job) {
		return nil
	}
	d := u.Catalog()
	decided := u.decidedSourceKeys(d.Votes, job.Topic)
	approved := map[string]bool{}
	for _, kind := range CatalogKinds {
		list := d.List(kind.Kind)
		if list == nil {
			continue
		}
		for _, entry := range *list {
			if u.CatalogEntryMatchesTopic(entry, job.Topic, true) {
				approved[recommendationSourceKey(SourceIDFor(kind.Platform, jx.Str(entry, "id")))] = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, item := range d.Suggestions {
		topic := jx.Str(item, "topic")
		if topic == "" || autopilotTopicKey(topic) != job.TopicKey ||
			!u.TopicsAreEquivalent(topic, job.Topic) {
			continue
		}
		sid, ok := autopilotCandidateID(DiscoveryCandidate{
			Platform: jx.Str(item, "platform"), ID: jx.Str(item, "id"),
		})
		if !ok || jx.Str(item, "source_id") == "" ||
			recommendationSourceKey(sid) != recommendationSourceKey(jx.Str(item, "source_id")) {
			continue
		}
		identity := recommendationSourceKey(sid)
		if seen[identity] || recommendationExcluded(decided, identity) || approved[identity] ||
			u.UASourceBlocked(sid, jx.M{"title": jx.Str(item, "name")}) != "" {
			continue
		}
		seen[identity] = true
		out = append(out, sid)
	}
	return out
}

// Transfer up to limit new suggestions per run. Previously imported sources
// are never reset, and unavailable/invalid/unsupported sources remain on the
// old shelf rather than disappearing without an auditable decision.
func (a *App) autopilotImportLegacyPending(ctx context.Context, job *autopilotJob, limit int) (int, error) {
	if limit < 1 || !validAutopilotPublicJob(job) {
		return 0, nil
	}
	u := a.User(ctx, "", nil)
	sources := u.autopilotLegacyPendingCandidates(job)
	if len(sources) == 0 {
		return 0, nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=ANY($3)`, job.Scope, job.TopicKey, sources)
	if err != nil {
		return 0, err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		existing[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	added := 0
	for _, source := range sources {
		if existing[source] {
			continue
		}
		if err := registerAutopilotCandidateWithOrigin(ctx, a.DB, job, source, "legacy_pending_import", time.Now()); err != nil {
			return added, err
		}
		added++
		if added >= limit {
			break
		}
	}
	return added, nil
}
