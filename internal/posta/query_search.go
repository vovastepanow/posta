package posta

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
)

// Поиск по запросу: вместо каталога темы — посты площадок, найденные по формулировкам запроса.
// Каталог темы остаётся одной из площадок. Отбор — по словам запроса, один пост с источника.

const (
	querySearchPlanTTL     = 7 * 24 * time.Hour
	querySearchOffsetTTL   = 30 * 24 * time.Hour
	querySearchQueueMax    = 200
	querySearchPlatformCap = 0.25 // не больше этой доли очереди с одной площадки
	querySearchChannels    = 20   // каналов Telegram, которые читаем по запросу за проход
	querySearchNewsCount   = 30   // записей из одной новостной ленты
)

// QueryPlan — формулировки запроса: ru и en — слова поиска, hashtags — с «#», must — слова, без которых пост не про запрос.
type QueryPlan struct {
	RU       []string `json:"ru"`
	EN       []string `json:"en"`
	Hashtags []string `json:"hashtags"`
	Must     []string `json:"must"`
}

// QuerySearchPlan — формулировки запроса. Кэш на 7 дней; без модели — сам запрос.
func (u *U) QuerySearchPlan(query, topic string) QueryPlan {
	key := "query_plan:" + sha1Short(query+"|"+topic, 20)
	var cached QueryPlan
	if u.CacheGet(u.Ctx, key, &cached) && len(cached.RU) > 0 {
		return cached
	}
	plan, ok := u.querySearchPlanModel(query, topic)
	if !ok {
		return queryPlanFallback(query)
	}
	u.CacheSet(u.Ctx, key, plan, querySearchPlanTTL)
	return plan
}

// querySearchPlanModel — один вызов модели: русские и английские формулировки, хэштеги, обязательные слова.
func (u *U) querySearchPlanModel(query, topic string) (QueryPlan, bool) {
	str := jx.M{"type": "string"}
	list := jx.M{"type": "array", "items": str, "maxItems": 5}
	schema := jx.M{"type": "object", "properties": jx.M{"ru": list, "en": list, "hashtags": list, "must": list},
		"required": []string{"ru", "en", "hashtags", "must"}}
	prompt := "Пользователь ищет посты в соцсетях, на видеосервисах и в новостях: «" + query + "». Тема подборки: «" + topic + "».\n\n" +
		"Составь формулировки для поиска по площадкам:\n" +
		"ru — 3–5 русских формулировок: синонимы и разговорные варианты;\n" +
		"en — 1–2 английских формулировки;\n" +
		"hashtags — 3–5 хэштегов без пробелов, без знака #;\n" +
		"must — 1–3 обязательных слова или имени: пост без них точно не про запрос.\n" +
		"Ответь JSON по схеме."
	resp, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Schema: schema, MaxTokens: 300, Temperature: 0, UserID: u.ID, Quiet: true})
	if err != nil {
		return QueryPlan{}, false
	}
	a := llm.DecodeJSON(resp.Text)
	if a == nil {
		return QueryPlan{}, false
	}
	plan := QueryPlan{RU: cleanPhrases(jx.Strs(a, "ru"), 5), EN: cleanPhrases(jx.Strs(a, "en"), 2),
		Hashtags: hashtagsFrom(jx.Strs(a, "hashtags")), Must: cleanPhrases(jx.Strs(a, "must"), 3)}
	if len(plan.RU) == 0 {
		return QueryPlan{}, false
	}
	if len(plan.Must) == 0 {
		plan.Must = []string{longestWord(query)}
	}
	return plan, true
}

// queryPlanFallback — без модели: сам запрос, хэштег из него и самое длинное слово как обязательное.
func queryPlanFallback(query string) QueryPlan {
	return QueryPlan{RU: []string{query}, Hashtags: []string{"#" + spacesAllRe.ReplaceAllString(query, "")}, Must: []string{longestWord(query)}}
}

func cleanPhrases(list []string, n int) []string {
	var out []string
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	out = uniqueStrings(out)
	return out[:min(len(out), n)]
}

func hashtagsFrom(tags []string) []string {
	var out []string
	for _, t := range tags {
		if t = "#" + spacesAllRe.ReplaceAllString(strings.TrimLeft(strings.TrimSpace(t), "#"), ""); t != "#" {
			out = append(out, t)
		}
	}
	out = uniqueStrings(out)
	return out[:min(len(out), 5)]
}

func longestWord(s string) string {
	best := ""
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if runeLen(w) > runeLen(best) {
			best = w
		}
	}
	return best
}

