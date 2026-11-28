package posta

import (
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// ---- Сбор постов ----

// ParseDvach — медиа свежих тредов доски; пригодность определяет анализ медиа.
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
			id := jx.Str(post, "num")
			rec := jx.M{"source_key": "dvach:" + board + ":" + b.thread, "posted_at": jx.Int(post, "timestamp"), "board": board, "thread_id": b.thread, "post_id": id,
				"source": PlatformDvach + " /" + board + "/ · " + b.title, "source_url": DvachPostURL(board, b.thread, id),
				"image": files[0]["image"], "images": []any{files[0]["image"]}, "video": files[0]["video"], "captions": []any{},
				"engagement": eng[id], "thread_title": trimRunes(title, config.ThreadTitleMax), "is_op": id == b.thread}
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

// ParseDvachThread collects only the approved thread, independently of board catalog ordering.
func (u *U) ParseDvachThread(board, thread, name string) []jx.M {
	posts := DvachThreadPosts(u.DvachJSONCached(board, thread))
	if len(posts) == 0 {
		return nil
	}
	if jx.Bool(posts[0], "closed") {
		return nil
	}
	eng := DvachThreadEngagement(posts, nil)
	var out []jx.M
	for _, post := range posts[:min(len(posts), config.DvachPostsPerThread)] {
		files := DvachPostMedia(board, post, true)
		if len(files) == 0 {
			continue
		}
		id := jx.Str(post, "num")
		rec := jx.M{"source_key": "dvach:" + board + ":" + thread, "posted_at": jx.Int(post, "timestamp"), "board": board, "thread_id": thread, "post_id": id,
			"source": name, "source_url": DvachPostURL(board, thread, id), "image": files[0]["image"], "images": []any{files[0]["image"]}, "video": files[0]["video"],
			"captions": []any{}, "engagement": eng[id], "thread_title": name, "is_op": id == thread}
		if len(files) > 1 {
			rec["gallery"] = files
		}
		out = append(out, rec)
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
	name = SourceDisplayName(platform, name)
	src := jx.M{"id": id, "name": name}
	key := SourceIDFor(platform, id)
	u.Log("Парсинг %s %s...", platform, label)
	var posts []jx.M
	var times []int64
	var subs int64
	var rate *float64
	page := ""
	var err error
	acceptText := u.TopicsAcceptText(jx.Strs(entry, "topics"))
	switch platform {
	case PlatformTelegram:
		page = u.FetchTelegramChannelPage(id, 0)
		if page == "" || !strings.Contains(page, `data-post="`) {
			// A personal MTProto read is not a shared catalog activity verdict.
			if u.ID != "" && u.TelegramUserConnected() {
				if native, err := u.telegramUserHistoryPosts(id, name, config.TelegramPostsPerChannel); err == nil {
					var ready []jx.M
					for _, post := range native {
						if NewsPostOutdated(post, entry) {
							continue
						}
						if len(jx.Maps(post, "source_attachments")) > 0 || jx.Str(post, "image") != "" || (acceptText && jx.Bool(post, "text_post")) {
							ready = append(ready, post)
						}
					}
					u.Log("-> %s %s: резервное чтение MTProto, готовых постов: %d", platform, label, len(ready))
					return ready, nil, ""
				}
			}
			u.Log("-> %s %s: страница не загрузилась или не содержит доступной истории.", platform, label)
			return nil, nil, ""
		}
		posts = TelegramPostsFromHTML(page, src, config.TelegramPostsPerChannel, acceptText, 120)
		f := TelegramPageFacts(page)
		times, subs = f.PostTimes, f.Subscribers
		rate = SourceViewRate(subs, f.PostViews)
		if len(times) == 0 && len(posts) == 0 {
			// A login/challenge page or a temporarily unreadable channel
			// is not proof that the source is closed or inactive.
			u.Log("-> %s %s: нет подтверждённых публикаций.", platform, label)
			return nil, nil, ""
		}
	case PlatformRSS:
		withText := acceptText && !jx.Bool(entry, "news")
		posts, err = u.RSSFetchPosts(id, config.RSSPostsPerFeed, name, true, withText)
		if err == nil {
			posts = u.NewsEnrichPosts(posts, entry)
		}
	case PlatformOK:
		posts, err = u.OKFetchPosts(id, OKPostsPerGroup, name)
	case PlatformTikTok:
		posts, err = u.TikTokFetchPosts(u.Ctx, id, TikTokPostsPerAccount, name)
	default:
		if u.VKCanRead(u.Ctx) {
			posts = u.BuildVKPreviewPosts(u.Ctx, src, config.VKPostsPerPublic, acceptText, 120)
		} else {
			posts, err = u.vkPublicPosts(id, name, config.VKPostsPerPublic, acceptText, 120)
		}
	}
	if err != nil {
		u.Log("-> %s %s: %s", platform, label, err)
		return nil, nil, ""
	}
	if platform != PlatformTelegram {
		times = make([]int64, 0, len(posts))
		for _, p := range posts {
			times = append(times, jx.Int(p, "posted_at"))
		}
	}
	activity := SourceActivity(times, 0)
	updated, event := ApplySourceActivity(entry, activity, 0, SourceDeadAudienceReason(subs, rate))
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
	switch event {
	case "frozen":
		u.registryUpsert(registryWrite{SourceID: SourceIDFor(platform, jx.Str(entry, "id")), Platform: platform, Status: RegStatusDead, Reason: "заморожен: " + jx.Str(updated, "frozen_reason")})
	case "revived":
		u.registryUpsert(registryWrite{SourceID: SourceIDFor(platform, jx.Str(entry, "id")), Platform: platform, Status: RegStatusCatalog, Reason: "в каталоге"})
	}
	if event == "frozen" || event == "still_frozen" {
		skip := activeOnly
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
	freshCount := len(fresh)
	playable := fresh[:0]
	for _, p := range fresh {
		if (!jx.Bool(p, "video_unavailable") && !jx.Bool(p, "media_incomplete")) || len(jx.Maps(p, "source_attachments")) > 0 {
			playable = append(playable, p)
		}
	}
	old, hidden := len(posts)-freshCount, freshCount-len(playable)
	line := fmt.Sprintf("-> %s %s: найдено медиа-постов: %d", platform, label, len(playable))
	if old > 0 {
		line += fmt.Sprintf(", устаревших пропущено: %d", old)
	}
	if hidden > 0 {
		line += fmt.Sprintf(", неполных медиа-постов пропущено: %d", hidden)
	}
	line += " (" + DescribeSourceActivity(activity)
	if subs > 0 {
		line += "; " + DescribeSourceAudience(subs, rate, nil)
	}
	u.Log("%s).", line)
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
	blogMode := u.personalBlogMode(topic)
	blogKinds := map[string]string{}
	if blogMode != personalBlogAny {
		var err error
		blogKinds, err = u.personalBlogSourceKinds(topic)
		if err != nil {
			u.Log("Не удалось получить типы источников: %v", err)
			return by // strict filter fails closed
		}
	}
	blogAllowed := func(source string) bool { return personalBlogAllowed(blogMode, blogKinds[source]) }
	overrides := u.sourceOverridesForTopic(topic)
	sourceAllowed := func(source string) bool {
		return !sourceExcludedByOverride(overrides, source) && blogAllowed(source)
	}
	stats := map[string]int{}
	updates := map[string]jx.M{}
	rep := map[string]*platReport{}
	for _, k := range reportOrder {
		rep[k] = &platReport{}
	}
	// Boards are discovery locations; only explicitly approved threads are collected.
	for _, thread := range u.approvedImageboardThreads(u.Catalog(), topic) {
		if !sourceAllowed(jx.Str(thread, "source_id")) {
			continue
		}
		if !u.Alive() {
			return by
		}
		platform, board, id := jx.Str(thread, "platform"), jx.Str(thread, "board"), jx.Str(thread, "thread_id")
		var found []jx.M
		label := "fourchan"
		if platform == PlatformDvach {
			label = "dvach"
			found = u.ParseDvachThread(board, id, jx.Str(thread, "name"))
		} else {
			found = u.FourchanThreadMedia(board, jx.M{"no": jx.ToInt(id)}, 200, nil)
		}
		report := rep[label]
		report.Sources++
		if len(found) > 0 {
			report.With++
		}
		report.Posts += len(found)
		by[platform] = append(by[platform], found...)
	}

	activeOnly := jx.Bool(u.ContentFilterValues(), "active_sources_only")
	d := u.catalogWithPersonalPins(topic)
	t := now()
	poll := map[string]jx.M{}
	sharedCache := map[string]sharedPosts{}
	telegramFallback := newAttachmentRecovery(u)
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			rawID := jx.Str(e, "id")
			id := strings.TrimLeft(strings.TrimSpace(rawID), "@")
			if !sourceAllowed(SourceIDFor(c.Platform, id)) {
				continue
			}
			if u.CatalogEntryMatchesTopic(e, topic, false) && !IsSourceFrozen(e, 0) {
				var shared sharedPosts
				pkey := sourcePollMapKey(c.Platform, rawID)
				if sharedSourceEligible(c.Platform, e) && u.CacheGet(u.Ctx, sharedSourceKey(c.Platform, id), &shared) {
					sharedCache[pkey] = shared
					continue // готовый общий источник не занимает бюджет сетевых запросов
				}
				poll[pkey] = e
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
		if u.CacheGet(u.Ctx, personalSourcePollKey(u.ID, platform, jx.Str(e, "id")), &cp) {
			cachedMap[pkey], cache[pkey] = true, cp
		}
	}
	fetchSet := map[string]bool{}
	for _, k := range sourcePollFetchSet(poll, cachedMap, t, u.TInt("fetches_per_pass"), u.CombinedSourceVerdicts(u.Ctx, topic)) {
		fetchSet[k] = true
	}
	fetched, fromCache := 0, 0
	var nextMin *int64
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if !u.Alive() {
				return by
			}
			if !u.CatalogEntryMatchesTopic(e, topic, false) {
				stats["other_topic"]++
				continue
			}
			rawID := jx.Str(e, "id")
			sourceID := SourceIDFor(c.Platform, strings.TrimLeft(strings.TrimSpace(rawID), "@"))
			if !sourceAllowed(sourceID) {
				continue
			}
			if u.UASourceBlocked(SourceIDFor(c.Platform, rawID), jx.M{"title": jx.Str(e, "name")}) != "" {
				stats["ukraine_source"]++
				continue
			}
			label := platformLabel(c.Platform, e)
			rep[label].Sources++
			pkey := sourcePollMapKey(c.Platform, rawID)
			var posts []jx.M
			var updated jx.M
			event := ""
			shared, useShared := sharedCache[pkey]
			if useShared {
				posts = shared.Posts
				if c.Platform == PlatformRSS {
					posts = u.NewsEnrichPosts(posts, e)
				}
				fromCache++
			} else if _, inPoll := poll[pkey]; inPoll && !fetchSet[pkey] {
				if !cachedMap[pkey] {
					continue
				}
				posts = cache[pkey].Posts
				fromCache++
				activity := jx.Map(e, "activity")
				left := SourcePollInterval(activity) - (t - jx.Int(activity, "checked_at"))
				if nextMin == nil || left < *nextMin {
					nextMin = &left
				}
			} else {
				posts, updated, event = u.collectCatalogSource(c.Platform, e, activeOnly)
				if len(posts) > 0 {
					u.CacheSet(u.Ctx, personalSourcePollKey(u.ID, c.Platform, rawID), cachedPosts{now(), posts}, config.SourcePollMax*2*time.Second)
				}
				if _, inPoll := poll[pkey]; inPoll {
					fetched++
				}
			}
			if useShared && c.Platform == PlatformTelegram && len(posts) == 0 {
				posts = telegramFallback.telegramSnapshot(strings.TrimLeft(rawID, "@"), SourceDisplayName(c.Platform, jx.Str(e, "name")))
			}
			if sharedSourceEligible(c.Platform, e) {
				stored := u.publicSourcePosts(u.Ctx, sourceID, t-7*config.DaySeconds, 100)
				posts = mergePublicPosts(posts, stored)
			}
			if event != "" {
				stats[event]++
			}
			if updated != nil {
				updates[catalogSourceUpdateKey(c.Kind, c.Platform, rawID)] = updated
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
	u.collectSuggestedPosts(topic, activeOnly, sourceAllowed, by, rep)
	if u.TBool("post_search") && u.CurrentQuery() == "" {
		search := u.PostSearchCollect(topic)
		for _, p := range []string{PlatformRSS, PlatformTelegram, PlatformVK} {
			by[p] = append(by[p], search.By[p]...)
		}
		u.Log("Поиск постов: YouTube %d (запросов %d), Telegram %d (запросов %d), VK %d (запросов %d) — после фильтра по словам.",
			len(search.By[PlatformRSS]), search.Done["youtube"], len(search.By[PlatformTelegram]), search.Done["telegram"], len(search.By[PlatformVK]), search.Done["vk"])
	}
	for platform, posts := range u.classifiedPublicPosts(topic) {
		by[platform] = mergePublicPosts(by[platform], posts)
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
	u.Log("%s", platformSummaryLine(rep, true))
	if u.TBool("post_search") && u.CurrentQuery() == "" && u.Alive() {
		// These search platforms were previously used only with an explicit query.
		plan := u.QuerySearchPlan(topic, topic)
		off := u.querySearchOffset("topic:" + TopicNorm(topic))
		extra, _ := u.searchEngineRun(plan, off, time.Duration(min(45, u.TInt("query_search_seconds")))*time.Second,
			map[string]bool{"Rutube": true, "9GAG": true, "Lemmy": true, "Mastodon": true, "Joyreactor": true})
		for platform, posts := range extra {
			by[platform] = append(by[platform], posts...)
		}
	}

	if blogMode != personalBlogAny {
		// This also covers direct post-search and cached public posts, whose
		// sources may never have appeared in the legacy catalog.
		for platform, posts := range by {
			allowed := make([]jx.M, 0, len(posts))
			for _, post := range posts {
				if blogAllowed(ContentSourceKey(post)) {
					allowed = append(allowed, post)
				}
			}
			by[platform] = allowed
		}
	}
	return by
}

var collectorSuggestionPrefixRe = regexp.MustCompile(`^(Telegram|ВКонтакте|RSS|Одноклассники|TikTok) \| `)

func (u *U) collectSuggestedPosts(topic string, activeOnly bool, sourceAllowed func(string) bool, by map[string][]jx.M, rep map[string]*platReport) {
	// Находки автоподбора подмешиваются всегда: с каждой берём один лучший по горячести пост (поровну по площадкам, предел — suggestion_sources_per_run)
	byPlat := map[string][]jx.M{}
	for _, s := range u.Catalog().Suggestions {
		p := jx.Str(s, "platform")
		sid := jx.Str(s, "source_id")
		if sid == "" {
			sid = SourceIDFor(p, jx.Str(s, "id"))
		}
		if !sourceAllowed(sid) {
			continue
		}
		if CatalogKind(p) == "" || !u.TopicSame(jx.Str(s, "topic"), topic) || int(jx.Int(s, "score")) < u.TInt("suggest_score") {
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
		name := collectorSuggestionPrefixRe.ReplaceAllString(jx.Str(s, "name"), "")
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
