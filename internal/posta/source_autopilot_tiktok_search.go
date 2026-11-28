package posta

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var autopilotTikTokProfileRE = regexp.MustCompile(`^/@([A-Za-z0-9_.]{2,24})/?$`)

// Bing can wrap result links in /ck/a?u=a1<base64url>. Decode locally;
// the target is still checked against the TikTok allowlist below.
func autopilotTikTokUnwrapBing(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "www.bing.com" && u.Host != "bing.com") || u.Path != "/ck/a" {
		return raw
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(values["u"]) != 1 {
		return ""
	}
	encoded := values.Get("u")
	if !strings.HasPrefix(encoded, "a1") || len(encoded) > 8192 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded[2:], "="))
	if err != nil || len(decoded) > 4096 {
		return ""
	}
	return string(decoded)
}

var autopilotTikTokBingPause = searchProviderPause{key: "autopilot_tiktok_bing_pause_until"}

// Bing's RSS entries can be unrelated even when site: was requested. Only
// direct, canonical TikTok links are accepted; an empty result is unknown.
func autopilotTikTokBingLinks(r FetchResultURL) []string {
	final, err := url.Parse(r.URL)
	if !r.OK || r.Status == 206 || err != nil || final.Scheme != "https" || final.Host != "www.bing.com" || final.User != nil || final.Path != "/search" {
		return nil
	}
	doc := rssLoad(r.Bytes)
	if !rssIsFeed(doc) {
		return nil
	}
	var links []string
	seen := map[string]bool{}
	for _, item := range rssItems(doc) {
		raw := autopilotTikTokUnwrapBing(rssItemLink(item, r.URL))
		p, err := url.Parse(raw)
		if err != nil || p.Scheme != "https" || p.User != nil || p.Port() != "" || (p.Host != "www.tiktok.com" && p.Host != "tiktok.com") {
			continue
		}
		p.RawQuery = ""
		p.Fragment = ""
		p.Host = "www.tiktok.com"
		normalized := p.String()
		if _, _, ok := autopilotTikTokVideoParts(normalized); !ok {
			m := autopilotTikTokProfileRE.FindStringSubmatch(p.Path)
			if len(m) != 2 || !IsValidTikTokUser(m[1]) {
				continue
			}
			normalized = "https://www.tiktok.com/@" + m[1]
		}
		if !seen[normalized] {
			seen[normalized] = true
			links = append(links, normalized)
		}
	}
	return links
}

func (u *U) autopilotTikTokSearch(query string) FetchResultURL {
	return autopilotTikTokSearchWith(query, u.autopilotBraveSearch, u.autopilotTikTokBingFetch)
}

func (u *U) autopilotTikTokBingFetch(raw string, maxBytes int64, timeout time.Duration, ua string) FetchResultURL {
	return u.autopilotSearchFetch(&autopilotTikTokBingPause, raw, maxBytes, timeout, ua, u.RSSGet)
}

func autopilotTikTokSearchWith(query string, brave func(string) FetchResultURL, fetch func(string, int64, time.Duration, string) FetchResultURL) FetchResultURL {
	r := brave(query)
	if autopilotTikTokSearchResponseValid(r) {
		if strings.Contains(query, "/video/") {
			if autopilotTikTokSearchHasLinks(r.Bytes, true) {
				return r
			}
		} else if len(autopilotSearchLinkedCandidates(r, PlatformTikTok)) > 0 {
			return r
		}
	}
	bing := fetch("https://www.bing.com/search?q="+url.QueryEscape(query)+"&format=rss", rssMaxBytes, 15*time.Second, BrowserUA)
	links := autopilotTikTokBingLinks(bing)
	if strings.Contains(query, "/video/") {
		videos := links[:0]
		for _, link := range links {
			if _, _, ok := autopilotTikTokVideoParts(link); ok {
				videos = append(videos, link)
			}
		}
		links = videos
	}
	if len(links) == 0 {
		return r
	}
	bing.Bytes = []byte(strings.Join(links, "\n"))
	return bing
}

func autopilotTikTokSearchHasLinks(body []byte, requireVideo bool) bool {
	if requireVideo {
		for _, link := range autopilotTikTokLinksRE.FindAllString(string(body), -1) {
			if _, _, ok := autopilotTikTokVideoParts(link); ok {
				return true
			}
		}
		return false
	}
	for _, candidate := range SourceLinkedSources(string(body)) {
		if candidate.Platform == PlatformTikTok {
			if _, ok := autopilotCandidateID(candidate); ok {
				return true
			}
		}
	}
	return false
}
func autopilotTikTokSearchResponseValid(r FetchResultURL) bool {
	p, err := url.Parse(r.URL)
	if !r.OK || r.Status == 206 || err != nil || p.Scheme != "https" || p.User != nil || p.Path != "/search" || (p.Host != "search.brave.com" && p.Host != "www.bing.com") {
		return false
	}
	lower := strings.ToLower(string(r.Bytes))
	return !strings.Contains(lower, "captcha") && !strings.Contains(lower, "challenge_required")
}
