package posta

import (
	"context"
	"strings"
	"time"

	"posta/internal/jx"
)

// Accepted public posts can nominate their authors without bypassing the
// autopilot's leased source verification, overrides or probation limits.
const autopilotSourceHintMax = 100
const autopilotSourceHintTTL = 14 * 24 * time.Hour

func autopilotSourceHintKey(topic string) string {
	return "autopilot:source_hints:" + autopilotTopicKey(topic)
}

func appendAutopilotSourceHint(hints []string, source string) []string {
	if source == "" {
		return hints
	}
	for _, existing := range hints {
		if existing == source {
			return hints
		}
	}
	hints = append(hints, source)
	if len(hints) > autopilotSourceHintMax {
		hints = hints[len(hints)-autopilotSourceHintMax:]
	}
	return hints
}

func (u *U) suggestAutopilotSource(topic string, candidate DiscoveryCandidate) {
	if u == nil || u.App == nil || u.DB == nil || !u.TBool("autopilot_auto") || u.TopicIsRestricted(topic) {
		return
	}
	source, ok := autopilotCandidateID(candidate)
	if !ok {
		return
	}
	var active bool
	err := u.DB.Pool.QueryRow(u.Ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_demands
		WHERE topic_key=$1 AND mode='auto' AND NOT paused AND posts_per_day>0)`, autopilotTopicKey(topic)).Scan(&active)
	if err != nil || !active {
		return
	}
	_, err = KVUpdate(u.App, u.Ctx, autopilotSourceHintKey(topic), autopilotSourceHintTTL, func(current []string, _ bool) *[]string {
		updated := appendAutopilotSourceHint(current, source)
		return &updated
	})
	if err != nil {
		u.Log("Автопилот: источник %s не удалось поставить в очередь: %v", source, err)
	}
}

// Register hints only inside the public auto worker's fenced lease. Do not
// consume hints on errors: a retry can safely register them idempotently.
func (a *App) autopilotConsumeSourceHints(ctx context.Context, job *autopilotJob, limit int) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind || limit <= 0 {
		return nil
	}
	key := autopilotSourceHintKey(job.Topic)
	var hints []string
	if !a.KVGet(ctx, key, &hints) || len(hints) == 0 {
		return nil
	}
	done := make(map[string]bool)
	for _, source := range hints[:min(limit, len(hints))] {
		if err := ctx.Err(); err != nil {
			return err
		}
		platform, id, _, ok := autopilotSourceParts(source)
		if ok {
			candidate := DiscoveryCandidate{Platform: platform, ID: strings.TrimPrefix(id, "@")}
			normalized, valid := autopilotCandidateID(candidate)
			if valid && normalized == source {
				if err := registerAutopilotCandidate(ctx, a.DB, job, source, time.Now()); err != nil {
					return err
				}
			}
		}
		done[source] = true
	}
	_, err := KVUpdate(a, ctx, key, autopilotSourceHintTTL, func(current []string, _ bool) *[]string {
		remaining := make([]string, 0, len(current))
		for _, source := range current {
			if !done[source] {
				remaining = append(remaining, source)
			}
		}
		return &remaining
	})
	return err
}

// Accepted results are only hints. The public worker still fetches and
// assesses fresh content before any source becomes probationary or active.
var _ = jx.M{}
