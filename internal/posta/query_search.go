package posta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
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
	prompt := u.prompt("query_plan", map[string]string{"query": query, "topic": topic})
	settings := u.LLM.Settings()
	private := u.TopicIsRestricted(topic)
	key := queryScoreCacheKey("plan", u.ID, prompt, string(jx.Marshal([]any{settings.Roles[llm.RoleSmart], settings.FastOnly, private})), "", "")
	var cached QueryPlan
	if u.CacheGet(u.Ctx, key, &cached) && len(cached.RU) > 0 {
		return queryPlanIncludeOriginal(query, cached)
	}
	plan, ok := u.querySearchPlanModel(query, topic)
	if !ok {
		return queryPlanFallback(query)
	}
	plan = queryPlanIncludeOriginal(query, plan)
	u.CacheSet(u.Ctx, key, plan, querySearchPlanTTL)
	return plan
}

// querySearchPlanModel — один вызов модели: русские и английские формулировки, хэштеги, обязательные слова.
func (u *U) querySearchPlanModel(query, topic string) (QueryPlan, bool) {
	str := jx.M{"type": "string"}
	list := jx.M{"type": "array", "items": str, "maxItems": 5}
	schema := jx.M{"type": "object", "properties": jx.M{"ru": list, "en": list, "hashtags": list, "must": list},
		"required": []string{"ru", "en", "hashtags", "must"}}
	prompt := u.prompt("query_plan", map[string]string{"query": query, "topic": topic})
	resp, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Schema: schema, MaxTokens: 300, Temperature: 0, UserID: u.ID, Quiet: true, Private: u.TopicIsRestricted(topic)})
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

// Always search the user's wording as well as model-generated alternatives.
func queryPlanIncludeOriginal(query string, plan QueryPlan) QueryPlan {
	query = strings.TrimSpace(query)
	if query == "" {
		return plan
	}
	var ru []string
	ru = append(ru, query)
	for _, term := range plan.RU {
		if TopicNorm(term) != TopicNorm(query) {
			ru = append(ru, term)
		}
	}
	plan.RU = cleanPhrases(ru, 5)
	return plan
}

