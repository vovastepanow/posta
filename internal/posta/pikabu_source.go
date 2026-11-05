package posta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"posta/internal/jx"
)

var pikabuCommunityPath = regexp.MustCompile(`^/community/([a-zA-Z0-9_]{2,80})/?$`)

func pikabuCommunityURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "pikabu.ru" || u.User != nil || u.Port() != "" {
		return ""
	}
	m := pikabuCommunityPath.FindStringSubmatch(u.Path)
	if m == nil {
		return ""
	}
	return "https://pikabu.ru/community/" + m[1]
}

func pikabuHTML(data []byte) (*html.Node, error) {
	r, err := charset.NewReader(bytes.NewReader(data), "text/html")
	if err != nil {
		return nil, err
	}
	return html.Parse(r)
}

func pikabuAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func pikabuHasClass(n *html.Node, name string) bool {
	for _, v := range strings.Fields(pikabuAttr(n, "class")) {
		if v == name {
			return true
		}
	}
	return false
}

func pikabuWalk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		pikabuWalk(c, fn)
	}
}

func pikabuSearchCommunities(data []byte) ([]DiscoveryCandidate, error) {
	doc, err := pikabuHTML(data)
	if err != nil {
		return nil, err
	}
	var out []DiscoveryCandidate
	pikabuWalk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "script" || pikabuAttr(n, "data-entry") != "search" || n.FirstChild == nil {
			return
		}
		var config struct {
			Found struct {
				Communities []struct {
					Name   string `json:"name"`
					Link   string `json:"link"`
					Locked bool   `json:"is_locked"`
					NSFW   bool   `json:"is_nsfw"`
				} `json:"communities"`
			} `json:"found"`
		}
		if json.Unmarshal([]byte(n.FirstChild.Data), &config) != nil {
			return
		}
		for _, c := range config.Found.Communities {
			if c.Locked || c.NSFW {
				continue
			}
			id := pikabuCommunityURL("https://pikabu.ru/community/" + c.Link)
			if id != "" {
				out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: id, Info: jx.M{"title": c.Name}})
			}
		}
	})
	return out, nil
}

func pikabuCommunityPosts(data []byte, source string, count int, withText bool) ([]jx.M, error) {
	doc, err := pikabuHTML(data)
	if err != nil {
		return nil, err
	}
	var out []jx.M
	pikabuWalk(doc, func(n *html.Node) {
		if len(out) >= count || n.Type != html.ElementNode || n.Data != "article" || !pikabuHasClass(n, "story") {
			return
		}
		var title, link, img, text string
		var at int64
		pikabuWalk(n, func(x *html.Node) {
			if x.Type != html.ElementNode {
				return
			}
			if x.Data == "a" && pikabuHasClass(x, "story__title-link") {
				link = RSSAbsoluteURL(source, pikabuAttr(x, "href"))
				var parts []string
				pikabuWalk(x, func(y *html.Node) {
					if y.Type == html.TextNode {
						parts = append(parts, y.Data)
					}
				})
				title = strings.TrimSpace(strings.Join(parts, " "))
			}
			if at == 0 {
				if v, err := strconv.ParseInt(pikabuAttr(x, "data-timestamp"), 10, 64); err == nil && v > 0 {
					at = v
				}
				if v, err := time.Parse(time.RFC3339, pikabuAttr(x, "datetime")); err == nil {
					at = v.Unix()
				}
			}
			inContent := false
			for parent := x; parent != nil && parent != n; parent = parent.Parent {
				if pikabuHasClass(parent, "story-block_type_image") || pikabuHasClass(parent, "story__content-inner") {
					inContent = true
					break
				}
			}
			if img == "" && inContent && (x.Data == "img" || pikabuHasClass(x, "story-block_type_image")) {
				for _, key := range []string{"data-large-image", "data-src", "src"} {
					if v := pikabuAttr(x, key); strings.HasPrefix(v, "https://") {
						img = v
						break
					}
				}
			}
			if pikabuHasClass(x, "story-block_type_text") {
				text += " " + strings.TrimSpace(nodeText(x))
			}
		})
		p, err := url.Parse(link)
		if err != nil || p.Scheme != "https" || p.Hostname() != "pikabu.ru" || !strings.HasPrefix(p.Path, "/story/") || title == "" {
			return
		}
		text = strings.TrimSpace(title + " " + text)
		if img == "" && (!withText || runeLen(text) < 120) {
			return
		}
		out = append(out, searchPost("Пикабу", "pikabu:"+source, source, title, text, img, "", link, at))
	})
	return out, nil
}

func nodeText(n *html.Node) string {
	var parts []string
	pikabuWalk(n, func(x *html.Node) {
		if x.Type == html.TextNode {
			parts = append(parts, x.Data)
		}
	})
	return strings.Join(parts, " ")
}

func (u *U) discoveryFindPikabu(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_pikabu") {
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		r := u.RSSGet("https://pikabu.ru/search?q="+url.QueryEscape(q), 4<<20, 15*time.Second, BrowserUA)
		if !r.OK {
			u.Log("-> Пикабу «%s»: поиск недоступен.", q)
			continue
		}
		found, err := pikabuSearchCommunities(r.Bytes)
		if err != nil {
			u.Log("-> Пикабу «%s»: %s", q, err)
			continue
		}
		for _, c := range found {
			c.Origin, c.OriginKind = fmt.Sprintf("поиск сообществ Пикабу «%s»", q), "other"
			out = append(out, c)
		}
		u.Log("-> Пикабу «%s»: сообществ найдено %d.", q, len(found))
	}
	return out
}
