package posta

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"posta/internal/config"
	"posta/internal/jx"
)

var habrHubPath = regexp.MustCompile(`^/(?:ru|en)/hubs/([a-zA-Z0-9_-]+)/?(?:articles/?)?$`)

// discoveryNewsSites extracts publisher origins, never the news aggregator itself.
func discoveryNewsSites(data []byte) []string {
	doc := rssLoad(data)
	if !rssIsFeed(doc) {
		return nil
	}
	seen := map[string]bool{}
	var sites []string
	for _, item := range rssItems(doc) {
		raw := ""
		if source := item.kid("source"); source != nil {
			raw = source.A["url"]
		}
		if raw == "" {
			raw = rssItemLink(item)
		}
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil || parsed.Port() != "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if !strings.Contains(host, ".") || host == "bing.com" || strings.HasSuffix(host, ".bing.com") || host == "google.com" || strings.HasSuffix(host, ".google.com") {
			continue
		}
		origin := "https://" + host + "/"
		if !seen[origin] {
			seen[origin] = true
			sites = append(sites, origin)
		}
	}
	return sites
}

// Discover publishers through Bing News, then resolve each site's advertised feed.
func (u *U) discoveryFindNewsFeeds(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_news") {
		return nil
	}
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		feed := "https://www.bing.com/news/search?q=" + url.QueryEscape(q) + "&format=rss&setlang=ru"
		r := u.RSSGet(feed, rssMaxBytes, 15*time.Second, HTTPUA)
		if !r.OK {
			u.Log("-> Поиск сайтов «%s»: новостной поиск недоступен.", q)
			continue
		}
		for _, site := range discoveryNewsSites(r.Bytes) {
			if seen[site] {
				continue
			}
			if len(seen) >= 6 {
				return out
			}
			seen[site] = true
			feedURL, err := u.RSSDiscover(site)
			if err != nil {
				continue
			}
			out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: feedURL, Origin: "издание по запросу «" + q + "»", OriginKind: "other"})
		}
	}
	u.Log("-> Поиск сайтов: RSS-лент найдено %d, сайтов проверено %d.", len(out), len(seen))
	return out
}

func discoveryHabrHubs(page string, queries []string) []DiscoveryCandidate {
	z := html.NewTokenizer(strings.NewReader(page))
	var href string
	var title strings.Builder
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	for {
		t := z.Next()
		if t == html.ErrorToken {
			if z.Err() == io.EOF {
				break
			}
			return out
		}
		switch t {
		case html.StartTagToken:
			tok := z.Token()
			if tok.Data == "a" {
				href = ""
				title.Reset()
				for _, a := range tok.Attr {
					if a.Key == "href" {
						href = a.Val
					}
				}
			}
		case html.TextToken:
			if href != "" {
				title.Write(z.Text())
			}
		case html.EndTagToken:
			if z.Token().Data != "a" || href == "" {
				continue
			}
			parsed, err := url.Parse(href)
			if err != nil || (parsed.Host != "" && parsed.Hostname() != "habr.com") {
				href = ""
				continue
			}
			m := habrHubPath.FindStringSubmatch(parsed.Path)
			name := strings.TrimSpace(title.String())
			if m == nil || name == "" || seen[m[1]] {
				href = ""
				continue
			}
			match := false
			for _, q := range queries {
				if QueryTextHit(strings.ToLower(name), strings.ToLower(q)) {
					match = true
					break
				}
			}
			if match {
				seen[m[1]] = true
				out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://habr.com/ru/rss/hubs/" + m[1] + "/articles/all/?fl=ru", Info: jx.M{"title": name}, Origin: "хаб Хабра", OriginKind: "other"})
			}
			href = ""
		}
	}
	return out
}

func (u *U) discoveryFindHabr(queries []string, turn int) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	for i := 0; i < 2; i++ {
		page := (turn*2+i)%11 + 1
		r := u.RSSGet(fmt.Sprintf("https://habr.com/ru/hubs/page%d/", page), 3<<20, 15*time.Second, BrowserUA)
		if !r.OK {
			u.Log("-> Хабр: страница хабов %d не загрузилась.", page)
			continue
		}
		out = append(out, discoveryHabrHubs(string(r.Bytes), queries)...)
	}
	u.Log("-> Хабр: подходящих хабов найдено %d.", len(out))
	return out
}

