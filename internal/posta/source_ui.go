package posta

import (
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Вкладка «Источники»: кандидаты по одному, превью, решения, источники темы.

var SourcePlatformFilters = map[string]string{"any": "", "telegram": PlatformTelegram, "vk": PlatformVK, "tiktok": PlatformTikTok, "youtube": PlatformRSS,
	"shorts": PlatformRSS, "ok": PlatformOK, "pinterest": PlatformRSS, "tumblr": PlatformRSS, "rss": PlatformRSS, "fourchan": Platform4chan, "dvach": PlatformDvach}

var sourceKindFilters = map[string]string{"youtube": "YouTube", "shorts": "YouTube Shorts", "pinterest": "Pinterest", "tumblr": "Tumblr", "rss": "RSS"}

var SourceRejectReasons = []string{"off_topic", "dead", "other"}

const (
	sourcePreviewAttempts   = 12
	sourcePreviewPosts      = 20
	sourcePreviewPostsTT    = 8
	sourcePreviewPostsSlow  = 6
	sourcePreviewTextMax    = 2000
	SourceAddMaxLink        = 500
)

func NewsSiteKind(feed string) string {
	u, _ := url.Parse(feed)
	host := ""
	if u != nil {
		host = strings.TrimPrefix(u.Hostname(), "www.")
	}
	return "Сайт · " + host
}

func sourceCandidate(sid, platform, name, reason string, extra jx.M) jx.M {
	return jx.Merge(jx.M{"source_id": sid, "platform": platform, "name": name, "reason": reason}, extra)
}

// dvachBlockedBoards — доски, посты которых пользователи стабильно оценивают плохо или с ≥3 отклонёнными тредами без одобренных.
func (u *U) dvachBlockedBoards(votes []jx.M, topic string) map[string]bool {
	blocked := map[string]bool{}
	for key, s := range u.SourceFeedbackStats(u.Ctx, topic) {
		if strings.HasPrefix(key, "dvach:") && s.Votes >= config.SourceFeedbackBanVotes && s.Score < config.SourceFeedbackBanScore {
			blocked[strings.TrimPrefix(key, "dvach:")] = true
		}
	}
	count := map[string]map[string]int{}
	for _, v := range votes {
		if b := jx.Str(v, "board"); b != "" {
			if count[b] == nil {
				count[b] = map[string]int{}
			}
			count[b][jx.Str(v, "vote")]++
		}
	}
	for b, c := range count {
		if c["reject"] >= 3 && c["approve"] == 0 {
			blocked[b] = true
		}
	}
	return blocked
}

func (u *U) dvachSourceCandidates(voted map[string]bool, blocked map[string]bool, topic string) []jx.M {
	var boards []string
	for _, b := range u.DvachBoardsForTopic(topic, false) {
		if !blocked[b] {
			boards = append(boards, b)
		}
	}
	rand.Shuffle(len(boards), func(i, j int) { boards[i], boards[j] = boards[j], boards[i] })
	var out []jx.M
	for _, board := range boards[:min(4, len(boards))] {
		for _, t := range jx.Maps(u.Fetch2chJSON(board, ""), "threads") {
			thread := jx.Str(t, "num")
			sid := "dvach:" + board + ":" + thread
			if !IsValidDvachID(thread) || voted[sid] || DvachThreadDeadReason(t, now()) != "" {
				continue
			}
			summary := fmt.Sprintf("%d постов", jx.Int(t, "posts_count"))
			if jx.Has(t, "lasthit") {
				hours := (now() - jx.Int(t, "lasthit")) / 3600
				switch {
				case hours < 1:
					summary += ", последний меньше часа назад"
				case hours < 48:
					summary += fmt.Sprintf(", последний %d ч назад", hours)
				default:
					summary += fmt.Sprintf(", последний %d дн назад", hours/24)
				}
			}
			reason := CleanHTMLText(jx.Str(t, "subject"))
			if reason == "" {
				reason = "Тред с постами и изображениями."
			}
			out = append(out, sourceCandidate(sid, PlatformDvach, PlatformDvach+" /"+board+"/ · "+DvachThreadTitle(jx.Str(t, "subject"), thread), reason+" · "+summary,
				jx.M{"board": board, "thread_id": thread, "thread_posts": jx.Int(t, "posts_count"), "thread_files": jx.Int(t, "files_count"), "last_hit": jx.Int(t, "lasthit")}))
			if len(out) >= 30 {
				return out
			}
		}
	}
	return out
}

func (u *U) catalogSourceCandidates(d *SourceDB, platform string, voted map[string]bool, topic string) []jx.M {
	var out []jx.M
	reasons := map[string]string{PlatformTelegram: "Публичный Telegram-канал с недавними постами.", PlatformRSS: "Лента сайта или блога с картинками в записях.",
		PlatformOK: "Открытая группа в Одноклассниках с картинками в постах.", PlatformTikTok: "Аккаунт TikTok с короткими видео."}
	for _, e := range *d.List(CatalogKind(platform)) {
		id := strings.TrimLeft(strings.TrimSpace(jx.Str(e, "id")), "@")
		sid := SourceIDFor(platform, id)
		if !u.CatalogEntryMatchesTopic(e, topic, false) || !IsValidSourceID(platform, id) || voted[sid] || u.UASourceBlocked(sid, jx.M{"title": jx.Str(e, "name")}) != "" {
			continue
		}
		reason := reasons[platform]
		if reason == "" {
			reason = "Публичное сообщество ВКонтакте с изображениями в постах."
		}
		name := jx.Str(e, "name")
		if name == "" {
			name = id
		}
		extra := jx.M{"id": id}
		if platform == PlatformRSS {
			if jx.Bool(e, "news") {
				extra["kind"] = NewsSiteKind(id)
			} else {
				extra["kind"] = RSSFeedKind(id)
			}
		}
		out = append(out, sourceCandidate(sid, platform, SourceDisplayName(platform, name), reason, extra))
	}
	return out
}

func (u *U) suggestionSourceCandidates(d *SourceDB, platform string, voted map[string]bool, topic string) []jx.M {
	var list []jx.M
	vk := u.VKCanRead(u.Ctx)
	for _, s := range d.Suggestions {
		p := jx.Str(s, "platform")
		if !u.TopicsAreEquivalent(jx.Str(s, "topic"), topic) || (platform != "" && p != platform) || voted[jx.Str(s, "source_id")] || (p == PlatformVK && !vk) ||
			u.UASourceBlocked(jx.Str(s, "source_id"), jx.M{"title": jx.Str(s, "name")}) != "" {
			continue
		}
		list = append(list, s)
	}
	bonus := map[string]float64{}
	rank := func(s jx.M) float64 {
		kind := jx.Str(jx.Map(s, "origin"), "kind")
		if kind == "" {
			kind = "unknown"
		}
		if _, ok := bonus[kind]; !ok {
			bonus[kind] = u.DiscoveryOriginBonus(d.Votes, topic, kind)
		}
		return SuggestionRank(s, bonus[kind])
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i]) > rank(list[j]) })
	out := []jx.M{}
	for _, s := range list {
		extra := jx.M{"id": s["id"], "suggested": true, "score": s["score"]}
		if jx.Str(s, "platform") == PlatformRSS {
			extra["kind"] = RSSFeedKind(jx.Str(s, "id"))
		}
		out = append(out, sourceCandidate(jx.Str(s, "source_id"), jx.Str(s, "platform"), jx.Str(s, "name"), DiscoveryReasonWithRefs(jx.Str(s, "reason"), jx.Strs(s, "refs")), extra))
	}
	return out
}