// querySearchOffset — номер прохода по запросу: каждый следующий проход берёт следующие формулировки.
func (u *U) querySearchOffset(key string) int {
	pass, _ := KVUpdate(u.App, u.Ctx, "query_search:offset:"+key, querySearchOffsetTTL, func(n int, _ bool) *int { n++; return &n })
	return pass - 1
}

// QuerySearchCollect — посты площадок по запросу в пределах query_search_seconds, отобранные по словам и по одному с источника.
func (u *U) QuerySearchCollect(key string) map[string][]jx.M {
	query, topic := u.CurrentQuery(), u.CurrentTopic()
	plan := u.QuerySearchPlan(query, topic)
	off := u.querySearchOffset(key)
	ru, en, tags := DiscoveryRotate(plan.RU, off), DiscoveryRotate(plan.EN, off), DiscoveryRotate(plan.Hashtags, off)
	deadline := time.Now().Add(time.Duration(u.TInt("query_search_seconds")) * time.Second)
	got := map[string][]jx.M{}
	counts := map[string]int{}
	// каждая площадка — отдельно: сбой или нехватка времени не роняют остальные
	step := func(name, plat string, fetch func() []jx.M) {
		if time.Now().After(deadline) || !u.Alive() {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				u.Log("Поиск «%s»: %s — ошибка, пропускаю: %v", query, name, r)
			}
		}()
		posts := fetch()
		got[plat] = append(got[plat], posts...)
		counts[name] += len(posts)
	}
	if len(tags) > 0 {
		step("Telegram", PlatformTelegram, func() []jx.M { return u.PostSearchTelegram(tags[0], PostSearchLimit) })
	}
	if len(ru) > 0 {
		step("Telegram", PlatformTelegram, func() []jx.M { return u.PostSearchTelegram(ru[0], PostSearchLimit) })
		step("Telegram каналы", PlatformTelegram, func() []jx.M { return u.querySearchTelegramChannels(ru, plan.Must) })
		step("YouTube", PlatformRSS, func() []jx.M { return u.PostSearchYouTube(ru[0], PostSearchLimit) })
		step("Новости", PlatformRSS, func() []jx.M { return u.queryNewsRSS(ru[0]) })
		step("vc/dtf", PlatformRSS, func() []jx.M { return u.queryOsnovaSearch(ru[0]) })
		for _, q := range ru[:min(len(ru), 2)] {
			q := q
			step("VK", PlatformVK, func() []jx.M { return u.PostSearchVK(q, PostSearchLimit) })
		}
	}
	if len(en) > 0 {
		step("YouTube", PlatformRSS, func() []jx.M { return u.PostSearchYouTube(en[0], PostSearchLimit) })
	}
	if len(tags) > 0 {
		step("TikTok", PlatformTikTok, func() []jx.M { return u.PostSearchTikTok(tags[0], PostSearchLimit) })
	}
	// каталог темы — одна из площадок; старый путь сборки, но без встроенных поисков (их заменяет этот сборщик)
	if time.Now().Before(deadline) && u.Alive() {
		for p, posts := range u.collectPostsByPlatform(key) {
			got[p] = append(got[p], posts...)
			counts["каталог"] += len(posts)
		}
	}
	u.Log("Поиск «%s»: Telegram %d (каналов %d), VK %d, YouTube %d, Новости %d, vc/dtf %d, TikTok %d, каталог %d.",
		query, counts["Telegram"], counts["Telegram каналы"], counts["VK"], counts["YouTube"], counts["Новости"], counts["vc/dtf"], counts["TikTok"], counts["каталог"])
	ranked := rankQuerySearch(got, plan, u.TInt("content_max_age"), func(it jx.M) bool { return u.UAItemBlocked(it) != "" })
	n := 0
	for _, posts := range ranked {
		n += len(posts)
	}
	u.Log("После отбора по словам запроса: %d постов, по одному с источника.", n)
	return ranked
}

// querySearchTelegramChannels — каналы Telegram по запросу через аккаунт владельца; читаем их посты, в каталог каналы не добавляем.
func (u *U) querySearchTelegramChannels(ru, must []string) []jx.M {
	chans := u.discoveryTelegramAccountChannels(ru[:min(len(ru), 2)], nil)
	var out []jx.M
	for _, ch := range chans[:min(len(chans), querySearchChannels)] {
		name := ch.Username
		if !IsValidTelegramChannel(name) {
			continue
		}
		page := u.FetchTelegramChannelPage(name, 0)
		if page == "" {
			continue
		}
		src := jx.M{"id": name, "name": SourceDisplayName(PlatformTelegram, name)}
		for _, p := range TelegramPostsFromHTML(page, src, config.TelegramPostsPerChannel, false, 120) {
			if !queryTextHitAny(jx.Str(p, "text"), must) {
				continue
			}
			p["source_key"] = SourceIDFor(PlatformTelegram, name)
			p["source_url"] = "https://t.me/" + name
			p["found_by"] = "поиск Telegram «" + ru[0] + "»"
			out = append(out, p)
		}
	}
	return out
}

