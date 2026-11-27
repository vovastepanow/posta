package posta

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"

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
			raw = rssItemLink(item, "")
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
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("links", q, "unavailable")
			}
			u.discoveryNewsError(q, r.FetchResult)
			continue
		}
		if !rssIsFeed(rssLoad(r.Bytes)) {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("links", q, "unavailable")
			}
			u.discoveryError(fmt.Sprintf("Bing News «%s»: HTTP %d, ответ не является RSS или Atom (тип: %s)", q, r.Status, r.Type))
			continue
		}
		sites := discoveryNewsSites(r.Bytes)
		if u.discoverySearchObservation != nil {
			status := "ok"
			if len(sites) == 0 {
				status = "empty"
			}
			u.discoverySearchObservation("links", q, status)
		}
		for _, site := range sites {
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

func (u *U) discoveryNewsError(query string, result FetchResult) {
	u.discoveryError(fmt.Sprintf("Bing News «%s»: %s", query, fetchFailure(result)))
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

// discoveryLemmyCommunities extracts stable community feeds from the search response.
func discoveryLemmyCommunities(data []byte) ([]DiscoveryCandidate, error) {
	var response struct {
		Communities []struct {
			Community struct {
				ActorID string `json:"actor_id"`
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Deleted     bool   `json:"deleted"`
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
		candidate.Info = jx.M{"title": c.Title, "name": c.Name, "description": c.Description}
		out = append(out, *candidate)
	}
	return out, nil
}

// Lemmy's search API may match a query anywhere inside a community handle.
// "smm" for instance matches "womensmma" (women-SMM-a), which is unrelated
// to the SMM taxonomy topic. Do not trust raw search results as evidence of
// topical relevance, especially for short ambiguous terms.
func discoveryLemmyMatchesTopic(c DiscoveryCandidate, topic string) bool {
	if autopilotCanonicalTopic(topic) != "SMM и блогинг" {
		return true
	}
	meta := strings.Join([]string{
		jx.Str(c.Info, "title"), jx.Str(c.Info, "name"),
		jx.Str(c.Info, "description"),
	}, " ")
	return okSMMProfessionRe.MatchString(meta)
}

// Legacy candidates (including those nominated by individual post search)
// must pass the same topical check before consuming an AI assessment.
// A community can have a creative name: two independent relevant posts may
// establish its focus even when title/description don't contain SMM words.
// Inconclusive feeds stay eligible for later checks; only demonstrably
// unrelated communities are rejected.
func lemmySMMFeedOffTopic(feed string, body []byte, posts []jx.M) bool {
	u, err := url.Parse(feed)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		!strings.HasPrefix(u.Path, "/feeds/c/") || !strings.HasSuffix(u.Path, ".xml") {
		return false
	}
	doc := rssLoad(body)
	if !rssIsFeed(doc) {
		return false
	}
	channel := doc
	if doc.N != "feed" {
		channel = doc.kid("channel")
	}
	if channel == nil {
		return false
	}
	title := strings.TrimSpace(channel.kid("title").text())
	if title == "" || len(posts) < 2 {
		return false
	}
	metadata := title + " " + channel.kid("description").text() + " " + channel.kid("subtitle").text()
	if okSMMProfessionRe.MatchString(metadata) {
		return false
	}
	var matching int
	for _, post := range posts {
		if okSMMProfessionRe.MatchString(jx.Str(post, "text")+" "+jx.Str(post, "title")) {
			matching++
			if matching >= 2 {
				return false
			}
		}
	}
	return true
}

func (u *U) discoveryFindLemmy(queries []string, topic string) []DiscoveryCandidate {
	if !u.TBool("search_lemmy") {
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(3, len(queries))] {
		body, err := u.searchGetJSON("https://lemmy.world/api/v3/search?type_=Communities&limit=30&q=" + url.QueryEscape(q))
		if err != nil {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("lemmy", q, "unavailable")
			}
			u.Log("-> Lemmy «%s»: %s", q, err)
			continue
		}
		found, err := discoveryLemmyCommunities(body)
		if err != nil {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("lemmy", q, "unavailable")
			}
			u.Log("-> Lemmy «%s»: %s", q, err)
			continue
		}
		if u.discoverySearchObservation != nil {
			u.discoverySearchObservation("lemmy", q, "ok")
		}
		admitted := 0
		for _, c := range found {
			if !discoveryLemmyMatchesTopic(c, topic) {
				continue
			}
			c.Origin, c.OriginKind = "поиск сообществ Lemmy «"+q+"»", "other"
			out = append(out, c)
			admitted++
		}
		u.Log("-> Поиск сообществ Lemmy «%s»: показано API %d, отобрано по теме %d.", q, len(found), admitted)
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
			Description string `json:"description"`
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
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://bsky.app/profile/" + actor.DID + "/rss", Info: jx.M{"title": name, "handle": actor.Handle, "description": actor.Description}})
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
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://bsky.app/profile/" + a.DID + "/rss", Info: jx.M{"title": name, "handle": a.Handle}})
	}
	return out, nil
}

// Require word-level evidence rather than fuzzy substring matches: SMM must
// not match MMA (nor should a broad result be deemed topical just because
// Bluesky returned it). This is a cheap prefilter, never final approval.
func discoveryQueryEvidenceMatch(query, evidence string) bool {
	tokenize := func(text string) []string {
		words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '#'
		})
		for i, word := range words {
			words[i] = strings.TrimLeft(word, "#")
		}
		return words
	}
	observed := map[string]bool{}
	for _, word := range tokenize(evidence) {
		observed[word] = true
	}
	// Matching one generic word ("media" in "social media marketing")
	// is weak evidence. Require a majority of distinct meaningful terms,
	// while preserving exact acronyms and C#/C++ as different tokens.
	stop := map[string]bool{"and": true, "the": true, "for": true, "with": true, "from": true, "about": true,
		"how": true, "to": true, "in": true, "of": true, "a": true, "an": true, "or": true,
		"и": true, "или": true, "для": true, "по": true, "на": true, "про": true, "как": true,
		"об": true, "о": true, "в": true, "с": true}
	terms := map[string]bool{}
	matched := 0
	for _, word := range tokenize(query) {
		if !letterAnyRe.MatchString(word) || stop[word] || terms[word] {
			continue
		}
		terms[word] = true
		if observed[word] {
			matched++
		}
	}
	return len(terms) > 0 && matched >= (2*len(terms)+2)/3
}

