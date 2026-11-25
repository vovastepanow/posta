package posta

import (
	"strings"

	"posta/internal/jx"
)

// Preference is deliberately a small, reversible reordering of already
// admitted content, not an admission rule or a global source verdict.
// Recent explicit likes/dislikes are the only signals; no inference calls.
const (
	personalPoolDays      = 30
	personalPoolMinVotes  = 5
	personalPoolVoteLimit = 300
	personalPoolMaxBonus  = 6 * 3600
)

type poolAffinityStat struct {
	signed, weight float64
	count          int
}

type personalPoolAffinity struct {
	votes                  int
	sources, tags, formats map[string]poolAffinityStat
}

func (a *personalPoolAffinity) add(m map[string]poolAffinityStat, key string, sign, weight float64) {
	if key == "" {
		return
	}
	s := m[key]
	s.signed += sign * weight
	s.weight += weight
	s.count++
	m[key] = s
}

func affinityValue(s poolAffinityStat) float64 {
	if s.count < 2 {
		return 0
	}
	// Neutral prior keeps small samples from dominating.
	return s.signed / (s.weight + 4)
}

func personalPoolFormat(item jx.M) string {
	if PostItemHasVideo(item) || jx.Str(item, "format") == "video" {
		return "video"
	}
	if jx.Bool(item, "text_post") && !PostItemHasMedia(item) {
		return "text"
	}
	return strings.ToLower(strings.TrimSpace(jx.Str(item, "format")))
}

func uniquePreferenceTags(item jx.M) []string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range jx.Strs(item, "tags") {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

// Scheduling or sending a post can create a history "liked" record for
// repeat protection without any explicit like. Keep those two signals apart.
// Legacy records remain compatible because their origin cannot be recovered.
func preferenceVoteSign(item jx.M, collection string) (float64, bool) {
	switch jx.Str(item, "preference_vote") {
	case "like":
		return 1, true
	case "dislike":
		return -1, true
	case "":
		if collection == "liked" {
			return 1, true
		}
		if collection == "disliked" {
			return -1, true
		}
	}
	return 0, false
}

func buildPersonalPoolAffinity(h History, topic, key string, same func(string, string) bool, at int64) personalPoolAffinity {
	a := personalPoolAffinity{
		sources: map[string]poolAffinityStat{},
		tags:    map[string]poolAffinityStat{},
		formats: map[string]poolAffinityStat{},
	}
	for _, coll := range []string{"liked", "disliked"} {
		for _, item := range h[coll] {
			sign, explicit := preferenceVoteSign(item, coll)
			if !explicit {
				continue
			}
			t, ts := jx.Str(item, "topic"), jx.Int(item, "timestamp")
			if t == "" || (!same(t, topic) && !same(t, key)) || ts <= 0 || ts > at || at-ts > personalPoolDays*86400 {
				continue
			}
			weight := 1.0
			if at-ts > 7*86400 {
				weight = 0.5
			}
			a.votes++
			a.add(a.sources, SourceQualityKey(jx.Str(item, "source_key")), sign, weight)
			a.add(a.formats, personalPoolFormat(item), sign, weight)
			for _, tag := range uniquePreferenceTags(item) {
				a.add(a.tags, tag, sign, weight)
			}
		}
	}
	return a
}

func (a personalPoolAffinity) bonus(item jx.M) int64 {
	if a.votes < personalPoolMinVotes {
		return 0
	}
	source := affinityValue(a.sources[SourceQualityKey(ContentSourceKey(item))])
	format := affinityValue(a.formats[personalPoolFormat(item)])
	tagScore, tagCount := 0.0, 0
	for _, tag := range uniquePreferenceTags(item) {
		if s := a.tags[tag]; s.count >= 2 {
			tagScore += affinityValue(s)
			tagCount++
		}
	}
	if tagCount > 0 {
		tagScore /= float64(tagCount)
	}
	seconds := int64(3*3600*source + 2*3600*tagScore + 3600*format)
	return max(-personalPoolMaxBonus, min(personalPoolMaxBonus, seconds))
}

func (u *U) loadPersonalPoolAffinity(topic, key string) personalPoolAffinity {
	// Only the current owner's recent explicit votes. Limit work and select
	// by vote time, not database row id (maintenance may rewrite row ids).
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT collection,data FROM history_items
		WHERE user_id=$1 AND collection IN ('liked','disliked')
		AND COALESCE(data->>'preference_vote','') IN ('','like','dislike')
		AND CASE WHEN jsonb_typeof(data->'timestamp')='number' THEN (data->>'timestamp')::numeric ELSE 0 END >= $2
		ORDER BY CASE WHEN jsonb_typeof(data->'timestamp')='number' THEN (data->>'timestamp')::numeric ELSE 0 END DESC,id DESC LIMIT $3`,
		u.ID, now()-personalPoolDays*86400, personalPoolVoteLimit)
	if err != nil {
		return personalPoolAffinity{}
	}
	defer rows.Close()
	h := EmptyHistory()
	for rows.Next() {
		var coll string
		var raw []byte
		if err := rows.Scan(&coll, &raw); err != nil {
			return personalPoolAffinity{}
		}
		if item := jx.Unmarshal(raw); item != nil {
			h[coll] = append(h[coll], item)
		}
	}
	if rows.Err() != nil {
		return personalPoolAffinity{}
	}
	return buildPersonalPoolAffinity(h, topic, key, u.TopicSame, now())
}