// discoveryLemmyCommunities extracts stable community feeds from the search response.
func discoveryLemmyCommunities(data []byte) ([]DiscoveryCandidate, error) {
	var response struct {
		Communities []struct {
			Community struct {
				ActorID string `json:"actor_id"`
				Name    string `json:"name"`
				Title   string `json:"title"`
				Deleted bool   `json:"deleted"`
				Removed bool   `json:"removed"`
			} `json:"community"`
		} `json:"communities"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []DiscoveryCandidate
	for _, row := range response.Communities {
		c := row.Community
		if c.Deleted || c.Removed {
			continue
		}
		candidate := PostSearchAuthor(jx.M{"source_key": "lemmy:community", "source_author_url": c.ActorID})
		if candidate == nil {
			continue
		}
		candidate.Info = jx.M{"title": c.Title}
		out = append(out, *candidate)
	}
	return out, nil
}

func (u *U) discoveryFindLemmy(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_lemmy") {
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(3, len(queries))] {
		body, err := u.searchGetJSON("https://lemmy.world/api/v3/search?type_=Communities&limit=30&q=" + url.QueryEscape(q))
		if err != nil {
			u.Log("-> Lemmy «%s»: %s", q, err)
			continue
		}
		found, err := discoveryLemmyCommunities(body)
		if err != nil {
			u.Log("-> Lemmy «%s»: %s", q, err)
			continue
		}
		for _, c := range found {
			c.Origin, c.OriginKind = "поиск сообществ Lemmy «"+q+"»", "other"
			out = append(out, c)
		}
		u.Log("-> Поиск сообществ Lemmy «%s»: найдено %d.", q, len(found))
	}
	return out
}

var redditCommunityName = regexp.MustCompile(`^[A-Za-z0-9_]{3,21}$`)

var blueskyDID = regexp.MustCompile(`^did:plc:[a-z2-7]{24}$`)

func discoveryBlueskyActors(data []byte) ([]DiscoveryCandidate, error) {
	var response struct {
		Actors []struct {
			DID         string `json:"did"`
			Handle      string `json:"handle"`
			DisplayName string `json:"displayName"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []DiscoveryCandidate
	for _, actor := range response.Actors {
		if !blueskyDID.MatchString(actor.DID) {
			continue
		}
		name := strings.TrimSpace(actor.DisplayName)
		if name == "" {
			name = actor.Handle
		}
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://bsky.app/profile/" + actor.DID + "/rss", Info: jx.M{"title": name}})
	}
	return out, nil
}

func discoveryBlueskyPostAuthors(data []byte) ([]DiscoveryCandidate, error) {
	var response struct {
		Posts []struct {
			Author struct {
				DID         string `json:"did"`
				Handle      string `json:"handle"`
				DisplayName string `json:"displayName"`
			} `json:"author"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	for _, post := range response.Posts {
		a := post.Author
		if !blueskyDID.MatchString(a.DID) || seen[a.DID] {
			continue
		}
		seen[a.DID] = true
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			name = a.Handle
		}
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://bsky.app/profile/" + a.DID + "/rss", Info: jx.M{"title": name}})
	}
	return out, nil
}

func (u *U) discoveryFindBluesky(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_bluesky") {
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(3, len(queries))] {
		body, err := u.searchGetJSON("https://public.api.bsky.app/xrpc/app.bsky.actor.searchActors?limit=25&q=" + url.QueryEscape(q))
		if err == nil {
			var found []DiscoveryCandidate
			found, err = discoveryBlueskyActors(body)
			for _, c := range found {
				c.Origin, c.OriginKind = "поиск аккаунтов Bluesky «"+q+"»", "other"
				out = append(out, c)
			}
			u.Log("-> Поиск аккаунтов Bluesky «%s»: найдено %d.", q, len(found))
		}
		if err != nil {
			u.Log("-> Bluesky «%s»: %s", q, err)
		}
		body, err = u.searchGetJSON("https://public.api.bsky.app/xrpc/app.bsky.feed.searchPosts?limit=25&q=" + url.QueryEscape(q))
		if err != nil {
			u.Log("-> Посты Bluesky «%s»: %s", q, err)
			continue
		}
		authors, err := discoveryBlueskyPostAuthors(body)
		if err != nil {
			u.Log("-> Посты Bluesky «%s»: %s", q, err)
			continue
		}
		for _, c := range authors {
			c.Origin, c.OriginKind = "автор поста Bluesky «"+q+"»", "post_search"
			out = append(out, c)
		}
		u.Log("-> Поиск постов Bluesky «%s»: авторов %d.", q, len(authors))
	}
	return out
}

// Reddit's subreddit search returns communities; each public community has an RSS feed.
func discoveryRedditCommunities(data []byte) ([]DiscoveryCandidate, error) {
	var response struct {
		Data struct {
			Children []struct {
				Data struct {
					Name  string `json:"display_name"`
					Title string `json:"title"`
					Type  string `json:"subreddit_type"`
					NSFW  bool   `json:"over18"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []DiscoveryCandidate
	for _, row := range response.Data.Children {
		c := row.Data
		if !redditCommunityName.MatchString(c.Name) || c.NSFW || c.Type != "public" {
			continue
		}
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://www.reddit.com/r/" + strings.ToLower(c.Name) + "/.rss", Info: jx.M{"title": c.Title}})
	}
	return out, nil
}

func (u *U) discoveryFindReddit(queries []string) []DiscoveryCandidate {
	if !u.TBool("search_reddit") {
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(3, len(queries))] {
		var body []byte
		var err error
		if u.NetworkAppKey("REDDIT_CLIENT_ID") != "" && u.NetworkAppKey("REDDIT_CLIENT_SECRET") != "" {
			body, err = u.redditRead("/subreddits/search?limit=25&q=" + url.QueryEscape(q))
		} else {
			body, err = u.searchGetJSON("https://www.reddit.com/subreddits/search.json?limit=25&q=" + url.QueryEscape(q))
		}
		if err != nil {
			u.Log("-> Reddit «%s»: %s", q, err)
			continue
		}
		found, err := discoveryRedditCommunities(body)
		if err != nil {
			u.Log("-> Reddit «%s»: %s", q, err)
			continue
		}
		for _, c := range found {
			c.Origin, c.OriginKind = "поиск сообществ Reddit «"+q+"»", "other"
			out = append(out, c)
		}
		u.Log("-> Поиск сообществ Reddit «%s»: найдено %d.", q, len(found))
	}
	return out
}

// Find JoyReactor authors from posts and resolve their advertised RSS feeds.
func (u *U) discoveryFindJoyAuthors(queries []string, topic string) []DiscoveryCandidate {
	if !u.TBool("search_joyreactor") {
		return nil
	}
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	for _, c := range out {
		seen[strings.ToLower(c.ID)] = true
	}
	for _, q := range queries[:min(2, len(queries))] {
		posts, _, err := u.SearchJoyreactor(q, 1)
		if err != nil {
			u.Log("-> JoyReactor «%s»: %s", q, err)
			continue
		}
		for _, p := range posts {
			if len(seen) >= 5 {
				return out
			}
			if !u.PostSearchMatches(topic, p) {
				continue
			}
			profile := jx.Str(p, "source_author_url")
			if profile == "" || seen[profile] {
				continue
			}
			seen[profile] = true
			feed, err := u.RSSDiscover(profile)
			if err != nil {
				continue
			}
			parsed, err := url.Parse(feed)
			if err != nil || parsed.Scheme != "https" || (parsed.Hostname() != "joyreactor.cc" && parsed.Hostname() != "reactor.cc") {
				continue
			}
			out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: feed, Origin: "автор JoyReactor «" + q + "»", OriginKind: "post_search"})
		}
	}
	return out
}

const (
	discoveryYouTubeMax     = 12
	discoveryYouTubeSeconds = 90
	discoveryOKMax          = 15
	discoveryLinksMax       = 30
)

var okDiscoveryFeeds = []string{"hobby", "collection"}

func DiscoveryLabel(c DiscoveryCandidate) string {
	switch c.Platform {
	case PlatformTelegram:
		return "@" + c.ID
	case PlatformVK:
		return "vk.com/" + c.ID
	case PlatformTikTok:
		return "tiktok @" + c.ID
	case PlatformOK:
		return "ok:" + c.ID
	}
	return strings.ToLower(RSSFeedKind(c.ID)) + " " + hostOf(c.ID)
}

var (
	lsTelegramRe = regexp.MustCompile(`(?i)(?:^|[^\w.])(?:https?://)?(?:t|telegram)\.me/(?:s/)?([A-Za-z][A-Za-z0-9_]{4,31})([/?"'<\s#]|$)`)
	lsVKRe       = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.|m\.)?vk\.com/([A-Za-z0-9_.-]{2,64})([/?"'<\s#]|$)`)
	lsVKBadRe    = regexp.MustCompile(`(?i)^(id\d+|wall|video|photo|feed|im|away|app|docs|share|login)$`)
	lsTikTokRe   = regexp.MustCompile(`(?i)tiktok\.com/@([A-Za-z0-9_.]{2,24})`)
	lsOKRe       = regexp.MustCompile(`ok\.ru/group/(\d{10,20})`)
	lsYTHandleRe = regexp.MustCompile(`(?i)youtube\.com/@([A-Za-z0-9_.-]{3,30})`)
	lsYTChanRe   = regexp.MustCompile(`youtube\.com/channel/(UC[A-Za-z0-9_-]{22})`)
	lsPinRe      = regexp.MustCompile(`(?i)pinterest\.(?:com|ru)/([A-Za-z0-9_.-]{2,64})/`)
	lsTumblrRe   = regexp.MustCompile(`(?i)(?:^|[^\w-])([a-z0-9-]{1,32})\.tumblr\.com`)
	lsRedditRe   = regexp.MustCompile(`(?i)(?:^|[^\w.])(?:www\.|old\.)?reddit\.com/r/([a-z0-9_]{3,21})(?:[/?:#"'<\s]|$)`)
	lsBlueskyRe  = regexp.MustCompile(`(?i)(?:^|[^\w.])bsky\.app/profile/(did:plc:[a-z2-7]{24}|[a-z0-9.-]{3,253})(?:[/?:#"'<\s]|$)`)
	lsPinBoardRe = regexp.MustCompile(`(?i)(?:^|[^\w.])(?:www\.)?pinterest\.(?:com|ru)/([a-z0-9_.-]{2,64})/([a-z0-9_.-]{1,100})(?:[/?:#"'<\s]|$)`)
	lsRutubeRe   = regexp.MustCompile(`(?i)(?:^|[^\w.])rutube\.ru/(?:channel|video/person)/(\d{1,20})(?:[/?:#"'<\s]|$)`)
	lsPikabuRe   = regexp.MustCompile(`(?i)(?:^|[^\w.])pikabu\.ru/community/([a-z0-9_]{2,80})(?:[/?:#"'<\s]|$)`)
	lsXRe        = regexp.MustCompile(`(?i)(?:^|[^\w.])(?:x\.com|twitter\.com)/([a-z0-9_]{1,15})(?:[/?:#"'<\s]|$)`)
)

// SourceLinkedSources — ссылки на площадки в тексте или HTML (только распознавание, сети нет).
func SourceLinkedSources(text string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	add := func(p, id string) {
		sid := SourceIDFor(p, id)
		if len(out) < discoveryLinksMax && !seen[sid] {
			seen[sid] = true
			out = append(out, DiscoveryCandidate{Platform: p, ID: id})
		}
	}
	for _, m := range lsTelegramRe.FindAllStringSubmatch(text, -1) {
		h := m[1]
		if IsValidTelegramChannel(h) && !contains(telegramReservedPaths, strings.ToLower(h)) && !strings.HasSuffix(strings.ToLower(h), "bot") {
			add(PlatformTelegram, h)
		}
	}
	for _, m := range lsVKRe.FindAllStringSubmatch(text, -1) {
		if IsValidVKDomain(m[1]) && !lsVKBadRe.MatchString(m[1]) {
			add(PlatformVK, m[1])
		}
	}
	for _, m := range lsTikTokRe.FindAllStringSubmatch(text, -1) {
		if IsValidTikTokUser(m[1]) {
			add(PlatformTikTok, m[1])
		}
	}
	for _, m := range lsOKRe.FindAllStringSubmatch(text, -1) {
		add(PlatformOK, m[1])
	}
	for _, m := range lsYTHandleRe.FindAllStringSubmatch(text, -1) {
		add(PlatformRSS, "https://www.youtube.com/@"+m[1])
	}
	for _, m := range lsYTChanRe.FindAllStringSubmatch(text, -1) {
		add(PlatformRSS, YouTubeChannelFeedURL(m[1]))
	}
	for _, m := range lsPinRe.FindAllStringSubmatch(text, -1) {
		if !contains(pinterestReserved, strings.ToLower(m[1])) {
			add(PlatformRSS, "https://www.pinterest.com/"+m[1]+"/")
		}
	}
	for _, m := range lsTumblrRe.FindAllStringSubmatch(text, -1) {
		if strings.ToLower(m[1]) != "www" {
			add(PlatformRSS, "https://"+strings.ToLower(m[1])+".tumblr.com/")
		}
	}
	for _, m := range lsRedditRe.FindAllStringSubmatch(text, -1) {
		add(PlatformRSS, "https://www.reddit.com/r/"+strings.ToLower(m[1])+"/.rss")
	}
	for _, m := range lsRutubeRe.FindAllStringSubmatch(text, -1) {
		if m[1] != "0" {
			add(PlatformRSS, "https://rutube.ru/channel/"+m[1]+"/")
		}
	}
	for _, m := range lsPikabuRe.FindAllStringSubmatch(text, -1) {
		add(PlatformRSS, "https://pikabu.ru/community/"+m[1])
	}
	for _, m := range lsXRe.FindAllStringSubmatch(text, -1) {
		if user := xProfileName("https://x.com/" + m[1]); user != "" {
			add(PlatformRSS, "https://x.com/"+user)
		}
	}
	for _, m := range lsBlueskyRe.FindAllStringSubmatch(text, -1) {
		add(PlatformRSS, "https://bsky.app/profile/"+strings.ToLower(m[1])+"/rss")
	}
	for _, m := range lsPinBoardRe.FindAllStringSubmatch(text, -1) {
		if !contains(pinterestReserved, strings.ToLower(m[1])) && !contains(pinterestReserved, strings.ToLower(m[2])) {
			add(PlatformRSS, "https://www.pinterest.com/"+m[1]+"/"+m[2]+".rss")
		}
	}
	return out
}

func discoveryPinterestBoards(page string) []DiscoveryCandidate {
	z := html.NewTokenizer(strings.NewReader(page))
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	for {
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
			parsed, err := url.Parse(a.Val)
			if err != nil || parsed.User != nil || (parsed.Host != "" && !strings.EqualFold(parsed.Hostname(), "www.pinterest.com") && !strings.EqualFold(parsed.Hostname(), "pinterest.com")) {
				continue
			}
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) != 2 || !pinUserRe.MatchString(parts[0]) || !pinBoardRe.MatchString(parts[1]) || contains(pinterestReserved, strings.ToLower(parts[0])) || contains(pinterestReserved, strings.ToLower(parts[1])) {
				continue
			}
			id := "https://www.pinterest.com/" + parts[0] + "/" + parts[1] + ".rss"
			if !seen[id] {
				seen[id] = true
				out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: id})
			}
		}
	}
	return out
}

