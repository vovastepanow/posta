package posta

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/jx"
)

// Сборщик: посты всех источников темы → очередь кандидатов → быстрая оценка пачками → полный анализ лучших → пул.

func PostCaptionForAI(item jx.M) string {
	t := strings.TrimSpace(jx.Str(item, "text"))
	if t == EmptyPostText {
		return ""
	}
	return t
}

func candidateLogLabel(item jx.M) string {
	u := jx.Str(item, "source_url")
	s := "[" + jx.Str(item, "source") + "]"
	if IsTrustedSourceURL(u) {
		s += "(" + u + ")"
	}
	return s
}

// ---- Сбор постов ----

// ParseDvach — посты свежих тредов доски; с темой — треды отбираются по ней.
// Оценка идёт по каталогу (название и шапка уже там), тела тредов грузим только у выбранных.
func (u *U) ParseDvach(board, topic string) []jx.M {
	catalog := u.DvachJSONCached(board, "")
	allThreads := jx.Maps(catalog, "threads")
	var pool []jx.M
	for _, t := range allThreads {
		if !jx.Bool(t, "sticky") && !jx.Bool(t, "endless") && !jx.Bool(t, "closed") && IsValidDvachID(jx.Str(t, "num")) {
			pool = append(pool, t)
		}
	}
	pool = pool[:min(len(pool), config.DvachThreadPool)]
	gates := map[string]*int{}
	if topic != "" {
		var infos []threadInfo
		for _, t := range pool {
			infos = append(infos, threadInfo{jx.Str(t, "num"), CleanHTMLText(jx.Str(t, "subject")), DvachPostText(t)})
		}
		gates = u.ImageboardThreadScores(topic, board, infos)
		var skipped []jx.M
		pool, skipped = DvachByTopic(pool, gates)
		for _, t := range skipped {
			id := jx.Str(t, "num")
			u.Log("-> Двач /%s/: тред «%s» не по теме (%d) — пропуск.", board, DvachThreadTitle(jx.Str(t, "subject"), id), *gates[id])
		}
	}
	pool = pool[:min(len(pool), config.DvachThreadsPerBoard)]
	type batch struct {
		thread, title string
		subject       jx.M
		posts         []jx.M
	}
	var batches []batch
	for _, t := range pool {
		id := jx.Str(t, "num")
		batches = append(batches, batch{id, DvachThreadTitle(jx.Str(t, "subject"), id), t, DvachThreadPosts(u.DvachJSONCached(board, id))})
	}
	var out []jx.M
	for _, b := range batches {
		gate := ImageboardThreadGate(gates[b.thread])
		opText := ""
		if len(b.posts) > 0 {
			opText = DvachPostText(b.posts[0])
		}
		eng := DvachThreadEngagement(b.posts, allThreads)
		title := CleanHTMLText(jx.Str(b.subject, "subject"))
		if title == "" {
			title = b.title
		}
		for _, post := range b.posts[:min(len(b.posts), config.DvachPostsPerThread)] {
			files := DvachPostMedia(board, post, true)
			if len(files) == 0 {
				continue
			}
			text := DvachPostText(post)
			id := jx.Str(post, "num")
			threadOp := trimRunes(opText, config.ThreadOpMax)
			if id == b.thread {
				threadOp = ""
			}
			rec := jx.M{"source_key": "dvach:" + board, "posted_at": jx.Int(post, "timestamp"), "board": board, "thread_id": b.thread, "post_id": id,
				"source": PlatformDvach + " /" + board + "/ · " + b.title, "source_url": DvachPostURL(board, b.thread, id),
				"image": files[0]["image"], "images": []any{files[0]["image"]}, "video": files[0]["video"], "captions": []any{text}, "text": text,
				"engagement": eng[id], "thread_title": trimRunes(title, config.ThreadTitleMax), "thread_op": threadOp, "is_op": id == b.thread, "thread_trust": gate == "trust"}
			if len(files) > 1 {
				rec["gallery"] = files
			}
			out = append(out, rec)
			if len(out) >= 200 {
				return out
			}
		}
	}
	return out
}

// DvachByTopic — треды доски по теме: ниже порога «не по теме» выкидываем, остальные — по убыванию оценки.
// Тред без оценки (nil) считаем «непонятно» — 50, он остаётся в очереди.
func DvachByTopic(pool []jx.M, gates map[string]*int) (keep, skipped []jx.M) {
	for _, t := range pool {
		if ImageboardThreadGate(gates[jx.Str(t, "num")]) == "skip" {
			skipped = append(skipped, t)
			continue
		}
		keep = append(keep, t)
	}
	score := func(t jx.M) int {
		if s := gates[jx.Str(t, "num")]; s != nil {
			return *s
		}
		return 50
	}
	sort.SliceStable(keep, func(i, j int) bool { return score(keep[i]) > score(keep[j]) })
	return keep, skipped
}

// collectCatalogSource — сбор одного источника каталога: посты, обновлённая запись каталога, событие активности.
func (u *U) collectCatalogSource(platform string, entry jx.M, activeOnly bool) ([]jx.M, jx.M, string) {
	id := strings.TrimLeft(strings.TrimSpace(jx.Str(entry, "id")), "@")
	if id == "" {
		return nil, nil, ""
	}
	label := id
	switch platform {
	case PlatformTelegram:
		label = "@" + id
	case PlatformRSS:
		label = hostOf(id)
	case PlatformOK:
		label = "ok:" + id
	case PlatformTikTok:
		label = "tiktok:" + id
	}
	if IsSourceFrozen(entry, 0) {
		return nil, nil, "skipped_frozen"
	}
	name := jx.Str(entry, "name")
	if name == "" {
		name = id
	}
	src := jx.M{"id": id, "name": SourceDisplayName(platform, name)}
	u.Log("Парсинг %s %s...", platform, label)
	var posts []jx.M
	var times []int64
	var subs int64
	var rate *float64
	gone := ""
	page := ""
	var err error
	acceptText := u.TopicsAcceptText(jx.Strs(entry, "topics"))
	switch platform {
	case PlatformTelegram:
		page = u.FetchTelegramChannelPage(id, 0)
		if page == "" {
			u.Log("-> %s %s: страница не загрузилась.", platform, label)
			return nil, nil, ""
		}
		posts = TelegramPostsFromHTML(page, src, config.TelegramPostsPerChannel, acceptText, 120)
		f := TelegramPageFacts(page)
		times, subs = f.PostTimes, f.Subscribers
		rate = SourceViewRate(subs, f.PostViews)
		if !strings.Contains(page, `data-post="`) {
			gone = "канала нет или он закрыт"
		}
	case PlatformRSS:
		withText := acceptText && !jx.Bool(entry, "news")
		posts, err = u.RSSFetchPosts(id, config.RSSPostsPerFeed, jx.Str(src, "name"), true, withText)
		if err == nil {
			posts = u.NewsEnrichPosts(posts, entry)
		}
	case PlatformOK:
		posts, err = u.OKFetchPosts(id, OKPostsPerGroup, jx.Str(src, "name"))
	case PlatformTikTok:
		posts, err = u.TikTokFetchPosts(u.Ctx, id, TikTokPostsPerAccount, jx.Str(src, "name"))
	default:
		posts = u.BuildVKPreviewPosts(u.Ctx, src, config.VKPostsPerPublic, acceptText, 120)
	}
	if err != nil {
		u.Log("-> %s %s: %s", platform, label, err)
		return nil, nil, ""
	}
	if platform != PlatformTelegram {
		for _, p := range posts {
			times = append(times, jx.Int(p, "posted_at"))
		}
	}
	activity := SourceActivity(times, 0)
	reason := gone
	if reason == "" {
		reason = SourceDeadAudienceReason(subs, rate)
	}
	updated, event := ApplySourceActivity(entry, activity, 0, reason)
	if subs > 0 {
		aud := jx.M{"subscribers": subs, "view_rate": nil, "checked_at": now()}
		if rate != nil {
			aud["view_rate"] = *rate
		}
		updated["audience"] = aud
	}
	if platform == PlatformTelegram {
		if m := ogImageRe.FindStringSubmatch(page); m != nil {
			updated["avatar"] = htmlAttr(m[1])
		}
	}
	if prev := SourcePreviewImages(posts); len(prev) > 0 {
		updated["preview"] = prev
	}
	if event == "frozen" || event == "revived" {
		status := map[string]string{"frozen": RegStatusDead, "revived": RegStatusCatalog}[event]
		u.registryUpsert(registryWrite{SourceID: SourceIDFor(platform, jx.Str(entry, "id")), Platform: platform, Status: status,
			Reason: map[string]string{"frozen": "заморожен: " + jx.Str(updated, "frozen_reason"), "revived": "в каталоге"}[event]})
	}
	if event == "frozen" || event == "still_frozen" {
		skip := activeOnly || gone != ""
		what := "всё ещё не активен"
		if event == "frozen" {
			what = "заморожен"
		}
		tail := "."
		if !skip {
			tail = " — «Только актуальные источники» выключено, посты всё равно берутся."
		}
		u.Log("-> %s %s: %s (%s), перепроверка через %d дн%s", platform, label, what, jx.Str(updated, "frozen_reason"), config.SourceFrozenRecheckDays, tail)
		if skip {
			return nil, updated, event
		}
	}
	if event == "revived" {
		u.Log("-> %s %s: ожил и разморожен (%s).", platform, label, DescribeSourceActivity(activity))
	}
	var fresh []jx.M
	for _, p := range posts {
		if !NewsPostOutdated(p, entry) {
			fresh = append(fresh, p)
		}
	}
	if platform == PlatformTelegram {
		fresh = u.TelegramBigVideosViaAccount(fresh)
	}
	var playable []jx.M
	for _, p := range fresh {
		if !jx.Bool(p, "video_unavailable") {
			playable = append(playable, p)
		}
	}
	old, hidden := len(posts)-len(fresh), len(fresh)-len(playable)
	line := fmt.Sprintf("-> %s %s: найдено медиа-постов: %d", platform, label, len(playable))
	if old > 0 {
		line += fmt.Sprintf(", устаревших пропущено: %d", old)
	}
	if hidden > 0 {
		line += fmt.Sprintf(", больших видео без файла пропущено: %d", hidden)
	}
	line += " (" + DescribeSourceActivity(activity)
	if subs > 0 {
		line += "; " + DescribeSourceAudience(subs, rate, nil)
	}
	u.Log("%s).", line)
	key := SourceIDFor(platform, id)
	out := make([]jx.M, 0, len(playable))
	for _, p := range playable {
		q := jx.Copy(p)
		q["source_key"], q["source_url"] = key, jx.Str(p, "url")
		if !jx.Has(q, "video") {
			q["video"] = ""
		}
		out = append(out, q)
	}
	return out, updated, event
}

