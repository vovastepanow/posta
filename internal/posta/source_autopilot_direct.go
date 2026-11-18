package posta

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

// Independent public post assessments are not a vote for their source.
// They are delivered only to opt-in auto subscribers, with every owner
// restriction, content filter and repeat guard rechecked transactionally.
const autopilotDirectMaxPerRun = 6
const autopilotDirectMaxAge = 48 * time.Hour

func autopilotDirectSeenKey(topicKey, source, postHash string) string {
	return "autopilot:direct_seen:" + strconv.Itoa(TopicProfileVersion) + ":" + topicKey + ":" + repeatDigest(source+":"+postHash)
}

func autopilotDirectPostEligible(u *U, source, topic string, post, analysis jx.M, at int64) bool {
	if u == nil || u.LLM == nil || post == nil || analysis == nil ||
		!autopilotPostMatchesSource(source, post) ||
		len(autopilotSafePostHints(source, []jx.M{post}, at)) != 1 ||
		jx.Bool(analysis, "tags_classification_pending") ||
		jx.Str(analysis, "model") == "" {
		return false
	}
	if _, ok := analysis["advertising"].(bool); !ok {
		return false
	}
	return u.contentAdmissionReason(analysis, post, topic, AnalysisText(post)) == ""
}

func (a *App) saveAutopilotDirectResult(ctx context.Context, job *autopilotJob, source string, post, analysis jx.M) error {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	u := a.User(ctx, "", nil)
	if !autopilotDirectPostEligible(u, source, job.Topic, post, analysis, time.Now().Unix()) {
		return nil
	}
	return withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		// No source promotion: this is a single accepted text item, not
		// evidence that other content from the author may be delivered.
		_, err := tx.Exec(ctx, `INSERT INTO source_autopilot_direct_results
			(scope,topic_key,source_id,post_hash,post,analysis,published_at,assessed_at,policy_version)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT(scope,topic_key,source_id,post_hash) DO UPDATE SET
			post=EXCLUDED.post,analysis=EXCLUDED.analysis,published_at=EXCLUDED.published_at,
			assessed_at=EXCLUDED.assessed_at,policy_version=EXCLUDED.policy_version
			WHERE source_autopilot_direct_results.policy_version<>EXCLUDED.policy_version
			   OR source_autopilot_direct_results.assessed_at < EXCLUDED.assessed_at - 172800`,
			job.Scope, job.TopicKey, source, repeatDigest(CandidatePostKey(post)), jx.Marshal(post),
			jx.Marshal(analysis), jx.Int(post, "posted_at"), time.Now().Unix(), TopicProfileVersion)
		return err
	})
}

type autopilotDirectCandidate struct {
	source string
	post   jx.M
}

// Assess one post per source before taking a second. A prolific author
// must not consume the entire model budget while other public sources
// remain untested. The same post cannot be selected twice.
func autopilotInterleaveDirectPosts(candidates []DiscoveryCandidate, pending func(string) []jx.M, limit int) []autopilotDirectCandidate {
	if limit <= 0 {
		return nil
	}
	sources := make([]string, 0, len(candidates))
	postsBySource := map[string][]jx.M{}
	for _, candidate := range candidates {
		source, ok := autopilotCandidateID(candidate)
		if !ok {
			continue
		}
		if _, exists := postsBySource[source]; exists {
			continue
		}
		postsBySource[source] = pending(source)
		sources = append(sources, source)
	}
	selected := make([]autopilotDirectCandidate, 0, limit)
	seen := map[string]bool{}
	for round := 0; len(selected) < limit; round++ {
		more := false
		for _, source := range sources {
			posts := postsBySource[source]
			if round >= len(posts) {
				continue
			}
			more = true
			post := posts[round]
			key := CandidatePostKey(post)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			selected = append(selected, autopilotDirectCandidate{source: source, post: post})
			if len(selected) == limit {
				break
			}
		}
		if !more {
			break
		}
	}
	return selected
}