// discoveryFindPinterest uses public board search when Pinterest includes board links in HTML.
func (u *U) discoveryFindPinterest(queries []string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		page := "https://www.pinterest.com/search/boards/?q=" + url.QueryEscape(q)
		r := u.RSSGet(page, 3<<20, 15*time.Second, BrowserUA)
		if !r.OK {
			u.Log("-> Pinterest «%s»: поиск недоступен.", q)
			continue
		}
		count := 0
		for _, c := range discoveryPinterestBoards(string(r.Bytes)) {
			c.Origin, c.OriginKind = "поиск досок Pinterest «"+q+"»", "other"
			out = append(out, c)
			count++
		}
		u.Log("-> Поиск досок Pinterest «%s»: найдено %d.", q, count)
	}
	return out
}

var foreignStrong = []string{"terbaru", "yang", "untuk", "nie", "jest", "smieszne", "filmy", "nowe", "sono", "und", "che"}
var foreignWeak = []string{"dan", "del", "la", "il", "di", "el", "los", "las", "por", "para", "der"}

// DiscoveryLanguageReject — латиница с явно индонезийскими, польскими, итальянскими, испанскими, немецкими словами.
func DiscoveryLanguageReject(text string) bool {
	t := strings.ToLower(text)
	if cyrRe.MatchString(t) {
		return false
	}
	weak := 0
	for _, w := range nonLetterRe.Split(t, -1) {
		if contains(foreignStrong, w) {
			return true
		}
		if contains(foreignWeak, w) {
			weak++
		}
	}
	return weak >= 2
}