// dvachBoardsThisRun — длинный список досок обходится порциями: половина — лучшие по прошлым запускам, остальное — случайные.
func (u *U) dvachBoardsThisRun(boards []string, topic string) []string {
	if len(boards) <= config.DvachBoardsPerRun {
		return boards
	}
	rates := u.CombinedSourceRates(u.Ctx, SourcePassRates(u.TopicVerdictStats(topic)), topic)
	rate := func(b string, def float64) float64 {
		if v, ok := rates["dvach:"+b]; ok {
			return v
		}
		return def
	}
	var always, proven []string
	for _, b := range boards {
		if contains(config.DvachAlwaysBoards, b) {
			always = append(always, b)
		} else if rate(b, 0) >= 0.2 {
			proven = append(proven, b)
		}
	}
	sort.SliceStable(proven, func(i, j int) bool { return rate(proven[i], 0) > rate(proven[j], 0) })
	chosen := append(always, proven[:min(len(proven), config.DvachBoardsPerRun/2)]...)
	var rest []string
	for _, b := range boards {
		if !contains(chosen, b) {
			rest = append(rest, b)
		}
	}
	rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	chosen = append(chosen, rest[:min(len(rest), config.DvachBoardsPerRun-len(chosen))]...)
	u.Log("Двач: в списке %d досок, в этот запуск — %d (лучшие по прошлым запускам и случайные, остальные — в следующие запуски).", len(boards), len(chosen))
	return chosen
}


func platformLabel(platform string, entry jx.M) string {
	if platform == PlatformRSS {
		k := RSSFeedKind(jx.Str(entry, "id"))
		if k == "YouTube" || k == "YouTube Shorts" {
			return "youtube"
		}
	}
	return map[string]string{PlatformTelegram: "telegram", PlatformVK: "vk", PlatformRSS: "rss", PlatformOK: "ok", PlatformTikTok: "tiktok", PlatformDvach: "dvach", Platform4chan: "fourchan"}[platform]
}

var platformNames = map[string]string{"telegram": "Telegram", "dvach": "Двач", "fourchan": "4chan", "youtube": "YouTube", "vk": "VK", "tiktok": "TikTok", "ok": "ОК", "rss": "RSS"}
var reportOrder = []string{"telegram", "dvach", "fourchan", "youtube", "vk", "tiktok", "ok", "rss"}

type platReport struct{ Sources, With, Posts int }

func platformSummaryLine(rep map[string]*platReport, vk bool) string {
	units := map[string]string{"telegram": "каналов", "youtube": "каналов", "rss": "лент", "dvach": "доски", "fourchan": "доски"}
	var parts []string
	for _, k := range reportOrder {
		r := rep[k]
		name := platformNames[k]
		if r.Posts > 0 {
			switch k {
			case "dvach":
				w := "доски"
				if r.With%10 == 1 && r.With%100 != 11 {
					w = "доска"
				}
				parts = append(parts, fmt.Sprintf("%s %d (%d %s)", name, r.Posts, r.With, w))
			case "fourchan":
				parts = append(parts, fmt.Sprintf("%s %d", name, r.Posts))
			default:
				unit := units[k]
				if unit == "" {
					unit = "источников"
				}
				parts = append(parts, fmt.Sprintf("%s %d постов (%d %s)", name, r.Posts, r.With, unit))
			}
			continue
		}
		reason := "нет источников по теме"
		switch {
		case k == "vk" && !vk:
			reason = "нет ключа"
		case k == "rss":
			reason = "нет лент по теме"
		case k == "dvach" || k == "fourchan":
			reason = "нет досок по теме"
		case r.Sources > 0:
			reason = "источники есть, постов нет"
		}
		parts = append(parts, fmt.Sprintf("%s 0 — %s", name, reason))
	}
	return "Площадки: " + strings.Join(parts, " · ") + "."
}

// collectPostsByPlatform — посты всех источников темы по площадкам.
func (u *U) collectPostsByPlatform(topic string) map[string][]jx.M {
	by := map[string][]jx.M{}
	stats := map[string]int{}
	updates := map[string]jx.M{}
	rep := map[string]*platReport{}
	for _, k := range reportOrder {
		rep[k] = &platReport{}
	}
	boards := u.DvachBoardsForTopic(topic, true)
	if len(boards) == 0 {
		u.Log("Подходящих теме досок Двача нет — Двач пропущен.")
	}
	for _, b := range u.dvachBoardsThisRun(boards, topic) {
		if !u.Alive() {
			return by
		}
		u.Log("Парсинг Двача /%s/...", b)
		found := u.ParseDvach(b, topic)
		by[PlatformDvach] = append(by[PlatformDvach], found...)
		rep["dvach"].Sources++
		if len(found) > 0 {
			rep["dvach"].With++
		}
		rep["dvach"].Posts += len(found)
		u.Log("-> Двач /%s/: найдено медиа-постов: %d", b, len(found))
	}
	chanBoards := u.FourchanBoardsForTopic(topic)
	if len(chanBoards) == 0 {
		u.Log("Подходящих теме досок 4chan нет — 4chan пропущен.")
	}
	threads := 0
	for _, b := range FourchanBoardsThisRun(chanBoards) {
		if !u.Alive() {
			return by
		}
		u.Log("Парсинг 4chan /%s/...", b)
		found := u.ParseFourchan(b, FourchanThreadsPerBoard, topic)
		by[Platform4chan] = append(by[Platform4chan], found...)
		rep["fourchan"].Sources++
		if len(found) > 0 {
			rep["fourchan"].With++
		}
		rep["fourchan"].Posts += len(found)
		ts := map[string]bool{}
		for _, p := range found {
			ts[jx.Str(p, "thread_id")] = true
		}
		threads += len(ts)
		u.Log("-> 4chan /%s/: найдено медиа-постов: %d", b, len(found))
	}
	if len(chanBoards) > 0 && u.run != nil {
		u.Log("4chan: %d доски, %d тредов с медиа, %d постов с медиа, %d запросов к API (из кэша: %d).", min(len(chanBoards), FourchanBoardsPerRun), threads,
			len(by[Platform4chan]), u.run.FourchanAPI, u.run.FourchanAPICached)
	}
	vk := u.VKCanRead(u.Ctx)
	if !vk {
		u.Log("VK: нет ключа — паблики VK пропущены (добавьте сервисный ключ VK_SERVICE_TOKEN в posta.env).")
	}
	activeOnly := jx.Bool(u.ContentFilterValues(), "active_sources_only")
	d := u.Catalog()
	t := now()
	poll := map[string]jx.M{}
	for _, c := range CatalogKinds {
		if c.Platform == PlatformVK && !vk {
			continue
		}
		for _, e := range *d.List(c.Kind) {
			if u.CatalogEntryMatchesTopic(e, topic, false) && !IsSourceFrozen(e, 0) {
				poll[c.Platform+"|"+strings.ToLower(jx.Str(e, "id"))] = e
			}
		}
	}
	type cachedPosts struct {
		At    int64  `json:"at"`
		Posts []jx.M `json:"posts"`
	}
	cachedMap := map[string]bool{}
	cache := map[string]cachedPosts{}
	for pkey, e := range poll {
		platform := strings.SplitN(pkey, "|", 2)[0]
		var cp cachedPosts
		if u.CacheGet(u.Ctx, SourcePollCacheKey(platform, jx.Str(e, "id")), &cp) {
			cachedMap[pkey], cache[pkey] = true, cp
		}
	}
	fetchSet := map[string]bool{}
	for _, k := range SourcePollFetchSet(poll, cachedMap, t) {
		fetchSet[k] = true
	}
	fetched, fromCache := 0, 0
	var nextMin *int64
	for _, c := range CatalogKinds {
		if c.Platform == PlatformVK && !vk {
			continue
		}
		for _, e := range *d.List(c.Kind) {
			if !u.Alive() {
				return by
			}
			if !u.CatalogEntryMatchesTopic(e, topic, false) {
				stats["other_topic"]++
				continue
			}
			if u.UASourceBlocked(SourceIDFor(c.Platform, jx.Str(e, "id")), jx.M{"title": jx.Str(e, "name")}) != "" {
				stats["ukraine_source"]++
				continue
			}
			label := platformLabel(c.Platform, e)
			rep[label].Sources++
			pkey := c.Platform + "|" + strings.ToLower(jx.Str(e, "id"))
			var posts []jx.M
			var updated jx.M
			event := ""
			if _, inPoll := poll[pkey]; inPoll && !fetchSet[pkey] && cachedMap[pkey] {
				posts = cache[pkey].Posts
				fromCache++
				left := SourcePollInterval(jx.Map(e, "activity")) - (t - jx.Int(jx.Map(e, "activity"), "checked_at"))
				if nextMin == nil || left < *nextMin {
					nextMin = &left
				}
			} else {
				posts, updated, event = u.collectCatalogSource(c.Platform, e, activeOnly)
				if len(posts) > 0 {
					u.CacheSet(u.Ctx, SourcePollCacheKey(c.Platform, jx.Str(e, "id")), cachedPosts{now(), posts}, config.SourcePollMax*2*time.Second)
				}
				if _, inPoll := poll[pkey]; inPoll {
					fetched++
				}
			}
			if event != "" {
				stats[event]++
			}
			if updated != nil {
				updates[c.Kind+":"+strings.ToLower(jx.Str(e, "id"))] = updated
			}
			if len(posts) > 0 {
				rep[label].With++
			}
			rep[label].Posts += len(posts)
			by[c.Platform] = append(by[c.Platform], posts...)
		}
	}
	u.SaveCatalogEntryUpdates(u.Ctx, updates)
	if len(poll) > 0 {
		next := ""
		if fromCache > 0 && nextMin != nil {
			next = fmt.Sprintf(" (следующий через ~%d мин)", max(1, int64(math.Ceil(float64(*nextMin)/60))))
		}
		u.Log("Опрошено %d из %d источников, остальные из кэша%s.", fetched, len(poll), next)
	}
	// Находки автоподбора подмешиваются всегда: с каждой берём один лучший по горячести пост (поровну по площадкам, предел — suggestion_sources_per_run)
	byPlat := map[string][]jx.M{}
	for _, s := range u.Catalog().Suggestions {
		p := jx.Str(s, "platform")
		if CatalogKind(p) == "" || (p == PlatformVK && !vk) || !u.TopicSame(jx.Str(s, "topic"), topic) || int(jx.Int(s, "score")) < u.TInt("suggest_score") {
			continue
		}
		byPlat[p] = append(byPlat[p], s)
	}
	var lists [][]jx.M
	for _, c := range CatalogKinds {
		l := byPlat[c.Platform]
		if len(l) == 0 {
			continue
		}
		sort.SliceStable(l, func(i, j int) bool { return jx.Int(l[i], "score") > jx.Int(l[j], "score") })
		lists = append(lists, l)
	}
	picked := DiscoveryBalance(lists, u.TInt("suggestion_sources_per_run"), 2)
	byLabel := map[string]int{}
	for _, s := range picked {
		p := jx.Str(s, "platform")
		name := regexp.MustCompile(`^(Telegram|ВКонтакте|RSS|Одноклассники|TikTok) \| `).ReplaceAllString(jx.Str(s, "name"), "")
		if name == "" {
			name = jx.Str(s, "id")
		}
		posts, _, _ := u.collectCatalogSource(p, jx.M{"id": jx.Str(s, "id"), "name": name, "topics": []any{topic}}, activeOnly)
		label := platformLabel(p, jx.M{"id": jx.Str(s, "id")})
		rep[label].Posts += len(posts)
		byLabel[label]++
		if best := bestHeatPost(posts); best != nil {
			rep[label].With++
			by[p] = append(by[p], best)
		}
	}
	if len(picked) > 0 {
		var names []string
		for l, n := range byLabel {
			names = append(names, fmt.Sprintf("%s %d", platformNames[l], n))
		}
		sort.Strings(names)
		u.Log("Из находок автоподбора взято по одному посту с каждой: %d (%s; решение по ним — на вкладке «Источники»).", len(picked), strings.Join(names, ", "))
	}
	if u.TBool("post_search") && u.CurrentQuery() == "" {
		search := u.PostSearchCollect(topic)
		for _, p := range []string{PlatformRSS, PlatformTelegram, PlatformVK} {
			by[p] = append(by[p], search.By[p]...)
		}
		u.Log("Поиск постов: YouTube %d (запросов %d), Telegram %d (запросов %d), VK %d (запросов %d) — после фильтра по словам.",
			len(search.By[PlatformRSS]), search.Done["youtube"], len(search.By[PlatformTelegram]), search.Done["telegram"], len(search.By[PlatformVK]), search.Done["vk"])
	}
	labels := map[string]string{"other_topic": "другой темы", "skipped_frozen": "заморожено (ждут перепроверки)", "frozen": "заморожено сейчас", "revived": "ожило",
		"ukraine_source": "украинских источников не собираем"}
	var notes []string
	for _, k := range []string{"other_topic", "skipped_frozen", "frozen", "revived", "ukraine_source"} {
		if stats[k] > 0 {
			notes = append(notes, fmt.Sprintf("%s: %d", labels[k], stats[k]))
		}
	}
	if len(notes) > 0 {
		u.Log("Источники: %s.", strings.Join(notes, ", "))
	}
	u.Log("%s", platformSummaryLine(rep, vk))
	return by
}

