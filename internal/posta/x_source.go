package posta

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"posta/internal/jx"
)

func xSearchCandidates(page string) []DiscoveryCandidate {
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	z := html.NewTokenizer(strings.NewReader(page))
	for len(out) < 20 {
		t := z.Next()
		if t == html.ErrorToken {
			break
		}
		if t != html.StartTagToken && t != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "a" {
			continue
		}
		for _, attr := range tok.Attr {
			if attr.Key != "href" {
				continue
			}
			u, err := url.Parse(xSearchResultURL(attr.Val))
			if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "x.com" && u.Hostname() != "twitter.com" && u.Hostname() != "www.twitter.com" && u.Hostname() != "www.x.com") {
				continue
			}
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 0 {
				continue
			}
			username := xProfileName("https://x.com/" + parts[0])
			if username != "" && !seen[username] {
				seen[username] = true
				out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://x.com/" + username})
			}
		}
	}
	return out
}

var xSearchPause struct {
	sync.Mutex
	until time.Time
}

// Bing wraps result links in a base64url-encoded redirect. Decode only its
// own redirect format and validate the resulting X host in xSearchCandidates.
func xSearchResultURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "www.bing.com" && u.Hostname() != "bing.com") || u.Path != "/ck/a" {
		return raw
	}
	encoded := u.Query().Get("u")
	if !strings.HasPrefix(encoded, "a1") {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded[2:], "="))
	if err != nil {
		return ""
	}
	return string(decoded)
}

func (u *U) discoveryFindX(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_x_sources") {
		return nil
	}
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	for _, q := range queries[:min(2, len(queries))] {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		key := "bing_x_search:" + sha1Short(strings.ToLower(q), 20)
		var found []DiscoveryCandidate
		if u.DB != nil && u.CacheGet(u.Ctx, key, &found) {
			for _, c := range found {
				if !seen[c.ID] {
					seen[c.ID] = true
					out = append(out, c)
				}
			}
			continue
		}
		xSearchPause.Lock()
		until := xSearchPause.until
		xSearchPause.Unlock()
		if time.Now().Before(until) {
			u.discoveryError("Поиск Bing временно ограничен; повтор не раньше " + until.Format("02.01 15:04") + ".")
			break
		}
		params := url.Values{"q": {"site:x.com " + q}, "count": {"20"}}
		r := u.RSSGet("https://www.bing.com/search?"+params.Encode(), 3<<20, 15*time.Second, BrowserUA)
		if r.Status == 429 {
			xSearchPause.Lock()
			xSearchPause.until = time.Now().Add(30 * time.Minute)
			xSearchPause.Unlock()
			u.discoveryError("Bing ограничил поиск X (HTTP 429); пауза на 30 минут.")
			break
		}
		if !r.OK {
			u.discoveryError(fmt.Sprintf("Поиск X через Bing недоступен: %s", fetchFailure(r.FetchResult)))
			break
		}
		found = xSearchCandidates(string(r.Bytes))
		for i := range found {
			found[i].Origin, found[i].OriginKind = "поиск Bing «"+q+"»", "other"
		}
		if len(found) > 0 && u.DB != nil {
			u.CacheSet(u.Ctx, key, found, time.Hour)
		}
		for _, c := range found {
			if !seen[c.ID] {
				seen[c.ID] = true
				out = append(out, c)
			}
		}
		u.Log("-> Поиск профилей X через Bing «%s»: найдено %d.", q, len(found))
	}
	return out
}

var xProfilePath = regexp.MustCompile(`^/([A-Za-z0-9_]{1,15})/?$`)
var xTimelineValue = regexp.MustCompile(`value:("(?:\\.|[^"\\])*")`)

func xProfileName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "x.com" && host != "twitter.com" && host != "www.twitter.com" {
		return ""
	}
	m := xProfilePath.FindStringSubmatch(u.Path)
	if m == nil || contains([]string{"home", "search", "explore", "login", "i", "settings", "intent", "share"}, strings.ToLower(m[1])) {
		return ""
	}
	return strings.ToLower(m[1])
}

// X embeds its public profile timeline in JSON strings inside relay scripts.
func xProfilePosts(page []byte, username string, count int, withText bool) []jx.M {
	var out []jx.M
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		if len(out) >= count {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			m := jx.M(x)
			if jx.Str(m, "__typename") == "Tweet" {
				id := jx.Str(m, "rest_id")
				user := jx.Str(jx.Map(jx.Map(jx.Map(jx.Map(m, "core"), "user_results"), "result"), "core"), "screen_name")
				details := jx.Map(m, "details")
				text := jx.Str(details, "full_text")
				if id != "" && !seen[id] && strings.EqualFold(user, username) {
					var media []jx.M
					for _, m := range jx.Maps(m, "media_entities2") {
						poster := jx.Str(m, "media_url_https")
						if !strings.HasPrefix(poster, "https://pbs.twimg.com/") {
							poster = ""
						}
						switch jx.Str(m, "type") {
						case "photo":
							if poster != "" {
								media = append(media, jx.M{"image": poster})
							}
						case "video", "animated_gif":
							video, rate := "", int64(-1)
							for _, v := range jx.Maps(jx.Map(m, "video_info"), "variants") {
								if candidate := jx.Str(v, "url"); jx.Str(v, "content_type") == "video/mp4" && strings.HasPrefix(candidate, "https://video.twimg.com/") && jx.Int(v, "bitrate") > rate {
									video, rate = candidate, jx.Int(v, "bitrate")
								}
							}
							if video != "" {
								media = append(media, jx.M{"image": poster, "video": video})
							}
						}
					}
					if len(media) > 0 || (withText && runeLen(strings.TrimSpace(text)) >= 120) {
						seen[id] = true
						link := "https://x.com/" + username + "/status/" + id
						post := searchPost("X", "x:"+username, "https://x.com/"+username, trimRunes(text, 80), text, "", "", link, jx.Int(details, "created_at_ms")/1000)
						post["source_sensitive"] = jx.Bool(jx.Map(m, "legacy"), "possibly_sensitive")
						if len(media) > 0 {
							setPostMedia(post, media)
							post["text_post"] = false
						}
						out = append(out, post)
					}
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	for _, match := range xTimelineValue.FindAllSubmatch(page, -1) {
		var value string
		if json.Unmarshal(match[1], &value) != nil || !strings.Contains(value, "profile_user_originals_timeline") {
			continue
		}
		var data any
		if json.Unmarshal([]byte(value), &data) == nil {
			walk(data)
		}
	}
	return out
}
