package posta

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/jx"
	"posta/internal/llm"
)

// Сборщик: посты всех источников темы → очередь кандидатов → быстрая оценка пачками → полный анализ лучших → пул.

func PostCaptionForAI(item jx.M) string {
	if ImageboardItem(item) {
		return ""
	}
	t := strings.TrimSpace(jx.Str(item, "text"))
	if t == EmptyPostText {
		t = ""
	}
	if jx.Bool(item, "forwarded") {
		from := jx.Str(item, "forwarded_from")
		if from == "" {
			from = "неизвестен"
		}
		t = "[Это пересланное сообщение из «" + from + "». Оценивай содержание самого пересланного сообщения и авторский комментарий отдельно; не приписывай пересланный текст текущему каналу и не считай сам факт репоста доказательством релевантности.]\n" + t
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
		if u.VKCanRead(u.Ctx) {
			posts = u.BuildVKPreviewPosts(u.Ctx, src, config.VKPostsPerPublic, acceptText, 120)
		} else {
			posts, err = u.vkPublicPosts(id, jx.Str(src, "name"), config.VKPostsPerPublic, acceptText, 120)
		}
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
	stats := map[string]int{}
	updates := map[string]jx.M{}
	rep := map[string]*platReport{}
	for _, k := range reportOrder {
		rep[k] = &platReport{}
	}
	// Boards are discovery locations; only explicitly approved threads are collected.
	for _, thread := range u.approvedImageboardThreads(u.Catalog(), topic) {
		if sourceExcludedByOverride(overrides, jx.Str(thread, "source_id")) || !blogAllowed(jx.Str(thread, "source_id")) {
			continue
		}
		if !u.Alive() {
			return by
		}
		platform, board, id := jx.Str(thread, "platform"), jx.Str(thread, "board"), jx.Str(thread, "thread_id")
		var found []jx.M
		if platform == PlatformDvach {
			found = u.ParseDvachThread(board, id, jx.Str(thread, "name"))
			rep["dvach"].Sources++
			if len(found) > 0 {
				rep["dvach"].With++
			}
			rep["dvach"].Posts += len(found)
		} else {
			found = u.FourchanThreadMedia(board, jx.M{"no": jx.ToInt(id)}, 200, nil)
			rep["fourchan"].Sources++
			if len(found) > 0 {
				rep["fourchan"].With++
			}
			rep["fourchan"].Posts += len(found)
		}
		by[platform] = append(by[platform], found...)
	}

	vk := true
	activeOnly := jx.Bool(u.ContentFilterValues(), "active_sources_only")
	d := u.catalogWithPersonalPins(topic)
	t := now()
	poll := map[string]jx.M{}
	sharedCache := map[string]sharedPosts{}
	for _, c := range CatalogKinds {
		if c.Platform == PlatformVK && !vk {
			continue
		}
		for _, e := range *d.List(c.Kind) {
			if sourceExcludedByOverride(overrides, SourceIDFor(c.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@"))) ||
				!blogAllowed(SourceIDFor(c.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@"))) {
				continue
			}
			if u.CatalogEntryMatchesTopic(e, topic, false) && !IsSourceFrozen(e, 0) {
				id := strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@")
				var shared sharedPosts
				if sharedSourceEligible(c.Platform, e) && u.CacheGet(u.Ctx, sharedSourceKey(c.Platform, id), &shared) {
					sharedCache[sourcePollMapKey(c.Platform, jx.Str(e, "id"))] = shared
					continue // готовый общий источник не занимает бюджет сетевых запросов
				}
				poll[sourcePollMapKey(c.Platform, jx.Str(e, "id"))] = e
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
			if sourceExcludedByOverride(overrides, SourceIDFor(c.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@"))) ||
				!blogAllowed(SourceIDFor(c.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@"))) {
				continue
			}
			if u.UASourceBlocked(SourceIDFor(c.Platform, jx.Str(e, "id")), jx.M{"title": jx.Str(e, "name")}) != "" {
				stats["ukraine_source"]++
				continue
			}
			label := platformLabel(c.Platform, e)
			rep[label].Sources++
			pkey := sourcePollMapKey(c.Platform, jx.Str(e, "id"))
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
				left := SourcePollInterval(jx.Map(e, "activity")) - (t - jx.Int(jx.Map(e, "activity"), "checked_at"))
				if nextMin == nil || left < *nextMin {
					nextMin = &left
				}
			} else {
				posts, updated, event = u.collectCatalogSource(c.Platform, e, activeOnly)
				if len(posts) > 0 {
					u.CacheSet(u.Ctx, personalSourcePollKey(u.ID, c.Platform, jx.Str(e, "id")), cachedPosts{now(), posts}, config.SourcePollMax*2*time.Second)
				}
				if _, inPoll := poll[pkey]; inPoll {
					fetched++
				}
			}
			if sharedSourceEligible(c.Platform, e) {
				stored := u.publicSourcePosts(u.Ctx, SourceIDFor(c.Platform, strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@")), t-7*config.DaySeconds, 100)
				posts = mergePublicPosts(posts, stored)
			}
			if event != "" {
				stats[event]++
			}
			if updated != nil {
				updates[catalogSourceUpdateKey(c.Kind, c.Platform, jx.Str(e, "id"))] = updated
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
		sid := jx.Str(s, "source_id")
		if sid == "" {
			sid = SourceIDFor(p, jx.Str(s, "id"))
		}
		if sourceExcludedByOverride(overrides, sid) || !blogAllowed(sid) {
			continue
		}
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
	u.Log("%s", platformSummaryLine(rep, vk))
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
		p = ImageboardMediaOnly(p)
		imgs := uniqueStrings(jx.Strs(p, "images"))
		if len(imgs) == 0 {
			if s := jx.Str(p, "image"); s != "" {
				imgs = []string{s}
			}
		}
		if len(imgs) == 0 {
			if video := jx.Str(p, "video"); strings.HasPrefix(video, "https://") {
				c := jx.Copy(p)
				c["image"], c["images"], c["_video_without_poster"] = video, []string{video}, true
				out = append(out, c)
				continue
			}
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

// CandidatePostKey — canonical post URL, keeping query parameters or anchors
// when they identify the post rather than tracking a visit.
func CandidatePostKey(item jx.M) string {
	raw := jx.Str(item, "url")
	if raw == "" {
		raw = jx.Str(item, "source_url")
	}
	if raw == "" && jx.Str(item, "board") != "" && jx.Str(item, "thread_id") != "" && jx.Str(item, "post_id") != "" {
		raw = DvachPostURL(jx.Str(item, "board"), jx.Str(item, "thread_id"), jx.Str(item, "post_id"))
	}
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "post:" + raw
	}
	host := strings.ToLower(parsed.Hostname())
	query := parsed.Query()
	if (host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")) && parsed.Path == "/watch" && query.Get("v") != "" {
		parsed.Host = "www.youtube.com"
		query = url.Values{"v": {query.Get("v")}}
	} else {
		for key := range query {
			k := strings.ToLower(key)
			if strings.HasPrefix(k, "utm_") || k == "fbclid" || k == "gclid" || k == "yclid" {
				query.Del(key)
			}
		}
	}
	parsed.RawQuery = query.Encode()
	if host == "2ch.hk" || strings.HasSuffix(host, ".2ch.hk") || host == "boards.4chan.org" {
		if id := jx.Str(item, "post_id"); id != "" {
			if host == "boards.4chan.org" {
				parsed.Fragment = "p" + id
			} else {
				parsed.Fragment = id
			}
		}
	} else {
		parsed.Fragment = ""
	}
	return "post:" + parsed.String()
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
		for _, key := range repeatKeys(r) {
			k.keys["remember:"+key] = true
		}
		if img := ContentImageKey(r); img != "" && !repeatFileIsVideo(r) {
			k.keys[img] = true
		}
		for _, g := range jx.Maps(r, "gallery") {
			if s := jx.Str(g, "image"); s != "" && !repeatFileIsVideo(g) {
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
	known := recordKnownKeys(append(u.Pool(), recentRepeatRecords(u.History().All(), now())...))
	if err := loadRepeatMemory(u.Ctx, u.DB.Pool, u.ID, known, nil); err != nil {
		u.Log("Не удалось прочитать память повторов: %v", err)
	}
	return known
}

func (u *U) collectKnownHashes() map[string]string {
	out := map[string]string{}
	for _, r := range append(u.Pool(), recentRepeatRecords(u.History().All(), now())...) {
		for _, h := range repeatPhashes(r) {
			out[h] = jx.Str(r, "source")
		}
	}
	if err := loadRepeatMemory(u.Ctx, u.DB.Pool, u.ID, nil, out); err != nil {
		u.Log("Не удалось прочитать отпечатки повторов: %v", err)
	}
	return out
}

func candidateIsKnown(item jx.M, k *knownKeys) bool {
	for _, key := range repeatKeys(item) {
		if k.keys["remember:"+key] {
			return true
		}
	}
	var imgs []string
	for _, f := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		if !PostItemHasVideo(f) {
			imgs = append(imgs, jx.Str(f, "image"))
		}
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
	known := recordKnownKeys(recentRepeatRecords(u.History().All(), now()))
	before := u.Pool()
	after, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
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
	if err != nil {
		u.Log("Не удалось проверить пул на оценённые записи: %v", err)
		return 0
	}
	u.MediaReleaseRemoved(u.Ctx, before, after)
	return len(before) - len(after)
}

// AddItemToPoolSafely keeps visible and storage quotas separate for the
// ordinary collector. The source autopilot retains its existing quota policy.
func (u *U) AddItemToPoolSafely(item jx.M) bool {
	added := false
	topicKey := jx.Str(item, "topic")
	_, autopilotDelivery := u.Ctx.Value(autopilotDeliveryContextKey{}).(autopilotDeliveryGuard)
	var visible func(jx.M) bool
	if !autopilotDelivery {
		var err error
		visible, err = u.poolVisibilityFilter(topicKey)
		if err != nil {
			u.Log("Не удалось проверить фильтры перед сохранением: %v", err)
			return false
		}
		if !visible(item) {
			u.Log("-> Запись не сохранена: текущие фильтры её скрывают.")
			return false
		}
	}
	u.prepareRepeatFiles(item)
	if !jx.Has(item, "pooled_at") {
		item["pooled_at"] = now()
	}
	before := u.Pool()
	known := u.collectKnown()
	target := u.TInt("pool_target")
	after, err := u.poolUpdate(func(tx pgx.Tx, pool []jx.M) ([]jx.M, error) {
		if guard, ok := u.Ctx.Value(autopilotDeliveryContextKey{}).(autopilotDeliveryGuard); ok {
			if PostItemHasVideo(item) && u.MediaExisting(jx.Str(item, "video_local")) == "" {
				return nil, nil
			}
			if !PostItemHasVideo(item) && ContentImageKey(item) != "" && u.MediaExisting(jx.Str(item, "image_local")) == "" {
				return nil, nil
			}
			filters := u.ContentFilterValues()
			frozen := map[string]bool{}
			if jx.Bool(filters, "active_sources_only") {
				frozen = u.FrozenSourceLabels()
			}
			if !autopilotFormatAllowsItem(u, guard.topic, guard.post) || u.contentAdmissionReason(guard.analysis, guard.post, guard.topic, AnalysisText(guard.post)) != "" || !u.PoolItemInTopic(item, guard.topic, guard.topic, u.AdultAllowed()) || !poolMatchesDisplayFilters(item, filters, frozen) {
				return nil, nil
			}
		}
		memory := recordKnownKeys(recentRepeatRecords(u.historyLoad(u.Ctx, tx, u.ID).All(), now()))
		hashes := map[string]string{}
		if err := loadRepeatMemory(u.Ctx, tx, u.ID, memory, hashes); err != nil {
			return nil, err
		}
		if repeatMatches(item, memory, hashes) {
			autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_repeat_memory", 1)
			return nil, nil
		}
		if candidateIsKnown(item, known) {
			autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_known_content", 1)
			return nil, nil
		}
		topic := TopicNorm(topicKey)
		fresh := u.freshPoolItems(pool)
		if visible != nil {
			fresh = u.visibleFreshPool(pool, topicKey, visible)
		}
		if candidateIsKnown(item, recordKnownKeys(pool)) {
			autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_duplicate", 1)
			return nil, nil
		}
		sourceCount, freshCount := sourcePoolCount(fresh, topic, ContentSourceKey(item)), topicPoolCount(fresh, topic)
		if visible != nil {
			sourceCount, freshCount = 0, len(fresh)
			for _, record := range fresh {
				if ContentSourceKey(record) == ContentSourceKey(item) {
					sourceCount++
				}
			}
		}
		if sourceCount >= sourcePoolLimit(u.TInt("per_source_pool"), target, freshCount) {
			autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_source_limit", 1)
			return nil, nil
		}
		if _, _, _, valid := autopilotSourceParts(ContentSourceKey(item)); valid {
			rows, err := tx.Query(u.Ctx, `SELECT source_id FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND state='probation'`, autopilotTopicKey(topicKey))
			if err != nil {
				return nil, err
			}
			probation := map[string]bool{}
			for rows.Next() {
				var source string
				if err := rows.Scan(&source); err != nil {
					rows.Close()
					return nil, err
				}
				probation[source] = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			if !autopilotProbationPoolAllowed(fresh, topicKey, ContentSourceKey(item), probation, target, u.TInt("autopilot_probation_pool_percent")) {
				autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_probation_limit", 1)
				return nil, nil
			}
		}
		if visible != nil {
			remove, allowed := poolQuotaInsertion(pool, target, func(record jx.M) bool {
				return u.poolProfileMatches(jx.Str(record, "topic"), topicKey)
			}, visible, u.poolItemFresh)
			if !allowed {
				u.Log("-> Пул достиг лимита видимых записей или хранилища.")
				return nil, nil
			}
			if remove >= 0 {
				pool = append(pool[:remove], pool[remove+1:]...)
			}
		} else {
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
					autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_full", 1)
					return nil, nil
				}
				sort.SliceStable(expired, func(a, b int) bool {
					return jx.Int(pool[expired[a]], "pooled_at") < jx.Int(pool[expired[b]], "pooled_at")
				})
				pool = append(pool[:expired[0]], pool[expired[0]+1:]...)
			}
		}
		if guard, ok := u.Ctx.Value(autopilotDeliveryContextKey{}).(autopilotDeliveryGuard); ok {
			if err := recordAutopilotAdmission(u.Ctx, tx, u.ID, guard.topic, guard.post, true, now()); err != nil {
				return nil, err
			}
		}
		added = true
		return PoolOrder(append(pool, item)), nil
	})
	if err != nil {
		u.Log("Не удалось сохранить запись в пул: %v", err)
		autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_transaction_error", 1)
		return false
	}
	u.MediaReleaseRemoved(u.Ctx, before, after)
	if added {
		if _, autopilot := u.Ctx.Value(autopilotDeliveryContextKey{}).(autopilotDeliveryGuard); !autopilot {
			u.PoolItemsEnqueueVideos(u.Ctx, []jx.M{item}, u.ID)
			go u.TranslateWarmCache(item)
		}
	}
	return added
}

func (a *App) poolItemFresh(item jx.M) bool {
	if !jx.Has(item, "pooled_at") {
		return true
	}
	return now()-jx.Int(item, "pooled_at") <= int64(a.TInt("pool_max_age"))*60
}

// candidatePlatformOrder also accepts search platforms added in the future.
func candidatePlatformOrder(by map[string][]jx.M) []string {
	order := []string{PlatformTelegram, PlatformVK, PlatformRSS, PlatformOK, PlatformTikTok, PlatformDvach, Platform4chan}
	seen := map[string]bool{}
	for _, p := range order {
		seen[p] = true
	}
	var extra []string
	for p := range by {
		if !seen[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

func (a *App) freshPoolItems(pool []jx.M) []jx.M {
	var fresh []jx.M
	for _, item := range pool {
		if a.poolItemFresh(item) {
			fresh = append(fresh, item)
		}
	}
	return fresh
}

type queueSkip map[string]int

// buildCandidateQueue — без виденных, устаревших, отсеянных раньше; лучшие источники первыми, горячие — в шортлист. До 500.
func (u *U) buildCandidateQueue(byPlatform map[string][]jx.M, seen *knownKeys, rejected map[string]bool, rates map[string][2]int, format *TopicFormat, filters jx.M, topic string) ([]jx.M, queueSkip) {
	skipped := queueSkip{}
	sourcePolicy, policyErr := u.collectorSourcePolicy(topic)
	if policyErr != nil {
		u.Log("Не удалось применить фильтр источников: %v", policyErr)
	}
	busy := u.sourceBusy(topic)
	unique := map[string]jx.M{}
	var order, deferred []string
	uaHits, uaNames, uaAll := map[string]int{}, map[string]string{}, map[string]bool{}
	maxAge := u.TInt("content_max_age")
	adult := u.AdultAllowed()
	frozen := map[string]bool{}
	if jx.Bool(filters, "active_sources_only") {
		frozen = u.FrozenSourceLabels()
	}
	platOrder := candidatePlatformOrder(byPlatform)
	for _, p := range platOrder {
		for _, item := range ExpandPostImages(byPlatform[p]) {
			if policyErr != nil {
				skipped["source_policy_unavailable"]++
				continue
			}
			if reason := sourcePolicy(item); reason != "" {
				skipped[reason]++
				continue
			}
			if reason := u.collectorDisplayReason(item, filters, frozen, adult); reason != "" {
				notes := map[string]string{"filter": "Не прошёл фильтры выдачи", "advertising": "Явная рекламная маркировка", "adult": "18+ и чувствительный контент выключены"}
				u.rememberRejected(topic, "filter", item, notes[reason], nil)
				skipped[reason]++
				continue
			}
			if ua := u.UAItemBlocked(item); ua != "" {
				u.rememberRejected(topic, "filter", item, "Фильтр украинского контента: "+ua, nil)
				skipped["ukraine"]++
				src := ContentSourceKey(item)
				uaAll[src] = true
				if !strings.HasPrefix(ua, "источник:") {
					uaHits[src]++
					uaNames[src] = jx.Str(item, "source")
				}
				continue
			}
			// Distinct videos can share a cover. Short text records can have
			// no lexical fingerprint but still have a valid record identity.
			ukey := ContentItemKey(item)
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
				switch reason {
				case "rejected_before":
					u.rememberRejected(topic, "prescreen", item, "Отсеян быстрой оценкой ранее", nil)
				case "outdated":
					u.rememberRejected(topic, "filter", item, "Устаревший пост", nil)
				case "format":
					u.rememberRejected(topic, "filter", item, "Не подходит по формату", nil)
				}
				skipped[reason]++
				continue
			}
			unique[ukey] = item
			if busy[src] {
				deferred = append(deferred, ukey)
			} else {
				order = append(order, ukey)
			}
		}
	}
	// The source cap and cooldown are for variety, not a reason to leave a
	// topic empty.  If the normal queue is thin, bring back enough of the
	// otherwise valid posts from resting/full sources to give the analyzer a
	// realistic chance of filling the feed.  They remain interleaved by source.
	queueGoal := min(500, max(config.PrescreenBatch, 2*max(1, min(u.TInt("added_per_run"), u.TInt("pool_target")-u.PoolTopicCount(topic)))))
	returned := 0
	if len(order) < queueGoal && len(deferred) > 0 {
		fallback := collectorDeferredKeys(unique, deferred, queueGoal-len(order))
		order = append(order, fallback...)
		returned = len(fallback)
		skipped["source_busy_returned"] = returned
	}
	skipped["source_busy"] = len(deferred) - returned
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
		if !ImageboardItem(it) && query != "" && QueryTextHit(jx.Str(it, "text"), query) {
			return 1
		}
		return 0
	}
	chance := func(it jx.M) (bool, float64) {
		r := rates[ContentSourceKey(it)]
		rate := float64(r[0]+1) / float64(r[1]+2)
		return r[1] >= config.SourceVerdictMin && rate < config.SourceVerdictLow, rate
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if x, y := named(a), named(b); x != y {
			return x > y
		}
		ca, ra := chance(a)
		cb, rb := chance(b)
		if ca != cb {
			return !ca
		}
		// The optional text match is a bounded priority adjustment. Neither
		// a weak caption nor absence of a score rejects the candidate.
		ra *= queryRerankMultiplier(a, query)
		rb *= queryRerankMultiplier(b, query)
		if ra != rb {
			return ra > rb
		}
		return jx.Int(a, "posted_at") > jx.Int(b, "posted_at")
	})
	short := u.hotShortlist(all, rates, topic)
	return short[:min(len(short), 500)], skipped
}

// Restore exactly the identities used when building the unique map. Image
// URLs and long text fingerprints are not the keys of videos/short posts.
func collectorDeferredKeys(unique map[string]jx.M, deferred []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	fallback := make([]jx.M, 0, len(deferred))
	for _, key := range deferred {
		if it := unique[key]; it != nil {
			fallback = append(fallback, it)
		}
	}
	var keys []string
	for _, it := range InterleaveBySource(fallback) {
		key := ContentItemKey(it)
		if key == "" || unique[key] == nil {
			continue
		}
		keys = append(keys, key)
		if len(keys) >= limit {
			break
		}
	}
	return keys
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
	shortN := u.TInt("hot_shortlist")
	var first, rest, softTail []jx.M
	var boost []float64
	for _, it := range all {
		text := jx.Str(it, "text")
		if ImageboardItem(it) {
			text = ""
		}
		// Profile exclusions are model-generated prose, not user bans.
		// A single word can appear in a comparison, negation or quotation;
		// postpone to full analysis instead of discarding the publication.
		if collectorExcludeHint(text, exclude) {
			softTail = append(softTail, it)
			continue
		}
		named := query != "" && QueryTextHit(text, query)
		if named && len(first) < shortN/2 {
			first = append(first, it)
			continue
		}
		b := 1.0
		if named {
			b *= config.HotQueryBoost
		}
		boost = append(boost, b)
		rest = append(rest, it)
	}
	heat := hotRelativeHeat(rest)
	score := map[int]float64{}
	tail := 0
	for i, it := range rest {
		text := jx.Str(it, "text")
		if ImageboardItem(it) {
			text = ""
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
		// Ограничиваем влияние вирусности: популярность не заменяет попадание в тему.
		score[i] = math.Min(3, math.Max(0.25, heat[i])) * mult * kw * boost[i] * queryRerankMultiplier(it, query)
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
		hs := jx.M{"total": len(all), "short": len(picked) + len(first), "per": per, "excluded": 0, "keyword_demoted": len(softTail), "trusted": trusted, "imageboard_tail": tail, "top": nil,
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
	return append(append(append(first, InterleaveBySource(hot)...), InterleaveBySource(tailItems)...), InterleaveBySource(softTail)...)
}

func collectorExcludeHint(text string, exclude []string) bool {
	return len(exclude) > 0 && hotHasWord(text, exclude)
}

// A caption is incomplete evidence when the post also contains media.
func captionVerdictCanReject(item jx.M, judge string) bool {
	return judge == "text" && !PostItemHasMedia(item)
}

// hotTextJudge — пакетная проверка подписей: слабые подписи медиа откладываются до полного анализа.
func (u *U) hotTextJudge(items []jx.M, topic string) []jx.M {
	judge := u.TopicFormatOf(topic).Judge
	if judge != "text" && judge != "both" {
		return items
	}
	// Keep actual text posts and media captions in different batches. Only
	// the former produce a complete verdict that can be reused at admission.
	type textGroup struct {
		indices         []int
		full, ownerOnly bool
	}
	groups := []textGroup{{full: true}, {full: true, ownerOnly: true}, {}, {ownerOnly: true}}
	for i, item := range items {
		if u.analysisPrivate(topic, item) {
			continue // private records are analyzed individually when reached
		}
		hasMedia := PostItemHasMedia(item)
		text := AnalysisText(item)
		if hasMedia {
			text = PostCaptionForAI(item)
		}
		if runeLen(text) < config.HotTextMinChars {
			continue
		}
		group := 0
		if hasMedia {
			group += 2
		}
		if !u.collectorPublicEvidence(item, topic) {
			group++
		}
		groups[group].indices = append(groups[group].indices, i)
	}
	drop, late := map[int]bool{}, map[int]bool{}
	h := u.History()
	for _, group := range groups {
		// Token budget in AnalyzeTextBatch covers at most six records.
		batchSize := min(config.HotTextBatch, 6)
		for off := 0; off < len(group.indices) && u.Alive(); off += batchSize {
			var texts []string
			ids := group.indices[off:min(len(group.indices), off+batchSize)]
			for _, i := range ids {
				if group.full {
					texts = append(texts, AnalysisText(items[i]))
				} else {
					texts = append(texts, PostCaptionForAI(items[i]))
				}
			}
			res := u.cachedCollectorTextBatch(texts, topic, h, false, group.full, group.ownerOnly)
			for k, r := range res {
				if r == nil || jx.Int(r, "relevance") >= config.HotTextRejectScore {
					continue
				}
				i := ids[k]
				if captionVerdictCanReject(items[i], judge) {
					drop[i] = true
				} else {
					late[i] = true
				}
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
	if u.run != nil {
		u.run.TextPrefilterDropped += len(drop)
		u.run.TextPrefilterLate += len(late)
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

// A cover or first album image is not enough to reject the entire post.
func collectorPrescreenComplete(item jx.M) bool {
	return !PostItemHasVideo(item) && jx.Str(item, "video_via") == "" && len(ContentGallery(item)) < 2
}

func (u *U) prescreenIsJunk(score *int, item jx.M) bool {
	return collectorPrescreenComplete(item) && score != nil && *score >= u.prescreenThreshold(item)
}

func (a *App) prescreenThreshold(item jx.M) int {
	t := a.TInt("prescreen_junk")
	if !ImageboardItem(item) && jx.Bool(item, "thread_trust") {
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
	if ImageboardItem(item) {
		suffix = "|imageboard-media-only-v1"
	}
	if contains(config.SubtopicCacheTopics, t) {
		suffix += "|subtopic2"
	}
	// The same poster may belong to distinct videos, and short text posts
	// have no CandidateTextKey. Bind the verdict to the actual model input.
	var media []any
	for _, f := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		media = append(media, []any{ContentImageKey(f), jx.Strs(f, "images"), jx.Str(f, "video"), jx.Str(f, "video_page"), jx.Str(f, "video_via")})
	}
	caption := AnalysisText(item)
	if ImageboardItem(item) {
		caption = ""
	}
	h := sha1.Sum(jx.Marshal([]any{config.AnalysisPromptVersion, "v5content", t, suffix,
		CandidatePostKey(item), media, caption, PostCaptionForAI(item), AnalysisContextBlock(item)}))
	return hex.EncodeToString(h[:])
}

// A user's complaint examples affect the vision prompt and must never be
// reused for another user or after those examples change.
func (u *U) analysisCacheKey(item jx.M, topic string, h History) string {
	base := AnalysisCacheKey(item, topic)
	if h == nil {
		h = EmptyHistory()
	}
	complaints := TopicComplaintTexts(u.TopicComplaints(topic, h), TopicComplaintExamples)
	if len(complaints) == 0 && !u.analysisPrivate(topic, item) {
		return base
	}
	sum := sha1.Sum([]byte(base + "|" + u.ID + "|" + string(jx.Marshal(complaints))))
	return hex.EncodeToString(sum[:])
}

// A cached poster-only assessment must never replace a storyboard verdict.
// Hash the actual frames plus the exact rendered prompt and configured route.
func visionEvidenceKey(base, prompt, settings string, frames [][]byte) string {
	hashes := make([]string, len(frames))
	for i, frame := range frames {
		sum := sha256.Sum256(frame)
		hashes[i] = hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(jx.Marshal([]any{"vision-evidence-v1", base, prompt, settings, hashes}))
	return hex.EncodeToString(sum[:])
}

func (u *U) collectorVisionKey(item jx.M, topic string, h History, frames [][]byte, video *Storyboard, caption, context string) string {
	s := u.LLM.Settings()
	settings := string(jx.Marshal([]any{s.Roles, s.FastOnly, s.ThinkInParser, u.contentAnalysisSchemaForTopic(TopicQueryOf(topic), topic)}))
	base := u.analysisCacheKey(item, topic, h)
	if !u.collectorPublicEvidence(item, topic) {
		base += "|owner:" + u.ID
	}
	return visionEvidenceKey(base, u.editableVisionPrompt(topic, strings.TrimSpace(caption), h, video, context, visualEvidenceDescription(item, len(frames), video)), settings, frames)
}

// ---- Оценка ----

func (u *U) fetchCandidatePoster(item jx.M) []byte {
	img := jx.Str(item, "image")
	if jx.Bool(item, "_video_without_poster") {
		b := u.videoPosterFromPublicURL(jx.Str(item, "video"))
		if len(b) == 0 {
			u.Log("-> Не удалось получить кадр видео, пропуск.")
			return nil
		}
		local := u.MediaStoreImage("video-poster:"+jx.Str(item, "video"), b)
		if local == "" {
			return nil
		}
		item["image"], item["images"] = local, []string{local}
		delete(item, "_video_without_poster")
		for _, f := range jx.Maps(item, "gallery") {
			if jx.Str(f, "video") == jx.Str(item, "video") && jx.Str(f, "image") == "" {
				f["image"] = local
				break
			}
		}
		return b
	}
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
	Item     jx.M
	Poster   []byte
	Pos      int
	Score    *int
	Source   string
	Phash    string
	CacheKey string
	Private  bool
}

type prescreenCtx struct {
	stats       map[string]int
	cached      map[string]int
	newCache    map[string]screenedEntry
	knownHashes map[string]string
	passedTotal int
}

// Bind a prescreen result to the actual visual evidence and editable prompts.
// A perceptual hash is useful for duplicate detection, not for classifying a
// different caption or a modified image as junk.
func prescreenEvidenceKey(topic, scope, caption, prompts string, poster []byte) string {
	sum := sha256.Sum256(poster)
	key := sha256.Sum256(jx.Marshal([]any{"prescreen-input-v2", topic, scope, caption, prompts, hex.EncodeToString(sum[:])}))
	return hex.EncodeToString(key[:])
}

func (u *U) prescreenInputKey(item jx.M, topic string, poster []byte) string {
	caption := trimRunes(PostCaptionForAI(item), 300)
	values := map[string]string{"caption": caption, "thread": ""}
	private := u.analysisPrivate(topic, item) || jx.Bool(item, "source_sensitive") || jx.Bool(item, "nsfw")
	scope := ""
	if private {
		scope = "private:" + u.ID
	} else if !u.collectorPublicEvidence(item, topic) {
		scope = "owner:" + u.ID
	}
	settings := u.LLM.Settings()
	route := settings.Roles[llm.RolePrescreen]
	prompts := string(jx.Marshal([]any{route, settings.FastOnly, u.prompt("prescreen_yesno", values), u.prompt("prescreen_score", values)}))
	return prescreenEvidenceKey(topic, scope, caption, prompts, poster)
}

func (u *U) screen(topic, stage string, item jx.M, score int, passed bool, reason, model string) {
	if !passed {
		u.rememberRejected(topic, stage, item, reason, &score)
	}
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
		if repeatFileIsVideo(item) {
			hash = ""
		}
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
		row := prescreenRow{Item: item, Poster: poster, Pos: pos, Source: src, Phash: hash, Private: u.analysisPrivate(topic, item)}
		if !collectorPrescreenComplete(item) {
			// No extra AI call for evidence that cannot justify a rejection.
			c.stats["prescreen_bypassed_media"]++
			c.passedTotal++
			passed = append(passed, row)
			continue
		}
		row.CacheKey = u.prescreenInputKey(item, topic, poster)
		if s, ok := c.cached[row.CacheKey]; ok {
			sc := s
			c.stats["prescreen_cache_hits"]++
			if u.prescreenIsJunk(&sc, item) {
				u.rememberRejected(topic, "prescreen", item, "Оценка из кеша", &sc)
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
			c.newCache[row.CacheKey] = screenedEntry{Topic: topic, Source: row.Source, Score: *q.Score}
			c.cached[row.CacheKey] = *q.Score
			c.stats["prescreened"]++
			reason := ""
			if junk {
				reason = "быстрая оценка: мусор"
			}
			u.screen(topic, "prescreen", item, *q.Score, !junk, reason, model)
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

// admissionReason contains the same content decisions for collection and diagnostics.
func (u *U) admissionReason(a jx.M, item jx.M, topic, text string) string {
	if reason := u.contentAdmissionReason(a, item, topic, text); reason != "" {
		return reason
	}
	if u.SearchTopicKey() != topic {
		return "тема поиска сменилась"
	}
	return ""
}

// contentAdmissionReason is shared by collection and read-only replay diagnostics.
func (u *U) contentAdmissionReason(a jx.M, item jx.M, topic, text string) string {
	if ad, ok := a["advertising"].(bool); !ok || ad {
		return "реклама или не проверена на рекламу"
	}
	if PostIsAdvertisement(item) || IsSpamContent(CleanPostAttribution(text, item), jx.Str(a, "description")) {
		return "спам"
	}
	o := DefaultQualityOpts()
	o.Threshold = u.LLM.ThresholdForModel(jx.Str(a, "model"), u.TInt("relevance_threshold"))
	if reason := u.QualityReasons(a, item, topic, o); reason != "" {
		return reason
	}
	if u.professionalValueTopic(topic) && professionalAnalysisLowValue(a) {
		return "нет конкретной пользы, фактов или интересного вывода"
	}
	if u.ContentAdultBlocked(jx.Merge(item, jx.M{"tags": a["tags"]}), topic) {
		return "18+ и чувствительный контент выключены"
	}
	if a["ukraine"] == true && u.UAFilterOn() {
		return "украинский контент"
	}
	return ""
}

func (u *U) admit(a jx.M, item jx.M, topic, verdict, stage, text, evidence string, h History) bool {
	reason := u.admissionReason(a, item, topic, text)
	if u.run != nil {
		incrementCollectorCount(&u.run.AdmissionOutcomes, collectorAdmissionCode(reason))
	}
	if u.DB != nil && reason != "тема поиска сменилась" {
		if err := recordAutopilotAdmission(u.Ctx, u.DB.Pool, u.ID, topic, item, reason == "", now()); err != nil {
			u.Log("Не удалось сохранить наблюдение автопилота: %v", err)
		}
	}
	if passed, known := collectorSourceVerdict(a, reason, evidence); known {
		// A user's filter or complaint is not a global source-quality signal.
		if u.DB != nil && !u.analysisPrivate(topic, item) && len(u.TopicComplaints(topic, h)) == 0 {
			u.SourceVerdictRecord(topic, ContentSourceKey(item), passed)
		}
		u.sourceMissNote(ContentSourceKey(item), passed)
	}
	if reason != "" {
		u.Log("-> Отброшено: %s (%s).", reason, verdict)
		u.screen(topic, stage, item, int(jx.Int(a, "relevance")), false, reason, jx.Str(a, "model"))
		return false
	}
	u.screen(topic, stage, item, int(jx.Int(a, "relevance")), true, "", jx.Str(a, "model"))
	return true
}

// analyzeCandidateVision is the common uncached image evaluation used by the collector and the AI test.
func (u *U) analyzeCandidateVision(frames [][]byte, item jx.M, h History, topic string, video *Storyboard) (Analysis, string, error) {
	context := AnalysisContextBlock(item)
	caption := ""
	if context == "" {
		caption = PostCaptionForAI(item)
	}
	a, err := u.AnalyzeContentWithVision(frames, caption, h, topic, video, u.LLM.Settings().ThinkInParser, context, item)
	return a, caption, err
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
	delete(rec, "_query_rank_for")
	delete(rec, "_query_rank_score")
	for k, v := range (jx.M{"description": desc, "why_relevant": why, "relevance": a["relevance"], "rating": nil, "format": format, "tags": orList(jx.List(a, "tags")),
		"tags_classification_pending": jx.Bool(a, "tags_classification_pending"), "tags_classification_reason": jx.Str(a, "tags_classification_reason"),
		"quote": jx.Bool(a, "quote"), "entity": jx.Str(a, "entity"), "evidence": jx.Str(a, "evidence"), "source_hashtags": PostSourceHashtags(item), "topic": topic, "analysis_model": jx.Str(a, "model"),
		"interest": a["interest"], "practical_value": a["practical_value"], "specificity": a["specificity"]}) {
		rec[k] = v
	}
	// Метка — по содержанию поста (place от модели); нет места — поля geo в записи нет
	if g := GeoClean(a["geo"]); g != nil {
		rec["geo"] = g
	} else {
		delete(rec, "geo")
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
	h := u.History()
	ownerOnly := !u.collectorPublicEvidence(item, topic)
	res := u.cachedCollectorTextBatch([]string{text}, topic, h, u.analysisPrivate(topic, item), true, ownerOnly)
	if len(res) == 0 || res[0] == nil {
		if u.run != nil {
			u.run.UnknownAnalyses++
		}
		u.Log("-> ИИ не вернул подтверждённую оценку текста, откладываю.")
		return false
	}
	a := res[0]
	// A valid relevance verdict is reusable even when the optional tag
	// classifier failed. Retry only tags, and persist successful completion.
	if jx.Bool(a, "tags_classification_pending") && !PostItemHasVideo(item) {
		private := u.analysisPrivate(topic, item)
		u.applyContentClassification(a, "", text, private)
		if !jx.Bool(a, "tags_classification_pending") && (private || ownerOnly || len(u.contentSafetyTags(a["tags"])) == 0) {
			u.writeAnalysisCache(map[string]jx.M{u.collectorTextKey(text, topic, h, private, true, ownerOnly): a})
		}
	}
	if PostItemHasVideo(item) {
		u.applyVideoClassification(a, item, "", PostCaptionForAI(item), u.analysisPrivate(topic, item))
	}
	verdict := fmt.Sprintf("соответствие теме %d%%", jx.Int(a, "relevance"))
	u.classifyPublicPost(item, a, topic, h)
	evidence := "text"
	if PostItemHasMedia(item) {
		evidence = "caption_only"
	}
	if !u.admit(a, item, topic, verdict, "text", text, evidence, h) {
		return false
	}
	u.Log("-> Принято: %s.", verdict)
	u.PostSearchSuggest(item, topic)
	rec := analysisRecord(a, item, topic, "text")
	u.GeoFill(rec)
	rec["image_local"], rec["phash"], rec["image"], rec["images"], rec["captions"] = "", "", "", []any{}, []any{text}
	return u.AddItemToPoolSafely(rec)
}

// analyzeCandidate — полный анализ (видео — раскадровкой), порог, кэш медиа и запись в пул.
func (u *U) analyzeCandidate(item jx.M, poster []byte, h History, topic string) bool {
	if jx.Str(item, "image") == "" {
		return u.analyzeTextCandidate(item, topic)
	}
	isVideo := PostItemHasVideo(item)
	kind := "картинка"
	if isVideo {
		kind = "видео"
	}
	u.Log("Полный анализ %s %s %s", candidateLogLabel(item), kind, jx.Str(item, "image"))
	var video *Storyboard
	if direct := jx.Str(item, "video"); direct != "" {
		video = u.VideoAnalysisInput(direct, poster)
		if video.File != "" {
			defer os.Remove(video.File)
		}
	} else if jx.Str(item, "video_via") == "ytdlp" && jx.Int(item, "duration") <= ytdlpWatchMaxSeconds {
		var cleanup func()
		video, cleanup = u.VideoPageAnalysisInput(jx.Str(item, "video_page"), poster)
		defer cleanup()
	}
	frames := [][]byte{poster}
	var galleryBytes map[string][]byte
	if video != nil {
		frames = video.Frames
	} else if !isVideo {
		frames, galleryBytes = galleryVisionFrames(item, poster, u.discoveryImageBytes)
	}
	storyboard := video != nil && len(frames) > 1
	context := AnalysisContextBlock(item)
	caption := ""
	if context == "" {
		caption = PostCaptionForAI(item)
	}
	var sb *Storyboard
	if storyboard {
		sb = video
	}
	cacheKey := u.collectorVisionKey(item, topic, h, frames, sb, caption, context)
	private := u.analysisPrivate(topic, item)
	a := u.readAnalysisCache([]string{cacheKey})[cacheKey]
	if a != nil {
		if u.run != nil {
			u.run.VisionCacheHits++
		}
		u.Log("-> Анализ взят из кэша: совпадают содержание, кадры и настройки модели.")
		if !isVideo && jx.Bool(a, "tags_classification_pending") {
			tagCaption := PostCaptionForAI(item)
			if tagCaption == "" {
				tagCaption = caption
			}
			u.applyContentClassification(a, jx.Str(a, "description"), tagCaption, private)
			if !jx.Bool(a, "tags_classification_pending") && (private || len(u.contentSafetyTags(a["tags"])) == 0) {
				u.writeAnalysisCache(map[string]jx.M{cacheKey: a})
			}
		}
	} else {
		var err error
		if u.run != nil {
			u.run.VisionCalls++
		}
		a, caption, err = u.analyzeCandidateVision(frames, item, h, topic, sb)
		if err != nil {
			if u.run != nil {
				u.run.UnknownAnalyses++
			}
			u.Log("-> ИИ не вернул корректную оценку: %s", err)
			return false
		}
		if private || (!u.ContentIsAdult(a) && len(u.contentSafetyTags(a["tags"])) == 0) {
			u.writeAnalysisCache(map[string]jx.M{cacheKey: a})
		}
	}
	if isVideo {
		localVideo := ""
		if video != nil {
			localVideo = video.File
		}
		u.applyVideoClassification(a, item, localVideo, PostCaptionForAI(item), u.analysisPrivate(topic, item))
	}
	if u.run != nil {
		incrementCollectorCount(&u.run.VisualEvidenceKinds, visualEvidenceKind(item, len(frames), sb))
	}
	verdict := fmt.Sprintf("соответствие теме %d%%", jx.Int(a, "relevance"))
	u.classifyPublicPost(item, a, topic, h)
	if !u.admit(a, item, topic, verdict, "analysis", caption, visualEvidenceKind(item, len(frames), sb), h) {
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
	rec["analysis_evidence"] = visualEvidenceKind(item, len(frames), sb)
	u.GeoFill(rec)
	rec["image_local"] = u.MediaStoreImage(jx.Str(item, "image"), poster)
	rec["phash"] = ImagePhash(poster)
	if isVideo {
		direct, page := jx.Str(item, "video"), jx.Str(item, "video_page")
		if video != nil && video.File != "" {
			mediaKey := direct
			if mediaKey == "" {
				mediaKey = page
			}
			rec["video_local"] = u.CacheDownloadedVideo(mediaKey, video.File)
			if direct == "" && jx.Str(rec, "video_local") != "" {
				// The video was already downloaded through yt-dlp for
				// analysis. Do not enqueue the same download again.
				rec["video"] = rec["video_local"]
			}
		} else if direct != "" {
			rec["video_local"] = u.MediaStoreVideo(direct, "", false, nil)
		}
		if jx.Str(rec, "video_local") != "" {
			u.Log("-> Видео сохранено в кэш сайта.")
		} else {
			u.Log("-> Видео не удалось сохранить в кэш, на сайте будет исходная ссылка.")
		}
	}
	if g := jx.Maps(rec, "gallery"); len(g) > 0 {
		for i, f := range g {
			image := jx.Str(f, "image")
			if b := galleryBytes[image]; len(b) > 0 {
				g[i]["image_local"] = u.MediaStoreImage(image, galleryPrepareImage(b))
				g[i]["phash"] = ImagePhash(b)
			}
		}
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
	}
	key, target := u.SearchTopicKey(), u.TInt("pool_target")
	available, allowed, err := u.poolRefillState(key, target)
	if err != nil {
		return "Не удалось проверить фильтры подборки."
	}
	if available >= target {
		return fmt.Sprintf("Пул заполнен до %d записей.", target)
	}
	if !allowed {
		return "Резерв скрытых записей заполнен. Подбор возобновится после освобождения места."
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
// When fresh topic supply is low, search posts directly instead of
// blocking the collector on source vetting. The existing candidate, full
// analysis, safety, source-override and deduplication checks remain in place.
func shouldFastTopicSearch(hasQuery, discoveryEnabled, restricted bool, sources, requiredSources, available, target int) bool {
	return !hasQuery && discoveryEnabled && !restricted && target > 0 && available < target &&
		((requiredSources > 0 && sources < requiredSources) || available < max(1, (target+3)/4))
}

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
	usableSources := u.TopicUsableSources(d, topic)
	// The public source autopilot now owns source discovery. The content
	// parser can still explore individual posts without running the legacy
	// scanner or waiting for manual source approvals.
	fastTopicSearch := shouldFastTopicSearch(u.CurrentQuery() != "", u.TBool("discovery"), u.TopicIsRestricted(topic),
		usableSources, u.TInt("min_sources"), u.PoolTopicCount(key), u.TInt("pool_target"))
	if usableSources < u.TInt("min_sources") {
		u.Log("Источников по теме %d из %d — проверкой новых источников занимается автопилот.", usableSources, u.TInt("min_sources"))
	}
	if reason := u.collectorStopReason(started, 0, 0); reason != "" {
		u.Log("%s", reason)
		return false
	}
	cache := u.screenedCacheLoad(key)
	scores := map[string]int{}
	// A prescreen verdict is only valid after the actual image bytes and
	// caption have been checked. Never reject a record by its URL alone.
	for _, e := range cache {
		scores[e.URL] = e.Score
	}
	format := u.TopicFormatOf(key)
	var byPlatform map[string][]jx.M
	if fastTopicSearch && u.PoolTopicCount(key) == 0 {
		u.Log("У новой темы пока нет источников — не жду общий опрос, ищу публикации напрямую.")
	} else {
		u.parserPhaseWithTopic("ожидание общего сбора", topic)
		u.ParserProgress(jx.M{"shared_wait": true}, false)
		u.waitSharedCollection(u.Ctx, topic)
		u.ParserProgress(jx.M{"shared_wait": false}, false)
	}
	if !u.Alive() {
		return false
	}
	fastPostKeys := map[string]bool{}
	fastPostFound := 0
	fastSearchSeconds := 0.0
	if u.CurrentQuery() != "" {
		byPlatform = u.QuerySearchCollect(key) // поиск по запросу сам включает каталог темы
	} else {
		byPlatform = u.collectPostsByPlatform(key)
		if byPlatform == nil {
			byPlatform = map[string][]jx.M{}
		}
		if fastTopicSearch && u.Alive() && u.CurrentTopic() == topic {
			fastStarted := time.Now()
			fastPosts := u.TopicFastSearchCollect(topic)
			fastSearchSeconds = time.Since(fastStarted).Seconds()
			for platform, posts := range fastPosts {
				fastPostFound += len(posts)
				for _, post := range posts {
					if postKey := CandidatePostKey(post); postKey != "" {
						fastPostKeys[postKey] = true
					}
				}
				byPlatform[platform] = append(byPlatform[platform], posts...)
			}
		}
	}
	u.ParserStage("filter")
	candidates, skipped := u.buildCandidateQueue(byPlatform, seen, nil, u.CombinedSourceVerdicts(u.Ctx, key), &format, u.ContentFilterValues(), key)
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
		if x := jx.Int(hs, "keyword_demoted"); x > 0 {
			line += fmt.Sprintf(". Отложено в хвост по словам «не подходит» (не отброшено): %d", x)
		}
		line += fmt.Sprintf(". Срез очереди на источник: %d, срез шортлиста: %d", jx.Int(hs, "per_source_cut"), jx.Int(hs, "shortlist_cut"))
		u.Log("%s, из доверенных тредов %d; имиджборд без признаков темы — в хвост: %d.", line, jx.Int(hs, "trusted"), jx.Int(hs, "imageboard_tail"))
	}
	// Evaluate captions only for the block about to be processed. A large
	// shortlist must not hold back the first usable result.
	collected := 0
	for _, posts := range byPlatform {
		collected += len(posts)
	}
	notes := fmt.Sprintf("📌 Тема: «%s». Собрано постов: %d. Кандидатов к проверке: %d.", topic, collected, len(candidates))
	for _, s := range []struct{ k, t string }{{"rejected_before", " Уже отсеяны в прошлых запусках: %d."}, {"outdated", " Устаревших: %d."},
		{"ukraine", " Украинский контент отсеян: %d."}, {"format", " Отсеяно форматом темы: %d."}, {"filter", " Отсеяно фильтрами выдачи: %d."}, {"source_excluded", " Исключённые источники: %d."}, {"personal_blog", " Фильтр личных блогов: %d."}, {"advertising", " Явная реклама: %d."}, {"adult", " Ограничение 18+: %d."},
		{"source_busy", " Источники в ленте или на отдыхе: %d."}, {"seen", " Уже видели: %d."}, {"duplicate", " Дубли: %d."}, {"no_key", " Без текста и картинки: %d."}} {
		if skipped[s.k] > 0 {
			notes += fmt.Sprintf(s.t, skipped[s.k])
		}
	}
	if n := skipped["source_busy_returned"]; n > 0 {
		notes += fmt.Sprintf(" Вернул из них в очередь из-за нехватки кандидатов: %d.", n)
	}
	u.Log("%s Быстрая оценка по картинке %d px: отсев мусора при уверенности от %d%%, тему не проверяет.", notes, config.PrescreenMaxSide, u.TInt("prescreen_junk"))
	pc := &prescreenCtx{stats: map[string]int{}, cached: scores, newCache: map[string]screenedEntry{}, knownHashes: u.collectKnownHashes()}
	analyzed, added, done := 0, 0, 0
	fastPostAdded := 0
	firstAddedSeconds := 0.0
	funnel := func() jx.M {
		f := passFunnel(collected, len(candidates), skipped, pc.stats, analyzed, added)
		f["fast_search_found"] = fastPostFound
		f["fast_search_added"] = fastPostAdded
		f["fast_search_seconds"] = fastSearchSeconds
		f["prescreen_cache_hits"] = pc.stats["prescreen_cache_hits"]
		f["prescreen_bypassed_media"] = pc.stats["prescreen_bypassed_media"]
		f["text_cache_hits"] = u.run.TextCacheHits
		f["text_batch_calls"] = u.run.TextBatchCalls
		f["vision_cache_hits"] = u.run.VisionCacheHits
		f["vision_calls"] = u.run.VisionCalls
		f["admission_outcomes"] = collectorCountSnapshot(u.run.AdmissionOutcomes)
		f["visual_evidence"] = collectorCountSnapshot(u.run.VisualEvidenceKinds)
		f["analysis_unknown"] = u.run.UnknownAnalyses
		f["text_evidence_failures"] = u.run.TextEvidenceFailures
		f["text_prefilter_rejected"] = u.run.TextPrefilterDropped
		f["text_prefilter_deferred"] = u.run.TextPrefilterLate
		if added > 0 {
			f["first_added_seconds"] = firstAddedSeconds
		}
		return f
	}
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
		originalCount := len(batch)
		batch = u.hotTextJudge(batch, key)
		done += originalCount - len(batch)
		if stop = u.collectorStopReason(started, analyzed, added); stop != "" {
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
				if added == 1 {
					firstAddedSeconds = time.Since(started).Seconds()
				}
				if fastPostKeys[CandidatePostKey(c.Item)] {
					fastPostAdded++
				}
				if src := ContentSourceKey(c.Item); u.sourceVisibleQuotaReached(key, src) {
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
	u.KVSet(u.Ctx, parserLastPassKey(u.ID), jx.M{"at": now(), "funnel": funnel()}, 0) // воронка — для конвейера студии вне работы
	u.Log("")
	u.Log("✅ Подбор завершён за %.1f мин: добавлено %d, проанализировано %d, отсеяно быстрой оценкой %d, повторов %d.", time.Since(started).Minutes(), added, analyzed,
		pc.stats["rejected"], pc.stats["duplicates"])
	total := time.Since(started)
	u.Log("Время прохода: сбор %.1f мин, анализ %.1f мин.", max(0, (total-tAnalyze).Minutes()), tAnalyze.Minutes())
	u.Log("Кэш: быстрый отсев %d, текст %d, визуальный анализ %d; запуски анализа: текстовые пачки %d, визуальные %d.", pc.stats["prescreen_cache_hits"], u.run.TextCacheHits, u.run.VisionCacheHits, u.run.TextBatchCalls, u.run.VisionCalls)
	u.Log("Отбор: %s; без достоверной оценки %d; до полного анализа отброшено текстов %d.", string(jx.Marshal(u.run.AdmissionOutcomes)), u.run.UnknownAnalyses, u.run.TextPrefilterDropped)
	u.Log("Визуальные данные: %s; неподтверждённых цитат текста %d.", string(jx.Marshal(u.run.VisualEvidenceKinds)), u.run.TextEvidenceFailures)
	if fastTopicSearch {
		u.Log("Быстрый поиск: найдено %d кандидатов, после полного отбора добавлено %d.", fastPostFound, fastPostAdded)
	}
	u.Log("")
	return restart
}
