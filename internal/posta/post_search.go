package posta

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"posta/internal/config"
	"posta/internal/jx"
)

// ---- Бережный опрос источников: интервал по активности ----

// SourcePollInterval — средний промежуток между постами / 2, в пределах SourcePollMin..SourcePollMax.
func SourcePollInterval(activity jx.M) int64 {
	pw := jx.Float(activity, "per_week")
	if pw <= 0 {
		return config.SourcePollMax
	}
	return int64(math.Max(config.SourcePollMin, math.Min(config.SourcePollMax, 7*config.DaySeconds/pw/2)))
}

func sourcePollOverdue(e jx.M, at int64) bool {
	act := jx.Map(e, "activity")
	if act == nil {
		return true
	}
	return at-jx.Int(act, "checked_at") >= SourcePollInterval(act)
}

func sourcePollLateness(e jx.M, at int64) float64 {
	act := jx.Map(e, "activity")
	if act == nil {
		return math.Inf(1)
	}
	return float64(at-jx.Int(act, "checked_at")) / float64(SourcePollInterval(act))
}

func SourcePollCacheKey(platform, id string) string {
	h := sha1.Sum([]byte(platform + "|" + strings.ToLower(id)))
	return "source_posts:" + hex.EncodeToString(h[:])
}

// SourcePollFetchSet — кому идти в сеть на этом проходе: без кэша или с просроченным опросом, самые просроченные первыми.
func SourcePollFetchSet(entries map[string]jx.M, cached map[string]bool, at int64) []string {
	type need struct {
		key string
		v   float64
	}
	var list []need
	for k, e := range entries {
		if !cached[k] {
			list = append(list, need{k, math.Inf(1)})
		} else if sourcePollOverdue(e, at) {
			list = append(list, need{k, sourcePollLateness(e, at)})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].key < list[j].key
	})
	var out []string
	for _, n := range list[:min(len(list), config.SourceFetchesPerPass)] {
		out = append(out, n.key)
	}
	return out
}

// ---- «Добавить источник» вручную ----

const sourceAddPerDay = 20

