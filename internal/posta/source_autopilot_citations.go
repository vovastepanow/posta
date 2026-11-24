package posta

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
)

// References in an accepted post are discovery hints, never trusted evidence
// that the referenced publisher is relevant or safe. Do not fetch arbitrary
// URLs found in text, follow private invitations, or infer a channel from
// a video URL. Only already-supported public publisher identities qualify.
var autopilotCitationLink = regexp.MustCompile(`https://[^\s<>"'\[\]{}]+`)
var autopilotCitationNumber = regexp.MustCompile(`^[0-9]+$`)

func autopilotCitationSource(raw string, telegram, vk, youtube bool) string {
	u, err := url.Parse(strings.TrimRight(raw, ".,;!?)"))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var c DiscoveryCandidate
	switch strings.ToLower(u.Hostname()) {
	case "t.me":
		if !telegram {
			return ""
		}
		if len(parts) > 1 && parts[0] == "s" {
			parts = parts[1:]
		}
		if len(parts) != 1 && (len(parts) != 2 || !autopilotCitationNumber.MatchString(parts[1])) {
			return ""
		}
		c = DiscoveryCandidate{Platform: PlatformTelegram, ID: parts[0]}
	case "vk.com", "www.vk.com":
		if !vk || len(parts) != 1 {
			return ""
		}
		id := strings.ToLower(parts[0])
		// A numeric public group or community is unambiguous; a personal
		// profile, arbitrary screen name or wall post is not.
		if !(strings.HasPrefix(id, "club") && autopilotCitationNumber.MatchString(strings.TrimPrefix(id, "club"))) &&
			!(strings.HasPrefix(id, "public") && autopilotCitationNumber.MatchString(strings.TrimPrefix(id, "public"))) {
			return ""
		}
		c = DiscoveryCandidate{Platform: PlatformVK, ID: id}
	case "youtube.com", "www.youtube.com":
		if !youtube || len(parts) != 2 || parts[0] != "channel" || !ytChannelIDRe.MatchString(parts[1]) {
			return ""
		}
		c = DiscoveryCandidate{Platform: PlatformRSS, ID: YouTubeChannelFeedURL(parts[1])}
	default:
		return ""
	}
	id, ok := autopilotCandidateID(c)
	if !ok {
		return ""
	}
	return id
}

func autopilotCitationSources(text, parent string, telegram, vk, youtube bool) []string {
	var result []string
	seen := map[string]bool{parent: true}
	for _, raw := range autopilotCitationLink.FindAllString(text, 40) {
		if source := autopilotCitationSource(raw, telegram, vk, youtube); source != "" && !seen[source] {
			seen[source] = true
			result = append(result, source)
			if len(result) == 4 {
				break
			}
		}
	}
	return result
}

// Work only on saved, admitted public posts for this exact topic and policy.
// The queue is bounded per topic and per day. Repeated links do not reset a
// previously rejected source, technical cooldown, or manual exclusion.
func (a *App) autopilotConsumeCitations(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope {
		return nil
	}
	telegram, vk, youtube := a.TBool("search_telegram"), a.TBool("search_vk"), a.TBool("search_youtube")
	if !telegram && !vk && !youtube {
		return nil
	}
	now := time.Now().UTC()
	var pending, daily int
	if err := a.DB.Pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE state='candidate'),
		count(*) FILTER (WHERE evidence->>'discovery_origin'='citation' AND first_seen_at >= $3)
		FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2`,
		job.Scope, job.TopicKey, now.Truncate(24*time.Hour).Unix()).Scan(&pending, &daily); err != nil {
		return err
	}
	limit := min(4, 64-pending, 16-daily)
	if limit <= 0 {
		return nil
	}
	rows, err := a.DB.Pool.Query(ctx, `WITH accepted AS (
		SELECT r.source_id,r.post,r.assessed_at,r.post_hash FROM source_autopilot_content_results r
		JOIN source_autopilot_sources s USING(scope,topic_key,source_id)
		WHERE r.scope=$1 AND r.topic_key=$2 AND r.policy_version=$3
		AND r.published_at >= $4 AND r.assessed_at >= $4 AND s.state IN ('active','probation')
		UNION ALL
		SELECT source_id,post,assessed_at,post_hash FROM source_autopilot_direct_results
		WHERE scope=$1 AND topic_key=$2 AND policy_version=$3 AND published_at >= $4 AND assessed_at >= $4
	), diverse AS (
		SELECT *,row_number() OVER (PARTITION BY source_id ORDER BY assessed_at DESC,post_hash) AS n
		FROM accepted
	)
	SELECT source_id,post FROM diverse d WHERE n<=2
	AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o
		WHERE o.scope=$1 AND o.topic_key=$2 AND o.owner_id='' AND o.source_id=d.source_id AND o.decision='excluded')
	ORDER BY n,assessed_at DESC,source_id,post_hash LIMIT 48`,
		job.Scope, job.TopicKey, TopicProfileVersion, now.Add(-48*time.Hour).Unix())
	if err != nil {
		return err
	}
	type sample struct {
		source string
		post   jx.M
	}
	var samples []sample
	for rows.Next() {
		var source string
		var raw []byte
		if err := rows.Scan(&source, &raw); err != nil {
			rows.Close()
			return err
		}
		samples = append(samples, sample{source, jx.Unmarshal(raw)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range samples {
		if jx.Bool(item.post, "source_sensitive") || jx.Bool(item.post, "nsfw") || len(jx.ToStrs(item.post["safety"])) > 0 || PostIsAdvertisement(item.post) {
			continue
		}
		for _, source := range autopilotCitationSources(jx.Str(item.post, "text"), item.source, telegram, vk, youtube) {
			if seen[source] {
				continue
			}
			seen[source] = true
			var exists bool
			if err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_sources
				WHERE scope=$1 AND topic_key=$2 AND source_id=$3)
				OR EXISTS(SELECT 1 FROM source_autopilot_overrides WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND owner_id='' AND decision='excluded')`,
				job.Scope, job.TopicKey, source).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			evidence := jx.Marshal(jx.M{"discovery_origin": "citation", "citation_parent": repeatDigest(item.source)})
			if err := registerAutopilotCandidateWithEvidence(ctx, a.DB, job, source, "discovered", evidence, now); err != nil {
				return err
			}
			limit--
			if limit == 0 {
				return nil
			}
		}
	}
	return nil
}
