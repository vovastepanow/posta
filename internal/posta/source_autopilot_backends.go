package posta

// Keep the search order and configuration switches in one table. Disabled
// backends are filtered before the adaptive runner rotates the enabled list.
func (u *U) autopilotSearchBackends(query, topic string, turn int64) []func() []DiscoveryCandidate {
	queries := []string{query}
	options := []struct {
		setting string
		search  func() []DiscoveryCandidate
	}{
		{"search_telegram", func() []DiscoveryCandidate { return u.autopilotFindPublicSocial(query, PlatformTelegram) }},
		{"search_vk", func() []DiscoveryCandidate { return u.autopilotFindPublicSocial(query, PlatformVK) }},
		{"search_youtube", func() []DiscoveryCandidate { return u.autopilotFindYouTube(query) }},
		{"search_rutube", func() []DiscoveryCandidate { return u.autopilotFindRutube(query) }},
		{"autopilot_search_tiktok", func() []DiscoveryCandidate { return u.autopilotFindPublicSocial(query, PlatformTikTok) }},
		{"autopilot_search_ok", func() []DiscoveryCandidate { return u.discoveryFindOK(queries, topic) }},
		{"search_bluesky", func() []DiscoveryCandidate { return u.discoveryFindBluesky(queries) }},
		{"search_lemmy", func() []DiscoveryCandidate { return u.discoveryFindLemmy(queries, topic) }},
		{"search_mastodon", func() []DiscoveryCandidate { return u.autopilotFindMastodon(query) }},
		{"search_reddit", func() []DiscoveryCandidate { return u.autopilotFindReddit(query) }},
		{"autopilot_search_habr", func() []DiscoveryCandidate { return u.autopilotFindHabr(query, int(turn%11)) }},
		{"autopilot_search_tumblr", func() []DiscoveryCandidate { return u.autopilotFindTumblr(query) }},
		{"search_news", func() []DiscoveryCandidate { return u.discoveryFindNewsFeeds(queries) }},
	}
	var enabled []func() []DiscoveryCandidate
	for _, option := range options {
		if u.App.TBool(option.setting) {
			enabled = append(enabled, option.search)
		}
	}
	return enabled
}
