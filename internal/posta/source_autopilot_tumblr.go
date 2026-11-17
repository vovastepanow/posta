package posta

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

var autopilotTumblrBlogName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// Search results are only hints. Construct canonical public RSS URLs rather
// than fetching the result's arbitrary links, then run the normal assessment.
func autopilotTumblrCandidates(page, query string) []DiscoveryCandidate {
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	for _, match := range lsTumblrRe.FindAllStringSubmatch(page, -1) {
		name := strings.ToLower(match[1])
		if !autopilotTumblrBlogName.MatchString(name) || contains(tumblrReserved, name) || seen[name] {
			continue
		}
		seen[name] = true
		feed := "https://" + name + ".tumblr.com/rss"
		if _, ok := autopilotPublicRSSID(feed); !ok {
			continue
		}
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: feed, Origin: "публичный поиск Tumblr «" + query + "»", OriginKind: "other"})
		if len(out) == 5 {
			break
		}
	}
	return out
}

func (u *U) autopilotFindTumblr(query string) []DiscoveryCandidate {
	tag := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(query)), " ", "-")
	if tag == "" {
		return nil
	}
	r := u.RSSGet("https://www.tumblr.com/tagged/"+url.PathEscape(tag), 3<<20, 15*time.Second, BrowserUA)
	final, err := url.Parse(r.URL)
	page := strings.ToLower(string(r.Bytes))
	status := "unavailable"
	var candidates []DiscoveryCandidate
	if r.OK && err == nil && final.Scheme == "https" && final.Host == "www.tumblr.com" && strings.HasPrefix(final.Path, "/tagged/") && !strings.Contains(page, "challenge_required") && !strings.Contains(page, "captcha") {
		candidates = autopilotTumblrCandidates(string(r.Bytes), query)
		status = "ok"
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation("tumblr", query, status)
	}
	return candidates
}