func postedTimes(posts []jx.M) []int64 {
	var out []int64
	for _, p := range posts {
		if t := jx.Int(p, "posted_at"); t > 0 {
			out = append(out, t)
		}
	}
	return out
}

// BuildSourcePreview — посты и сводка карточки кандидата: url, description, subscribers, view_rate, posts_per_week, last_post, dead.
func (u *U) BuildSourcePreview(c jx.M, count int) jx.M {
	stats := jx.M{"url": "", "description": "", "subscribers": 0, "view_rate": nil, "posts_per_week": nil, "last_post": nil, "thread_posts": nil, "thread_files": nil, "dead": nil}
	platform := jx.Str(c, "platform")
	var lastHit any
	if h := jx.Int(c, "last_hit"); h > 0 {
		lastHit = h
	}
	previewList := func(posts []jx.M) []jx.M {
		out := []jx.M{}
		for _, p := range posts {
			out = append(out, u.SourcePreviewPost(p))
		}
		return out
	}
	switch platform {
	case Platform4chan:
		var thread int64
		fmt.Sscan(jx.Str(c, "thread_id"), &thread)
		stats["url"], stats["thread_posts"], stats["last_post"] = fourchanPostURL(jx.Str(c, "board"), thread, thread), c["thread_posts"], lastHit
		return jx.M{"posts": previewList(u.FourchanPreview(c, count)), "stats": stats}
	case PlatformDvach:
		stats["url"], stats["thread_posts"], stats["thread_files"], stats["last_post"] = DvachPostURL(jx.Str(c, "board"), jx.Str(c, "thread_id"), jx.Str(c, "thread_id")),
			c["thread_posts"], c["thread_files"], lastHit
		return jx.M{"posts": previewList(u.BuildDvachPreviewPosts(c, count)), "stats": stats}
	}
	var posts, textPosts []jx.M
	var activity jx.M
	switch platform {
	case PlatformTelegram:
		info := u.TelegramChannelInfo(jx.Str(c, "id"), 0)
		if info == nil {
			stats["dead"] = "канала нет или он закрыт"
			return jx.M{"posts": []jx.M{}, "stats": stats}
		}
		feed := u.TelegramChannelMediaPosts(jx.Str(info, "id"), 0, 0)
		posts = u.TelegramBigVideosViaAccount(jx.Maps(feed, "posts"))
		textPosts = jx.Maps(feed, "text_posts")
		var times []int64
		for _, t := range jx.List(info, "post_times") {
			times = append(times, jx.ToInt(t))
		}
		activity = SourceActivity(times, now())
		subs := jx.Int(info, "subscribers")
		var views []*int64
		if v, ok := info["post_views"].([]*int64); ok {
			views = v
		}
		rate := SourceViewRate(subs, views)
		dead := SourceInactivityReason(activity, now())
		if dead == "" {
			dead = SourceDeadAudienceReason(subs, rate)
		}
		stats["url"], stats["description"], stats["subscribers"], stats["view_rate"] = "https://t.me/"+url.PathEscape(jx.Str(info, "id")), info["description"], subs, rate
		stats["dead"] = nilIfEmpty(dead)
		for _, k := range []string{"media_total", "text_total", "posts_total", "pages"} {
			stats[k] = feed[k]
		}
	case PlatformRSS:
		posts = u.RSSPreviewPosts(c, count)
		activity = SourceActivity(postedTimes(posts), now())
		stats["url"] = jx.Str(c, "id")
		stats["dead"] = deadOr(posts, SourceInactivityReason(activity, now()), "лента не открывается или в ней нет картинок")
	case PlatformOK:
		posts = u.OKPreviewPosts(c, count)
		activity = SourceActivity(postedTimes(posts), now())
		stats["url"] = "https://ok.ru/group/" + url.PathEscape(jx.Str(c, "id"))
		stats["dead"] = deadOr(posts, SourceInactivityReason(activity, now()), "группа не открывается или в ней нет картинок")
	case PlatformTikTok:
		posts = u.TikTokPreviewPosts(c, count)
		activity = SourceActivity(postedTimes(posts), now())
		stats["url"] = "https://www.tiktok.com/@" + url.PathEscape(jx.Str(c, "id"))
		stats["dead"] = deadOr(posts, SourceInactivityReason(activity, now()), "аккаунт не открывается или в нём нет коротких видео")
	default:
		posts = u.BuildVKPreviewPosts(u.Ctx, c, count, true, 0)
		activity = SourceActivity(postedTimes(posts), now())
		stats["url"] = "https://vk.com/" + url.PathEscape(jx.Str(c, "id"))
		if len(posts) > 0 {
			stats["dead"] = nilIfEmpty(SourceInactivityReason(activity, now()))
		}
	}
	posts = append(posts, textPosts...)
	sort.SliceStable(posts, func(i, j int) bool { return jx.Int(posts[i], "posted_at") > jx.Int(posts[j], "posted_at") })
	posts = posts[:min(len(posts), count)]
	posts = u.VideoPreviewPrepare(u.Ctx, posts, u.ID)
	stats["posts_per_week"], stats["last_post"] = activity["per_week"], activity["last_post"]
	return jx.M{"posts": previewList(posts), "stats": stats}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func deadOr(posts []jx.M, reason, empty string) any {
	if len(posts) == 0 {
		return empty
	}
	return nilIfEmpty(reason)
}

// SourcePreviewPost — пост превью в одном виде для всех площадок.
func (u *U) SourcePreviewPost(p jx.M) jx.M {
	eng := jx.Map(p, "engagement")
	text := jx.Str(p, "text")
	if text == EmptyPostText {
		text = ""
	}
	var views, reactions, comments any
	if jx.Has(p, "views") && p["views"] != nil {
		views = jx.Int(p, "views")
	} else if eng != nil && eng["views"] != nil {
		views = jx.Int(eng, "views")
	}
	if jx.Has(p, "likes") && p["likes"] != nil {
		reactions = jx.Int(p, "likes")
	} else if eng != nil && eng["reactions"] != nil {
		reactions = sumReactions(eng["reactions"])
	}
	if jx.Has(p, "comments") && p["comments"] != nil {
		comments = jx.Int(p, "comments")
	}
	return jx.M{"text": trimRunes(strings.TrimSpace(text), sourcePreviewTextMax), "url": jx.Str(p, "url"), "posted_at": jx.Int(p, "posted_at"),
		"text_post": jx.Bool(p, "text_post"), "media": u.sourcePreviewMedia(p), "views": views, "reactions": reactions, "comments": comments}
}

func (u *U) sourcePreviewMedia(p jx.M) []jx.M {
	https := func(s string) string {
		if strings.HasPrefix(s, "https://") {
			return s
		}
		return ""
	}
	item := func(kind, src, poster, page string, pending, unavailable bool) jx.M {
		return jx.M{"kind": kind, "src": src, "poster": poster, "page": page, "pending": pending, "unavailable": unavailable}
	}
	page := jx.Str(p, "url")
	out := []jx.M{}
	if g := jx.Maps(p, "gallery"); len(g) > 0 {
		for _, x := range g {
			poster, video := https(jx.Str(x, "image")), https(jx.Str(x, "video"))
			if video != "" {
				out = append(out, item("video", video, poster, page, false, false))
			} else if poster != "" {
				out = append(out, item("image", poster, "", "", false, false))
			}
		}
		return out
	}
	var images []string
	for _, s := range append([]string{jx.Str(p, "image")}, jx.Strs(p, "images")...) {
		if https(s) != "" && !contains(images, s) {
			images = append(images, s)
		}
	}
	poster := ""
	if len(images) > 0 {
		poster = images[0]
	}
	video := https(jx.Str(p, "video"))
	if video == "" {
		video = u.MediaExisting(jx.Str(p, "video_local"))
	}
	videoPage := jx.Str(p, "video_page")
	unavailable := jx.Bool(p, "video_unavailable")
	switch {
	case video != "":
		out = append(out, item("video", video, poster, page, false, false))
	case unavailable:
		out = append(out, item("video", "", poster, page, false, true))
	case videoPage != "":
		out = append(out, item("video", "", poster, videoPage, jx.Bool(p, "video_pending"), false))
	}
	start := 0
	if video != "" || unavailable || videoPage != "" {
		start = 1
	}
	for _, img := range images[min(start, len(images)):] {
		out = append(out, item("image", img, "", "", false, false))
	}
	return out
}

// tiktokLiveCandidates — поиск аккаунтов TikTok прямо по нажатию: каталог TikTok обычно пуст.
func (u *U) tiktokLiveCandidates(voted map[string]bool, topic string) []jx.M {
	var queries []string
	title := topic
	var q2 string
	if e := u.TopicLookup(topic); e != nil {
		title = e.Title
		if e.Profile != nil && len(e.Profile.Queries) > 0 {
			q2 = e.Profile.Queries[0]
		}
	}
	for _, q := range []string{DiscoveryCleanQuery(title), DiscoveryCleanQuery(q2)} {
		if q != "" && !contains(queries, q) {
			queries = append(queries, q)
		}
	}
	seen := map[string]bool{}
	out := []jx.M{}
	for _, q := range queries[:min(2, len(queries))] {
		for _, user := range u.TikTokSearchUsers(q) {
			sid := SourceIDFor(PlatformTikTok, user)
			if !voted[sid] && !seen[sid] {
				seen[sid] = true
				out = append(out, sourceCandidate(sid, PlatformTikTok, SourceDisplayName(PlatformTikTok, user), "Найден поиском TikTok по запросу «"+q+"».", jx.M{"id": user}))
			}
		}
	}
	return out
}

func sourcePreviewCount(platform string) int {
	switch platform {
	case PlatformTikTok:
		return sourcePreviewPostsTT
	case PlatformDvach, Platform4chan:
		return sourcePreviewPostsSlow
	}
	return sourcePreviewPosts
}

func (u *U) firstPreviewable(cands []jx.M) jx.M {
	for _, c := range cands[:min(len(cands), sourcePreviewAttempts)] {
		p := func() (p jx.M) {
			defer func() {
				if recover() != nil {
					p = nil
				}
			}()
			return u.BuildSourcePreview(c, sourcePreviewCount(jx.Str(c, "platform")))
		}()
		if p != nil && jx.Map(p, "stats")["dead"] == nil && len(jx.Maps(p, "posts")) > 0 {
			return jx.Merge(c, p)
		}
	}
	return nil
}

func sourceAutoSearchable(platform, kind string) bool {
	if platform == PlatformRSS {
		return kind == "YouTube" || kind == "YouTube Shorts"
	}
	return platform == "" || contains([]string{PlatformTelegram, PlatformVK, PlatformDvach, Platform4chan, PlatformOK, PlatformTikTok}, platform)
}

// SuggestSources — следующий кандидат для карточки «Источники».
func (u *U) SuggestSources(in jx.M) (jx.M, int, error) {
	filter := strings.ToLower(strings.TrimSpace(jx.Str(in, "platform")))
	if filter == "" {
		filter = "any"
	}
	platform, ok := SourcePlatformFilters[filter]
	if !ok {
		return nil, 400, Bad("Неизвестная платформа источника.")
	}
	wants := func(p string) bool { return platform == "" || platform == p }
	d := u.Catalog()
	topic := u.CurrentTopic()
	var votes []jx.M
	for _, v := range d.Votes {
		if t := jx.Str(v, "topic"); t == "" || u.TopicSame(t, topic) {
			votes = append(votes, v)
		}
	}
	voted := map[string]bool{}
	for _, v := range votes {
		voted[jx.Str(v, "source_id")] = true
	}
	for i, id := range jx.Strs(in, "skip") {
		if i < 200 && len(id) <= 200 {
			voted[id] = true
		}
	}
	kind := sourceKindFilters[filter]
	kindOK := func(c jx.M) bool {
		k := jx.Str(c, "kind")
		return kind == "" || k == kind || (kind == "RSS" && strings.HasPrefix(k, "Сайт · "))
	}
	filterKind := func(l []jx.M) []jx.M {
		var out []jx.M
		for _, c := range l {
			if kindOK(c) {
				out = append(out, c)
			}
		}
		return out
	}
	suggested := filterKind(u.suggestionSourceCandidates(d, platform, voted, topic))
	if jx.Bool(in, "suggested_only") {
		if f := u.firstPreviewable(suggested); f != nil {
			return jx.M{"discovered": f}, 200, nil
		}
		return jx.M{"discovered": nil, "message": "Подбор пока ничего не нашёл."}, 200, nil
	}
	vk := wants(PlatformVK) && u.VKCanRead(u.Ctx)
	var cands []jx.M
	if wants(PlatformDvach) {
		cands = append(cands, u.dvachSourceCandidates(voted, u.dvachBlockedBoards(votes, topic), topic)...)
	}
	if wants(Platform4chan) {
		cands = append(cands, u.FourchanSourceCandidates(voted, topic)...)
	}
	for _, p := range []string{PlatformTelegram, PlatformRSS, PlatformVK, PlatformOK, PlatformTikTok} {
		if wants(p) && (p != PlatformVK || vk) {
			cands = append(cands, u.catalogSourceCandidates(d, p, voted, topic)...)
		}
	}
	cands = filterKind(cands)
	if platform == PlatformTikTok && len(cands) == 0 && len(suggested) == 0 {
		if note := u.TikTokSearchPauseNote(u.Ctx); note != "" {
			return jx.M{"discovered": nil, "message": "TikTok не отдаёт поиск (похоже, капча): повторим позже. Пока добавьте аккаунт ссылкой в «Добавить источник» выше.", "action": "add_link"}, 200, nil
		}
		if cands = u.tiktokLiveCandidates(voted, topic); len(cands) == 0 {
			return jx.M{"discovered": nil, "message": "TikTok по этой теме аккаунтов не нашёл. Добавьте аккаунт ссылкой в «Добавить источник» выше.", "action": "add_link"}, 200, nil
		}
	}
	if len(cands) == 0 && len(suggested) == 0 {
		switch {
		case platform == PlatformVK && !vk:
			return jx.M{"discovered": nil, "message": "Подключите VK: без этого паблики не ищутся.", "action": "connect_vk"}, 200, nil
		case !sourceAutoSearchable(platform, kind):
			return jx.M{"discovered": nil, "message": "Сами не находятся: добавьте ссылку в «Добавить источник» выше.", "action": "add_link"}, 200, nil
		}
		return jx.M{"discovered": nil, "message": "Пока ничего нового. Автоподбор ищет и здесь — загляните позже.", "action": "discover"}, 200, nil
	}
	by := map[string][]jx.M{}
	for _, c := range cands {
		by[jx.Str(c, "platform")] = append(by[jx.Str(c, "platform")], c)
	}
	counts := map[string]int{}
	for _, v := range votes {
		counts[jx.Str(v, "platform")]++
	}
	platforms := jx.Keys(jx.M{})
	for p := range by {
		platforms = append(platforms, p)
	}
	rand.Shuffle(len(platforms), func(i, j int) { platforms[i], platforms[j] = platforms[j], platforms[i] })
	sort.SliceStable(platforms, func(i, j int) bool { return counts[platforms[i]] < counts[platforms[j]] })
	ordered := append([]jx.M{}, suggested...)
	for _, p := range platforms {
		l := by[p]
		rand.Shuffle(len(l), func(i, j int) { l[i], l[j] = l[j], l[i] })
		ordered = append(ordered, l...)
	}
	if f := u.firstPreviewable(ordered); f != nil {
		return jx.M{"discovered": f}, 200, nil
	}
	return jx.M{"discovered": nil, "message": "Не удалось получить посты с изображениями ни из одного кандидата. Для VK подключите аккаунт, Telegram-каналы должны быть публичными."}, 502, nil
}

var votePlatforms = []string{PlatformDvach, Platform4chan, PlatformTelegram, PlatformVK, PlatformRSS, PlatformOK, PlatformTikTok}

func sourceVoteRecord(s jx.M, vote string) (jx.M, error) {
	id := strings.TrimSpace(jx.Str(s, "source_id"))
	if id == "" || len(id) > 600 {
		return nil, Bad("Некорректный источник.")
	}
	platform := jx.Str(s, "platform")
	if !contains(votePlatforms, platform) {
		platform = "Неизвестно"
	}
	name := trimRunes(strings.TrimSpace(jx.Str(s, "name")), 200)
	if name == "" {
		name = "Источник"
	}
	r := jx.M{"source_id": id, "platform": platform, "name": name, "vote": vote, "timestamp": now()}
	if b := jx.Str(s, "board"); IsValidDvachBoard(b) {
		r["board"] = b
	}
	if t := jx.Str(s, "thread_id"); IsValidDvachID(t) {
		r["thread_id"] = t
	}
	if platform == PlatformRSS && IsValidRSSURL(jx.Str(s, "id")) {
		r["kind"] = RSSFeedKind(jx.Str(s, "id"))
	}
	return r, nil
}

func (u *U) addApprovedSource(d *SourceDB, s jx.M, topic string) {
	id := strings.TrimLeft(strings.TrimSpace(jx.Str(s, "id")), "@")
	switch jx.Str(s, "platform") {
	case Platform4chan:
		u.FourchanApproveBoard(topic, jx.Str(s, "board"))
		return
	case PlatformDvach:
		b := jx.Str(s, "board")
		if !IsValidDvachBoard(b) {
			return
		}
		if u.TopicSame(config.DefaultTopic, topic) {
			d.Boards = uniqueStrings(append(d.Boards, b))
		} else {
			k := strings.ToLower(topic)
			d.DvachTopicBoards[k] = uniqueStrings(append(d.DvachTopicBoards[k], b))
		}
		return
	}
	platform := jx.Str(s, "platform")
	list := d.List(CatalogKind(platform))
	if list == nil || !IsValidSourceID(platform, id) {
		return
	}
	for _, e := range *list {
		if !strings.EqualFold(jx.Str(e, "id"), id) {
			continue
		}
		topics := jx.List(e, "topics")
		has := false
		for _, t := range topics {
			if u.TopicSame(jx.ToStr(t), topic) {
				has = true
			}
		}
		if len(topics) > 0 && !has {
			e["topics"] = append(topics, topic)
		}
		return
	}
	name := strings.TrimSpace(strings.TrimPrefix(jx.Str(s, "name"), SourceNamePrefixes[platform]))
	if name == "" {
		name = id
	}
	*list = append(*list, jx.M{"id": id, "name": trimRunes(name, 200), "topics": []any{topic}})
}

func (u *U) removeRejectedSource(d *SourceDB, s jx.M, topic string) {
	id := strings.TrimLeft(strings.TrimSpace(jx.Str(s, "id")), "@")
	list := d.List(CatalogKind(jx.Str(s, "platform")))
	if list == nil || id == "" {
		return
	}
	var kept []jx.M
	for _, e := range *list {
		if strings.EqualFold(jx.Str(e, "id"), id) {
			var topics []any
			for _, t := range jx.List(e, "topics") {
				if !u.TopicSame(jx.ToStr(t), topic) {
					topics = append(topics, t)
				}
			}
			if len(topics) == 0 {
				continue
			}
			e["topics"] = topics
		}
		kept = append(kept, e)
	}
	*list = orMaps(kept)
}

// SourceVote — решение по кандидату: approve — в каталог темы, reject — убрать из темы, maybe — только запомнить.
func (u *U) SourceVote(vote string, source jx.M, reason string) error {
	if !contains([]string{"approve", "reject", "maybe"}, vote) || source == nil {
		return Bad("Некорректное решение")
	}
	topic := u.CurrentTopic()
	record, err := sourceVoteRecord(source, vote)
	if err != nil {
		return err
	}
	record["topic"] = topic
	if vote == "reject" && contains(SourceRejectReasons, reason) {
		record["reason"] = reason
	}
	sid := jx.Str(record, "source_id")
	_, err = u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		var found jx.M
		for _, s := range d.Suggestions {
			st := jx.Str(s, "topic")
			if st == "" {
				st = topic
			}
			if jx.Str(s, "source_id") == sid && u.TopicSame(st, topic) && found == nil {
				found = s
			}
		}
		rec := jx.Merge(record, SourceVoteOrigin(found))
		exists := false
		for _, o := range d.Votes {
			if jx.Str(o, "source_id") == sid && u.TopicSame(jx.Str(o, "topic"), topic) {
				exists = true
			}
		}
		if !exists {
			d.Votes = append(d.Votes, rec)
		}
		src, srcTopic := source, topic
		var keep []jx.M
		for _, s := range d.Suggestions {
			st := jx.Str(s, "topic")
			if st == "" {
				st = topic
			}
			if jx.Str(s, "source_id") == sid && u.TopicSame(st, topic) {
				src, srcTopic = jx.M{"platform": s["platform"], "id": s["id"], "name": s["name"]}, st
				continue
			}
			keep = append(keep, s)
		}
		d.Suggestions = orMaps(keep)
		switch vote {
		case "approve":
			u.addApprovedSource(d, src, srcTopic)
		case "reject":
			u.removeRejectedSource(d, src, srcTopic)
		}
		return true
	})
	return err
}