var (
	atNameRe     = regexp.MustCompile(`^@([A-Za-z0-9_]+)$`)
	tmeLinkAddRe = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?(?:t|telegram)\.me/(?:s/)?([^/?#]+)`)
	vkClubRe     = regexp.MustCompile(`(?i)^(club|public)(\d+)$`)
	vkLinkRe     = regexp.MustCompile(`(?i)^(?:https?://)?(?:m\.)?vk\.(?:com|ru)/([^/?#]+)`)
	okLinkRe     = regexp.MustCompile(`(?i)^https?://(?:m\.)?ok\.ru(?:[/?#]|$)`)
	tiktokLinkRe = regexp.MustCompile(`(?i)^https?://(?:(?:www|m|vm|vt)\.)?tiktok\.com(?:[/?#]|$)`)
	httpLinkRe   = regexp.MustCompile(`(?i)^https?://`)
	badLinkRe    = regexp.MustCompile(`[\s<>"']`)
)

// SourceParseLink — ссылка → площадка и id.
func (a *App) SourceParseLink(input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 300 || badLinkRe.MatchString(input) {
		return "", "", Bad("Вставьте одну ссылку: на Telegram-канал, паблик VK или сайт.")
	}
	m := atNameRe.FindStringSubmatch(input)
	if m == nil {
		m = tmeLinkAddRe.FindStringSubmatch(input)
	}
	if m != nil {
		name := m[1]
		if strings.HasPrefix(name, "+") || strings.ToLower(name) == "joinchat" {
			return "", "", Bad("Закрытые каналы не читаются: нужна публичная ссылка t.me/имя")
		}
		low := strings.ToLower(name)
		if !IsValidTelegramChannel(name) || (contains(telegramReservedPaths, low) && low != "durov") {
			return "", "", Bad("Не похоже на публичный Telegram-канал: нужна ссылка t.me/имя.")
		}
		return PlatformTelegram, name, nil
	}
	if m := vkClubRe.FindStringSubmatch(input); m != nil {
		return PlatformVK, strings.ToLower(m[1]) + m[2], nil
	}
	if m := vkLinkRe.FindStringSubmatch(input); m != nil {
		if !IsValidVKDomain(m[1]) {
			return "", "", Bad("Не похоже на ссылку на паблик VK: нужна vk.com/имя.")
		}
		return PlatformVK, m[1], nil
	}
	if okLinkRe.MatchString(input) {
		id, err := a.OKGroupID(input)
		return PlatformOK, id, err
	}
	if tiktokLinkRe.MatchString(input) {
		u, err := a.TikTokUserFromLink(input)
		return PlatformTikTok, u, err
	}
	if httpLinkRe.MatchString(input) {
		feed, err := a.RSSKnownFeed(input)
		if err != nil {
			return "", "", err
		}
		if feed == "" {
			if feed, err = a.RSSDiscover(input); err != nil {
				return "", "", err
			}
		}
		if !IsValidRSSURL(feed) {
			return "", "", Bad("Адрес ленты должен начинаться с https:// и быть не длиннее 500 символов.")
		}
		return PlatformRSS, feed, nil
	}
	return "", "", Bad("Не понял ссылку. Telegram: t.me/канал · VK: vk.com/паблик · сайт или блог: адрес страницы.")
}

func (a *App) sourceVKTitle(domain string) string {
	r, err := a.VKAPI(a.ctxOrBg(), "groups.getById", url.Values{"group_id": {domain}}, true)
	if err != nil {
		return domain
	}
	if g := jx.Maps(r, "groups"); len(g) > 0 {
		if t := trimRunes(CleanHTMLText(jx.Str(g[0], "name")), 120); t != "" {
			return t
		}
	}
	return domain
}

// SourceProbe — название и свежие посты с картинками.
func (u *U) SourceProbe(platform, id string) (string, []jx.M, error) {
	var name string
	var posts []jx.M
	var err error
	switch platform {
	case PlatformTelegram:
		info := u.TelegramChannelInfo(id, 10)
		if info == nil {
			return "", nil, Bad("Канал не найден или закрыт: нужна публичная ссылка t.me/имя.")
		}
		name, posts = jx.Str(info, "title"), jx.Maps(info, "posts")
	case PlatformVK:
		name, posts = u.sourceVKTitle(id), u.BuildVKPreviewPosts(u.Ctx, jx.M{"id": id}, 10, false, 120)
	case PlatformRSS:
		name = u.RSSFeedTitle(id)
		posts, err = u.RSSFetchPosts(id, config.RSSPostsPerFeed, "", false, false)
	case PlatformOK:
		page, e := u.OKGroupHTML(id)
		if e != nil {
			return "", nil, e
		}
		name, posts = OKTitleFromHTML(page, id), OKParseFeed(page, id, OKPostsPerGroup, "")
	case PlatformTikTok:
		name = "@" + id
		posts, err = u.TikTokFetchPosts(u.Ctx, id, TikTokPostsPerAccount, "")
	}
	if err != nil {
		return "", nil, err
	}
	var fresh []jx.M
	for _, p := range posts {
		if jx.Str(p, "image") != "" && !IsPostOutdated(jx.Int(p, "posted_at"), 0, config.ContentMaxAgeDays) {
			fresh = append(fresh, p)
		}
	}
	return name, fresh, nil
}

func (u *U) sourceAddQuotaTake() error {
	key := "source_add:" + u.ID + ":" + time.Now().Format("2006-01-02")
	n, _ := KVUpdate(u.App, u.Ctx, key, 48*time.Hour, func(n int, _ bool) *int { n++; return &n })
	if n > sourceAddPerDay {
		return Bad("Сегодня добавлено слишком много источников: попробуйте завтра (до %d в сутки).", sourceAddPerDay)
	}
	return nil
}

// SourceAddManual — источник в каталог с темой пользователя.
func (u *U) SourceAddManual(input, topic string) (jx.M, error) {
	platform, id, err := u.SourceParseLink(input)
	if err != nil {
		return nil, err
	}
	if platform == PlatformVK && !u.VKCanRead(u.Ctx) {
		return nil, Bad("Сначала подключите VK на вкладке «Источники».")
	}
	if err := u.sourceAddQuotaTake(); err != nil {
		return nil, err
	}
	name, posts, err := u.SourceProbe(platform, id)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, Bad("В источнике нет свежих постов с картинками.")
	}
	sid := SourceIDFor(platform, id)
	uaPosts := 0
	for _, p := range posts {
		if u.UAItemBlocked(p) != "" {
			uaPosts++
		}
	}
	if u.UASourceBlocked(sid, jx.M{"title": name}) != "" || uaPosts >= 2 {
		return nil, Bad("Украинские источники в Posta недоступны.")
	}
	kind := CatalogKind(platform)
	added := false
	_, err = u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		list := d.List(kind)
		found := false
		for _, e := range *list {
			if !strings.EqualFold(strings.TrimLeft(jx.Str(e, "id"), "@"), id) {
				continue
			}
			found = true
			topics := jx.Strs(e, "topics")
			has := false
			for _, t := range topics {
				if u.TopicSame(t, topic) {
					has = true
				}
			}
			if len(topics) > 0 && !has {
				e["topics"] = append(jx.List(e, "topics"), topic)
				added = true
			}
		}
		if !found {
			*list = append(*list, jx.M{"id": id, "name": name, "topics": []any{topic}, "manual": true, "preview": SourcePreviewImages(posts)})
			added = true
		}
		var votes []jx.M
		for _, v := range d.Votes {
			if !(jx.Str(v, "source_id") == sid && jx.Str(v, "vote") == "reject" && u.TopicSame(jx.Str(v, "topic"), topic)) {
				votes = append(votes, v)
			}
		}
		d.Votes = orEmptyList(votes)
		return true
	})
	if err != nil {
		return nil, err
	}
	return jx.M{"platform": platform, "id": id, "name": name, "added": added, "posts": len(posts)}, nil
}

