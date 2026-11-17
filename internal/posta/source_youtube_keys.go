package posta

import (
	"net/url"
	"strings"
)

// YouTube channel IDs are case-sensitive. Only the fixed, public feed URL
// form is recognized here; arbitrary query-bearing RSS URLs keep old behavior.
func canonicalYouTubeFeed(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Path != "/feeds/videos.xml" || (!strings.EqualFold(u.Hostname(), "youtube.com") && !strings.EqualFold(u.Hostname(), "www.youtube.com")) {
		return "", false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 {
		return "", false
	}
	if values := q["channel_id"]; len(values) == 1 && ytChannelIDRe.MatchString(values[0]) {
		return YouTubeChannelFeedURL(values[0]), true
	}
	if values := q["playlist_id"]; len(values) == 1 && strings.HasPrefix(values[0], "UUSH") && ytChannelIDRe.MatchString("UC"+strings.TrimPrefix(values[0], "UUSH")) {
		return RSSYouTubeShortsFeed("UC" + strings.TrimPrefix(values[0], "UUSH")), true
	}
	return "", false
}

// Old keys discarded the channel's case. Their owner cannot be recovered
// reliably; callers use this alias conservatively to preserve old exclusions.
func legacyYouTubeSourceKey(source string) string {
	if !strings.HasPrefix(source, "rss:") {
		return source
	}
	feed, ok := canonicalYouTubeFeed(strings.TrimPrefix(source, "rss:"))
	if !ok {
		return source
	}
	return "rss:" + strings.ToLower(feed)
}

func sourceIDMatchesLegacy(candidate, source string) bool {
	if candidate == source || candidate == legacyYouTubeSourceKey(source) {
		return true
	}
	legacy := legacyYouTubeSourceKey(source)
	return legacy != source && candidate == strings.Replace(legacy, "rss:https://www.youtube.com/", "rss:https://youtube.com/", 1)
}

func sourceKnownWithLegacy(known map[string]bool, source string) bool {
	if known[source] {
		return true
	}
	legacy := legacyYouTubeSourceKey(source)
	if known[legacy] {
		return true
	}
	return legacy != source && known[strings.Replace(legacy, "rss:https://www.youtube.com/", "rss:https://youtube.com/", 1)]
}