// RemoveTopicSource — убрать источник из темы; решение запоминается, автоподбор его не вернёт.
func (u *U) RemoveTopicSource(platform, id, name string) error {
	topic := u.CurrentTopic()
	id = strings.TrimLeft(strings.TrimSpace(id), "@")
	if platform == PlatformDvach {
		if !IsValidDvachBoard(id) {
			return Bad("Некорректная доска.")
		}
		_, err := u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
			drop := func(l []string) []string {
				var out []string
				for _, b := range l {
					if b != id {
						out = append(out, b)
					}
				}
				return orStrs(out)
			}
			if u.TopicSame(config.DefaultTopic, topic) {
				d.Boards = drop(d.Boards)
			} else {
				k := strings.ToLower(topic)
				d.DvachTopicBoards[k] = drop(d.DvachTopicBoards[k])
			}
			return true
		})
		return err
	}
	if CatalogKind(platform) == "" || id == "" || len(id) > 500 {
		return Bad("Некорректный источник.")
	}
	if name == "" {
		name = id
	}
	source := jx.M{"platform": platform, "id": id, "name": name, "source_id": SourceIDFor(platform, id)}
	record, _ := sourceVoteRecord(source, "reject")
	record["topic"], record["reason"] = topic, "other"
	_, err := u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		var votes []jx.M
		for _, o := range d.Votes {
			if !(jx.Str(o, "source_id") == jx.Str(record, "source_id") && u.TopicSame(jx.Str(o, "topic"), topic)) {
				votes = append(votes, o)
			}
		}
		d.Votes = append(votes, record)
		u.removeRejectedSource(d, source, topic)
		return true
	})
	return err
}

