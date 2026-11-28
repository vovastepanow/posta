package posta

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func autopilotBraveRetryAt(at time.Time, header string) time.Time {
	delay := 30 * time.Minute
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds > 0 {
		delay = max(delay, time.Duration(min(seconds, int64((365*24*time.Hour).Seconds())))*time.Second)
	} else if until, err := http.ParseTime(header); err == nil && until.After(at) {
		delay = max(delay, min(until.Sub(at), 365*24*time.Hour))
	}
	return at.Add(delay + 4*time.Minute)
}

// The anonymous search shares the existing Brave cooldown across platforms
// and processes, but never selects the configured paid API key.
func (u *U) autopilotBraveSearch(query string) FetchResultURL {
	return u.autopilotBraveSearchWith(query, u.RSSGet)
}

func (u *U) autopilotBraveSearchWith(query string, fetch func(string, int64, time.Duration, string) FetchResultURL) FetchResultURL {
	bravePause.Lock()
	until := bravePause.until
	bravePause.Unlock()
	if u.DB != nil {
		if saved := KV(u.App, u.Ctx, "brave_search_pause_until", int64(0)); saved > until.Unix() {
			until = time.Unix(saved, 0)
		}
	}
	if time.Now().Before(until) {
		return FetchResultURL{FetchResult: FetchResult{Status: 429, Error: "search cooldown"}}
	}
	r := fetch("https://search.brave.com/search?q="+url.QueryEscape(query), 3<<20, 15*time.Second, BrowserUA)
	if r.Status == 429 {
		until = autopilotBraveRetryAt(time.Now(), r.RetryAfter)
		bravePause.Lock()
		if until.After(bravePause.until) {
			bravePause.until = until
		}
		bravePause.Unlock()
		if u.DB != nil {
			_ = u.KVSet(u.Ctx, "brave_search_pause_until", until.Unix(), time.Until(until))
		}
	}
	return r
}

// Both engines are public, anonymous endpoints. Their rate limits remain
// independent; a Brave 429 does not make all publisher discovery unavailable.
// Bing's site: filter is not trusted: the shared extractor verifies hosts.
func (u *U) autopilotPublisherSearch(query, platform string) FetchResultURL {
	return autopilotPublisherSearchWith(query, platform, u.autopilotBraveSearch, u.autopilotTikTokBingFetch)
}

func autopilotPublisherSearchWith(query, platform string, brave func(string) FetchResultURL, bing func(string, int64, time.Duration, string) FetchResultURL) FetchResultURL {
	primary := brave(query)
	_, primaryStatus := autopilotReadSearchDocument(primary, platform).assess(platform)
	if primaryStatus == "ok" {
		return primary
	}
	endpoint := "https://www.bing.com/search?q=" + url.QueryEscape(query)
	secondary := bing(endpoint+"&format=rss", rssMaxBytes, 15*time.Second, BrowserUA)
	doc := autopilotReadSearchDocument(secondary, platform)
	if !doc.valid {
		return primary
	}
	_, secondaryStatus := doc.assess(platform)
	if secondaryStatus == "ok" {
		return secondary
	}
	// Bing RSS can ignore site:. Try the ordinary results once, but never
	// retry a blocked or incomplete response.
	if doc.feed {
		page := bing(endpoint, 3<<20, 15*time.Second, BrowserUA)
		if _, status := autopilotReadSearchDocument(page, platform).assess(platform); status == "ok" {
			return page
		}
	}
	// Prefer a confirmed empty result over an unavailable provider.
	if secondaryStatus == "empty" || primaryStatus != "empty" {
		return secondary
	}
	return primary
}

// Parse each search document once per assessment. Off-site links are not
// evidence of an empty topic, and a partial or blocked response is unusable.
type autopilotSearchDocument struct {
	links []string
	valid bool
	feed  bool
	empty bool
}

