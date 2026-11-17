package posta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

// Independent public post assessments are not a vote for their source.
// They are delivered only to opt-in auto subscribers, with every owner
// restriction, content filter and repeat guard rechecked transactionally.
const autopilotDirectMaxPerRun = 6
const autopilotDirectMaxAge = 48 * time.Hour

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
			ON CONFLICT(scope,topic_key,source_id,post_hash) DO NOTHING`,
			job.Scope, job.TopicKey, source, repeatDigest(CandidatePostKey(post)), jx.Marshal(post),
			jx.Marshal(analysis), jx.Int(post, "posted_at"), time.Now().Unix(), TopicProfileVersion)
		return err
	})
}

// Run only with the public worker's leased context. Read independently found
// posts from the bounded, expiring staging queue. Never use personal accounts,
// source histories, or any cached private user content for model evidence.
func (a *App) autopilotAssessDirectPosts(ctx context.Context, job *autopilotJob, candidates []DiscoveryCandidate) error {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind || len(candidates) == 0 {
		return nil
	}
	u := a.User(ctx, "", nil)
	if u.LLM == nil {
		return nil
	}
	var posts []jx.M
	var texts, sources []string
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if len(posts) == autopilotDirectMaxPerRun {
			break
		}
		source, ok := autopilotCandidateID(candidate)
		if !ok {
			continue
		}
		for _, post := range a.autopilotPendingPublicPosts(ctx, job, source) {
			postHash := repeatDigest(CandidatePostKey(post))
			if seen[postHash] {
				continue
			}
			seen[postHash] = true
			var saved bool
			err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM source_autopilot_direct_results
				WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5)`,
				job.Scope, job.TopicKey, source, postHash, TopicProfileVersion).Scan(&saved)
			if err != nil {
				return err
			}
			if saved {
				continue
			}
			posts = append(posts, post)
			texts = append(texts, AnalysisText(post))
			sources = append(sources, source)
			if len(posts) == autopilotDirectMaxPerRun {
				break
			}
		}
	}
	if len(posts) == 0 {
		return nil
	}
	analyses, err := u.AnalyzeTextBatch(texts, job.Topic, EmptyHistory())
	if err != nil {
		return err
	}
	if err := autopilotBudgetError(ctx); err != nil {
		return err
	}
	for i, post := range posts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i >= len(analyses) || !autopilotDirectPostEligible(u, sources[i], job.Topic, post, analyses[i], time.Now().Unix()) {
			continue
		}
		if err := a.saveAutopilotDirectResult(ctx, job, sources[i], post, analyses[i]); err != nil {
			return err
		}
	}
	return nil
}
