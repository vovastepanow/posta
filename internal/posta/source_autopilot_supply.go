package posta

import (
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// autopilotSupplyCount shares one seen set across the publication queue and
// pool, so the same content cannot satisfy two channels' demand twice.
// Repeat memory is an admission guard, not an inventory filter: inserting a
// post into the pool immediately records its repeat keys. Seeding seen with
// that memory would make every newly delivered post disappear from supply.
func autopilotSupplyCount(pool, queue []jx.M, matches func(jx.M) bool, queueReady func(jx.M) bool) int {
	seen := map[string]bool{}
	count := 0
	add := func(keys []string, eligible bool) {
		if len(keys) == 0 {
			return
		}
		duplicate := false
		valid := false
		for _, key := range keys {
			if key != "" {
				valid = true
				if seen[key] {
					duplicate = true
				}
			}
		}
		if !valid {
			return
		}
		for _, key := range keys {
			if key != "" {
				seen[key] = true
			}
		}
		if eligible && !duplicate {
			count++
		}
	}
	for _, item := range queue {
		keys := jx.Strs(jx.Map(item, "repeat_identity"), "keys")
		if len(keys) == 0 && jx.Str(item, "record") != "" {
			keys = []string{repeatDigest("image:" + jx.Str(item, "record"))}
		}
		add(keys, matches(item) && queueReady(item))
	}
	for _, item := range pool {
		if matches(item) {
			add(repeatKeys(item), true)
		}
	}
	return count
}

// AutopilotAvailableSupply computes a conservative personal count without
// relying on the currently selected screen topic.
func (u *U) AutopilotAvailableSupply(topic string) (int, error) {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT data FROM pool_items WHERE user_id=$1 ORDER BY position`, u.ID)
	if err != nil {
		return 0, err
	}
	pool, err := decodeRows(rows)
	if err != nil {
		return 0, err
	}
	rows, err = u.DB.Pool.Query(u.Ctx, `SELECT data FROM outbox WHERE user_id=$1 AND status IN ('queued','telegram') ORDER BY publish_at`, u.ID)
	if err != nil {
		return 0, err
	}
	queue, err := decodeRows(rows)
	if err != nil {
		return 0, err
	}
	filters := u.ContentFilterValues()
	frozen := map[string]bool{}
	if jx.Bool(filters, "active_sources_only") {
		frozen = u.FrozenSourceLabels()
	}
	adult := u.AdultAllowed()
	blogs, err := u.poolPersonalBlogFilter(topic)
	if err != nil {
		return 0, err
	}
	overrides := u.sourceOverridesForTopic(topic)
	key := autopilotTopicKey(topic)
	matches := func(item jx.M) bool {
		if autopilotTopicKey(jx.Str(item, "topic")) != key {
			return false
		}
		if sourceExcludedByOverride(overrides, jx.Str(item, "source_key")) {
			return false
		}
		if image := jx.Str(item, "image"); image != "" && image == jx.Str(item, "image_local") && u.MediaExisting(image) == "" {
			return false
		}
		if video := jx.Str(item, "video"); video != "" && video == jx.Str(item, "video_local") && u.MediaExisting(video) == "" {
			return false
		}
		if _, queued := item["publish_at"]; queued {
			return true
		}
		if !blogs(item) {
			return false
		}
		posted := jx.Int(item, "posted_at")
		return posted > 0 && posted <= now()+300 && !IsPostOutdated(posted, 0, config.ContentMaxAgeDays) && !poolUnavailableVideo(item) && u.poolItemFresh(item) &&
			u.PoolItemInTopic(item, topic, topic, adult) && poolMatchesDisplayFilters(item, filters, frozen) && !strings.HasPrefix(ContentImageKey(item), "deleted:")
	}
	return autopilotSupplyCount(pool, queue, matches, outboxMediaReady), nil
}
