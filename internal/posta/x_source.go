package posta

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

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
			u, err := url.Parse(attr.Val)
			if err != nil || (u.Hostname() != "x.com" && u.Hostname() != "twitter.com" && u.Hostname() != "www.twitter.com") {
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

func (u *U) discoveryFindX(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_x_sources") {
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		page, err := u.braveSearchPage("site:x.com " + q)
		if err != nil {
			u.Log("-> Поиск профилей X «%s»: %s", q, err)
			continue
		}
		found := xSearchCandidates(page)
		for _, c := range found {
			c.Origin, c.OriginKind = "веб-поиск X «"+q+"»", "other"
			out = append(out, c)
		}
		u.Log("-> Поиск профилей X «%s»: найдено %d.", q, len(found))
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
				if id != "" && !seen[id] && strings.EqualFold(user, username) && !jx.Bool(jx.Map(m, "legacy"), "possibly_sensitive") {
					var images []string
					for _, media := range jx.Maps(m, "media_entities2") {
						if jx.Str(media, "type") == "photo" {
							if image := jx.Str(media, "media_url_https"); strings.HasPrefix(image, "https://") {
								images = append(images, image)
							}
						}
					}
					if len(images) > 0 || (withText && runeLen(strings.TrimSpace(text)) >= 120) {
						seen[id] = true
						link := "https://x.com/" + username + "/status/" + id
						post := searchPost("X", "x:"+username, "https://x.com/"+username, trimRunes(text, 80), text, "", "", link, jx.Int(details, "created_at_ms")/1000)
						if len(images) > 0 {
							post["image"], post["images"], post["text_post"] = images[0], images, false
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