func queryTextHitAny(text string, words []string) bool {
	for _, w := range words {
		if queryWordHit(strings.ToLower(text), w) {
			return true
		}
	}
	return false
}

// queryNewsRSS — Google News и Bing News по запросу.
func (u *U) queryNewsRSS(q string) []jx.M {
	feeds := []string{
		"https://news.google.com/rss/search?q=" + url.QueryEscape(q+" when:2d") + "&hl=ru&gl=RU&ceid=RU:ru",
		"https://www.bing.com/news/search?q=" + url.QueryEscape(q) + "&format=rss&setlang=ru",
	}
	var out []jx.M
	for _, feed := range feeds {
		out = append(out, u.newsFeedPosts(feed, q)...)
	}
	return out
}

// newsFeedPosts — записи одной новостной ленты; издание — source_key rss:<домен>.
func (u *U) newsFeedPosts(feed, q string) []jx.M {
	r := u.RSSGet(feed, rssMaxBytes, 15*time.Second, HTTPUA)
	if !r.OK {
		return nil
	}
	posts, err := RSSParse(r.Bytes, querySearchNewsCount, "", r.URL, true, true, 120)
	if err != nil {
		return nil
	}
	publisher := googleNewsPublishers(rssLoad(r.Bytes))
	for _, p := range posts {
		link := jx.Str(p, "url")
		host := publisher[link]
		if host == "" {
			host = hostOf(link)
		}
		p["source"] = host
		p["source_key"] = SourceIDFor(PlatformRSS, host)
		p["source_url"] = "https://" + host
		p["found_by"] = "поиск новостей «" + q + "»"
	}
	return posts
}

// googleNewsPublishers — ссылка новости → домен издания из <source url=…>. У Bing такого элемента нет: домен берётся из ссылки.
func googleNewsPublishers(doc *xnode) map[string]string {
	out := map[string]string{}
	if doc == nil || !rssIsFeed(doc) {
		return out
	}
	for _, item := range rssItems(doc) {
		src := item.kid("source")
		if src == nil || src.A["url"] == "" {
			continue
		}
		if link := rssItemLink(item); link != "" {
			out[link] = hostOf(src.A["url"])
		}
	}
	return out
}

// queryOsnovaSearch — статьи vc.ru и dtf.ru по запросу: в ответе поиска уже есть текст статьи.
func (u *U) queryOsnovaSearch(q string) []jx.M {
	var out []jx.M
	for _, host := range []string{"vc.ru", "dtf.ru"} {
		body, status, err := u.HTTPGetStatus("https://api."+host+"/v2.10/search/posts?q="+url.QueryEscape(q), 15*time.Second, map[string]string{"User-Agent": HTTPUA})
		if err != nil || status != 200 {
			continue
		}
		for _, it := range jx.Maps(jx.Map(jx.Unmarshal(body), "result"), "items") {
			if p := osnovaSearchPost(jx.Map(it, "data"), host, q); p != nil {
				out = append(out, p)
			}
		}
	}
	return out
}

// osnovaSearchPost — запись из результата поиска Osnova; nil — нет ссылки или статьи.
func osnovaSearchPost(d jx.M, host, q string) jx.M {
	link := jx.Str(d, "url")
	if link == "" {
		return nil
	}
	a := OsnovaFromAPI(jx.Marshal(jx.M{"result": d}), link)
	if a == nil {
		return nil
	}
	p := jx.M{"url": link, "posted_at": jx.Int(d, "date"), "source": host, "source_key": SourceIDFor(PlatformRSS, host),
		"source_url": "https://" + host, "found_by": "поиск " + host + " «" + q + "»", "image": "", "images": []any{}, "gallery": []any{},
		"video": "", "text_post": false, "video_unavailable": false}
	articleApplyOsnova(p, a, link)
	return p
}

