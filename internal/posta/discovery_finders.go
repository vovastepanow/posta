package posta

import (
	"os"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

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

func newDiscoveryQueue() *discoveryQueue { return &discoveryQueue{groups: map[string][]DiscoveryCandidate{}} }

func (q *discoveryQueue) add(group string, c DiscoveryCandidate, front bool) {
	if front {
		q.groups[group] = append([]DiscoveryCandidate{c}, q.groups[group]...)
	} else {
		q.groups[group] = append(q.groups[group], c)
	}
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
	for _, q := range queries[:min(len(queries), u.TInt("post_search_queries"))] {
		take(u.PostSearchYouTube(q, PostSearchLimit))
		take(u.PostSearchTelegram(q, PostSearchLimit))
		take(u.PostSearchVK(q, PostSearchLimit))
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

func (u *U) discoveryFindVK(queries []string) []DiscoveryCandidate {
	if !u.VKIsConnected(u.Ctx) {
		u.Log("-> VK не подключён — поиск сообществ VK пропущен (кнопка «Подключить VK» на вкладке «Источники»).")
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(8, len(queries))] {
		found, err := u.VKSearchGroups(u.Ctx, q, 10)
		if err != nil {
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
func (u *U) discoveryProbe(c DiscoveryCandidate) (jx.M, error) {
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
		if title == "" {
			title = c.ID
		}
		posts := u.BuildVKPreviewPosts(u.Ctx, jx.M{"id": c.ID, "name": SourceDisplayName(PlatformVK, title)}, 8, false, 120)
		return u.discoveryFinishProbe(merge(jx.M{"title": title, "subscribers": jx.Int(c.Info, "subscribers"), "posts": posts, "post_times": times(posts)}), nil), nil
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
	posts, err := u.RSSFetchPosts(feed, 15, "", false, false)
	if err != nil {
		time.Sleep(2 * time.Second)
		if posts, err = u.RSSFetchPosts(feed, 15, "", false, false); err != nil {
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
