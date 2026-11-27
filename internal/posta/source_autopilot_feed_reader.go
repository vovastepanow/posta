package posta

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"posta/internal/jx"
)

// A bounded larger response is useful only for a confirmed size failure.
// Do not repeat a rate-limited or restricted request with another identity.
func autopilotReadListing(raw string, ua string, fetch func(string, int64, time.Duration, string) FetchResultURL) FetchResultURL {
	r := fetch(raw, rssMaxBytes, 15*time.Second, ua)
	if r.TooBig && r.Status >= 200 && r.Status < 300 {
		return fetch(raw, 5<<20, 15*time.Second, ua)
	}
	return r
}

// General RSS/Atom sources may move their feed to a new same-origin URL.
// Follow only an explicit HTML alternate link from the publisher, never
// guessed paths or links to login/challenge pages or another publisher.
func autopilotRSSAlternate(r FetchResultURL) string {
	if !r.OK || rssIsFeed(rssLoad(r.Bytes)) || len(r.Bytes) == 0 {
		return ""
	}
	base, err := url.Parse(r.URL)
	if err != nil || base.Scheme != "https" || base.User != nil || base.Port() != "" {
		return ""
	}
	for _, tag := range linkTagRe.FindAllString(string(r.Bytes), -1) {
		rel := strings.Fields(strings.ToLower(attrOf(tag, "rel")))
		typ := strings.ToLower(strings.TrimSpace(strings.Split(attrOf(tag, "type"), ";")[0]))
		if !contains(rel, "alternate") || (typ != "application/rss+xml" && typ != "application/atom+xml" && typ != "application/feed+json") {
			continue
		}
		raw := RSSAbsoluteURL(r.URL, attrOf(tag, "href"))
		p, err := url.Parse(raw)
		if err != nil || p.Scheme != "https" || !strings.EqualFold(p.Hostname(), base.Hostname()) || p.User != nil || p.Port() != "" || p.RawQuery != "" || p.Fragment != "" || raw == r.URL {
			continue
		}
		if sharedSourceEligible(PlatformRSS, jx.M{"id": raw}) {
			return raw
		}
	}
	return ""
}

func autopilotReadRSS(feed string, fetch func(string, int64, time.Duration, string) FetchResultURL) FetchResultURL {
	r := autopilotReadListing(feed, HTTPUA, fetch)
	if _, youtube := canonicalYouTubeFeed(feed); youtube {
		return r
	}
	// The initial final URL must still belong to the nominated publisher.
	original, err := url.Parse(feed)
	final, finalErr := url.Parse(r.URL)
	if err != nil || finalErr != nil || !strings.EqualFold(original.Hostname(), final.Hostname()) {
		return r
	}
	if alternate := autopilotRSSAlternate(r); alternate != "" {
		replacement := autopilotReadListing(alternate, HTTPUA, fetch)
		p, err := url.Parse(replacement.URL)
		if err == nil && strings.EqualFold(p.Hostname(), original.Hostname()) && replacement.OK && rssIsFeed(rssLoad(replacement.Bytes)) {
			return replacement
		}
	}
	return r
}

func autopilotFeedReadReason(r FetchResultURL) string {
	switch {
	case r.Status == http.StatusTooManyRequests:
		return "rate_limited"
	case r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden:
		return "feed_access_restricted"
	case r.Status == http.StatusNotFound || r.Status == http.StatusGone:
		return "source_not_found"
	case r.TooBig:
		return "feed_too_large"
	default:
		return "feed_unavailable"
	}
}

// Parse one listing independently of earlier sources. Keep an unrecognized
// format distinct from a recognized feed whose parser failed.
func autopilotParseReviewListing(platform, id, feed string, res FetchResultURL) ([]jx.M, string) {
	var posts []jx.M
	var err error
	if platform == PlatformTikTok {
		err = json.Unmarshal(res.Bytes, &posts)
	} else if platform == PlatformTelegram || platform == PlatformVK || platform == PlatformOK {
		posts, err = autopilotPublicSocialPosts(platform, id, res)
	} else if autopilotPublicRedditFeed(feed) {
		posts, err = autopilotRedditListing(res.Bytes, feed, res.URL)
	} else if _, ok := autopilotMastodonFeed(feed); ok {
		posts, err = autopilotMastodonSafeStatuses(res.Bytes, feed)
	} else if host, timelineID := osnovaTimelineSource(feed); host != "" {
		posts, err = osnovaTimelinePosts(res.Bytes, host, timelineID)
	} else if channel := RutubeChannelID(feed); channel != "" {
		posts, err = autopilotRutubeChannelPosts(res.Bytes, channel)
	} else if source := pikabuCommunityURL(feed); source != "" {
		posts, err = pikabuCommunityPosts(res.Bytes, source, 20, true)
	} else {
		doc := rssLoad(res.Bytes)
		if !rssIsFeed(doc) {
			return nil, "unrecognized_feed"
		}
		posts, err = rssParseLoadedDocument(doc, 20, "", res.URL, true, true, 0)
	}
	if err != nil {
		return posts, "feed_parse_failed"
	}
	return posts, ""
}