func orEmptyList(l []jx.M) []jx.M {
	if l == nil {
		return []jx.M{}
	}
	return l
}

// ---- Поиск постов встроенными поисками площадок ----

const (
	postSearchSeconds      = 60
	PostSearchLimit        = 30
	postSearchDateLookups  = 3
	postSearchCacheTTL     = time.Hour
	tgSearchChannelPages   = 15
	tiktokSearchAuthors    = 2
)

func (u *U) postSearchCached(platform, query string, fetch func() ([]jx.M, bool, error)) []jx.M {
	key := "post_search:" + sha1Short(platform+"|"+query, 20)
	var cached []jx.M
	if u.CacheGet(u.Ctx, key, &cached) {
		return cached
	}
	posts, ran, err := fetch()
	if err != nil {
		u.Log("Поиск постов %s «%s»: %s", platform, query, err)
		return nil
	}
	if !ran {
		return nil
	}
	u.CacheSet(u.Ctx, key, orEmptyList(posts), postSearchCacheTTL)
	return posts
}

var spacesAllRe = regexp.MustCompile(`\s+`)

// PostSearchCandidates — уточнение, хэштеги слов и имён персонажей, запросы профиля.
func (u *U) PostSearchCandidates(topic string) []string {
	var qs []string
	if q := u.CurrentQuery(); q != "" {
		qs = append(qs, q)
	}
	e := u.TopicLookup(topic)
	var p *TopicProfile
	var entities []string
	if e != nil {
		p, entities = e.Profile, e.Entities
	}
	if p != nil && len(p.Keywords) > 0 && strings.TrimSpace(p.Keywords[0]) != "" {
		qs = append(qs, "#"+spacesAllRe.ReplaceAllString(strings.TrimSpace(p.Keywords[0]), ""))
	}
	for _, w := range TopicEntityWords(entities) {
		qs = append(qs, "#"+w)
	}
	if p != nil {
		for _, q := range append(append([]string{}, p.Keywords...), p.Queries...) {
			if q = DiscoveryCleanQuery(q); q != "" {
				qs = append(qs, q)
			}
		}
	}
	var out []string
	for _, q := range qs {
		if q = strings.TrimSpace(q); q != "" && postSearchUseful(q) {
			out = append(out, q)
		}
	}
	return uniqueStrings(out)
}