func (d autopilotSearchDocument) assess(platform string) ([]DiscoveryCandidate, string) {
	if !d.valid {
		return nil, "unavailable"
	}
	if candidates := autopilotCandidatesFromSearchLinks(d.links, platform); len(candidates) > 0 {
		return candidates, "ok"
	}
	if d.empty {
		return nil, "empty"
	}
	return nil, "unavailable"
}

func autopilotPublisherSearchResponseValid(r FetchResultURL) bool {
	return autopilotReadSearchDocument(r, "").valid
}

func autopilotPublisherSearchStatus(r FetchResultURL, platform string) string {
	_, status := autopilotReadSearchDocument(r, platform).assess(platform)
	return status
}

func autopilotReadSearchDocument(r FetchResultURL, platform string) autopilotSearchDocument {
	final, err := url.Parse(r.URL)
	if !r.OK || r.Status == 206 || err != nil || final.Scheme != "https" || final.User != nil || final.Port() != "" || final.Path != "/search" {
		return autopilotSearchDocument{}
	}
	switch final.Host {
	case "search.brave.com":
		lower := strings.ToLower(string(r.Bytes))
		if strings.Contains(lower, "captcha") || strings.Contains(lower, "challenge_required") {
			return autopilotSearchDocument{}
		}
		d := autopilotSearchDocument{valid: true, empty: strings.Contains(lower, "no results found for")}
		z := html.NewTokenizer(strings.NewReader(string(r.Bytes)))
		for {
			t := z.Next()
			if t == html.ErrorToken {
				break
			}
			if t != html.StartTagToken && t != html.SelfClosingTagToken {
				continue
			}
			token := z.Token()
			if token.Data != "a" {
				continue
			}
			for _, attr := range token.Attr {
				if attr.Key == "href" {
					d.links = append(d.links, attr.Val)
					break
				}
			}
		}
		return d
	case "www.bing.com":
		doc := rssLoad(r.Bytes)
		if rssIsFeed(doc) {
			items := rssItems(doc)
			d := autopilotSearchDocument{valid: true, feed: true, empty: len(items) == 0}
			for _, item := range items {
				d.links = append(d.links, autopilotTikTokUnwrapBing(rssItemLink(item, r.URL)))
			}
			return d
		}
		if links := autopilotBingHTMLLinks(r.Bytes, final); len(links) > 0 {
			return autopilotSearchDocument{links: links, valid: true}
		}
		if platform == PlatformTikTok && !strings.Contains(string(r.Bytes), "<") {
			// The TikTok adapter returns already validated URLs, not a
			// search document. Its separate validity check is retained.
			return autopilotSearchDocument{links: strings.Fields(string(r.Bytes))}
		}
	}
	return autopilotSearchDocument{}
}

// Only result-title anchors within Bing's actual organic result rows count.
// Decode Bing's known tracking wrapper locally, without following redirects.
func autopilotBingHTMLLinks(body []byte, base *url.URL) []string {
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "challenge_required") || strings.Contains(lower, "id=\"b_captcha\"") || strings.Contains(lower, "id=\"captcha\"") {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	var links []string
	var walk func(*html.Node, bool, bool)
	walk = func(n *html.Node, result, title bool) {
		if len(links) >= 100 {
			return
		}
		if n.Type == html.ElementNode {
			if n.Data == "script" || n.Data == "style" || n.Data == "template" {
				return
			}
			if n.Data == "li" {
				result = false
				for _, attr := range n.Attr {
					if attr.Key == "class" {
						for _, class := range strings.Fields(attr.Val) {
							if class == "b_algo" {
								result = true
							}
						}
					}
				}
			}
			if result && n.Data == "h2" {
				title = true
			}
			if result && title && n.Data == "a" {
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						if href, e := url.Parse(attr.Val); e == nil {
							links = append(links, autopilotTikTokUnwrapBing(base.ResolveReference(href).String()))
						}
						break
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, result, title)
		}
	}
	walk(doc, false, false)
	return links
}