// bestHeatPost — пост с наибольшей горячестью; без данных о горячести — первый; пустой список — nil.
func bestHeatPost(posts []jx.M) jx.M {
	var best jx.M
	var bestHeat float64 = -1
	for _, p := range posts {
		h := 0.0
		if v := PostHeat(p); v != nil {
			h = *v
		}
		if best == nil || h > bestHeat {
			best, bestHeat = p, h
		}
	}
	return best
}

// ---- Кандидаты ----

// ExpandPostImages — один кандидат на пост: оценивается первая картинка, остальные — в галерею.
func ExpandPostImages(posts []jx.M) []jx.M {
	var out []jx.M
	for _, p := range posts {
		imgs := uniqueStrings(jx.Strs(p, "images"))
		if len(imgs) == 0 {
			if s := jx.Str(p, "image"); s != "" {
				imgs = []string{s}
			}
		}
		if len(imgs) == 0 {
			if jx.Bool(p, "text_post") {
				c := jx.Copy(p)
				c["image"], c["images"] = "", []any{}
				out = append(out, c)
			}
			continue
		}
		c := jx.Copy(p)
		c["image"], c["images"] = imgs[0], []any{imgs[0]}
		caption := jx.Str(p, "text")
		if caps := jx.List(p, "captions"); len(caps) > 0 {
			caption = jx.ToStr(caps[0])
		}
		c["captions"] = []any{caption}
		if g := jx.Maps(p, "gallery"); len(g) > 1 {
			c["gallery"] = g
		} else if len(imgs) > 1 {
			var g []jx.M
			for _, i := range imgs {
				g = append(g, jx.M{"image": i, "video": ""})
			}
			c["gallery"] = g
		}
		out = append(out, c)
	}
	return out
}

var queryFragRe = regexp.MustCompile(`[?#].*$`)

// CandidatePostKey — ключ поста (ссылка на пост).
func CandidatePostKey(item jx.M) string {
	u := jx.Str(item, "source_url")
	if u == "" {
		u = jx.Str(item, "url")
	}
	if u == "" && jx.Str(item, "board") != "" && jx.Str(item, "thread_id") != "" && jx.Str(item, "post_id") != "" {
		u = DvachPostURL(jx.Str(item, "board"), jx.Str(item, "thread_id"), jx.Str(item, "post_id"))
	}
	if u == "" {
		return ""
	}
	k := "post:" + queryFragRe.ReplaceAllString(u, "")
	if strings.Contains(u, "2ch.hk") {
		k += "#" + jx.Str(item, "post_id")
	}
	return k
}

var (
	ctkLinkRe  = regexp.MustCompile(`https?://\S+`)
	ctkPunctRe = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
)

// CandidateTextKey — ключ текстового поста без картинки; "" — текста мало.
func CandidateTextKey(item jx.M) string {
	if jx.Str(item, "image") != "" || !jx.Bool(item, "text_post") {
		return ""
	}
	t := strings.ToLower(jx.Str(item, "text"))
	t = collapseSpaces(ctkPunctRe.ReplaceAllString(ctkLinkRe.ReplaceAllString(t, " "), " "))
	if runeLen(t) < 20 {
		return ""
	}
	h := sha1.Sum([]byte(t))
	return "text:" + hex.EncodeToString(h[:])
}

type knownKeys struct {
	keys map[string]bool
	news [][]string
}

func recordKnownKeys(records []jx.M) *knownKeys {
	k := &knownKeys{keys: map[string]bool{}}
	for _, r := range records {
		if img := ContentImageKey(r); img != "" {
			k.keys[img] = true
		}
		for _, g := range jx.Maps(r, "gallery") {
			if s := jx.Str(g, "image"); s != "" {
				k.keys[s] = true
			}
		}
		if p := CandidatePostKey(r); p != "" {
			k.keys[p] = true
		}
		if t := CandidateTextKey(r); t != "" {
			k.keys[t] = true
		}
		if w := jx.Strs(r, "news_words"); len(w) > 0 {
			at := jx.Int(r, "pooled_at")
			if at == 0 {
				at = jx.Int(r, "posted_at")
			}
			if now()-at <= NewsDuplicateHours*3600 {
				k.news = append(k.news, w)
			}
		}
	}
	return k
}

func (u *U) collectKnown() *knownKeys {
	return recordKnownKeys(append(u.Pool(), u.History().All()...))
}

func (u *U) collectKnownHashes() map[string]string {
	out := map[string]string{}
	for _, r := range append(u.Pool(), u.History().All()...) {
		if h := jx.Str(r, "phash"); h != "" {
			out[h] = jx.Str(r, "source")
		}
	}
	return out
}

func candidateIsKnown(item jx.M, k *knownKeys) bool {
	imgs := []string{jx.Str(item, "image")}
	for _, g := range jx.Maps(item, "gallery") {
		imgs = append(imgs, jx.Str(g, "image"))
	}
	for _, i := range imgs {
		if i != "" && k.keys[i] {
			return true
		}
	}
	if t := CandidateTextKey(item); t != "" && k.keys[t] {
		return true
	}
	if w := jx.Strs(item, "news_words"); len(w) > 0 {
		for _, n := range k.news {
			if NewsSimilar(w, n) {
				return true
			}
		}
	}
	p := CandidatePostKey(item)
	return p != "" && k.keys[p]
}

// prunePoolAlreadyRated — убирает из пула записи, чей пост уже оценён.
func (u *U) prunePoolAlreadyRated() int {
	known := recordKnownKeys(u.History().All())
	before := u.Pool()
	after, _ := u.PoolUpdate(func(pool []jx.M) []jx.M {
		var kept []jx.M
		for _, it := range pool {
			if !candidateIsKnown(it, known) {
				kept = append(kept, it)
			}
		}
		if len(kept) == len(pool) {
			return nil
		}
		return orEmptyList(kept)
	})
	u.MediaReleaseRemoved(u.Ctx, before, after)
	return len(before) - len(after)
}