// Run only with the public worker's leased context. Read independently found
// posts from the bounded, expiring staging queue. Never use personal accounts,
// source histories, or any cached private user content for model evidence.
func (a *App) autopilotAssessDirectPosts(ctx context.Context, job *autopilotJob, candidates []DiscoveryCandidate) error {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	u := a.User(ctx, "", nil)
	if u.LLM == nil {
		return nil
	}
	return a.autopilotAssessDirectPostsWith(ctx, job, candidates, func(texts []string) ([]Analysis, error) {
		return u.AnalyzeTextBatch(texts, job.Topic, EmptyHistory())
	})
}

// Injecting only the model boundary lets PostgreSQL tests execute the entire
// public staging -> assessment -> delivery path without external AI services.
func (a *App) autopilotAssessDirectPostsWith(ctx context.Context, job *autopilotJob, candidates []DiscoveryCandidate, analyze func([]string) ([]Analysis, error)) error {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind || len(candidates) == 0 || analyze == nil {
		return nil
	}
	u := a.User(ctx, "", nil)
	if u.LLM == nil {
		return nil
	}
	var posts []jx.M
	var texts, sources []string
	for _, candidate := range autopilotInterleaveDirectPosts(candidates, func(source string) []jx.M {
		return a.autopilotPendingPublicPosts(ctx, job, source)
	}, autopilotDirectMaxPerRun*autopilotPostHintLimit) {
		if len(posts) == autopilotDirectMaxPerRun {
			break
		}
		post, source := candidate.post, candidate.source
		postHash := repeatDigest(CandidatePostKey(post))
		var saved bool
		err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM source_autopilot_direct_results
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4
			AND policy_version=$5 AND assessed_at >= $6)`,
			job.Scope, job.TopicKey, source, postHash, TopicProfileVersion,
			time.Now().Add(-autopilotDirectMaxAge).Unix()).Scan(&saved)
		if err != nil {
			return err
		}
		if saved {
			continue
		}
		var assessed bool
		if a.KVGet(ctx, autopilotDirectSeenKey(job.TopicKey, source, postHash), &assessed) && assessed {
			continue
		}
		posts = append(posts, post)
		texts = append(texts, AnalysisText(post))
		sources = append(sources, source)
	}
	if len(posts) == 0 {
		return nil
	}
	analyses, err := analyze(texts)
	if err != nil {
		return err
	}
	if err := autopilotBudgetError(ctx); err != nil {
		return err
	}
	accepted, rejected := 0, 0
	for i, post := range posts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i >= len(analyses) || analyses[i] == nil || jx.Bool(analyses[i], "tags_classification_pending") {
			// Incomplete model results remain eligible for retry.
			continue
		}
		if _, ok := analyses[i]["advertising"].(bool); !ok || jx.Str(analyses[i], "model") == "" || analyses[i]["relevance"] == nil {
			// Do not cache a transient schema or model failure as a rejection.
			continue
		}
		eligible := autopilotDirectPostEligible(u, sources[i], job.Topic, post, analyses[i], time.Now().Unix())
		if eligible {
			if err := a.saveAutopilotDirectResult(ctx, job, sources[i], post, analyses[i]); err != nil {
				return err
			}
			accepted++
		} else {
			rejected++
		}
		// A fully classified rejection should not consume another model
		// request on every discovery cycle. Keys are topic/policy scoped.
		key := autopilotDirectSeenKey(job.TopicKey, sources[i], repeatDigest(CandidatePostKey(post)))
		if err := a.KVSet(contextWithAutopilotLease(ctx, job), key, true, autopilotDirectMaxAge); err != nil {
			return err
		}
	}
	if accepted+rejected > 0 {
		checkpoint := jx.Unmarshal(job.Checkpoint)
		if checkpoint == nil {
			checkpoint = jx.M{}
		}
		checkpoint["direct_reviewed_total"] = jx.Int(checkpoint, "direct_reviewed_total") + int64(accepted+rejected)
		checkpoint["direct_accepted_total"] = jx.Int(checkpoint, "direct_accepted_total") + int64(accepted)
		checkpoint["direct_rejected_total"] = jx.Int(checkpoint, "direct_rejected_total") + int64(rejected)
		job.Checkpoint = jx.Marshal(checkpoint)
		if err := saveAutopilotCheckpoint(ctx, a.DB, job); err != nil {
			return err
		}
	}
	return nil
}