func (u *U) discoveryImageBytes(raw string) []byte {
	if p := u.MediaPath(raw); p != "" {
		b, _ := os.ReadFile(p)
		return b
	}
	b, err := u.FetchPublicImage(raw)
	if err != nil {
		return nil
	}
	return b
}

// DiscoveryBalance — каждой группе первые minN, потом по кругу до maxN.
func DiscoveryBalance(groups [][]jx.M, maxN, minN int) []jx.M {
	var out []jx.M
	rest := make([][]jx.M, len(groups))
	for i, l := range groups {
		take := l[:min(minN, len(l))]
		out = append(out, take...)
		rest[i] = l[len(take):]
	}
	if len(out) > maxN {
		out = out[:maxN]
	}
	for len(out) < maxN {
		moved := false
		for i := range rest {
			if len(rest[i]) == 0 || len(out) >= maxN {
				continue
			}
			out = append(out, rest[i][0])
			rest[i] = rest[i][1:]
			moved = true
		}
		if !moved {
			break
		}
	}
	return out
}

var discoveryGroupOrder = []string{"telegram", "youtube", "tiktok", "ok", "vk", "links"}
var discoveryGroupNames = map[string]string{"telegram": "Telegram", "youtube": "YouTube", "tiktok": "TikTok", "ok": "Одноклассники", "vk": "ВКонтакте", "links": "Ссылки"}

func DiscoveryGroupOf(platform, id string) string {
	switch {
	case platform == PlatformTelegram:
		return "telegram"
	case platform == PlatformTikTok:
		return "tiktok"
	case platform == PlatformOK:
		return "ok"
	case platform == PlatformVK:
		return "vk"
	case strings.Contains(id, "youtube.com/"):
		return "youtube"
	}
	return "links"
}

type discoveryQueue struct {
	groups map[string][]DiscoveryCandidate
	turn   int
}

func newDiscoveryQueue() *discoveryQueue {
	return &discoveryQueue{groups: map[string][]DiscoveryCandidate{}}
}

func (q *discoveryQueue) add(group string, c DiscoveryCandidate, front bool) {
	if front {
		q.groups[group] = append([]DiscoveryCandidate{c}, q.groups[group]...)
	} else {
		q.groups[group] = append(q.groups[group], c)
	}
}

// kindCounts — кандидатов в очереди по видам находки.
func (q *discoveryQueue) kindCounts() map[string]int {
	out := map[string]int{}
	for _, l := range q.groups {
		for _, c := range l {
			out[c.OriginKind]++
		}
	}
	return out
}

func (q *discoveryQueue) counts() map[string]int {
	out := map[string]int{}
	for g, l := range q.groups {
		out[g] = len(l)
	}
	return out
}

// next — следующий кандидат по кругу площадок; в группе — отложенные, затем не «хвост», затем приоритет, затем глубина.
func (q *discoveryQueue) next() *DiscoveryCandidate {
	n := len(discoveryGroupOrder)
	for i := 0; i < n; i++ {
		g := discoveryGroupOrder[(q.turn+i)%n]
		list := q.groups[g]
		if len(list) == 0 {
			continue
		}
		q.turn = (q.turn + i + 1) % n
		rank := func(c DiscoveryCandidate) [4]int {
			d := 10
			if c.Deferred {
				d = 0
			}
			return [4]int{d, c.Tail, -c.Priority, c.Depth}
		}
		best := 0
		for j := range list {
			if less4(rank(list[j]), rank(list[best])) {
				best = j
			}
		}
		c := list[best]
		q.groups[g] = append(list[:best], list[best+1:]...)
		return &c
	}
	return nil
}