// postSearchUseful — запрос годится для поиска по площадкам: не короче 3 букв и не общее слово вроде «image» (хэштег без # не в счёт).
func postSearchUseful(q string) bool {
	w := strings.ToLower(strings.TrimLeft(q, "#"))
	return utf8.RuneCountInString(w) >= 3 && !slices.Contains(config.PostSearchGenericWords, w)
}

func (u *U) postSearchQueries(topic string) []string {
	qs := u.PostSearchCandidates(topic)
	pass, _ := KVUpdate(u.App, u.Ctx, "post_search:pass", 0, func(n int, _ bool) *int { n++; return &n })
	r := DiscoveryRotate(qs, pass-1)
	return r[:min(len(r), u.TInt("post_search_queries"))]
}

var hashtagWordRe = regexp.MustCompile(`#(\p{L}[\p{L}\p{N}_]*)`)
var hashtagAnyRe = regexp.MustCompile(`#\S+`)

// PostSearchMatches — в тексте поста есть основа слова темы или уточнения.
func (u *U) PostSearchMatches(topic string, post jx.M) bool {
	words := u.DiscoveryTopicKeywords(topic)
	if q := u.CurrentQuery(); q != "" {
		words = append(words, q)
	}
	raw := strings.ToLower(jx.Str(post, "text"))
	var entities []string
	if e := u.TopicLookup(topic); e != nil {
		entities = e.Entities
	}
	ew := TopicEntityWords(entities)
	for _, m := range hashtagWordRe.FindAllStringSubmatch(raw, -1) {
		if contains(ew, m[1]) {
			return true
		}
	}
	text := hashtagAnyRe.ReplaceAllString(raw, " ")
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		stem := trimRunes(w, max(3, runeLen(w)-2))
		if stem != "" && strings.Contains(text, stem) {
			return true
		}
	}
	return false
}

// PostSearchYouTube — посты YouTube по запросу или #тегу; без даты пост не берём.
func (u *U) PostSearchYouTube(query string, limit int) []jx.M {
	return u.postSearchCached("youtube", query, func() ([]jx.M, bool, error) {
		hashtag := strings.HasPrefix(query, "#")
		target := "https://www.youtube.com/results?search_query=" + url.QueryEscape(query)
		if hashtag {
			target = "https://www.youtube.com/hashtag/" + url.PathEscape(strings.TrimLeft(query, "#")) + "/shorts"
		}
		rows, err := u.youtubeListRows([]string{"--flat-playlist", "--playlist-end", strconv.Itoa(limit), "--extractor-args", "youtube:lang=ru",
			"--print", "%(id)s|%(channel_id)s|%(duration)s|%(view_count)s|%(thumbnails.-1.url)s|%(title)s", "--", target}, 6)
		if err != nil {
			return nil, true, err
		}
		var ok [][]string
		for _, r := range rows {
			if videoIDRe.MatchString(r[0]) && len(ok) < postSearchDateLookups {
				ok = append(ok, r)
			}
		}
		if len(ok) == 0 {
			return []jx.M{}, true, nil
		}
		short := func(r []string) bool {
			d, err := strconv.Atoi(r[2])
			return hashtag || (err == nil && d <= 60)
		}
		page := func(r []string) string {
			if short(r) {
				return "https://www.youtube.com/shorts/" + r[0]
			}
			return "https://www.youtube.com/watch?v=" + r[0]
		}
		args := []string{"--no-playlist", "--print", "%(id)s|%(timestamp)s|%(channel_id)s", "--"}
		for _, r := range ok {
			args = append(args, page(r))
		}
		dated := u.YtdlpRun(u.Ctx, args, ytdlpListTimeout, false, nil)
		info := map[string][2]string{}
		for _, line := range strings.Split(strings.TrimSpace(dated.Stdout), "\n") {
			p := strings.SplitN(strings.TrimSpace(line), "|", 3)
			if len(p) == 3 && ytChannelIDRe.MatchString(p[2]) {
				if _, err := strconv.ParseInt(p[1], 10, 64); err == nil {
					info[p[0]] = [2]string{p[1], p[2]}
				}
			}
		}
		var posts []jx.M
		for _, r := range ok {
			inf, has := info[r[0]]
			if !has {
				continue
			}
			title := r[5]
			if title == "NA" {
				title = ""
			}
			at, _ := strconv.ParseInt(inf[0], 10, 64)
			views, _ := strconv.ParseInt(r[3], 10, 64)
			p := YouTubePost(r[0], title, r[4], at, views, short(r))
			markYouTubeLong(p, r[2])
			p["source_key"] = SourceIDFor(PlatformRSS, YouTubeChannelFeedURL(inf[1]))
			p["source_url"] = "https://www.youtube.com/channel/" + inf[1]
			p["found_by"] = "поиск YouTube «" + query + "»"
			posts = append(posts, p)
		}
		return posts, true, nil
	})
}