// queryWordHit — основа слова есть в тексте (как в PostSearchMatches): «тюмень» находит «Тюменский».
func queryWordHit(text, w string) bool {
	w = strings.ToLower(strings.TrimSpace(w))
	stem := trimRunes(w, max(3, runeLen(w)-2))
	return stem != "" && strings.Contains(text, stem)
}

// queryPhraseHit — есть все слова формулировки (короткие предлоги не считаем).
func queryPhraseHit(text, phrase string) bool {
	n := 0
	for _, w := range strings.Fields(strings.ToLower(phrase)) {
		if runeLen(w) < 3 {
			continue
		}
		if !queryWordHit(text, w) {
			return false
		}
		n++
	}
	return n > 0
}

// queryLex — насколько пост про запрос: 1 — есть все обязательные слова; 0.6 — часть из них или целая формулировка; 0 — не про запрос.
func queryLex(item jx.M, plan QueryPlan) float64 {
	text := strings.ToLower(jx.Str(item, "title") + " " + jx.Str(item, "text") + " " + jx.Str(item, "thread_title"))
	hit := 0
	for _, w := range plan.Must {
		if queryWordHit(text, w) {
			hit++
		}
	}
	if len(plan.Must) > 0 && hit == len(plan.Must) {
		return 1
	}
	if hit > 0 {
		return 0.6
	}
	for _, f := range append(append([]string{}, plan.RU...), plan.EN...) {
		if queryPhraseHit(text, f) {
			return 0.6
		}
	}
	return 0
}

// queryDedupKey — один и тот же пост или картинка на разных площадках.
func queryDedupKey(it jx.M) string {
	if img := jx.Str(it, "image"); img != "" {
		return "img:" + img
	}
	if t := CandidateTextKey(it); t != "" {
		return "txt:" + t
	}
	return "url:" + jx.Str(it, "url")
}

// rankQuerySearch — отбор до моделей: без рекламы, украинского и старого; по словам запроса; дубли — оставляем раньше опубликованный;
// один пост с источника (лучший по словам × горячесть); доля площадки не больше querySearchPlatformCap; очередь до querySearchQueueMax.
func rankQuerySearch(by map[string][]jx.M, plan QueryPlan, maxAge int, skip func(jx.M) bool) map[string][]jx.M {
	type cand struct {
		plat string
		item jx.M
		lex  float64
	}
	plats := make([]string, 0, len(by))
	for p := range by {
		plats = append(plats, p)
	}
	sort.Strings(plats)
	var all []cand
	for _, p := range plats {
		for _, it := range by[p] {
			if PostSearchIsAd(it) || skip(it) || IsPostOutdated(jx.Int(it, "posted_at"), 0, maxAge) {
				continue
			}
			if lex := queryLex(it, plan); lex > 0 {
				all = append(all, cand{p, it, lex})
			}
		}
	}
	earliest := map[string]int{}
	for i, c := range all {
		k := queryDedupKey(c.item)
		if j, ok := earliest[k]; !ok || jx.Int(c.item, "posted_at") < jx.Int(all[j].item, "posted_at") {
			earliest[k] = i
		}
	}
	var kept []cand
	var keptItems []jx.M
	for i, c := range all {
		if earliest[queryDedupKey(c.item)] == i {
			kept = append(kept, c)
			keptItems = append(keptItems, c.item)
		}
	}
	heat := hotRelativeHeat(keptItems)
	score := make([]float64, len(kept))
	for i, c := range kept {
		score[i] = c.lex * heat[i]
	}
	bestBySrc := map[string]int{}
	for i, c := range kept {
		k := ContentSourceKey(c.item)
		if j, ok := bestBySrc[k]; !ok || score[i] > score[j] {
			bestBySrc[k] = i
		}
	}
	var one []int
	for _, i := range bestBySrc {
		one = append(one, i)
	}
	sort.SliceStable(one, func(a, b int) bool {
		x, y := one[a], one[b]
		if kept[x].lex != kept[y].lex {
			return kept[x].lex > kept[y].lex
		}
		if score[x] != score[y] {
			return score[x] > score[y]
		}
		return jx.Int(kept[x].item, "posted_at") > jx.Int(kept[y].item, "posted_at")
	})
	limitPlat := int(querySearchPlatformCap * querySearchQueueMax)
	per := map[string]int{}
	out := map[string][]jx.M{}
	n := 0
	for _, i := range one {
		c := kept[i]
		if n >= querySearchQueueMax {
			break
		}
		if per[c.plat] >= limitPlat {
			continue
		}
		per[c.plat]++
		n++
		out[c.plat] = append(out[c.plat], c.item)
	}
	return out
}