func less4(a, b [4]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// prioritize — слово темы в названии, повторы, найден по постам, свежий ролик — выше; латиница без слов темы — в хвост группы.
func (q *discoveryQueue) prioritize(words []string, refs map[string][]string) {
	for g, list := range q.groups {
		for i := range list {
			c := &list[i]
			name := strings.ToLower(c.ID + " " + jx.Str(c.Info, "title"))
			hit := false
			for _, w := range words {
				if strings.Contains(name, w) {
					hit = true
					break
				}
			}
			p := 0
			if hit {
				p += 2
			}
			if len(refs[SourceIDFor(c.Platform, c.ID)]) >= 1 {
				p += 2
			}
			if c.OriginKind == "post_search" || c.OriginKind == "tg_posts" {
				p++
			}
			if jx.Bool(c.Info, "fresh") {
				p += 3
			}
			c.Priority = p
			c.Tail = 0
			if !hit && !cyrRe.MatchString(name) {
				c.Tail = 1
			}
		}
		q.groups[g] = list
	}
}

// DiscoveryRotate — элементы с позиции offset по кругу.
func DiscoveryRotate(items []string, offset int) []string {
	if len(items) == 0 {
		return nil
	}
	offset = ((offset % len(items)) + len(items)) % len(items)
	return append(append([]string{}, items[offset:]...), items[:offset]...)
}

func (u *U) discoverySummaryLines(report map[string]map[string]int, reasons map[string]string) []string {
	var lines []string
	for _, g := range discoveryGroupOrder {
		r := report[g]
		if r == nil {
			r = map[string]int{}
		}
		line := fmt.Sprintf("%s: найдено %d, проверено %d, принято %d, отказ %d, не загрузилось %d, известных пропущено %d",
			discoveryGroupNames[g], r["found"], r["checked"], r["accepted"], r["rejected"], r["failed"], r["known"])
		if u.run != nil && len(u.run.DiscoveryCheap[g]) > 0 {
			var parts []string
			for k, v := range u.run.DiscoveryCheap[g] {
				parts = append(parts, fmt.Sprintf("%d %s", v, k))
			}
			sort.Strings(parts)
			line += ", без модели: " + strings.Join(parts, ", ")
		}
		if why, ok := reasons[g]; ok {
			line += " (" + why + ")"
		}
		lines = append(lines, line)
	}
	names := map[string]string{"post_search": "Поиск постов", "pinterest": "Pinterest"}
	for k, t := range reasons {
		if !contains(discoveryGroupOrder, k) {
			lines = append(lines, names[k]+": "+t)
		}
	}
	return lines
}

// discoveryFindPosts — авторы постов по запросам плана: автор с ≥2 постами — в начало группы.
func (u *U) discoveryFindPosts(queries []string, topic string) []DiscoveryCandidate {
	type acc struct {
		c DiscoveryCandidate
		n int
	}
	authors := map[string]*acc{}
	var order []string
	take := func(posts []jx.M) {
		for _, p := range posts {
			if PostSearchIsAd(p) || !u.PostSearchMatches(topic, p) {
				continue
			}
			a := PostSearchAuthor(p)
			if a == nil {
				continue
			}
			key := a.Platform + "|" + a.ID
			if authors[key] == nil {
				a.Origin, a.OriginKind = "нашёлся по постам «"+jx.Str(p, "found_by")+"»", "post_search"
				authors[key] = &acc{*a, 0}
				order = append(order, key)
			}
			authors[key].n++
		}
	}
	for i, q := range queries[:min(len(queries), u.TInt("post_search_queries"))] {
		take(u.PostSearchYouTube(q, PostSearchLimit))
		take(u.PostSearchTelegram(q, PostSearchLimit))
		take(u.PostSearchVK(q, PostSearchLimit))
		if i < 3 && u.TBool("search_vc") {
			take(u.queryOsnovaSearch(q))
		}
		if u.TBool("search_lemmy") {
			posts, _, err := u.SearchLemmy(q, 1)
			if err == nil {
				take(posts)
			}
		}
		if i < 3 && u.TBool("search_rutube") {
			posts, _, err := u.SearchRutube(q, 1)
			if err == nil {
				take(posts)
			}
		}
		if i < 2 && u.TBool("search_mastodon") {
			posts, _, err := u.SearchMastodon(q, 1)
			if err == nil {
				take(posts)
			}
		}
	}
	if len(queries) > 0 {
		take(u.PostSearchTikTok(queries[0], PostSearchLimit))
	}
	var out []DiscoveryCandidate
	front := 0
	for _, k := range order {
		a := authors[k]
		a.c.Front = a.n >= 2
		if a.c.Front {
			front++
		}
		out = append(out, a.c)
	}
	u.Log("-> Поиск постов по запросам плана: авторов %d (с двумя и больше постами: %d).", len(out), front)
	return out
}

var tumblrReserved = []string{"www", "media", "assets", "static", "srvcs", "secure", "cdn", "col", "buttons", "api", "px", "vt", "staff", "new", "tumblr"}

func (u *U) discoveryFindTumblr(queries []string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		r := u.RSSGet("https://www.tumblr.com/tagged/"+url.PathEscape(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(q)), " ", "-")), 3<<20, 15*time.Second, BrowserUA)
		if !r.OK {
			u.Log("-> Tumblr «%s»: страница тега не ответила.", q)
			continue
		}
		var blogs []string
		for _, m := range lsTumblrRe.FindAllStringSubmatch(string(r.Bytes), -1) {
			if b := strings.ToLower(m[1]); !contains(tumblrReserved, b) {
				blogs = append(blogs, b)
			}
		}
		blogs = uniqueStrings(blogs)
		u.Log("-> Поиск Tumblr «%s»: блогов: %d.", q, len(blogs))
		for _, b := range blogs[:min(5, len(blogs))] {
			out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://" + b + ".tumblr.com/", Origin: "поиск Tumblr «" + q + "»", OriginKind: "other"})
		}
	}
	return out
}

// discoveryFindTelegram — поиск каналов через аккаунт и «похожие» на лучшие каналы темы.
func (u *U) discoveryFindTelegram(plan discoveryPlan, seeds []string) []DiscoveryCandidate {
	if !u.TelegramUserConnected() {
		u.Log("-> Аккаунт Telegram не подключён — поиск каналов внутри Telegram пропущен (подключается в «Личном кабинете»). Проверяю только адреса и ссылки.")
		return nil
	}
	searched := u.discoveryTelegramAccountChannels(plan.Queries, nil)
	u.Log("-> Поиск Telegram через аккаунт: найдено каналов: %d.", len(searched))
	byOrigin := map[string][]TGChannel{}
	var origins []string
	for _, ch := range searched {
		if byOrigin[ch.Origin] == nil {
			origins = append(origins, ch.Origin)
		}
		byOrigin[ch.Origin] = append(byOrigin[ch.Origin], ch)
	}
	var out []DiscoveryCandidate
	for i := 0; ; i++ {
		moved := false
		for _, o := range origins {
			if i < len(byOrigin[o]) {
				ch := byOrigin[o][i]
				out = append(out, DiscoveryCandidate{Platform: PlatformTelegram, ID: ch.Username, Origin: "поиск Telegram «" + o + "»", OriginKind: "tg_search"})
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	similar := seeds[:min(len(seeds), config.DiscoverySimilarSeeds)]
	if len(similar) > 0 {
		for _, ch := range u.discoveryTelegramAccountChannels(nil, similar) {
			out = append(out, DiscoveryCandidate{Platform: PlatformTelegram, ID: ch.Username, Origin: "похож на @" + ch.Origin, OriginKind: "similar"})
		}
	}
	return out
}

// Only direct VK profile/community links are accepted from public search results.
var vkWebPostPath = regexp.MustCompile(`(?i)^(?:wall|photo|video|topic)-?\d`)

func vkWebSearchCandidates(page string) []DiscoveryCandidate {
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	z := html.NewTokenizer(strings.NewReader(page))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		if token.Data != "a" {
			continue
		}
		for _, a := range token.Attr {
			if a.Key != "href" {
				continue
			}
			p, err := url.Parse(a.Val)
			if err != nil || p.User != nil || p.Scheme != "https" || (p.Hostname() != "vk.com" && p.Hostname() != "www.vk.com" && p.Hostname() != "m.vk.com") {
				continue
			}
			id := strings.Trim(p.Path, "/")
			if strings.Contains(id, "/") || !IsValidVKDomain(id) || lsVKBadRe.MatchString(id) || vkWebPostPath.MatchString(id) {
				continue
			}
			key := strings.ToLower(id)
			if !seen[key] {
				seen[key] = true
				out = append(out, DiscoveryCandidate{Platform: PlatformVK, ID: id})
			}
		}
	}
	return out
}

var vkDirectoryCategoryPath = regexp.MustCompile(`^/vk/([a-z0-9-]+)/$`)

func vkDirectoryCategories(page string, queries []string) []string {
	z := html.NewTokenizer(strings.NewReader(page))
	var href string
	var label strings.Builder
	seen := map[string]bool{}
	var out []string
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken:
			tok := z.Token()
			if tok.Data == "a" {
				href = ""
				label.Reset()
				for _, a := range tok.Attr {
					if a.Key == "href" {
						href = a.Val
					}
				}
			}
		case html.TextToken:
			if href != "" {
				label.Write(z.Text())
			}
		case html.EndTagToken:
			if z.Token().Data != "a" || href == "" {
				continue
			}
			m := vkDirectoryCategoryPath.FindStringSubmatch(href)
			name := strings.ToLower(label.String())
			if m != nil {
				for _, q := range queries {
					for _, word := range strings.Fields(strings.ToLower(q)) {
						if len([]rune(word)) >= 4 && strings.Contains(name, word) && !seen[href] {
							seen[href] = true
							out = append(out, href)
						}
					}
				}
			}
			href = ""
		}
	}
	return out
}