// Post search frequently returns authors who mentioned a topic once without
// being about it. Admit only repeated topical posts, or one topical post
// accompanied by relevant profile metadata.
func discoveryBlueskyRelevantPostAuthors(data []byte, query string) ([]DiscoveryCandidate, error) {
	all, err := discoveryBlueskyPostAuthors(data)
	if err != nil {
		return nil, err
	}
	var response struct {
		Posts []struct {
			Author struct { DID string `json:"did"` } `json:"author"`
			Record struct { Text string `json:"text"` } `json:"record"`
			Text string `json:"text"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	matched := map[string]int{}
	seenText := map[string]bool{}
	for _, post := range response.Posts {
		body := strings.TrimSpace(post.Record.Text)
		if body == "" {
			body = strings.TrimSpace(post.Text)
		}
		// Duplicate results or copy-pasted captions do not constitute two
		// independent examples of a publisher's topical focus.
		key := post.Author.DID + "|" + strings.ToLower(collapseSpaces(body))
		if !seenText[key] && discoveryQueryEvidenceMatch(query, body) {
			matched[post.Author.DID]++
			seenText[key] = true
		}
	}
	out := make([]DiscoveryCandidate, 0, len(all))
	for _, candidate := range all {
		did := strings.TrimSuffix(strings.TrimPrefix(candidate.ID, "https://bsky.app/profile/"), "/rss")
		if matched[did] >= 2 || matched[did] == 1 && discoveryQueryEvidenceMatch(query,
			jx.Str(candidate.Info, "title")+" "+jx.Str(candidate.Info, "handle")) {
			out = append(out, candidate)
		}
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
			if err == nil && u.discoverySearchObservation != nil {
				u.discoverySearchObservation("bluesky", q, "ok")
			}
			for _, c := range found {
				if !discoveryQueryEvidenceMatch(q, jx.Str(c.Info, "title")+" "+jx.Str(c.Info, "handle")+" "+jx.Str(c.Info, "description")) {
					continue
				}
				c.Origin, c.OriginKind = "поиск аккаунтов Bluesky «"+q+"»", "other"
				out = append(out, c)
			}
			u.Log("-> Поиск аккаунтов Bluesky «%s»: найдено %d.", q, len(found))
		}
		if err != nil {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("bluesky", q, "unavailable")
			}
			u.Log("-> Bluesky «%s»: %s", q, err)
		}
		body, err = u.autopilotBlueskySearchData(q)
		if err != nil {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("bluesky", q, "unavailable")
			}
			u.Log("-> Посты Bluesky «%s»: %s", q, err)
			continue
		}
		authors, err := discoveryBlueskyRelevantPostAuthors(body, q)
		if err != nil {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("bluesky", q, "unavailable")
			}
			u.Log("-> Посты Bluesky «%s»: %s", q, err)
			continue
		}
		if u.discoverySearchObservation != nil {
			u.discoverySearchObservation("bluesky", q, "ok")
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

const (
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

// DiscoveryRotate — элементы с позиции offset по кругу.
func DiscoveryRotate(items []string, offset int) []string {
	if len(items) == 0 {
		return nil
	}
	offset = ((offset % len(items)) + len(items)) % len(items)
	return append(append([]string{}, items[offset:]...), items[:offset]...)
}

var tumblrReserved = []string{"www", "media", "assets", "static", "srvcs", "secure", "cdn", "col", "buttons", "api", "px", "vt", "staff", "new", "tumblr"}

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

var okGroupLinkRe = regexp.MustCompile(`ok\.ru/group/(\d{10,20})`)

// These are professional SMM signals, not general words such as "blog",
// "posts", "video" or "business" which appear in unrelated OK groups.
// A professional group must advertise this subject in its own title.
var okSMMProfessionRe = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(smm|смм|таргетинг[\p{L}]*|таргетолог[\p{L}]*|интернет-маркетинг[\p{L}]*|контент-маркетинг[\p{L}]*|продвижени[\p{L}]*|маркетинг[\p{L}]*|соцсет[\p{L}]*|social media|контент-стратеги[\p{L}]*)([^\p{L}\p{N}]|$)`)

func okDiscoveryTopicTextMatches(text, topic string, queries []string) bool {
	if autopilotCanonicalTopic(topic) == "SMM и блогинг" {
		return okSMMProfessionRe.MatchString(text)
	}
	for _, query := range append([]string{topic}, queries...) {
		query = strings.TrimSpace(query)
		// Avoid single-word fragments and overly generic short matches.
		if runeLen(query) >= 4 && QueryTextHit(text, query) {
			return true
		}
	}
	return false
}

func okDiscoveryGroupTitleMatches(title, topic string, queries []string) bool {
	if autopilotCanonicalTopic(topic) == "SMM и блогинг" {
		return okSMMProfessionRe.MatchString(title)
	}
	return okDiscoveryTopicTextMatches(title, topic, queries)
}

// discoveryFindOK searches public OK discovery feeds for candidate group IDs,
// then verifies that each group exists, exposes readable posts, and describes
// the actual requested subject. A keyword in an unrelated aggregator post is
// never sufficient evidence that the group itself matches.
func (u *U) discoveryFindOK(queries []string, topic string) []DiscoveryCandidate {
	hits := map[string]int{}
	var order []string
	for _, name := range okDiscoveryFeeds {
		feed := "https://ok.ru/atom-feed/" + name
		r := u.RSSGet(feed, 3<<20, 15*time.Second, BrowserUA)
		if !r.OK || !rssIsFeed(rssLoad(r.Bytes)) {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("ok", topic, "unavailable")
			}
			continue
		}
		posts, err := RSSParse(r.Bytes, 100, PlatformOK, feed, true, false, 120)
		if err != nil {
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation("ok", topic, "unavailable")
			}
			continue
		}
		if u.discoverySearchObservation != nil {
			u.discoverySearchObservation("ok", topic, "ok")
		}
		for _, p := range posts {
			m := okGroupLinkRe.FindStringSubmatch(jx.Str(p, "url"))
			if m == nil || !okDiscoveryTopicTextMatches(jx.Str(p, "text"), topic, queries) {
				continue
			}
			if hits[m[1]] == 0 {
				order = append(order, m[1])
			}
			hits[m[1]]++
		}
	}
	var out []DiscoveryCandidate
	probed := 0
	for _, group := range order {
		if hits[group] < 2 || len(out) >= discoveryOKMax || probed >= 10 {
			continue
		}
		probed++
		// A valid-looking numeric ID is not proof that the OK group exists.
		// Check the actual mobile group page and readable posts before storing.
		res := u.RSSGet(okGroupURL(group), rssMaxBytes, 15*time.Second, BrowserUA)
		if !res.OK {
			continue
		}
		groupPosts, err := autopilotPublicSocialPosts(PlatformOK, group, res)
		if err != nil || len(groupPosts) < 2 {
			continue
		}
		title := OKTitleFromHTML(string(res.Bytes), group)
		if title == group || !okDiscoveryGroupTitleMatches(title, topic, queries) {
			continue
		}
		out = append(out, DiscoveryCandidate{Platform: PlatformOK, ID: group,
			Info: jx.M{"title": title}, Origin: "проверенная публичная группа ОК",
			OriginKind: "ok"})
	}
	u.Log("-> Одноклассники: проверено %d групп, подходящих публичных групп %d.", probed, len(out))
	return out
}