func (u *U) tgPostSearchAllowed() bool {
	if KV(u.App, u.Ctx, "tg_post_search:pause", int64(0)) > now() {
		return false
	}
	allowed := false
	type hour struct {
		At int64 `json:"at"`
		N  int   `json:"n"`
	}
	KVUpdate(u.App, u.Ctx, "tg_post_search:hour", 2*time.Hour, func(h hour, _ bool) *hour {
		if now()-h.At >= 3600 {
			h = hour{now(), 0}
		}
		if h.N >= u.TInt("tg_post_search_per_hour") {
			return &h
		}
		allowed = true
		h.N++
		return &h
	})
	return allowed
}

var (
	adBotRe   = regexp.MustCompile(`(?i)@\w+bot\b|t\.me/\+|\?start=|bit\.ly|u\.to/`)
	adWordsRe = regexp.MustCompile(`(?i)бесплатно|подписыва|переходи|скидк|акци|заработ|промокод|казино`)
)

// PostSearchIsAd — реклама и боты в результатах поиска (без модели).
func PostSearchIsAd(p jx.M) bool {
	user := strings.ToLower(strings.TrimPrefix(jx.Str(p, "source_url"), "https://t.me/"))
	if user != "" && strings.HasSuffix(user, "bot") {
		return true
	}
	text := jx.Str(p, "text")
	if adBotRe.MatchString(text) || adWordsRe.MatchString(text) {
		return true
	}
	return len(hashtagAnyRe.FindAllString(text, -1)) > 3
}