// Extract only catalog result rows; page-wide navigation and recommendations
// can contain communities from unrelated categories.
func vkDirectoryResults(page string) []DiscoveryCandidate {
	root, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "li" && strings.Contains(" "+publicAttr(n, "class")+" ", " row ") {
			var content strings.Builder
			var collect func(*html.Node)
			collect = func(x *html.Node) {
				if x.Type == html.TextNode {
					content.WriteString(x.Data)
					content.WriteByte(' ')
				}
				for c := x.FirstChild; c != nil; c = c.NextSibling {
					collect(c)
				}
			}
			collect(n)
			for _, c := range SourceLinkedSources(content.String()) {
				if c.Platform == PlatformVK && !seen[strings.ToLower(c.ID)] {
					seen[strings.ToLower(c.ID)] = true
					out = append(out, c)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func (u *U) discoveryFindVKDirectory(queries []string) []DiscoveryCandidate {
	base := "https://ugcplatform.ru"
	r := u.RSSGet(base+"/vk/", 3<<20, 15*time.Second, BrowserUA)
	if !r.OK {
		u.Log("-> Каталог VK недоступен.")
		return nil
	}
	paths := vkDirectoryCategories(string(r.Bytes), queries)
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	for _, path := range paths[:min(3, len(paths))] {
		p := u.RSSGet(base+path, 3<<20, 15*time.Second, BrowserUA)
		if !p.OK {
			continue
		}
		for _, c := range vkDirectoryResults(string(p.Bytes)) {
			if c.Platform != PlatformVK || seen[strings.ToLower(c.ID)] {
				continue
			}
			seen[strings.ToLower(c.ID)] = true
			c.Origin = "тематический каталог VK"
			c.OriginKind = "vk_search"
			out = append(out, c)
		}
	}
	u.Log("-> Тематический каталог VK: разделов %d, кандидатов %d.", min(3, len(paths)), len(out))
	return out
}

func (u *U) discoveryFindVKWeb(queries []string) []DiscoveryCandidate {
	out := u.discoveryFindVKDirectory(queries)
	seen := map[string]bool{}
	for _, q := range queries[:min(2, len(queries))] {
		page, err := u.braveSearchPage("site:vk.com " + q + " сообщество")
		if err != nil {
			u.Log("-> Веб-поиск сообществ VK «%s»: %s", q, err)
			break
		}
		found := vkWebSearchCandidates(page)
		for _, c := range found {
			key := strings.ToLower(c.ID)
			if !seen[key] {
				seen[key] = true
				c.Origin = "веб-поиск VK «" + q + "»"
				c.OriginKind = "vk_search"
				out = append(out, c)
			}
		}
		u.Log("-> Веб-поиск сообществ VK «%s»: кандидатов %d.", q, len(found))
	}
	return out
}

func (u *U) discoveryFindVK(queries []string) []DiscoveryCandidate {
	if !u.VKIsConnected(u.Ctx) {
		return u.discoveryFindVKWeb(queries)
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(8, len(queries))] {
		found, err := u.VKSearchGroups(u.Ctx, q, 10)
		if err != nil {
			var apiErr *VKAPIError
			if errors.As(err, &apiErr) && apiErr.Code == 1051 {
				u.Log("-> VK ID не разрешает поиск сообществ для этого профиля; пробую веб-поиск.")
				return append(out, u.discoveryFindVKWeb(queries)...)
			}
			u.Log("-> Поиск VK «%s»: %s", q, err)
			continue
		}
		u.Log("-> Поиск VK «%s»: найдено сообществ: %d.", q, len(found))
		for _, g := range found[:min(4, len(found))] {
			out = append(out, DiscoveryCandidate{Platform: PlatformVK, ID: jx.Str(g, "id"), Origin: "поиск VK «" + q + "»", OriginKind: "vk_search", Info: g})
		}
	}
	return out
}

func (u *U) discoveryYouTubeRows(target string) [][2]string {
	r := u.YtdlpRun(u.Ctx, []string{"--flat-playlist", "--playlist-end", "10", "--print", "%(channel_id)s|%(channel)s", "--", target}, ytdlpListTimeout, true, nil)
	if !r.OK && strings.TrimSpace(r.Stdout) == "" {
		u.Log("-> YouTube не отдал поиск: %s", YtdlpErrorText(r))
		return nil
	}
	var rows [][2]string
	for _, line := range strings.Split(r.Stdout, "\n") {
		p := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(p) == 2 && ytChannelIDRe.MatchString(p[0]) {
			rows = append(rows, [2]string{p[0], trimRunes(strings.TrimSpace(p[1]), 120)})
		}
	}
	return rows
}

func (u *U) discoveryFindYouTube(queries []string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	seen := map[string]bool{}
	until := time.Now().Add(discoveryYouTubeSeconds * time.Second)
	for _, q := range queries[:min(3, len(queries))] {
		if time.Now().After(until) {
			return out
		}
		freshRows := u.discoveryYouTubeRows("https://www.youtube.com/results?search_query=" + url.QueryEscape(q) + "&sp=EgIIAw%3D%3D")
		fresh := map[string]bool{}
		for _, r := range freshRows {
			fresh[r[0]] = true
		}
		rows := append(append(freshRows, u.discoveryYouTubeRows("https://www.youtube.com/results?search_query="+url.QueryEscape(q)+"&sp=EgIQAg%3D%3D")...),
			u.discoveryYouTubeRows("ytsearch10:"+q+" shorts")...)
		u.Log("-> Поиск YouTube «%s»: авторов свежих роликов: %d, каналов в ответе: %d.", q, len(fresh), len(rows))
		for _, r := range rows {
			if time.Now().After(until) {
				u.Log("-> YouTube: время поиска (%d с) вышло.", discoveryYouTubeSeconds)
				return out
			}
			if seen[r[0]] || len(out) >= discoveryYouTubeMax {
				continue
			}
			seen[r[0]] = true
			info := jx.M{"title": r[1]}
			if fresh[r[0]] {
				info["fresh"] = true
			}
			out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: YouTubeChannelFeedURL(r[0]), Origin: "поиск YouTube «" + q + "»", OriginKind: "youtube", Info: info})
		}
	}
	return out
}

func (u *U) discoveryFindTikTok(queries []string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		users := u.TikTokSearchVideos(q)
		u.Log("-> Поиск TikTok по видео «%s»: авторов: %d.", q, len(users))
		for _, us := range users {
			out = append(out, DiscoveryCandidate{Platform: PlatformTikTok, ID: us, Origin: "авторы видео TikTok «" + q + "»", OriginKind: "tiktok"})
		}
	}
	return out
}

// discoveryTikTokQueries — короткие разговорные запросы: слова темы, потом короткие запросы плана.
func (u *U) discoveryTikTokQueries(queries []string, topic string) []string {
	pool := append(append([]string{}, u.DiscoveryTopicKeywords(topic)...), queries...)
	var out []string
	for _, q := range pool {
		q = strings.ToLower(strings.TrimSpace(q))
		if q == "" || runeLen(q) > 24 || len(strings.Fields(q)) > 2 || strings.Contains(q, "видео") || contains(out, q) {
			continue
		}
		out = append(out, q)
	}
	return out[:min(2, len(out))]
}

var okGroupLinkRe = regexp.MustCompile(`ok\.ru/group/(\d{10,20})`)
var okWordSplitRe = regexp.MustCompile(`[\s,;:!?.()/|«»"]+`)

// discoveryFindOK — группы-авторы из общих лент разделов ОК, где слово темы встречается в двух и больше записях.
func (u *U) discoveryFindOK(queries []string, topic string) []DiscoveryCandidate {
	var words []string
	for _, w := range okWordSplitRe.Split(strings.Join(append([]string{topic}, queries...), " "), -1) {
		w = strings.ToLower(w)
		if runeLen(w) >= 3 {
			words = append(words, trimRunes(w, max(4, runeLen(w)-2)))
		}
	}
	words = uniqueStrings(words)
	hits := map[string]int{}
	origin := map[string]string{}
	var order []string
	for _, name := range okDiscoveryFeeds {
		feed := "https://ok.ru/atom-feed/" + name
		r := u.RSSGet(feed, 3<<20, 15*time.Second, BrowserUA)
		if !r.OK {
			u.Log("-> ОК: лента «%s» не отвечает.", name)
			continue
		}
		posts, err := RSSParse(r.Bytes, 100, PlatformOK, feed, true, false, 120)
		if err != nil {
			continue
		}
		for _, p := range posts {
			m := okGroupLinkRe.FindStringSubmatch(jx.Str(p, "url"))
			if m == nil {
				continue
			}
			text := strings.ToLower(jx.Str(p, "text"))
			hit := false
			for _, w := range words {
				if strings.Contains(text, w) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			if hits[m[1]] == 0 {
				order = append(order, m[1])
				origin[m[1]] = "лента ОК «" + name + "»"
			}
			hits[m[1]]++
		}
	}
	var out []DiscoveryCandidate
	for _, g := range order {
		if hits[g] >= 2 && len(out) < discoveryOKMax {
			out = append(out, DiscoveryCandidate{Platform: PlatformOK, ID: g, Origin: origin[g], OriginKind: "ok"})
		}
	}
	u.Log("-> Одноклассники: групп по теме в лентах разделов (слово темы в двух записях и больше): %d.", len(out))
	return out
}

// discoveryProbe — сведения о кандидате: название, описание, посты, подписчики, даты, просмотры, ссылки.
func (u *U) discoveryProbe(c DiscoveryCandidate, topic string) (jx.M, error) {
	base := jx.M{"id": c.ID, "title": "", "description": "", "subscribers": int64(0), "posts": []jx.M{}, "post_times": []any{}, "post_views": []any{}, "linked": []DiscoveryCandidate{}}
	merge := func(m jx.M) jx.M {
		out := jx.Copy(base)
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	times := func(posts []jx.M) []any {
		var t []any
		for _, p := range posts {
			t = append(t, jx.Int(p, "posted_at"))
		}
		return t
	}
	switch c.Platform {
	case PlatformTelegram:
		tg := u.TelegramChannelInfo(c.ID, 8)
		if tg == nil {
			page := u.FetchTelegramChannelPage(c.ID, 0)
			if page != "" && !strings.Contains(page, "tgme_channel_info") {
				return nil, fmt.Errorf("%w: канала нет: t.me отдал страницу без канала.", ErrDiscoveryMissing)
			}
			return nil, Bad("t.me не отдал страницу канала — проверю в следующий раз.")
		}
		var pairs []DiscoveryCandidate
		for _, h := range jx.Strs(tg, "linked") {
			pairs = append(pairs, DiscoveryCandidate{Platform: PlatformTelegram, ID: h})
		}
		views := []any{}
		for _, v := range tg["post_views"].([]*int64) {
			if v == nil {
				views = append(views, nil)
			} else {
				views = append(views, *v)
			}
		}
		pt := []any{}
		for _, t := range tg["post_times"].([]int64) {
			pt = append(pt, t)
		}
		tg["post_views"], tg["post_times"] = views, pt
		return u.discoveryFinishProbe(merge(tg), pairs), nil
	case PlatformVK:
		title := jx.Str(c.Info, "title")
		info := jx.Copy(c.Info)
		if title == "" {
			if response, err := u.VKAPI(u.Ctx, "groups.getById", url.Values{"group_id": {c.ID}, "fields": {"members_count,description,country"}}, true); err == nil {
				if groups := jx.Maps(response, "groups"); len(groups) > 0 {
					title = jx.Str(groups[0], "name")
					info["description"] = groups[0]["description"]
					info["subscribers"] = groups[0]["members_count"]
				}
			}
		}
		if title == "" {
			title = c.ID
		}
		posts := u.BuildVKPreviewPosts(u.Ctx, jx.M{"id": c.ID, "name": SourceDisplayName(PlatformVK, title)}, 8, false, 120)
		if len(posts) == 0 {
			posts, _ = u.vkPublicPosts(c.ID, title, 8, false, 120)
		}
		if len(posts) == 0 {
			return nil, Bad("VK не отдал доступные посты сообщества — проверю позже.")
		}
		return u.discoveryFinishProbe(merge(jx.M{"title": title, "subscribers": jx.Int(info, "subscribers"), "description": jx.Str(info, "description"), "posts": posts, "post_times": times(posts)}), nil), nil
	case PlatformTikTok:
		posts, err := u.TikTokFetchPosts(u.Ctx, c.ID, 8, "")
		if err != nil {
			if strings.Contains(err.Error(), "does not have any videos") {
				return nil, fmt.Errorf("%w: нет видео: аккаунт ничего не публиковал.", ErrDiscoveryMissing)
			}
			return nil, err
		}
		return u.discoveryFinishProbe(merge(jx.M{"title": "@" + c.ID, "posts": posts, "post_times": times(posts)}), nil), nil
	case PlatformOK:
		page, err := u.OKGroupHTML(c.ID)
		if err != nil {
			return nil, err
		}
		posts := OKParseFeed(page, c.ID, 8, "")
		return u.discoveryFinishProbe(merge(jx.M{"title": OKTitleFromHTML(page, c.ID), "posts": posts, "post_times": times(posts)}), nil), nil
	}
	if cid, _, ok := YouTubeFeedChannel(c.ID); ok {
		return u.discoveryYouTubeProbe(cid, c.Info, merge)
	}
	feed := c.ID
	if f, err := u.RSSKnownFeed(c.ID); err == nil && f != "" {
		feed = f
	}
	withText := u.TopicsAcceptText([]string{topic})
	posts, err := u.RSSFetchPosts(feed, 15, "", false, withText)
	if err != nil {
		time.Sleep(2 * time.Second)
		if posts, err = u.RSSFetchPosts(feed, 15, "", false, withText); err != nil {
			return nil, err
		}
	}
	title := jx.Str(c.Info, "title")
	if title == "" {
		title = u.RSSFeedTitle(feed)
	}
	return u.discoveryFinishProbe(merge(jx.M{"id": feed, "title": title, "posts": posts, "post_times": times(posts)}), nil), nil
}

func (u *U) discoveryYouTubeProbe(cid string, info jx.M, merge func(jx.M) jx.M) (jx.M, error) {
	videosID := YouTubeChannelFeedURL(cid)
	times := func(posts []jx.M) []any {
		var t []any
		for _, p := range posts {
			t = append(t, jx.Int(p, "posted_at"))
		}
		return t
	}
	base := jx.M{"title": jx.Str(info, "title"), "alt_id": videosID}
	shorts, _ := u.YouTubeChannelPosts(cid, "shorts", YouTubeListCount)
	freshShorts := false
	for _, p := range shorts {
		if !IsPostOutdated(jx.Int(p, "posted_at"), 0, config.ContentMaxAgeDays) {
			freshShorts = true
		}
	}
	with := func(id string, posts []jx.M, alt string) jx.M {
		m := jx.Merge(base, jx.M{"id": id, "posts": posts, "post_times": times(posts), "alt_id": alt})
		return u.discoveryFinishProbe(merge(m), nil)
	}
	if freshShorts {
		return with(RSSYouTubeShortsFeed(cid), shorts, videosID), nil
	}
	videos, err := u.YouTubeChannelPosts(cid, "videos", YouTubeListCount)
	if err != nil {
		if len(shorts) == 0 {
			return nil, err
		}
		return with(RSSYouTubeShortsFeed(cid), shorts, videosID), nil
	}
	return with(videosID, videos, ""), nil
}

func (u *U) discoveryFinishProbe(info jx.M, pairs []DiscoveryCandidate) jx.M {
	var texts []string
	for _, p := range jx.Maps(info, "posts") {
		texts = append(texts, jx.Str(p, "text"))
	}
	all := append(pairs, SourceLinkedSources(jx.Str(info, "description")+"\n"+strings.Join(texts, "\n"))...)
	seen := map[string]bool{}
	var unique []DiscoveryCandidate
	for _, p := range all {
		if sid := SourceIDFor(p.Platform, p.ID); !seen[sid] {
			seen[sid] = true
			unique = append(unique, p)
		}
	}
	info["linked"] = unique
	return info
}

// discoveryTopicSeeds — каналы темы и высокие предложения: сиды для «похожих» и ссылок.
func (u *U) discoveryTopicSeeds(d *SourceDB, topic string) []string {
	type seed struct {
		id    string
		score int64
	}
	var seeds []seed
	for _, e := range d.Telegram {
		if len(jx.List(e, "topics")) > 0 && u.CatalogEntryMatchesTopic(e, topic, true) {
			seeds = append(seeds, seed{strings.TrimLeft(jx.Str(e, "id"), "@"), jx.Int(e, "score")})
		}
	}
	for _, s := range d.Suggestions {
		if jx.Str(s, "platform") == PlatformTelegram && u.TopicSame(jx.Str(s, "topic"), topic) && int(jx.Int(s, "score")) >= u.TInt("suggest_score") {
			seeds = append(seeds, seed{strings.TrimLeft(jx.Str(s, "id"), "@"), jx.Int(s, "score")})
		}
	}
	liked, anti := u.telegramTastes()
	kept := seeds[:0]
	for _, s := range seeds {
		key := strings.ToLower(s.id)
		if anti[key] {
			continue
		}
		if liked[key] {
			s.score += 1 << 40
		}
		kept = append(kept, s)
	}
	seeds = kept
	sort.SliceStable(seeds, func(i, j int) bool { return seeds[i].score > seeds[j].score })
	var out []string
	for _, s := range seeds {
		out = append(out, s.id)
	}
	return uniqueStrings(out)
}

func shuffleStrings(s []string) {
	rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
}
