package posta

import "posta/internal/jx"

type autopilotSearchBackend struct {
	name, setting string
	search func(*U, string, string, int64) []DiscoveryCandidate
}

// Search order and public diagnostics are derived from the same registry.
var autopilotSearchRegistry = []autopilotSearchBackend{
	{"Telegram", "search_telegram", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindPublicSocial(query, PlatformTelegram)
	}},
	{"ВКонтакте", "search_vk", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindPublicSocial(query, PlatformVK)
	}},
	{"YouTube", "search_youtube", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindYouTube(query)
	}},
	{"Rutube", "search_rutube", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindRutube(query)
	}},
	{"TikTok", "autopilot_search_tiktok", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindPublicSocial(query, PlatformTikTok)
	}},
	{"Одноклассники", "autopilot_search_ok", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.discoveryFindOK([]string{query}, topic)
	}},
	{"Bluesky", "search_bluesky", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.discoveryFindBluesky([]string{query})
	}},
	{"Lemmy", "search_lemmy", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.discoveryFindLemmy([]string{query}, topic)
	}},
	{"Mastodon", "search_mastodon", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindMastodon(query)
	}},
	{"Reddit", "search_reddit", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindReddit(query)
	}},
	{"Хабр", "autopilot_search_habr", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindHabr(query, int(turn%11))
	}},
	{"Tumblr", "autopilot_search_tumblr", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.autopilotFindTumblr(query)
	}},
	{"Новости RSS", "search_news", func(u *U, query, topic string, turn int64) []DiscoveryCandidate {
		return u.discoveryFindNewsFeeds([]string{query})
	}},
}

func (u *U) autopilotSearchBackends(query, topic string, turn int64) []func() []DiscoveryCandidate {
	var enabled []func() []DiscoveryCandidate
	for _, backend := range autopilotSearchRegistry {
		if u.TBool(backend.setting) {
			enabled = append(enabled, func() []DiscoveryCandidate { return backend.search(u, query, topic, turn) })
		}
	}
	return enabled
}

func autopilotBackendStatus(enabled func(string) bool) []jx.M {
	out := make([]jx.M, 0, len(autopilotSearchRegistry))
	for _, backend := range autopilotSearchRegistry {
		out = append(out, jx.M{"name": backend.name, "enabled": enabled(backend.setting), "access": "public"})
	}
	return out
}