// TopicSources — источники текущей темы для вкладки.
func (u *U) TopicSources() []jx.M {
	d := u.Catalog()
	topic := u.CurrentTopic()
	cards := []jx.M{}
	for _, ck := range CatalogKinds {
		platform := ck.Platform
		for _, e := range *d.List(ck.Kind) {
			id := strings.TrimLeft(jx.Str(e, "id"), "@")
			if !u.CatalogEntryMatchesTopic(e, topic, false) || u.UASourceBlocked(SourceIDFor(platform, id), jx.M{"title": jx.Str(e, "name")}) != "" {
				continue
			}
			if (platform == PlatformRSS && !IsValidRSSURL(id)) || (platform == PlatformOK && !IsValidOKGroup(id)) || (platform == PlatformTikTok && !IsValidTikTokUser(id)) {
				continue
			}
			aud := jx.Map(e, "audience")
			subs := jx.Int(aud, "subscribers")
			var link, handle, kind string
			switch platform {
			case PlatformRSS:
				link = id
				if pu, err := url.Parse(id); err == nil {
					handle = pu.Hostname()
				}
				kind = RSSFeedKind(id)
				if jx.Bool(e, "news") {
					kind = NewsSiteKind(id)
				}
			case PlatformOK:
				link, handle, kind = "https://ok.ru/group/"+id, "ok.ru/group/"+id, platform
			case PlatformTikTok:
				link, handle, kind = "https://www.tiktok.com/@"+url.PathEscape(id), "tiktok.com/@"+id, platform
			case PlatformTelegram:
				link, handle, kind = "https://t.me/"+url.PathEscape(id), "@"+id, platform
			default:
				link, handle, kind = "https://vk.com/"+url.PathEscape(id), "vk.com/"+id, platform
			}
			name := jx.Str(e, "name")
			if name == "" {
				name = id
			}
			var score, rate any
			if jx.Has(e, "score") {
				score = jx.Int(e, "score")
			}
			if aud != nil && aud["view_rate"] != nil {
				rate = jx.Float(aud, "view_rate")
			}
			label, activity := "", ""
			if subs > 0 {
				label = FormatSubscribers(subs)
			}
			if a := jx.Map(e, "activity"); a != nil {
				activity = DescribeSourceActivity(a)
			}
			preview := jx.List(e, "preview")
			cards = append(cards, jx.M{"source_id": SourceIDFor(platform, id), "platform": platform, "id": id, "name": name, "url": link, "handle": handle,
				"topics": orAny(jx.List(e, "topics")), "kind": kind, "own": len(jx.List(e, "topics")) > 0 && u.CatalogEntryMatchesTopic(e, topic, true),
				"auto": jx.Bool(e, "auto"), "score": score, "muted": false, "subscribers": subs, "subscribers_label": label, "view_rate": rate, "activity": activity,
				"frozen": jx.Has(e, "frozen_until"), "frozen_reason": jx.Str(e, "frozen_reason"), "avatar": jx.Str(e, "avatar"), "preview": orAny(preview[:min(4, len(preview))])})
		}
	}
	names := u.dvachBoardList()
	for _, b := range u.DvachBoardsForTopic(topic, false) {
		title := jx.Str(names[b], "name")
		name := "Доска /" + b + "/"
		if title != "" {
			name = "/" + b + "/ — " + title
		}
		cards = append(cards, jx.M{"source_id": "dvach-board:" + b, "platform": PlatformDvach, "id": b, "name": name, "category": jx.Str(names[b], "category"),
			"url": "https://2ch.hk/" + url.PathEscape(b) + "/", "handle": "2ch.hk/" + b + "/", "topics": []any{}, "own": true, "auto": false, "score": nil, "subscribers": 0,
			"subscribers_label": "", "view_rate": nil, "activity": "", "frozen": false, "frozen_reason": "", "avatar": "", "preview": []any{}})
	}
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		if jx.Bool(a, "own") != jx.Bool(b, "own") {
			return jx.Bool(a, "own")
		}
		if jx.Bool(a, "frozen") != jx.Bool(b, "frozen") {
			return !jx.Bool(a, "frozen")
		}
		return jx.Int(a, "subscribers") > jx.Int(b, "subscribers")
	})
	return cards
}