// PostSearchTelegram — поиск постов через аккаунт владельца (хэштег или слова); пост берём со страницы канала.
func (u *U) PostSearchTelegram(query string, limit int) []jx.M {
	if !u.TelegramUserConnected() {
		return nil
	}
	return u.postSearchCached("telegram", query, func() ([]jx.M, bool, error) {
		if !u.tgPostSearchAllowed() {
			u.Log("Поиск постов Telegram «%s»: лимит поисков в час исчерпан — пропуск.", query)
			return nil, false, nil
		}
		hashtag := strings.HasPrefix(query, "#")
		res, err := u.TelegramUserSearchPosts(strings.TrimLeft(query, "#"), hashtag, limit)
		if err != nil {
			return nil, true, err
		}
		if res.FloodWait > 0 {
			u.KVSet(u.Ctx, "tg_post_search:pause", now()+3600, 2*time.Hour)
			u.Log("Поиск постов Telegram: FloodWait — прекращаю до конца часа.")
			return nil, false, nil
		}
		if res.Paid {
			u.Log("Поиск постов Telegram «%s»: поиск по словам платный сейчас — пропуск (хэштеги работают).", query)
			return nil, false, nil
		}
		byChannel := map[string]map[int]bool{}
		var order []string
		for _, p := range res.Posts {
			if !p.HasMedia {
				continue
			}
			if byChannel[p.Username] == nil {
				byChannel[p.Username] = map[int]bool{}
				order = append(order, p.Username)
			}
			byChannel[p.Username][p.ID] = true
		}
		var posts []jx.M
		for _, username := range order[:min(len(order), tgSearchChannelPages)] {
			ids := byChannel[username]
			maxID := 0
			for id := range ids {
				maxID = max(maxID, id)
			}
			if !IsValidTelegramChannel(username) {
				continue
			}
			page := u.FetchTelegramChannelPage(username, int64(maxID+1))
			if page == "" {
				continue
			}
			src := jx.M{"id": username, "name": SourceDisplayName(PlatformTelegram, username)}
			for _, p := range TelegramPostsFromHTML(page, src, config.TelegramPostsPerChannel, false, 120) {
				link := jx.Str(p, "url")
				id, _ := strconv.Atoi(link[strings.LastIndex(link, "/")+1:])
				if !ids[id] {
					continue
				}
				p["source_key"] = SourceIDFor(PlatformTelegram, username)
				p["source_url"] = "https://t.me/" + username
				p["found_by"] = "поиск Telegram «" + query + "»"
				posts = append(posts, p)
			}
		}
		return posts, true, nil
	})
}

// PostSearchVK — поиск новостей VK (нужен вход в VK).
func (u *U) PostSearchVK(query string, limit int) []jx.M {
	if !u.VKIsConnected(u.Ctx) {
		u.Log("Поиск постов VK «%s»: VK не подключён (нужен вход в VK) — пропуск.", query)
		return nil
	}
	return u.postSearchCached("vk", query, func() ([]jx.M, bool, error) {
		r, err := u.VKAPI(u.Ctx, "newsfeed.search", url.Values{"q": {query}, "count": {strconv.Itoa(max(1, min(100, limit)))}, "extended": {"0"}}, false)
		if err != nil {
			return nil, true, err
		}
		var posts []jx.M
		for _, it := range jx.Maps(r, "items") {
			owner := jx.Int(it, "owner_id")
			if owner == 0 {
				continue
			}
			p := VKWallPost(it, "", "", false, 120)
			if p == nil {
				continue
			}
			domain := "id" + strconv.FormatInt(owner, 10)
			if owner < 0 {
				domain = "club" + strconv.FormatInt(-owner, 10)
			}
			p["source_key"] = SourceIDFor(PlatformVK, domain)
			p["source_url"] = "https://vk.com/" + domain
			p["found_by"] = "поиск VK «" + query + "»"
			posts = append(posts, p)
		}
		return posts, true, nil
	})
}

// PostSearchTikTok — авторы свежих роликов по запросу и их последние ролики.
func (u *U) PostSearchTikTok(query string, limit int) []jx.M {
	if u.TikTokSearchPauseNote(u.Ctx) != "" {
		return nil
	}
	return u.postSearchCached("tiktok", query, func() ([]jx.M, bool, error) {
		var posts []jx.M
		users := u.TikTokSearchVideos(strings.TrimLeft(query, "#"))
		for _, user := range users[:min(len(users), tiktokSearchAuthors)] {
			got, err := u.TikTokFetchPosts(u.Ctx, user, min(limit, 8), "")
			if err != nil {
				continue
			}
			for _, p := range got {
				p["source_key"] = SourceIDFor(PlatformTikTok, user)
				p["source_url"] = "https://www.tiktok.com/@" + user
				p["found_by"] = "поиск TikTok «" + query + "»"
				posts = append(posts, p)
			}
		}
		return posts, true, nil
	})
}

var (
	authorTGRe = regexp.MustCompile(`^https://t\.me/([A-Za-z0-9_]+)$`)
	authorYTRe = regexp.MustCompile(`^https://www\.youtube\.com/channel/(UC[A-Za-z0-9_-]{22})$`)
	authorTTRe = regexp.MustCompile(`^https://www\.tiktok\.com/@([A-Za-z0-9_.]{2,24})$`)
	authorVKRe = regexp.MustCompile(`^https://vk\.com/((?:club|id)\d+)$`)
)

