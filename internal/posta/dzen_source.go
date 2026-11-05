package posta

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"posta/internal/jx"
)

var dzenChannelPath = regexp.MustCompile(`^/id/([0-9a-fA-F]{24})/?$`)
var dzenAge = regexp.MustCompile(`(?i)^(\d+)\s+(минут[аы]?|час(?:а|ов)?|д(?:ень|ня|ней)|недел(?:я|и|ь))\s+назад$`)

func dzenChannelID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "dzen.ru" || u.User != nil || u.Port() != "" {
		return ""
	}
	if m := dzenChannelPath.FindStringSubmatch(u.Path); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

func dzenPostTime(raw string, at time.Time) int64 {
	if t := ParseFeedTime(raw); t > 0 {
		return t
	}
	m := dzenAge.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	unit := time.Minute
	ageUnit := strings.ToLower(m[2])
	switch {
	case strings.HasPrefix(ageUnit, "час"):
		unit = time.Hour
	case strings.HasPrefix(ageUnit, "д"):
		unit = 24 * time.Hour
	case strings.HasPrefix(ageUnit, "недел"):
		unit = 7 * 24 * time.Hour
	}
	if n > 365*24*60 {
		return 0
	}
	return at.Add(-time.Duration(n) * unit).Unix()
}

func dzenChannelPosts(data []byte, channel string, count int, withText bool, at time.Time) ([]jx.M, error) {
	var response struct {
		Items []struct {
			Publisher string `json:"publisher_id"`
			ID        string `json:"publication_id"`
			Title     string `json:"title"`
			Text      string `json:"text"`
			Link      string `json:"link"`
			Created   string `json:"creation_time"`
			Channel   string `json:"domain_title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, p := range response.Items {
		if p.Publisher != channel {
			continue
		}
		link, err := url.Parse(p.Link)
		if err != nil || link.Scheme != "https" || link.Hostname() != "dzen.ru" {
			continue
		}
		posted := dzenPostTime(p.Created, at)
		text := strings.TrimSpace(p.Title + "\n" + p.Text)
		if posted == 0 || !withText || runeLen(text) < 120 {
			continue
		}
		post := searchPost("Дзен", "dzen:"+channel, "https://dzen.ru/id/"+channel, p.Title, text, "", "", p.Link, posted)
		if p.Channel != "" {
			post["source"] = p.Channel
		}
		out = append(out, post)
		if len(out) >= count {
			break
		}
	}
	return out, nil
}

func dzenSearchCandidates(page string) []DiscoveryCandidate {
	z := html.NewTokenizer(strings.NewReader(page))
	seen := map[string]bool{}
	var out []DiscoveryCandidate
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
		for _, a := range tok.Attr {
			if a.Key != "href" {
				continue
			}
			id := dzenChannelID(a.Val)
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://dzen.ru/id/" + id})
			}
		}
	}
	return out
}

func (u *U) discoveryFindDzen(queries []string, topic string) []DiscoveryCandidate {
	if !u.TBool("search_dzen") {
		return nil
	}
	if !u.TopicsAcceptText([]string{topic}) {
		u.Log("-> Дзен: текстовые анонсы не подходят формату темы.")
		return nil
	}
	probe := u.RSSGet("https://dzen.ru/", 64<<10, 8*time.Second, BrowserUA)
	if !probe.OK && probe.Status == 0 {
		u.Log("-> Дзен: сайт недоступен, поиск каналов пропущен.")
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		page, err := u.braveSearchPage("site:dzen.ru/id/ " + q)
		if err != nil {
			u.Log("-> Дзен «%s»: %s", q, err)
			continue
		}
		found := dzenSearchCandidates(page)
		for _, c := range found {
			c.Origin, c.OriginKind = "веб-поиск Дзена «"+q+"»", "other"
			out = append(out, c)
		}
		u.Log("-> Дзен «%s»: каналов найдено %d.", q, len(found))
	}
	return out
}
