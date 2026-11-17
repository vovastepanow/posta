package posta

import (
	"context"
	"strings"
	"time"

	"posta/internal/jx"
)

// Independent public search can discover useful posts before a source is
// verified. Keep a short-lived, bounded set of posts to assess after the
// source enters probation; do not treat these as approved content.
const autopilotPostHintTTL = 48 * time.Hour
const autopilotPostHintLimit = 4

func autopilotPostHintKey(topicKey, source string) string {
	return "autopilot:post_hints:" + topicKey + ":" + repeatDigest(source)
}

// Validate every post against its extracted public author, not the mutable
// source_key field supplied by search backends. Only text posts are staged:
// media must be fetched and attested via the normal source pipeline.
func autopilotSafePostHints(source string, posts []jx.M, at int64) []jx.M {
	seen := map[string]bool{}
	out := make([]jx.M, 0, min(autopilotPostHintLimit, len(posts)))
	for _, post := range posts {
		if len(out) >= autopilotPostHintLimit {
			break
		}
		date := jx.Int(post, "posted_at")
		if date <= 0 || date > at+300 || IsPostOutdated(date, at, 2) ||
			!autopilotPublicPostURL(jx.Str(post, "url"), false) ||
			jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") ||
			len(jx.ToStrs(post["safety"])) > 0 || jx.Bool(post, "advertising") ||
			PostIsAdvertisement(post) || PostSearchIsAd(post) || PostItemHasVideo(post) ||
			ContentImageKey(post) != "" {
			continue
		}
		candidate := PostSearchAuthor(post)
		if candidate == nil {
			continue
		}
		id, ok := autopilotCandidateID(*candidate)
		if !ok || id != source {
			continue
		}
		key := CandidatePostKey(post)
		if key == "" || seen[key] || len(jx.Marshal(post)) > 16000 ||
			strings.TrimSpace(AnalysisText(post)) == "" {
			continue
		}
		seen[key] = true
		out = append(out, jx.Copy(post))
	}
	return out
}

// Give independently discovered posts their first chance at assessment
// after probation, then retain the usual spread of feed posts. The two
// sources are deduplicated by canonical post key, without reducing feed checks
// or raising the normal per-run model budget.
func autopilotMergePostHints(source string, feed, hinted []jx.M, at int64) []jx.M {
	staged := autopilotSafePostHints(source, hinted, at)
	seen := map[string]bool{}
	out := make([]jx.M, 0, len(staged)+len(feed))
	for _, post := range staged {
		key := CandidatePostKey(post)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, post)
		}
	}
	for _, post := range feed {
		key := CandidatePostKey(post)
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, post)
		}
	}
	return out
}

func (a *App) autopilotStagePublicPosts(ctx context.Context, job *autopilotJob, source string, posts []jx.M) error {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	safe := autopilotSafePostHints(source, posts, time.Now().Unix())
	if len(safe) == 0 {
		return nil
	}
	key := autopilotPostHintKey(job.TopicKey, source)
	_, err := KVUpdate(a, contextWithAutopilotLease(ctx, job), key, autopilotPostHintTTL, func(current []jx.M, _ bool) *[]jx.M {
		combined := append(append([]jx.M{}, safe...), current...)
		filtered := autopilotSafePostHints(source, combined, time.Now().Unix())
		return &filtered
	})
	return err
}

func (a *App) autopilotPendingPublicPosts(ctx context.Context, job *autopilotJob, source string) []jx.M {
	if job == nil || job.Scope != autopilotAutoScope || job.Kind != autopilotAutoKind {
		return nil
	}
	var posts []jx.M
	if !a.KVGet(ctx, autopilotPostHintKey(job.TopicKey, source), &posts) {
		return nil
	}
	return autopilotSafePostHints(source, posts, time.Now().Unix())
}