// AddItemToPoolSafely — запись в пул. Полный пул темы: вытесняется самая старая просроченная запись; нет просроченных — не добавляем.
func (u *U) AddItemToPoolSafely(item jx.M) bool {
	added := false
	if !jx.Has(item, "pooled_at") {
		item["pooled_at"] = now()
	}
	before := u.Pool()
	known := u.collectKnown()
	target := u.TInt("pool_target")
	after, _ := u.PoolUpdate(func(pool []jx.M) []jx.M {
		if candidateIsKnown(item, known) {
			return nil
		}
		topic := TopicNorm(jx.Str(item, "topic"))
		if sourcePoolCount(pool, topic, ContentSourceKey(item)) >= u.TInt("per_source_pool") {
			return nil
		}
		var same []int
		for i, it := range pool {
			if TopicNorm(jx.Str(it, "topic")) == topic {
				same = append(same, i)
			}
		}
		if len(same) >= target {
			var expired []int
			for _, i := range same {
				if !u.poolItemFresh(pool[i]) {
					expired = append(expired, i)
				}
			}
			if len(expired) == 0 {
				return nil
			}
			sort.SliceStable(expired, func(a, b int) bool { return jx.Int(pool[expired[a]], "pooled_at") < jx.Int(pool[expired[b]], "pooled_at") })
			pool = append(pool[:expired[0]], pool[expired[0]+1:]...)
		}
		added = true
		return PoolOrder(append(pool, item))
	})
	u.MediaReleaseRemoved(u.Ctx, before, after)
	if added {
		u.PoolItemsEnqueueVideos(u.Ctx, []jx.M{item}, u.ID)
		go u.TranslateWarmCache(item)
	}
	return added
}

func (a *App) poolItemFresh(item jx.M) bool {
	if !jx.Has(item, "pooled_at") {
		return true
	}
	return now()-jx.Int(item, "pooled_at") <= int64(a.TInt("pool_max_age"))*60
}

type queueSkip map[string]int

// buildCandidateQueue — без виденных, устаревших, отсеянных раньше; лучшие источники первыми, горячие — в шортлист. До 500.
func (u *U) buildCandidateQueue(byPlatform map[string][]jx.M, seen *knownKeys, rejected map[string]bool, rates map[string][2]int, format *TopicFormat, filters jx.M, topic string) ([]jx.M, queueSkip) {
	skipped := queueSkip{}
	busy := u.sourceBusy(topic)
	unique := map[string]jx.M{}
	var order []string
	uaHits, uaNames, uaAll := map[string]int{}, map[string]string{}, map[string]bool{}
	maxAge := u.TInt("content_max_age")
	platOrder := []string{PlatformTelegram, PlatformVK, PlatformRSS, PlatformOK, PlatformTikTok, PlatformDvach, Platform4chan}
	for _, p := range platOrder {
		for _, item := range ExpandPostImages(byPlatform[p]) {
			if !ContentPassesMediaFilters(item, filters) {
				skipped["filter"]++
				continue
			}
			if ua := u.UAItemBlocked(item); ua != "" {
				skipped["ukraine"]++
				src := ContentSourceKey(item)
				uaAll[src] = true
				if !strings.HasPrefix(ua, "источник:") {
					uaHits[src]++
					uaNames[src] = jx.Str(item, "source")
				}
				continue
			}
			ukey := jx.Str(item, "image")
			if ukey == "" {
				ukey = CandidateTextKey(item)
			}
			if ukey == "" {
				if format != nil && jx.Str(item, "image") == "" {
					skipped["format"]++
				} else {
					skipped["no_key"]++
				}
				continue
			}
			if candidateIsKnown(item, seen) {
				skipped["seen"]++
				continue
			}
			if unique[ukey] != nil {
				skipped["duplicate"]++
				continue
			}
			src := ContentSourceKey(item)
			if busy[src] {
				skipped["source_busy"]++
				continue
			}
			days := maxAge
			if strings.HasPrefix(src, "dvach:") {
				days = config.DvachMaxAgeDays
			}
			reason := ""
			switch {
			case IsPostOutdated(jx.Int(item, "posted_at"), 0, days):
				reason = "outdated"
			case rejected[jx.Str(item, "image")]:
				reason = "rejected_before"
			case format != nil && jx.Str(item, "image") == "" && format.Media == "required":
				reason = "format"
			case format != nil && format.Text == "required" && runeLen(strings.TrimSpace(jx.Str(item, "text"))) < format.MinText:
				reason = "format"
			}
			if reason != "" {
				skipped[reason]++
				continue
			}
			unique[ukey] = item
			order = append(order, ukey)
		}
	}
	skipped["ukraine_sources"] = len(uaAll)
	for src, n := range uaHits {
		if n >= uaSourcePostsMax {
			u.UABlockSource(u.Ctx, src, uaNames[src], fmt.Sprintf("за проход украинских постов: %d", n))
		}
	}
	u.UACountSkipped(u.Ctx, skipped["ukraine"])
	all := make([]jx.M, 0, len(order))
	for _, k := range order {
		all = append(all, unique[k])
	}
	query := u.CurrentQuery()
	named := func(it jx.M) int {
		if query != "" && QueryTextHit(jx.Str(it, "text"), query) {
			return 1
		}
		return 0
	}
	chance := func(it jx.M) (bool, float64) {
		r := rates[ContentSourceKey(it)]
		rate := float64(r[0]+1) / float64(r[1]+2)
		return r[1] >= config.SourceVerdictMin && rate < config.SourceVerdictLow, rate
	}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if x, y := named(a), named(b); x != y {
			return x > y
		}
		if x, y := b2i(jx.Bool(a, "thread_trust")), b2i(jx.Bool(b, "thread_trust")); x != y {
			return x > y
		}
		ca, ra := chance(a)
		cb, rb := chance(b)
		if ca != cb {
			return !ca
		}
		if ra != rb {
			return ra > rb
		}
		return jx.Int(a, "posted_at") > jx.Int(b, "posted_at")
	})
	short := u.hotShortlist(all, rates, topic)
	return short[:min(len(short), 500)], skipped
}

// InterleaveBySource — по одному с каждого источника по очереди.
func InterleaveBySource(items []jx.M) []jx.M {
	bySrc := map[string][]jx.M{}
	var order []string
	for _, it := range items {
		k := ContentSourceKey(it)
		if bySrc[k] == nil {
			order = append(order, k)
		}
		bySrc[k] = append(bySrc[k], it)
	}
	var out []jx.M
	for round := 0; len(out) < len(items); round++ {
		for _, k := range order {
			if round < len(bySrc[k]) {
				out = append(out, bySrc[k][round])
			}
		}
	}
	return out
}

// HotPlatform — площадка поста для горячих.
func HotPlatform(item jx.M) string {
	e := jx.Map(item, "engagement")
	src := jx.Str(item, "source")
	switch {
	case jx.Str(e, "source") == "4chan" || strings.HasPrefix(src, Platform4chan):
		return "4chan"
	case jx.Has(e, "thread_posts") || strings.HasPrefix(src, PlatformDvach):
		return "Двач"
	case jx.Str(e, "source") == "telegram":
		return "Telegram"
	case strings.HasPrefix(src, PlatformTikTok):
		return "TikTok"
	case strings.HasPrefix(src, PlatformVK):
		return "ВКонтакте"
	case strings.HasPrefix(src, PlatformOK):
		return "ОК"
	case jx.Has(item, "views"):
		return "YouTube"
	}
	return "other"
}

// PostHeat — горячесть поста за час жизни: показатель площадки / (часов + 2); nil — данных нет.
func PostHeat(item jx.M) *float64 {
	e := jx.Map(item, "engagement")
	var v *float64
	set := func(x float64) { v = &x }
	switch HotPlatform(item) {
	case "Telegram":
		views := e["views"]
		if views == nil {
			views = item["post_views"]
		}
		if views != nil {
			sum := 0.0
			for _, r := range jx.Map(e, "reactions") {
				sum += jx.ToFloat(r)
			}
			set(jx.ToFloat(views) + 5*sum + 10*jx.Float(e, "forwards"))
		}
	case "Двач":
		if jx.Has(e, "thread_posts") {
			set(jx.Float(e, "thread_posts"))
		}
	case "4chan":
		if jx.Has(e, "replies") || jx.Has(e, "images") {
			set(jx.Float(e, "replies") + jx.Float(e, "images"))
		}
	case "YouTube":
		set(jx.Float(item, "views"))
	case "TikTok":
		if jx.Has(e, "view_count") || jx.Has(e, "like_count") {
			set(jx.Float(e, "view_count") + 5*jx.Float(e, "like_count"))
		}
	case "ВКонтакте":
		if jx.Has(e, "likes") || jx.Has(e, "views") {
			set(jx.Float(e, "views") + 5*jx.Float(e, "likes") + 10*jx.Float(e, "reposts"))
		}
	}
	if v == nil {
		return nil
	}
	hours := math.Max(1, float64(now()-jx.Int(item, "posted_at"))/3600)
	r := *v / (hours + 2)
	return &r
}

func hotRelativeHeat(items []jx.M) []float64 {
	heat := make([]*float64, len(items))
	bySrc, byPlat := map[string][]float64{}, map[string][]float64{}
	for i, it := range items {
		heat[i] = PostHeat(it)
		if heat[i] != nil {
			bySrc[ContentSourceKey(it)] = append(bySrc[ContentSourceKey(it)], *heat[i])
			byPlat[HotPlatform(it)] = append(byPlat[HotPlatform(it)], *heat[i])
		}
	}
	out := make([]float64, len(items))
	for i, it := range items {
		if heat[i] == nil {
			out[i] = 1
			continue
		}
		var base *float64
		if s := bySrc[ContentSourceKey(it)]; len(s) >= 3 {
			base = Median(s)
		} else {
			base = Median(byPlat[HotPlatform(it)])
		}
		if base != nil && *base > 0 {
			out[i] = *heat[i] / *base
		} else {
			out[i] = 1
		}
	}
	return out
}