// Search documents are not a list of sources. URLs in scripts, unrelated
// snippets, tracking parameters and lookalike hostnames must not nominate a
// publisher. Accept only actual result hyperlinks (or the already validated
// plain URL list from the TikTok Bing RSS adapter).
func autopilotSearchLinkedCandidates(r FetchResultURL, platform string) []DiscoveryCandidate {
	return autopilotCandidatesFromSearchLinks(autopilotReadSearchDocument(r, platform).links, platform)
}

func autopilotCandidatesFromSearchLinks(links []string, platform string) []DiscoveryCandidate {
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	for _, raw := range links {
		p, err := url.Parse(raw)
		if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.User != nil || p.Port() != "" {
			continue
		}
		host := strings.ToLower(p.Hostname())
		parts := strings.Split(strings.Trim(p.Path, "/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		var candidate DiscoveryCandidate
		candidate.Platform = platform
		switch platform {
		case PlatformTelegram:
			if host != "t.me" && host != "telegram.me" {
				continue
			}
			if parts[0] == "s" {
				parts = parts[1:]
			}
			if len(parts) < 1 || len(parts) > 2 {
				continue
			}
			if len(parts) == 2 {
				if n, err := strconv.ParseUint(parts[1], 10, 64); err != nil || n == 0 {
					continue
				}
			}
			candidate.ID = parts[0]
		case PlatformVK:
			if host != "vk.com" && host != "www.vk.com" && host != "m.vk.com" || len(parts) != 1 {
				continue
			}
			candidate.ID = parts[0]
			// A wall post is not a public-page slug. Resolve community
			// wall posts to their numeric club, never to a personal ID.
			if strings.HasPrefix(candidate.ID, "wall-") {
				owner, post, ok := strings.Cut(strings.TrimPrefix(candidate.ID, "wall-"), "_")
				n, ownerErr := strconv.ParseUint(owner, 10, 64)
				m, postErr := strconv.ParseUint(post, 10, 64)
				if !ok || ownerErr != nil || postErr != nil || n == 0 || m == 0 {
					continue
				}
				candidate.ID = "club" + owner
			} else {
				// Reject numeric post/media routes, not legitimate slugs
				// such as wallstreet, photography or videomaker.
				mediaRoute := false
				for _, prefix := range []string{"wall", "photo", "video", "clip", "album", "topic"} {
					tail, ok := strings.CutPrefix(candidate.ID, prefix)
					if ok && len(tail) > 0 && (tail[0] == '-' || (tail[0] >= '0' && tail[0] <= '9')) && strings.Contains(tail, "_") {
						mediaRoute = true
						break
					}
				}
				if mediaRoute {
					continue
				}
			}
		case PlatformTikTok:
			if host != "www.tiktok.com" && host != "tiktok.com" || !strings.HasPrefix(parts[0], "@") {
				continue
			}
			if len(parts) != 1 {
				if _, _, ok := autopilotTikTokVideoParts("https://www.tiktok.com" + p.Path); !ok {
					continue
				}
			}
			candidate.ID = strings.TrimPrefix(parts[0], "@")
		case PlatformRSS:
			if host != "youtube.com" && host != "www.youtube.com" && host != "m.youtube.com" {
				continue
			}
			if len(parts) >= 2 && parts[0] == "channel" && ytChannelIDRe.MatchString(parts[1]) {
				candidate.ID = YouTubeChannelFeedURL(parts[1])
			} else if strings.HasPrefix(parts[0], "@") {
				handle := "https://www.youtube.com/" + parts[0]
				match := lsYTHandleRe.FindStringSubmatch(handle)
				if len(match) != 2 || "@"+match[1] != parts[0] {
					continue
				}
				candidate.ID = handle
			} else {
				continue
			}
		default:
			continue
		}
		id, ok := autopilotCandidateID(candidate)
		// YouTube handles need the existing public-page resolution before
		// they can become valid feed identities.
		if !ok && platform == PlatformRSS && strings.HasPrefix(candidate.ID, "https://www.youtube.com/@") {
			id, ok = candidate.ID, true
		}
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, candidate)
		if len(out) >= discoveryLinksMax {
			break
		}
	}
	return out
}