// queryPlanFallback — без модели: сам запрос, хэштег из него и самое длинное слово как обязательное.
func queryPlanFallback(query string) QueryPlan {
	return QueryPlan{RU: []string{query}, EN: []string{query}, Hashtags: []string{"#" + spacesAllRe.ReplaceAllString(query, "")}, Must: []string{longestWord(query)}}
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

// QuerySearchCollect — посты площадок по запросу в пределах query_search_seconds, отобранные по словам.
func (u *U) QuerySearchCollect(key string) map[string][]jx.M {
	query, topic := u.CurrentQuery(), u.CurrentTopic()
	plan := u.QuerySearchPlan(query, topic)
	u.Log("Поиск: «%s» — ищу посты по площадкам (тема «%s» — только для оценки).", query, topic)
	off := u.querySearchOffset(key)
	ru := DiscoveryRotate(plan.RU, off)
	// площадки — параллельно, в одной общей очереди на площадку (см. search_engine.go); бюджет — query_search_seconds
	got, counts := u.SearchEngineRun(plan, off, time.Duration(u.TInt("query_search_seconds"))*time.Second)
	// каналы Telegram по запросу — через аккаунт владельца, после опроса площадок (у аккаунта свой лимит)
	if u.TBool("search_telegram") && len(ru) > 0 && u.Alive() {
		chans := u.querySearchTelegramChannels(ru, plan.Must)
		got[PlatformTelegram] = append(got[PlatformTelegram], chans...)
		counts["Telegram каналы"] = len(chans)
	}
	// Collect the actual topic sources, including imageboards and uncached
	// catalog entries; previously a new topic could see an empty cache forever.
	for p, posts := range u.collectPostsByPlatform(topic) {
		got[p] = append(got[p], posts...)
		counts["каталог"] += len(posts)
	}
	ranked := rankQuerySearch(got, plan, u.TInt("content_max_age"), func(it jx.M) bool { return u.UAItemBlocked(it) != "" })
	// Semantic candidates must enter after the lexical gate. Otherwise a
	// relevant synonym or a translation can be rejected before the judge.
	if !u.TopicIsRestricted(topic) && !u.TopicIsRestricted(query) {
		if policy, err := u.collectorSourcePolicy(key); err == nil {
			allowed := func(id string) bool { return policy(jx.M{"source_key": id}) == "" }
			for platform, posts := range u.searchPublicInventory(query, allowed, append(append([]string(nil), plan.RU...), plan.EN...)...) {
				ranked[platform] = mergePublicPosts(ranked[platform], posts)
			}
		}
	}
	ranked = u.queryRerank(query, topic, plan, ranked)
	n := 0
	for _, posts := range ranked {
		n += len(posts)
	}
	u.Log("После текстового и семантического поиска: %d кандидатов.", n)
	return ranked
}

// querySearchTelegramChannels — каналы Telegram по запросу через аккаунт владельца; читаем их посты, в каталог каналы не добавляем.
// Seed cold-start post search with curated topic vocabulary even when the
// query-planning model is unavailable or returns only the broad title.
func topicFastQueryPlan(plan QueryPlan, topic *Topic) QueryPlan {
	if topic == nil || len(plan.RU) == 0 {
		return plan
	}
	terms := []string{plan.RU[0]}
	terms = append(terms, topic.Queries...)
	if topic.Profile != nil {
		terms = append(terms, topic.Profile.Queries...)
	}
	terms = append(terms, topic.Aliases...)
	terms = append(terms, plan.RU[1:]...)
	var cleaned []string
	for _, term := range terms {
		if phrase := DiscoveryCleanQuery(term); phrase != "" {
			cleaned = append(cleaned, phrase)
		}
	}
	if len(cleaned) > 0 {
		plan.RU = cleanPhrases(cleaned, 8)
	}
	for _, alias := range topic.Aliases {
		if alias = DiscoveryCleanQuery(alias); alias != "" && !cyrRe.MatchString(alias) {
			plan.EN = append(plan.EN, alias)
		}
	}
	plan.EN = cleanPhrases(plan.EN, 3)
	return plan
}

// TopicFastSearchCollect searches public posts while discovery is still
// establishing sources for a new topic. Results enter the normal collector
// admission pipeline; no newly found source is automatically trusted.
func (u *U) TopicFastSearchCollect(topic string) map[string][]jx.M {
	if !u.TBool("discovery") || u.TopicIsRestricted(topic) || strings.TrimSpace(topic) == "" {
		return nil
	}
	plan := topicFastQueryPlan(u.QuerySearchPlan(TopicBase(topic), topic), u.TopicLookup(TopicBase(topic)))
	if len(plan.RU) == 0 {
		return nil
	}
	// Broad topics have no single mandatory keyword.
	plan.Must = nil
	off := u.querySearchOffset("topic-fast:" + TopicNorm(topic))
	seconds := max(15, min(90, u.TInt("query_search_seconds")))
	got, _ := u.SearchEngineRun(plan, off, time.Duration(seconds)*time.Second)
	ranked := rankQuerySearch(got, plan, u.TInt("content_max_age"), func(it jx.M) bool { return u.UAItemBlocked(it) != "" })
	tagFastTopicResults(ranked, topic)
	count := 0
	for _, posts := range ranked {
		count += len(posts)
	}
	u.Log("Быстрый поиск по теме «%s»: %d публикаций для стандартной проверки.", topic, count)
	return ranked
}

// A post that passes the collector's full checks can suggest its author as
// a source through PostSearchSuggest. Keep the search origin even when a
// platform backend did not add its own found_by label.
func tagFastTopicResults(by map[string][]jx.M, topic string) {
	for _, posts := range by {
		for _, post := range posts {
			if jx.Str(post, "found_by") == "" {
				post["found_by"] = "быстрый поиск по теме «" + topic + "»"
			}
		}
	}
}

func (u *U) querySearchTelegramChannels(ru, must []string) []jx.M {
	chans := u.discoveryTelegramAccountChannels(ru[:min(len(ru), 2)], nil)
	var out []jx.M
	withText := u.TBool("text_posts") && u.TopicFormatOf(u.CurrentTopic()).Media == "optional"
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
		for _, p := range TelegramPostsFromHTML(page, src, config.TelegramPostsPerChannel, withText, 120) {
			if !queryTextHitAny(jx.Str(p, "text"), must) && (jx.Str(p, "image") == "" || PostCaptionForAI(p) != "") {
				continue
			}
			p["source_key"] = SourceIDFor(PlatformTelegram, name)
			p["source_url"] = jx.Str(p, "url")
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
	for i, p := range posts {
		posts[i] = u.ArticleEnrich(p, false)
		p = posts[i]
		link := jx.Str(p, "url")
		host := publisher[link]
		if host == "" {
			host = hostOf(link)
		}
		p["source"] = host
		p["source_key"] = SourceIDFor(PlatformRSS, host)
		p["source_url"] = link
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
		if link := rssItemLink(item, ""); link != "" {
			out[link] = hostOf(src.A["url"])
		}
	}
	return out
}

// queryOsnovaSearch — статьи vc.ru и dtf.ru по запросу: в ответе поиска уже есть текст статьи.
func (u *U) queryOsnovaSearch(q string) []jx.M {
	var out []jx.M
	for _, host := range []string{"vc.ru", "dtf.ru"} {
		endpoint := "https://api." + host + "/v2.10/search/posts?q=" + url.QueryEscape(q)
		var body []byte
		if _, leased := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); leased {
			// Public autopilot calls must consume the same network budget
			// as all other source adapters, not the legacy App HTTP path.
			r := u.RSSGet(endpoint, rssMaxBytes, 15*time.Second, HTTPUA)
			final, err := url.Parse(r.URL)
			if !r.OK || err != nil || final.Scheme != "https" || final.Host != "api."+host ||
				final.User != nil || final.Path != "/v2.10/search/posts" || final.Query().Get("q") != q {
				continue
			}
			body = r.Bytes
		} else {
			var status int
			var err error
			body, status, err = u.HTTPGetStatus(endpoint, 15*time.Second, map[string]string{"User-Agent": HTTPUA})
			if err != nil || status != 200 {
				continue
			}
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
		"source_url": link, "found_by": "поиск " + host + " «" + q + "»", "image": "", "images": []any{}, "gallery": []any{},
		"video": "", "text_post": false, "video_unavailable": false}
	articleApplyOsnova(p, a, link)
	if sub := jx.Map(d, "subsite"); jx.Int(sub, "id") > 0 {
		p["source_author_url"] = osnovaTimelineURL(host, jx.Int(sub, "id"))
		p["author_name"] = jx.Str(sub, "name")
		// Preserve the official API's publisher and article identity
		// for the public post-to-source gate, not just the site name.
		p["publisher_id"] = jx.Int(sub, "id")
		p["publisher_uri"] = jx.Str(sub, "uri")
		p["publisher_post_id"] = jx.Int(d, "id")
	}
	return p
}

func osnovaTimelineURL(host string, id int64) string {
	if (host != "vc.ru" && host != "dtf.ru") || id <= 0 {
		return ""
	}
	return fmt.Sprintf("https://api.%s/v2.10/timeline?sorting=new&subsitesIds=%d", host, id)
}

func osnovaTimelineSource(raw string) (string, int64) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Path != "/v2.10/timeline" {
		return "", 0
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "api.")
	if u.Hostname() != "api."+host || (host != "vc.ru" && host != "dtf.ru") {
		return "", 0
	}
	rawID := u.Query().Get("subsitesIds")
	if rawID == "" {
		rawID = u.Query().Get("subsitesids") // historical catalog IDs were lowercased
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return "", 0
	}
	return host, id
}

func osnovaTimelinePosts(data []byte, host string, subsiteID int64) ([]jx.M, error) {
	var response struct {
		Result struct {
			Items []struct {
				Data jx.M `json:"data"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var posts []jx.M
	for _, item := range response.Result.Items {
		if jx.Int(jx.Map(item.Data, "subsite"), "id") != subsiteID && jx.Int(item.Data, "subsiteId") != subsiteID {
			continue
		}
		if p := osnovaSearchPost(item.Data, host, ""); p != nil {
			// The subsite is the SOURCE (community/category), not the author
			// of an individual article. Retain publisher metadata separately.
			// Without this, the autopilot saw only "api.vc.ru" and could
			// mistakenly mark a multi-author category as a personal blog.
			subsite := jx.Map(item.Data, "subsite")
			p["publisher_name"] = trimRunes(CleanHTMLText(jx.Str(subsite, "name")), 180)
			p["publisher_description"] = trimRunes(CleanHTMLText(jx.Str(subsite, "description")), 800)
			p["publisher_uri"] = jx.Str(subsite, "uri")
			if jx.Str(subsite, "subtype") == "community" {
				p["publisher_kind"] = "category"
			}
			posts = append(posts, p)
		}
	}
	return posts, nil
}

// queryWordHit — основа слова есть в тексте (как в PostSearchMatches): «тюмень» находит «Тюменский».
func queryWordHit(text, w string) bool {
	w = strings.TrimSpace(w)
	return w != "" && QueryTextHit(strings.ReplaceAll(strings.ToLower(text), "ё", "е"), strings.ReplaceAll(strings.ToLower(w), "ё", "е"))
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
	if ImageboardItem(item) {
		return 0
	}
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
	if PostItemHasVideo(it) {
		// A thumbnail is not the identity of a video.
		if page := jx.Str(it, "video_page"); page != "" {
			return "video:" + page
		}
		if video := jx.Str(it, "video"); video != "" {
			return "video:" + video
		}
		return CandidatePostKey(it)
	}
	if img := ContentImageKey(it); img != "" {
		return "img:" + img
	}
	if t := CandidateTextKey(it); t != "" {
		return "txt:" + t
	}
	if k := CandidatePostKey(it); k != "" {
		return k
	}
	if t := strings.TrimSpace(AnalysisText(it)); t != "" {
		return "short:" + repeatDigest(t)
	}
	return ""
}

// rankQuerySearch — отбор до моделей: без рекламы, украинского и старого; по словам запроса; дубли — оставляем раньше опубликованный;
// посты ранжируются по словам и горячести; доля площадки не больше querySearchPlatformCap; очередь до querySearchQueueMax.
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
			it = ImageboardMediaOnly(it)
			if PostSearchIsAd(it) || skip(it) || IsPostOutdated(jx.Int(it, "posted_at"), 0, maxAge) {
				continue
			}
			lex := queryLex(it, plan)
			// Image-only posts may contain the query in the image. Keep a small,
			// low-priority sample for the vision model; never infer a match.
			if lex == 0 && jx.Str(it, "image") != "" && (ImageboardItem(it) || strings.TrimSpace(jx.Str(it, "title")+PostCaptionForAI(it)) == "") {
				lex = 0.2
			}
			if lex > 0 {
				all = append(all, cand{p, it, lex})
			}
		}
	}
	earliest := map[string]int{}
	for i, c := range all {
		k := queryDedupKey(c.item)
		if k == "" {
			continue
		}
		if j, ok := earliest[k]; !ok || jx.Int(c.item, "posted_at") < jx.Int(all[j].item, "posted_at") {
			earliest[k] = i
		}
	}
	var kept []cand
	var keptItems []jx.M
	for i, c := range all {
		k := queryDedupKey(c.item)
		if k == "" || earliest[k] == i {
			kept = append(kept, c)
			keptItems = append(keptItems, c.item)
		}
	}
	heat := hotRelativeHeat(keptItems)
	score := make([]float64, len(kept))
	for i, c := range kept {
		score[i] = c.lex * heat[i]
	}
	one := make([]int, len(kept))
	for i := range kept {
		one[i] = i
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
	// First offer each source a candidate, then its remaining posts. The
	// collector can discard an already-seen first post and still use another.
	one = interleaveByKey(one, func(i int) string { return ContentSourceKey(kept[i].item) })
	limitPlat := int(querySearchPlatformCap * querySearchQueueMax)
	per := map[string]int{}
	out := map[string][]jx.M{}
	n, unverified := 0, 0
	for _, i := range one {
		c := kept[i]
		if c.lex == 0.2 && unverified >= 20 {
			continue
		}
		if n >= querySearchQueueMax {
			break
		}
		if per[c.plat] >= limitPlat {
			continue
		}
		per[c.plat]++
		if c.lex == 0.2 {
			unverified++
		}
		n++
		out[c.plat] = append(out[c.plat], c.item)
	}
	return out
}

// catalogCachedPosts — посты источников каталога темы из кэша опроса (без сети); по словам запроса отбирает rankQuerySearch.
func (u *U) catalogCachedPosts(topic string) map[string][]jx.M {
	type cachedPosts struct {
		At    int64  `json:"at"`
		Posts []jx.M `json:"posts"`
	}
	out := map[string][]jx.M{}
	d := u.Catalog()
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if !u.CatalogEntryMatchesTopic(e, topic, false) {
				continue
			}
			var cp cachedPosts
			id := strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@")
			if u.UASourceBlocked(SourceIDFor(c.Platform, id), jx.M{"title": jx.Str(e, "name")}) != "" {
				continue
			}
			if sharedSourceEligible(c.Platform, e) && u.CacheGet(u.Ctx, sharedSourceKey(c.Platform, id), &cp) {
				out[c.Platform] = append(out[c.Platform], cp.Posts...)
			} else if u.CacheGet(u.Ctx, personalSourcePollKey(u.ID, c.Platform, jx.Str(e, "id")), &cp) {
				out[c.Platform] = append(out[c.Platform], cp.Posts...)
			}
		}
	}
	return out
}

// Reject a malformed batch rather than silently interpreting null or strings as zero.
func validQueryScores(scores []any, expected int) bool {
	if len(scores) != expected {
		return false
	}
	for _, raw := range scores {
		v, ok := raw.(float64)
		if !ok || !(v >= 0 && v <= 100) {
			return false
		}
	}
	return true
}

// queryRerankN — сколько лучших по словам запроса отдаём модели на оценку «про ли это».
const queryRerankN = 60

// Cache entries depend on the rendered prompt, exact evidence, owner and
// model route. Old scores cannot survive prompt edits or model changes.
func queryScoreCacheKey(kind, owner, prompt, settings, title, body string) string {
	return "query_" + kind + ":v3:" + repeatDigest(string(jx.Marshal([]string{owner, prompt, settings, title, body})))
}

// The final collector reorders candidates by source and freshness. Carry
// an optional text-match hint through that stage without treating it as a
// final verdict, especially for video and images with incomplete captions.
func queryRerankHint(item jx.M, query string) float64 {
	if query == "" || jx.Str(item, "_query_rank_for") != TopicNorm(query) || !validModelPercent(item["_query_rank_score"]) {
		return 50
	}
	score, _ := modelNumber(item["_query_rank_score"])
	return score
}

func withQueryRerankHint(item jx.M, query string, score int) jx.M {
	copy := jx.Copy(item)
	copy["_query_rank_for"] = TopicNorm(query)
	copy["_query_rank_score"] = score
	return copy
}

func queryRerankMultiplier(item jx.M, query string) float64 {
	return 0.75 + queryRerankHint(item, query)/200
}

// Sensitive or owner-provided material stays in the candidate list, but
// the optional cloud reranker must not receive its text.
func queryRerankProtected(item jx.M) bool {
	source := ContentSourceKey(item)
	return strings.HasPrefix(source, "own:") || strings.HasPrefix(source, "source:") ||
		jx.Bool(item, "source_sensitive") || jx.Bool(item, "nsfw") || len(jx.ToStrs(item["safety"])) > 0
}

// queryRerank — текстовый реранк верхних queryRerankN записей (query_match 0–100).
// Без ответа модели список не меняется: отбор по словам уже сделан.
func (u *U) queryRerank(query, topic string, plan QueryPlan, ranked map[string][]jx.M) map[string][]jx.M {
	if u.TopicIsRestricted(topic) {
		return ranked
	}
	type ref struct {
		plat string
		item jx.M
		lex  float64
	}
	var refs []ref
	protected := map[string][]jx.M{}
	for _, p := range sortedKeys(ranked) {
		for _, it := range ranked[p] {
			if ImageboardItem(it) {
				protected[p] = append(protected[p], ImageboardMediaOnly(it))
				continue
			}
			if queryRerankProtected(it) || u.analysisPrivate(topic, it) {
				protected[p] = append(protected[p], it)
				continue
			}
			refs = append(refs, ref{p, it, queryLex(it, plan)})
		}
	}
	// верхние — по всем площадкам вместе: сначала совпадение слов, потом свежесть
	sort.SliceStable(refs, func(a, b int) bool {
		if refs[a].lex != refs[b].lex {
			return refs[a].lex > refs[b].lex
		}
		return jx.Int(refs[a].item, "posted_at") > jx.Int(refs[b].item, "posted_at")
	})
	n := min(len(refs), queryRerankN)
	if n == 0 {
		return ranked
	}
	promptTemplate := u.prompt("query_score", map[string]string{"query": query, "records": ""})
	settings := u.LLM.Settings()
	route := string(jx.Marshal([]any{settings.Roles[llm.RoleText], settings.FastOnly}))
	key := func(it jx.M) string {
		return queryScoreCacheKey("match", u.ID, promptTemplate, route,
			trimRunes(jx.Str(it, "title"), 120), AnalysisSnip(jx.Str(it, "text"), 300))
	}
	scores := make([]int, n)
	var todo []int
	for i := 0; i < n; i++ {
		var v int
		if u.CacheGet(u.Ctx, key(refs[i].item), &v) && v >= 0 && v <= 100 {
			scores[i] = v
		} else {
			todo = append(todo, i)
		}
	}
	if len(todo) > 0 && !u.Alive() {
		return ranked
	}
	if len(todo) > 0 {
		var lines []string
		for k, i := range todo {
			it := refs[i].item
			lines = append(lines, fmt.Sprintf("%d. Название: «%s». Текст: %s", k+1, trimRunes(jx.Str(it, "title"), 120), AnalysisSnip(jx.Str(it, "text"), 300)))
		}
		prompt := u.prompt("query_score", map[string]string{"query": query, "records": strings.Join(lines, "\n")})
		r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleText, Prompt: prompt, JSON: true, MaxTokens: 60 + 8*len(todo), Temperature: 0, UserID: u.ID, Quiet: true})
		var got []any
		if err == nil {
			got = jx.List(llm.DecodeJSON(r.Text), "scores")
		}
		if !validQueryScores(got, len(todo)) {
			u.Log("Поиск «%s»: оценка по тексту не получена — порядок по словам.", query)
			return ranked
		}
		for k, i := range todo {
			v := llm.ClampScore(jx.ToFloat(got[k]))
			scores[i] = v
			u.CacheSet(u.Ctx, key(refs[i].item), v, querySearchPlanTTL)
		}
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	out := map[string][]jx.M{}
	for _, i := range order {
		out[refs[i].plat] = append(out[refs[i].plat], withQueryRerankHint(refs[i].item, query, scores[i]))
	}
	for _, r := range refs[n:] {
		out[r.plat] = append(out[r.plat], r.item)
	}
	for p, items := range protected {
		out[p] = append(out[p], items...)
	}
	return out
}

// sortedKeys — ключи карты по порядку, чтобы результат не зависел от обхода map.
func sortedKeys(m map[string][]jx.M) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
