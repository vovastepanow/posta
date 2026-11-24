package posta

import (
	"context"
	"net/url"
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

// A search result's source_url is untrusted. The post itself must originate
// from the same public platform as the nominated author; an unrelated page
// cannot impersonate a Telegram channel or a publisher.
func autopilotPostMatchesSource(source string, post jx.M) bool {
	candidate := PostSearchAuthor(post)
	if candidate == nil {
		return false
	}
	canonical, ok := autopilotCandidateID(*candidate)
	if !ok || canonical != source {
		return false
	}
	platform, id, _, ok := autopilotSourceParts(source)
	if !ok {
		return false
	}
	link, err := url.Parse(jx.Str(post, "url"))
	if err != nil || link.Hostname() == "" || link.User != nil {
		return false
	}
	host := strings.ToLower(link.Hostname())
	switch platform {
	case PlatformTelegram:
		if host != "t.me" && host != "telegram.me" {
			return false
		}
		path := strings.Trim(link.Path, "/")
		path = strings.TrimPrefix(path, "s/")
		return strings.EqualFold(strings.SplitN(path, "/", 2)[0], id)
	case PlatformVK:
		if host != "vk.com" && host != "m.vk.com" && host != "www.vk.com" {
			return false
		}
		// PostSearchAuthor resolves public communities to clubNNN. Require
		// the wall post's numeric owner to match that community, not just
		// the host vk.com, which contains posts from unrelated authors.
		if strings.HasPrefix(id, "club") {
			number := strings.TrimPrefix(id, "club")
			return number != "" && strings.HasPrefix(strings.Trim(link.Path, "/"), "wall-"+number+"_")
		}
		return false
	case PlatformTikTok:
		return host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com")
	case PlatformOK:
		return host == "ok.ru" || strings.HasSuffix(host, ".ok.ru")
	case PlatformRSS:
		feed, err := url.Parse(id)
		if err != nil || feed.Hostname() == "" {
			return false
		}
		if feed.Hostname() == "bsky.app" {
			did := autopilotBlueskyProfileDID(strings.TrimSuffix(id, "/rss"))
			parts := strings.Split(strings.Trim(link.Path, "/"), "/")
			return did != "" && host == "bsky.app" && link.Scheme == "https" && link.Port() == "" &&
				link.RawQuery == "" && link.Fragment == "" && len(parts) == 4 && parts[0] == "profile" &&
				parts[1] == did && parts[2] == "post" && autopilotBlueskyRecordKey.MatchString(parts[3])
		}
		return strings.EqualFold(host, feed.Hostname()) || strings.EqualFold(strings.TrimPrefix(host, "www."), strings.TrimPrefix(strings.ToLower(feed.Hostname()), "www."))
	}
	return false
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
		if !autopilotPostMatchesSource(source, post) {
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