func hotWords(text string) []string {
	var out []string
	for _, w := range nonWordRe.Split(strings.ToLower(text), -1) {
		if runeLen(w) >= 4 {
			out = append(out, w)
		}
	}
	return uniqueStrings(out)
}

func hotHasWord(text string, words []string) bool {
	t := strings.ToLower(text)
	for _, w := range words {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

func (u *U) imageboardNativeTopic(topic string) bool {
	if topic == "" {
		return false
	}
	first := topic
	if p := TopicParts(topic); len(p) > 0 {
		first = p[0]
	}
	e := u.TopicLookup(first)
	if e == nil || e.Key == "" {
		return false
	}
	for _, n := range config.ImageboardNativeTopicKeys {
		if e.Key == n || strings.HasPrefix(e.Key, n+".") {
			return true
		}
	}
	return false
}

// hotShortlist — горячие первыми: уточнение, затем общий отбор по горячести × доля принятых × бонусы; хвост — по кругу источников.
func (u *U) hotShortlist(all []jx.M, rates map[string][2]int, topic string) []jx.M {
	if topic == "" && u.ID != "" {
		topic = u.SearchTopicKey()
	}
	query := u.CurrentQuery()
	var keywords []string
	exclude := []string{}
	if p := u.TopicProfileForPrompts(topic); p != nil {
		keywords, exclude = p.Keywords, hotWords(p.Exclude)
	}
	native := u.imageboardNativeTopic(topic)
	shortN := u.TInt("hot_shortlist")
	var first, rest []jx.M
	var boost []float64
	excluded := 0
	for _, it := range all {
		text := jx.Str(it, "text") + " " + jx.Str(it, "thread_title")
		if len(exclude) > 0 && hotHasWord(text, exclude) {
			excluded++
			continue
		}
		named := query != "" && QueryTextHit(jx.Str(it, "text"), query)
		if named && len(first) < shortN/2 {
			first = append(first, it)
			continue
		}
		b := 1.0
		if named {
			b *= config.HotQueryBoost
		}
		if jx.Bool(it, "thread_trust") {
			b *= config.HotTrustBoost
		}
		boost = append(boost, b)
		rest = append(rest, it)
	}
	heat := hotRelativeHeat(rest)
	score := map[int]float64{}
	tail := 0
	for i, it := range rest {
		board := contains([]string{PlatformDvach, Platform4chan}, HotPlatform(it))
		text := jx.Str(it, "text") + " " + jx.Str(it, "thread_title") + " " + jx.Str(it, "thread_op")
		if board && !native && !jx.Bool(it, "thread_trust") && !TopicWordsHit(text, keywords) {
			tail++
			continue
		}
		r := rates[ContentSourceKey(it)]
		rate := float64(r[0]+1) / float64(r[1]+2)
		mult := math.Min(1.5, math.Max(0.5, 0.5+rate))
		if r[1] >= config.SourceVerdictMin && rate < config.SourceVerdictLow {
			mult = 0.5
		}
		kw := 1.0
		if TopicWordsHit(text, keywords) {
			kw = config.HotKeywordBoost
		}
		score[i] = heat[i] * mult * kw * boost[i]
	}
	bySrc := map[string][]int{}
	var srcOrder []string
	for i := range rest {
		if _, ok := score[i]; !ok {
			continue
		}
		k := ContentSourceKey(rest[i])
		if bySrc[k] == nil {
			srcOrder = append(srcOrder, k)
		}
		bySrc[k] = append(bySrc[k], i)
	}
	var pool []int
	for _, k := range srcOrder {
		idx := bySrc[k]
		sort.SliceStable(idx, func(a, b int) bool { return score[idx[a]] > score[idx[b]] })
		pool = append(pool, idx[:min(len(idx), config.QueuePerSource)]...)
	}
	perSourceCut := len(score) - len(pool)
	sort.SliceStable(pool, func(a, b int) bool { return score[pool[a]] > score[pool[b]] })
	room := max(0, shortN-len(first))
	capN := int(math.Floor(config.HotPlatformShare * float64(shortN)))
	var picked []int
	per := map[string]int{}
	for _, i := range pool {
		if len(picked) >= room {
			break
		}
		p := HotPlatform(rest[i])
		if per[p] >= capN {
			continue
		}
		per[p]++
		picked = append(picked, i)
	}
	top := -1
	trusted := 0
	for _, i := range picked {
		if top < 0 || heat[i] > heat[top] {
			top = i
		}
		if jx.Bool(rest[i], "thread_trust") {
			trusted++
		}
	}
	if u.run != nil {
		hs := jx.M{"total": len(all), "short": len(picked) + len(first), "per": per, "excluded": excluded, "trusted": trusted, "imageboard_tail": tail, "top": nil,
			"per_source_cut": perSourceCut, "shortlist_cut": len(pool) - len(picked)}
		if top >= 0 {
			hs["top"] = jx.M{"source": jx.Str(rest[top], "source"), "times": math.Round(heat[top]*10) / 10}
		}
		u.run.HotStats = hs
	}
	sort.SliceStable(picked, func(a, b int) bool { return score[picked[a]] > score[picked[b]] })
	isPicked := map[int]bool{}
	var hot []jx.M
	for _, i := range picked {
		isPicked[i] = true
		hot = append(hot, rest[i])
	}
	var tailItems []jx.M
	for i, it := range rest {
		if !isPicked[i] {
			tailItems = append(tailItems, it)
		}
	}
	return append(append(first, InterleaveBySource(hot)...), InterleaveBySource(tailItems)...)
}

// hotTextJudge — пакетная проверка подписей (темы, где судят по тексту): ниже порога — отброс или в конец.
func (u *U) hotTextJudge(items []jx.M, topic string) []jx.M {
	judge := u.TopicFormatOf(topic).Judge
	if judge != "text" && judge != "both" {
		return items
	}
	var idx []int
	var texts []string
	for i, it := range items {
		if t := strings.TrimSpace(jx.Str(it, "text")); runeLen(t) >= config.HotTextMinChars {
			idx, texts = append(idx, i), append(texts, t)
		}
	}
	drop, late := map[int]bool{}, map[int]bool{}
	for off := 0; off < len(texts) && u.Alive(); off += config.HotTextBatch {
		res, err := u.AnalyzeTextBatch(texts[off:min(len(texts), off+config.HotTextBatch)], topic)
		if err != nil {
			continue
		}
		for k, r := range res {
			if r == nil || jx.Int(r, "relevance") >= config.HotTextRejectScore {
				continue
			}
			if judge == "text" {
				drop[idx[off+k]] = true
			} else {
				late[idx[off+k]] = true
			}
		}
	}
	var kept, tail []jx.M
	for i, it := range items {
		switch {
		case drop[i]:
		case late[i]:
			tail = append(tail, it)
		default:
			kept = append(kept, it)
		}
	}
	u.Log("Проверка подписей моделью: отброшено %d, в конец шортлиста %d.", len(drop), len(late))
	return append(kept, tail...)
}

// ---- Кэши оценок и итоги каналов ----

func SourcePassRates(verdicts map[string][2]int) map[string]float64 {
	out := map[string]float64{}
	for k, c := range verdicts {
		out[k] = float64(c[0]+1) / float64(c[1]+2)
	}
	return out
}

func (u *U) prescreenIsJunk(score *int, item jx.M) bool {
	return score != nil && *score >= u.prescreenThreshold(item)
}

func (a *App) prescreenThreshold(item jx.M) int {
	t := a.TInt("prescreen_junk")
	if jx.Bool(item, "thread_trust") {
		t += config.ThreadTrustPrescreenBonus
	}
	return t
}

func (u *U) sourceMissNote(src string, passed bool) {
	if u.run == nil {
		return
	}
	if passed {
		u.run.SourceMisses[src] = 0
	} else {
		u.run.SourceMisses[src]++
	}
}

// SourceVerdictRecord — итог полного анализа по каналу и теме (скользящее окно SourceVerdictWindow).
func (a *App) SourceVerdictRecord(topic, src string, passed bool) {
	if topic == "" || src == "" {
		return
	}
	p := 0
	if passed {
		p = 1
	}
	w := config.SourceVerdictWindow
	a.DB.Pool.Exec(a.ctxOrBg(), `INSERT INTO source_verdicts (topic, source, passed, total, at) VALUES ($1, $2, $3, 1, $4)
		ON CONFLICT (topic, source) DO UPDATE SET
		  passed = (CASE WHEN source_verdicts.total >= $5 THEN round(source_verdicts.passed * ($5 - 1)::numeric / $5)::int ELSE source_verdicts.passed END) + $3,
		  total = (CASE WHEN source_verdicts.total >= $5 THEN $5 - 1 ELSE source_verdicts.total END) + 1,
		  at = $4`, topic, src, p, now(), w)
}

func (a *App) TopicVerdictStats(topic string) map[string][2]int {
	out := map[string][2]int{}
	rows, err := a.DB.Pool.Query(a.ctxOrBg(), `SELECT source, passed, total FROM source_verdicts WHERE topic = $1`, topic)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var p, t int
		if rows.Scan(&s, &p, &t) == nil {
			out[s] = [2]int{p, t}
		}
	}
	return out
}

type screenedEntry struct {
	URL, Topic, Source, Phash string
	Score                     int
}

func (a *App) screenedCacheLoad(topicKey string) []screenedEntry {
	rows, err := a.DB.Pool.Query(a.ctxOrBg(), `SELECT url, topic, source, score, phash FROM screened_cache WHERE ver = $1 AND lower(topic) = lower($2) AND at > $3`,
		config.ScreenedCacheVersion, topicKey, now()-config.ScreenedCacheDays*config.DaySeconds)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []screenedEntry
	for rows.Next() {
		var e screenedEntry
		if rows.Scan(&e.URL, &e.Topic, &e.Source, &e.Score, &e.Phash) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (a *App) cacheMerge(entries map[string]screenedEntry) {
	if len(entries) == 0 {
		return
	}
	ctx := a.ctxOrBg()
	for url, e := range entries {
		a.DB.Pool.Exec(ctx, `INSERT INTO screened_cache (url, topic, source, score, phash, at, ver) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (url) DO UPDATE SET topic=EXCLUDED.topic, source=EXCLUDED.source, score=EXCLUDED.score, phash=EXCLUDED.phash, at=EXCLUDED.at, ver=EXCLUDED.ver`,
			url, e.Topic, e.Source, e.Score, e.Phash, now(), config.ScreenedCacheVersion)
	}
	a.DB.Pool.Exec(ctx, `DELETE FROM screened_cache WHERE at < $1`, now()-config.ScreenedCacheDays*config.DaySeconds)
	a.DB.Pool.Exec(ctx, `DELETE FROM screened_cache WHERE url IN (SELECT url FROM screened_cache ORDER BY at DESC OFFSET $1)`, config.ScreenedCacheLimit)
}

// AnalysisCacheKey — ключ общего кэша анализа: версия промпта, запись, тема.
func AnalysisCacheKey(item jx.M, topic string) string {
	t := strings.ToLower(strings.TrimSpace(topic))
	suffix := ""
	if contains(config.SubtopicCacheTopics, t) {
		suffix = "|subtopic2"
	}
	k := ContentImageKey(item)
	if k == "" {
		k = CandidateTextKey(item)
	}
	h := sha1.Sum([]byte(fmt.Sprintf("%d|%s|%s%s|v4tax", config.AnalysisPromptVersion, k, t, suffix)))
	return hex.EncodeToString(h[:])
}

func (a *App) analysisCacheGet(item jx.M, topic string) jx.M {
	var raw []byte
	err := a.DB.Pool.QueryRow(a.ctxOrBg(), `SELECT analysis FROM analysis_cache WHERE key = $1 AND at > $2`, AnalysisCacheKey(item, topic),
		now()-config.AnalysisCacheDays*config.DaySeconds).Scan(&raw)
	if err != nil {
		return nil
	}
	return jx.Unmarshal(raw)
}

func (a *App) analysisCachePut(item jx.M, topic string, an jx.M) {
	ctx := a.ctxOrBg()
	a.DB.Pool.Exec(ctx, `INSERT INTO analysis_cache (key, analysis, at) VALUES ($1, $2, $3) ON CONFLICT (key) DO UPDATE SET analysis = EXCLUDED.analysis, at = EXCLUDED.at`,
		AnalysisCacheKey(item, topic), jx.Marshal(an), now())
	a.DB.Pool.Exec(ctx, `DELETE FROM analysis_cache WHERE at < $1`, now()-config.AnalysisCacheDays*config.DaySeconds)
	a.DB.Pool.Exec(ctx, `DELETE FROM analysis_cache WHERE key IN (SELECT key FROM analysis_cache ORDER BY at DESC OFFSET $1)`, config.AnalysisCacheLimit)
}

// ---- Оценка ----

func (u *U) fetchCandidatePoster(item jx.M) []byte {
	img := jx.Str(item, "image")
	var b []byte
	if p := u.MediaPath(img); p != "" {
		b, _ = os.ReadFile(p)
	} else if strings.HasPrefix(img, FourchanImg+"/") {
		b = u.FourchanFetchMedia(img, false)
	} else {
		b = u.HTTPGet(img, 15*time.Second)
		if b == nil {
			b = u.HTTPGet(img, 25*time.Second)
		}
	}
	if len(b) == 0 {
		u.Log("-> Превью не скачалось, пропуск.")
		return nil
	}
	if err := imgx.Validate(b); err != nil {
		u.Log("-> Пропуск: %s", err)
		return nil
	}
	return b
}

type prescreenRow struct {
	Item   jx.M
	Poster []byte
	Pos    int
	Score  *int
	Source string
	Phash  string
}

type prescreenCtx struct {
	stats          map[string]int
	cached         map[string]int
	newCache       map[string]screenedEntry
	knownHashes    map[string]string
	screenedHashes map[string]int
	passedTotal    int
}

func (u *U) screen(topic, stage string, item jx.M, score int, passed bool, reason, model string) {
	u.CH.Screening(ch.Screening{TS: time.Now(), UserID: u.ID, Topic: topic, Stage: stage, SourceKey: ContentSourceKey(item), Platform: HotPlatform(item),
		ImageKey: ContentItemKey(item), Score: score, Passed: passed, Reason: reason, Model: model})
}

// prescreenBatch — быстрая оценка пачки: прошедшие в порядке очереди.
func (u *U) prescreenBatch(batch []jx.M, topic string, c *prescreenCtx, deadline time.Time) []prescreenRow {
	var passed, toScore []prescreenRow
	for pos, item := range batch {
		if time.Now().After(deadline) || !u.Alive() {
			break
		}
		src := ContentSourceKey(item)
		if c.passedTotal > 0 && u.run != nil && u.run.SourceMisses[src] >= config.PrescreenSourceMissLimit {
			c.stats["source_skipped"]++
			continue
		}
		poster := u.fetchCandidatePoster(item)
		if poster == nil {
			continue
		}
		hash := ImagePhash(poster)
		if hash != "" {
			if seen, ok := PhashFind(hash, c.knownHashes); ok {
				c.stats["duplicates"]++
				from := ""
				if c.knownHashes[seen] != "" {
					from = " (из «" + c.knownHashes[seen] + "»)"
				}
				u.Log("-> %s: повтор уже виденной картинки%s — пропуск.", candidateLogLabel(item), from)
				continue
			}
			c.knownHashes[hash] = jx.Str(item, "source")
		}
		row := prescreenRow{Item: item, Poster: poster, Pos: pos, Source: src, Phash: hash}
		if s, ok := c.cached[jx.Str(item, "image")]; ok {
			sc := s
			if u.prescreenIsJunk(&sc, item) {
				c.stats["rejected"]++
			} else {
				c.passedTotal++
				row.Score = &sc
				passed = append(passed, row)
			}
		} else if k, ok := PhashFind(hash, c.screenedHashes); hash != "" && ok {
			sc := c.screenedHashes[k]
			if u.prescreenIsJunk(&sc, item) {
				c.stats["rejected"]++
			} else {
				c.passedTotal++
				row.Score = &sc
				passed = append(passed, row)
			}
		} else {
			toScore = append(toScore, row)
		}
	}
	if len(toScore) > 0 {
		u.Log("Быстрая оценка %d кандидатов (до %d одновременно)...", len(toScore), min(5, len(toScore)))
		scores := u.ParallelQuickScores(toScore)
		model := u.LLM.ModelFor(u.Ctx, "prescreen")
		for i, q := range scores {
			row := toScore[i]
			item := row.Item
			if q.Score == nil {
				u.Log("-> %s: ошибка оценки, идёт на полный анализ", candidateLogLabel(item))
				passed = append(passed, row)
				continue
			}
			junk := u.prescreenIsJunk(q.Score, item)
			if junk {
				u.Log("-> %s: мусор %d%% — пропуск", candidateLogLabel(item), *q.Score)
			} else {
				u.Log("-> %s: мусора %d%%", candidateLogLabel(item), *q.Score)
			}
			c.newCache[jx.Str(item, "image")] = screenedEntry{Topic: topic, Source: row.Source, Phash: row.Phash, Score: *q.Score}
			c.stats["prescreened"]++
			u.screen(topic, "prescreen", item, *q.Score, !junk, "", model)
			if junk {
				c.stats["rejected"]++
			} else {
				c.passedTotal++
				row.Score = q.Score
				passed = append(passed, row)
			}
		}
	}
	sort.SliceStable(passed, func(i, j int) bool { return passed[i].Pos < passed[j].Pos })
	return passed
}

// textCandidatesPriority — текстовые записи: сначала со словами темы, потом по лайкам.
func (u *U) textCandidatesPriority(items []jx.M, topic string) []jx.M {
	words := u.DiscoveryTopicKeywords(topic)
	score := func(it jx.M) int64 {
		s := jx.Int(it, "likes")
		t := strings.ToLower(jx.Str(it, "text"))
		for _, w := range words {
			if strings.Contains(t, strings.ToLower(w)) {
				return s + 1_000_000
			}
		}
		return s
	}
	sort.SliceStable(items, func(i, j int) bool { return score(items[i]) > score(items[j]) })
	return items
}

func (u *U) admit(a jx.M, item jx.M, topic, verdict, stage, text string) bool {
	if IsSpamContent(text, jx.Str(a, "description")) {
		u.Log("-> Отброшено: явный маркер рекламы или спама.")
		u.screen(topic, stage, item, int(jx.Int(a, "relevance")), false, "спам", jx.Str(a, "model"))
		return false
	}
	a = u.QualityTagPenalty(a, topic)
	threshold := u.LLM.ThresholdForModel(jx.Str(a, "model"), u.TInt("relevance_threshold"))
	passedAnalysis := int(jx.Int(a, "relevance")) >= threshold
	u.SourceVerdictRecord(topic, ContentSourceKey(item), passedAnalysis)
	u.sourceMissNote(ContentSourceKey(item), passedAnalysis)
	o := DefaultQualityOpts()
	o.Threshold = threshold
	if reason := u.QualityReasons(a, item, topic, o); reason != "" {
		u.Log("-> Отброшено: %s (%s).", reason, verdict)
		u.screen(topic, stage, item, int(jx.Int(a, "relevance")), false, reason, jx.Str(a, "model"))
		return false
	}
	if u.ContentAdultBlocked(jx.Merge(item, jx.M{"tags": a["tags"]}), topic) {
		u.Log("-> Отклонено: 18+ или «Жесть» не по теме.")
		u.screen(topic, stage, item, int(jx.Int(a, "relevance")), false, "18+", jx.Str(a, "model"))
		return false
	}
	if a["ukraine"] == true && u.UAFilterOn() {
		u.Log("-> Отклонено: украинский контент.")
		u.screen(topic, stage, item, int(jx.Int(a, "relevance")), false, "украинский", jx.Str(a, "model"))
		return false
	}
	if u.SearchTopicKey() != topic {
		u.Log("-> Тема поиска сменилась — запись по старой теме не сохраняю.")
		return false
	}
	u.screen(topic, stage, item, int(jx.Int(a, "relevance")), true, "", jx.Str(a, "model"))
	return true
}

func analysisRecord(a, item jx.M, topic, defFormat string) jx.M {
	desc := jx.Str(a, "description")
	if desc == "" {
		desc = "Описание не получено"
	}
	why := jx.Str(a, "why_relevant")
	if why == "" {
		why = "Нейросеть не сформулировала объяснение."
	}
	format := jx.Str(a, "format")
	if format == "" {
		format = defFormat
	}
	rec := jx.Copy(item)
	for k, v := range (jx.M{"description": desc, "why_relevant": why, "relevance": a["relevance"], "rating": nil, "format": format, "tags": orList(jx.List(a, "tags")),
		"entity": jx.Str(a, "entity"), "source_hashtags": PostSourceHashtags(item), "topic": topic, "analysis_model": jx.Str(a, "model")}) {
		rec[k] = v
	}
	return rec
}

// analyzeTextCandidate — пост без картинки: оценка текста, те же проверки, запись без картинки.
func (u *U) analyzeTextCandidate(item jx.M, topic string) bool {
	text := AnalysisText(item)
	if !u.TBool("text_posts") || text == "" {
		return false
	}
	if u.run != nil {
		if u.run.TextAnalysesLeft <= 0 {
			u.run.TextAnalysesDeferred++
			u.Log("-> Текстовых: проанализировано %d, отложено %d (лимит за проход).", u.TInt("text_per_pass"), u.run.TextAnalysesDeferred)
			return false
		}
		u.run.TextAnalysesLeft--
	}
	u.Log("Полный анализ текстовой записи %s (без картинки)", candidateLogLabel(item))
	a := u.analysisCacheGet(item, topic)
	if a != nil {
		u.Log("-> Анализ взят из общего кэша: эту запись по этой теме уже оценивали.")
	} else {
		res, err := u.AnalyzeTextBatch([]string{text}, topic)
		if err != nil {
			u.Log("-> ИИ не вернул оценку текста: %s", err)
			return false
		}
		if len(res) == 0 || res[0] == nil {
			u.Log("-> ИИ не ответил по этой записи, пропускаю.")
			return false
		}
		a = res[0]
		a["model"] = u.LLM.ModelFor(u.Ctx, "text")
		u.analysisCachePut(item, topic, a)
	}
	verdict := fmt.Sprintf("соответствие теме %d%%", jx.Int(a, "relevance"))
	if !u.admit(a, item, topic, verdict, "text", text) {
		return false
	}
	u.Log("-> Принято: %s.", verdict)
	u.PostSearchSuggest(item, topic)
	rec := analysisRecord(a, item, topic, "text")
	rec["image_local"], rec["phash"], rec["image"], rec["images"], rec["captions"] = "", "", "", []any{}, []any{text}
	return u.AddItemToPoolSafely(rec)
}

// analyzeCandidate — полный анализ (видео — раскадровкой), порог, кэш медиа и запись в пул.
func (u *U) analyzeCandidate(item jx.M, poster []byte, h History, topic string) bool {
	if jx.Str(item, "image") == "" {
		return u.analyzeTextCandidate(item, topic)
	}
	isVideo := jx.Str(item, "video") != ""
	kind := "картинка"
	if isVideo {
		kind = "видео"
	}
	u.Log("Полный анализ %s %s %s", candidateLogLabel(item), kind, jx.Str(item, "image"))
	var video *Storyboard
	if isVideo {
		video = u.VideoAnalysisInput(jx.Str(item, "video"), poster)
		if video.File != "" {
			defer os.Remove(video.File)
		}
	}
	frames := [][]byte{poster}
	if video != nil {
		frames = video.Frames
	}
	storyboard := video != nil && len(frames) > 1
	context := AnalysisContextBlock(item)
	caption := ""
	if context == "" {
		caption = PostCaptionForAI(item)
	}
	a := u.analysisCacheGet(item, topic)
	if a != nil {
		u.Log("-> Анализ взят из общего кэша: эту запись по этой теме уже оценивали.")
	} else {
		var sb *Storyboard
		if storyboard {
			sb = video
		}
		var err error
		a, err = u.AnalyzeContentWithVision(frames, caption, h, topic, sb, u.LLM.Settings().ThinkInParser, context, item)
		if err != nil {
			u.Log("-> ИИ не вернул корректную оценку: %s", err)
			return false
		}
		u.analysisCachePut(item, topic, a)
	}
	verdict := fmt.Sprintf("соответствие теме %d%%", jx.Int(a, "relevance"))
	if !u.admit(a, item, topic, verdict, "analysis", caption) {
		return false
	}
	tags := u.ContentTagsPublic(a["tags"])
	tagNote := ""
	if len(tags) > 0 {
		var paths []string
		for _, t := range tags {
			paths = append(paths, jx.Str(t, "path"))
		}
		tagNote = " Метки: " + strings.Join(paths, ", ") + "."
	}
	u.Log("-> Принято: %s.%s", verdict, tagNote)
	u.PostSearchSuggest(item, topic)
	rec := analysisRecord(a, item, topic, "")
	rec["image_local"] = u.MediaStoreImage(jx.Str(item, "image"), poster)
	rec["phash"] = ImagePhash(poster)
	if isVideo {
		if video != nil && video.File != "" {
			rec["video_local"] = u.CacheDownloadedVideo(jx.Str(item, "video"), video.File)
		} else {
			rec["video_local"] = u.MediaStoreVideo(jx.Str(item, "video"), "", false, nil)
		}
		if jx.Str(rec, "video_local") != "" {
			u.Log("-> Видео сохранено в кэш сайта.")
		} else {
			u.Log("-> Видео не удалось сохранить в кэш, на сайте будет исходная ссылка.")
		}
	}
	if g := jx.Maps(rec, "gallery"); len(g) > 0 {
		rec["gallery"] = u.GalleryCacheFiles(g, jx.Str(item, "image"))
	}
	if jx.Str(rec, "image_local") == "" {
		u.Log("-> Картинку не удалось сохранить в кэш, на сайте будет исходная ссылка.")
	}
	added := u.AddItemToPoolSafely(rec)
	if added {
		u.Log("[!] Запись сохранена и выведена на сайт.")
	} else {
		u.Log("-> Запись уже в пуле или оценена пользователем, пропускаю.")
	}
	return added
}

func (u *U) collectorStopReason(started time.Time, analyzed, added int) string {
	switch {
	case !u.Alive():
		return ParserSessionEnded
	case time.Since(started) > time.Duration(u.TInt("parser_minutes"))*time.Minute:
		return "Достигнут лимит времени одного запуска."
	case analyzed >= u.TInt("analyze_per_run"):
		return "Достигнут лимит полных анализов за запуск."
	case added >= u.TInt("added_per_run"):
		return "Достигнут лимит новых записей за запуск."
	case u.PoolTopicCount(u.SearchTopicKey()) >= u.TInt("pool_target"):
		return fmt.Sprintf("Пул заполнен до %d записей.", u.TInt("pool_target"))
	}
	return ""
}

func (u *U) parserPhaseWithTopic(phase, topic string) {
	if topic == "" {
		topic = u.CurrentTopic()
	}
	if topic != "" {
		phase += " — тема: «" + topic + "»"
	}
	u.ParserPhase(phase)
}

// RunCollector — один проход. Возвращает true, если тема сменилась и сбор нужно начать заново.
func (u *U) RunCollector() bool {
	u.run = newRun(u.App)
	started := time.Now()
	topic := u.CurrentTopic()
	u.KickVideoWorker()
	u.ParserProgress(jx.M{"deadline": started.Unix() + int64(u.TInt("parser_minutes")*60)}, true)
	u.Log("")
	u.Log("⏱️ Начинается подбор контента — тема «%s», не дольше %d мин.", topic, u.TInt("parser_minutes"))
	u.Log("")
	u.ParserStage("collect")
	u.parserPhaseWithTopic("сбор постов", topic)
	u.MirrorCollectUser()
	if n := u.MediaCleanup(u.AllUsersPoolItems(u.Ctx), false, false); n > 0 {
		u.Log("Кэш медиа: удалено устаревших файлов: %d.", n)
	}
	if n := u.prunePoolAlreadyRated(); n > 0 {
		u.Log("Пул: убрано записей, пост которых уже оценён: %d.", n)
	}
	if n := u.PruneSourceDuplicates(); n > 0 {
		u.Log("Пул: убрано лишних записей одного источника: %d.", n)
	}
	u.PoolStampLegacy()
	u.CachePoolGalleries(4)
	u.PruneDeadPoolItems(false, false, true)
	seen := u.collectKnown()
	topic = u.CurrentTopic()
	key := u.SearchTopicKey()
	if q := u.CurrentQuery(); q != "" {
		u.Log("Поиск: «%s» — ищу посты по площадкам (тема «%s» — только для оценки).", q, topic)
	}
	u.Log("Модели: быстрая оценка — %s, полный анализ — %s.", u.LLM.ModelFor(u.Ctx, "prescreen"), u.LLM.ModelFor(u.Ctx, "analysis"))
	u.TopicEnsure(u.Ctx, topic)
	u.TopicClassifyPending(1, topic)
	u.TopicProfileEnsure(topic)
	u.DvachBoardsForTopic(topic, true)
	d := u.Catalog()
	due := u.SourceDiscoveryDue(d, topic)
	discovered := false
	var tDiscovery time.Duration
	if usable := u.TopicUsableSources(d, topic); usable < u.TInt("min_sources") && u.TBool("discovery") {
		last := DiscoveryLastRun(d, topic)
		if jx.Bool(d.Discovery, "requested") || now()-last >= config.DiscoveryShortageInterval {
			u.Log("Источников по теме %d из %d — сначала ищу новые (до %d мин).", usable, u.TInt("min_sources"), config.DiscoveryBlockingSeconds/60)
			t0 := time.Now()
			u.RunSourceDiscovery(topic, config.DiscoveryBlockingSeconds)
			tDiscovery += time.Since(t0)
			due, discovered = false, true
			if u.CurrentTopic() != topic {
				return true
			}
		} else {
			u.Log("Источников по теме %d из %d — автоподбор уже был в %s — собираю из того, что есть.", usable, u.TInt("min_sources"), time.Unix(last, 0).Format("15:04"))
		}
	}
	if reason := u.collectorStopReason(started, 0, 0); reason != "" {
		u.Log("%s", reason)
		if due && u.Alive() {
			u.RunSourceDiscovery(topic, 0)
		}
		return false
	}
	cache := u.screenedCacheLoad(key)
	scores := map[string]int{}
	screenedHashes := map[string]int{}
	rejected := map[string]bool{}
	for _, e := range cache {
		scores[e.URL] = e.Score
		if e.Phash != "" {
			screenedHashes[e.Phash] = e.Score
		}
		if e.Score >= u.TInt("prescreen_junk") {
			rejected[e.URL] = true
		}
	}
	h := u.History()
	format := u.TopicFormatOf(key)
	var byPlatform map[string][]jx.M
	if u.CurrentQuery() != "" {
		byPlatform = u.QuerySearchCollect(key)   // поиск по запросу сам включает каталог темы
	} else {
		byPlatform = u.collectPostsByPlatform(key)
	}
	collected := 0
	for _, l := range byPlatform {
		collected += len(l)
	}
	u.ParserStage("filter")
	candidates, skipped := u.buildCandidateQueue(byPlatform, seen, rejected, u.TopicVerdictStats(key), &format, u.ContentFilterValues(), key)
	if hs := u.run.HotStats; hs != nil {
		var per []string
		for p, n := range hs["per"].(map[string]int) {
			per = append(per, fmt.Sprintf("%s %d", p, n))
		}
		sort.Strings(per)
		line := fmt.Sprintf("Горячие: %d из %d", jx.Int(hs, "short"), jx.Int(hs, "total"))
		if len(per) > 0 {
			line += " (" + strings.Join(per, ", ") + ")"
		}
		if top := jx.Map(hs, "top"); top != nil {
			line += fmt.Sprintf("; самый горячий — %s ×%v к обычному", jx.Str(top, "source"), top["times"])
		}
		if x := jx.Int(hs, "excluded"); x > 0 {
			line += fmt.Sprintf(". Отброшено по словам «не подходит»: %d", x)
		}
		line += fmt.Sprintf(". Срез очереди на источник: %d, срез шортлиста: %d", jx.Int(hs, "per_source_cut"), jx.Int(hs, "shortlist_cut"))
		u.Log("%s, из доверенных тредов %d; имиджборд без признаков темы — в хвост: %d.", line, jx.Int(hs, "trusted"), jx.Int(hs, "imageboard_tail"))
	}
	candidates = u.hotTextJudge(candidates, key)
	collected := 0
	for _, posts := range byPlatform {
		collected += len(posts)
	}
	notes := fmt.Sprintf("📌 Тема: «%s». Собрано постов: %d. Кандидатов к проверке: %d.", topic, collected, len(candidates))
	for _, s := range []struct{ k, t string }{{"rejected_before", " Уже отсеяны в прошлых запусках: %d."}, {"outdated", " Устаревших: %d."},
		{"ukraine", " Украинский контент отсеян: %d."}, {"format", " Отсеяно форматом темы: %d."}, {"filter", " Отсеяно фильтрами «Только с медиа» / «Только медиа»: %d."},
		{"source_busy", " Источники в ленте или на отдыхе: %d."}, {"seen", " Уже видели: %d."}, {"duplicate", " Дубли: %d."}, {"no_key", " Без текста и картинки: %d."}} {
		if skipped[s.k] > 0 {
			notes += fmt.Sprintf(s.t, skipped[s.k])
		}
	}
	u.Log("%s Быстрая оценка по картинке %d px: отсев мусора при уверенности от %d%%, тему не проверяет.", notes, config.PrescreenMaxSide, u.TInt("prescreen_junk"))
	pc := &prescreenCtx{stats: map[string]int{}, cached: scores, newCache: map[string]screenedEntry{}, knownHashes: u.collectKnownHashes(), screenedHashes: screenedHashes}
	analyzed, added, done := 0, 0, 0
	funnel := func() jx.M { return passFunnel(collected, len(candidates), skipped, pc.stats, analyzed, added) }
	stop := ""
	restart := false
	var queue []prescreenRow
	var tAnalyze time.Duration
	need := max(1, min(u.TInt("added_per_run"), u.TInt("pool_target")-u.PoolTopicCount(key)))
	u.ParserProgress(jx.M{"total": len(candidates), "queue_at": now(), "need": need, "funnel": funnel()}, false)
	lastProgress := time.Now()
	deadline := started.Add(time.Duration(u.TInt("parser_minutes")) * time.Minute)
	nBatches := (len(candidates) + config.PrescreenBatch - 1) / config.PrescreenBatch
	takenSrc := map[string]bool{}
outer:
	for bi := 0; bi < nBatches; bi++ {
		batch := dropTakenSources(candidates[bi*config.PrescreenBatch:min(len(candidates), (bi+1)*config.PrescreenBatch)], takenSrc)
		if time.Since(lastProgress) >= 30*time.Second {
			left := time.Until(deadline)
			u.Log("⏳ Прогресс: %d%% (%d/%d пачек). Прошло: %.1f мин. Осталось до лимита: ≈ %dм %dс", (bi+1)*100/max(1, nBatches), bi, nBatches,
				time.Since(started).Minutes(), int(left.Minutes()), int(left.Seconds())%60)
			lastProgress = time.Now()
		}
		if stop = u.collectorStopReason(started, analyzed, added); stop != "" {
			break
		}
		if u.CurrentTopic() != topic {
			stop, restart = "Тема поиска изменилась на «"+u.CurrentTopic()+"» — начинаю заново с новой темой.", true
			break
		}
		if u.SearchTopicKey() != key {
			stop, restart = "Вторая тема изменилась — собираю заново.", true
			break
		}
		var texts, media []jx.M
		for _, it := range batch {
			if jx.Str(it, "image") == "" {
				texts = append(texts, it)
			} else {
				media = append(media, it)
			}
		}
		for _, t := range u.textCandidatesPriority(texts, topic) {
			queue = append(queue, prescreenRow{Item: t})
		}
		u.ParserStage("prescreen")
		u.parserPhaseWithTopic("быстрая оценка", topic)
		var passed []prescreenRow
		if len(media) > 0 {
			passed = u.prescreenBatch(media, key, pc, deadline)
		}
		u.cacheMerge(pc.newCache)
		pc.newCache = map[string]screenedEntry{}
		done += len(batch) - len(passed) - len(texts)
		u.ParserProgress(jx.M{"done": done, "funnel": funnel()}, false)
		queue = append(queue, passed...)
		if len(queue) < config.AnalyzeBlock && bi < nBatches-1 {
			continue
		}
		u.ParserStage("analysis")
		u.parserPhaseWithTopic("полный анализ", topic)
		for _, c := range queue {
			if stop = u.collectorStopReason(started, analyzed, added); stop != "" {
				break outer
			}
			if takenSrc[ContentSourceKey(c.Item)] {
				done++
				continue
			}
			analyzed++
			t0 := time.Now()
			if u.analyzeCandidate(c.Item, c.Poster, u.History(), key) {
				added++
				if src := ContentSourceKey(c.Item); sourcePoolCount(u.Pool(), key, src) >= u.TInt("per_source_pool") {
					takenSrc[src] = true
				}
			}
			tAnalyze += time.Since(t0)
			done++
			u.ParserProgress(jx.M{"done": done, "analyzed": analyzed, "added": added, "funnel": funnel()}, false)
		}
		queue = nil
	}
	u.cacheMerge(pc.newCache)
	u.ParserProgress(jx.M{"done": len(candidates)}, false)
	if !restart {
		u.parserExpectedSeconds(u.ID, int64(time.Since(started).Seconds()))
	}
	if stop != "" {
		u.Log("%s", stop)
	}
	u.ParserStage("pool")
	u.KVSet(u.Ctx, parserLastPassKey(u.ID), jx.M{"at": now(), "funnel": funnel()}, 0)   // воронка — для конвейера студии вне работы
	u.Log("")
	u.Log("✅ Подбор завершён за %.1f мин: добавлено %d, проанализировано %d, отсеяно быстрой оценкой %d, повторов %d.", time.Since(started).Minutes(), added, analyzed,
		pc.stats["rejected"], pc.stats["duplicates"])
	total := time.Since(started)
	u.Log("Время прохода: автоподбор %.1f мин, сбор %.1f мин, анализ %.1f мин.", tDiscovery.Minutes(), max(0, (total-tDiscovery-tAnalyze).Minutes()), tAnalyze.Minutes())
	u.Log("")
	_ = h
	if !restart && !discovered && (added == 0 || len(candidates) < u.TInt("pool_target")) && u.PoolTopicCount(key) < u.TInt("pool_target") && u.Alive() && u.CurrentTopic() == topic &&
		!u.TopicSame(config.DefaultTopic, topic) && now()-DiscoveryLastRun(u.Catalog(), topic) >= config.DiscoveryShortageInterval && u.TBool("discovery") {
		if added == 0 {
			u.Log("Из источников темы нечего оценивать — ищу новые источники и соберу заново.")
			before := u.TopicOwnSourcesCount(u.Catalog(), topic)
			u.RunSourceDiscovery(topic, 0)
			return u.TopicOwnSourcesCount(u.Catalog(), topic) > before
		}
		// Кандидатов мало — автоподбор в конце прохода, чтобы следующий проход был шире; сам проход не перезапускаем
		u.Log("Кандидатов к проверке %d при цели ленты %d — ищу новые источники в конце прохода.", len(candidates), u.TInt("pool_target"))
		u.RunSourceDiscovery(topic, 0)
		due = false
	}
	if due && !restart && u.Alive() {
		u.RunSourceDiscovery(topic, 0)
	}
	return restart
}
