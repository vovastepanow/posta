package posta

import (
	"net/url"
	"regexp"
	"strings"
)

// SourceWebURL is for clickable source cards only. Keep the original RSS
// address in source IDs, catalog entries, fetch jobs and deduplication keys.
func SourceWebURL(platform, id string) string {
	if platform == PlatformRSS {
		return RSSWebURL(id)
	}
	if platform == PlatformOK && IsValidOKGroup(id) {
		return "https://ok.ru/group/" + id
	}
	return registryURL(platform, id)
}

var webSourceSlug = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

func validWebSourceSlug(slug string) bool {
	return slug != "" && slug != "." && slug != ".." && !strings.Contains(slug, "..") && webSourceSlug.MatchString(slug)
}

// RSSWebURL points to a human-readable community, blog or author profile.
// Never link to XML/RSS when displaying a source. For unfamiliar feeds,
// open the publisher's site rather than guessing an unrelated profile.
func RSSWebURL(feed string) string {
	u, err := url.Parse(feed)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Hostname() == "" || u.User != nil {
		return ""
	}
	if cid, tab, ok := YouTubeFeedChannel(feed); ok {
		return "https://www.youtube.com/channel/" + cid + "/" + tab
	}
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	origin := u.Scheme + "://" + u.Host
	page := func(p string) string { return origin + p }

	// Lemmy instances: community and user feeds have corresponding web pages.
	for _, kind := range []string{"c", "u"} {
		prefix := "/feeds/" + kind + "/"
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, ".xml") {
			slug := strings.TrimSuffix(strings.TrimPrefix(path, prefix), ".xml")
			if validWebSourceSlug(slug) {
				return page("/" + kind + "/" + slug)
			}
		}
	}
	// Bluesky's generated profile feed is the web profile with /rss removed.
	if host == "bsky.app" && strings.HasPrefix(path, "/profile/") && strings.HasSuffix(path, "/rss") {
		profile := strings.TrimSuffix(strings.TrimPrefix(path, "/profile/"), "/rss")
		if validWebSourceSlug(profile) || strings.HasPrefix(profile, "did:plc:") {
			return page("/profile/" + profile)
		}
	}
	// Federated Mastodon handles include the leading @ in their web URL.
	if strings.HasPrefix(path, "/@") && strings.HasSuffix(path, ".rss") {
		handle := strings.TrimSuffix(strings.TrimPrefix(path, "/@"), ".rss")
		if validWebSourceSlug(handle) {
			return page("/@" + handle)
		}
	}
	// Community, user and saved-search RSS endpoints on Reddit.
	if host == "reddit.com" || host == "www.reddit.com" || strings.HasSuffix(host, ".reddit.com") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 3 && (parts[0] == "r" || parts[0] == "user") &&
			parts[2] == ".rss" && validWebSourceSlug(parts[1]) {
			return page("/" + parts[0] + "/" + parts[1] + "/")
		}
	}
	// RSS endpoints on Pinterest are not public profile URLs.
	if pinterestHostRe.MatchString(host) {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 2 && validWebSourceSlug(parts[0]) {
			if parts[1] == "feed.rss" {
				return page("/" + parts[0] + "/")
			}
			if board := strings.TrimSuffix(parts[1], ".rss"); board != parts[1] && validWebSourceSlug(board) {
				return page("/" + parts[0] + "/" + board + "/")
			}
		}
	}
	if strings.HasSuffix(host, ".tumblr.com") && path == "/rss" {
		return page("/")
	}
	if strings.HasSuffix(host, ".substack.com") && path == "/feed" {
		return page("/")
	}
	// Habr hub feeds: /ru/rss/hubs/python/articles/all/ -> /ru/hubs/python/
	if host == "habr.com" || host == "www.habr.com" {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 4 && parts[1] == "rss" && parts[2] == "hubs" && validWebSourceSlug(parts[3]) {
			return page("/" + parts[0] + "/hubs/" + parts[3] + "/")
		}
	}
	if host == "medium.com" && strings.HasPrefix(path, "/feed/") {
		rest := strings.TrimPrefix(path, "/feed/")
		if rest != "" && !strings.Contains(rest, "..") {
			return page("/" + rest)
		}
	}
	// Conventional /blog/feed, /blog/rss.xml and similar endpoints have
	// a human blog at their parent path, not at the XML address.
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 0 {
		last := strings.ToLower(parts[len(parts)-1])
		switch last {
		case "feed", "rss", "feed.xml", "rss.xml", "atom.xml", "index.xml", "feed.rss":
			if len(parts) > 1 {
				return page("/" + strings.Join(parts[:len(parts)-1], "/") + "/")
			}
		}
	}
	return page("/")
}