// PostSearchAuthor — автор поста по адресу источника; nil — не понять.
func PostSearchAuthor(p jx.M) *DiscoveryCandidate {
	u := jx.Str(p, "source_url")
	if m := authorTGRe.FindStringSubmatch(u); m != nil {
		return &DiscoveryCandidate{Platform: PlatformTelegram, ID: m[1]}
	}
	if m := authorYTRe.FindStringSubmatch(u); m != nil {
		return &DiscoveryCandidate{Platform: PlatformRSS, ID: YouTubeChannelFeedURL(m[1])}
	}
	if m := authorTTRe.FindStringSubmatch(u); m != nil {
		return &DiscoveryCandidate{Platform: PlatformTikTok, ID: m[1]}
	}
	if m := authorVKRe.FindStringSubmatch(u); m != nil {
		return &DiscoveryCandidate{Platform: PlatformVK, ID: m[1]}
	}
	return nil
}

// PostSearchSuggest — пост прошёл анализ и найден поиском: его автор — в отложенные подбора темы.
func (u *U) PostSearchSuggest(item jx.M, topic string) {
	if jx.Str(item, "found_by") == "" {
		return
	}
	a := PostSearchAuthor(item)
	if a == nil {
		return
	}
	if PostSearchSourceKnown(u.Catalog(), SourceIDFor(a.Platform, a.ID)) {
		return
	}
	link := jx.Str(item, "url")
	if link == "" {
		link = jx.Str(item, "source_url")
	}
	a.Origin = "пост прошёл отбор: " + link
	u.DiscoveryDefer(topic, *a, "пост прошёл отбор — проверю в следующем подборе")
}

func PostSearchSourceKnown(d *SourceDB, sid string) bool {
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if SourceIDFor(c.Platform, jx.Str(e, "id")) == sid {
				return true
			}
		}
	}
	for _, e := range append(append([]jx.M{}, d.Votes...), d.Suggestions...) {
		if jx.Str(e, "source_id") == sid {
			return true
		}
	}
	for c := range jx.Map(d.Discovery, "checked_at") {
		if c == sid || strings.HasSuffix(c, "|"+sid) {
			return true
		}
	}
	return false
}

type PostSearchResult struct {
	By      map[string][]jx.M
	Done    map[string]int
	Queries []string
}

// PostSearchCollect — все поиски постов за проход в пределах бюджета: YouTube, Telegram и VK по каждому запросу.
func (u *U) PostSearchCollect(topic string) PostSearchResult {
	deadline := time.Now().Add(postSearchSeconds * time.Second)
	res := PostSearchResult{By: map[string][]jx.M{PlatformTelegram: {}, PlatformRSS: {}, PlatformVK: {}}, Done: map[string]int{"youtube": 0, "telegram": 0, "vk": 0}}
	res.Queries = u.postSearchQueries(topic)
	for _, q := range res.Queries {
		if time.Now().After(deadline) || !u.Alive() {
			break
		}
		res.Done["youtube"]++
		res.By[PlatformRSS] = append(res.By[PlatformRSS], u.PostSearchYouTube(q, PostSearchLimit)...)
		if time.Now().After(deadline) {
			break
		}
		res.Done["telegram"]++
		res.By[PlatformTelegram] = append(res.By[PlatformTelegram], u.PostSearchTelegram(q, PostSearchLimit)...)
		if time.Now().After(deadline) {
			break
		}
		res.Done["vk"]++
		res.By[PlatformVK] = append(res.By[PlatformVK], u.PostSearchVK(q, PostSearchLimit)...)
	}
	for p, posts := range res.By {
		var keep []jx.M
		for _, x := range posts {
			if !PostSearchIsAd(x) && u.PostSearchMatches(topic, x) && u.UAItemBlocked(x) == "" {
				keep = append(keep, x)
			}
		}
		res.By[p] = orEmptyList(keep)
	}
	return res
}
