package posta

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
)

// Вкладка «Источники»: кандидаты по одному, превью, решения, источники темы.

var SourcePlatformFilters = map[string]string{"any": "", "telegram": PlatformTelegram, "vk": PlatformVK, "tiktok": PlatformTikTok, "youtube": PlatformRSS,
	"shorts": PlatformRSS, "ok": PlatformOK, "pinterest": PlatformRSS, "tumblr": PlatformRSS, "rss": PlatformRSS, "fourchan": Platform4chan, "dvach": PlatformDvach}

var sourceKindFilters = map[string]string{"youtube": "YouTube", "shorts": "YouTube Shorts", "pinterest": "Pinterest", "tumblr": "Tumblr", "rss": "RSS"}

var SourceRejectReasons = []string{"off_topic", "dead", "other"}

const (
	sourcePreviewAttempts  = 12
	sourcePreviewPosts     = 20
	sourcePreviewPostsTT   = 8
	sourcePreviewPostsSlow = 6
	sourcePreviewTextMax   = 2000
	SourceAddMaxLink       = 500
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
			if !IsValidDvachID(thread) || voted[sid] || jx.Int(t, "files_count") < 3 || DvachThreadDeadReason(t, now()) != "" {
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
	vk := true
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
			post := jx.Copy(p)
			if jx.Str(post, "source") == "" {
				post["source"] = jx.Str(c, "name")
			}
			out = append(out, u.SourcePreviewPost(post))
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
	text := CleanPostAttribution(jx.Str(p, "text"), p)
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

// Temporary exclusions are scoped to the user and topic and survive page reloads.
const sourcePostponeDuration = 7 * 24 * time.Hour
const sourcePreviewFailureDuration = 30 * time.Minute

func sourceSuggestionKey(uid, topic, kind string) string {
	h := sha256.Sum256([]byte(TopicNorm(topic)))
	return fmt.Sprintf("source_suggestion:%s:%s:%x", kind, uid, h[:12])
}

func activeSourceExclusions(entries map[string]int64, at int64) map[string]bool {
	active := map[string]bool{}
	for id, until := range entries {
		if until > at {
			active[id] = true
		}
	}
	return active
}

func (u *U) rememberSourceExclusion(topic, kind, id string, duration time.Duration) error {
	key := sourceSuggestionKey(u.ID, topic, kind)
	_, err := KVUpdate(u.App, u.Ctx, key, 0, func(entries map[string]int64, _ bool) *map[string]int64 {
		if entries == nil {
			entries = map[string]int64{}
		}
		for k, until := range entries {
			if until <= now() {
				delete(entries, k)
			}
		}
		entries[id] = now() + int64(duration/time.Second)
		return &entries
	})
	return err
}

// A displayed source is withheld until a deliberate postponement expires.
// Recommendation identity joins a YouTube channel's videos and Shorts feeds.
func recommendationSourceKey(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if strings.HasPrefix(id, "rss:") {
		if u, err := url.Parse(strings.TrimPrefix(id, "rss:")); err == nil && (u.Hostname() == "youtube.com" || u.Hostname() == "www.youtube.com") && u.Path == "/feeds/videos.xml" {
			q := u.Query()
			channel := q.Get("channel_id")
			if p := q.Get("playlist_id"); strings.HasPrefix(p, "uush") && len(p) == 26 {
				channel = "uc" + p[4:]
			}
			if len(channel) == 24 && strings.HasPrefix(channel, "uc") {
				return "youtube-channel:" + channel
			}
		}
	}
	return id
}

// Historical post ratings predate impression tracking. Never recommend an
// already rated source, even if its exact recommendation was never recorded.
func (u *U) ratedSourceExclusions() (map[string]bool, error) {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT DISTINCT data->>'source_key' FROM history_items WHERE user_id=$1 AND collection IN ('liked','disliked','undecided','complaints') AND COALESCE(data->>'source_key','')<>''`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		// Post ratings are not decisions about collecting an imageboard thread.
		if strings.HasPrefix(id, "dvach:") || strings.HasPrefix(id, "4chan:") {
			continue
		}
		out[recommendationSourceKey(id)] = true
	}
	return out, rows.Err()
}

func (u *U) sourceImpressionExclusions(topic string) (map[string]bool, error) {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT source_id FROM source_impressions WHERE user_id=$1 AND topic_key=$2 AND (available_after=0 OR available_after>$3 OR decision<>'')`, u.ID, TopicNorm(topic), now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (u *U) recordSourceShown(topic, id string) (bool, error) {
	var claimed int
	err := u.DB.Pool.QueryRow(u.Ctx, `INSERT INTO source_impressions(user_id,topic_key,source_id,first_shown,last_shown) VALUES($1,$2,$3,$4,$4)
 ON CONFLICT(user_id,topic_key,source_id) DO UPDATE SET last_shown=EXCLUDED.last_shown, show_count=source_impressions.show_count+1, available_after=0
 WHERE source_impressions.decision='' AND source_impressions.available_after>0 AND source_impressions.available_after<=$4 RETURNING 1`, u.ID, TopicNorm(topic), id, now()).Scan(&claimed)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (u *U) PostponeSource(source jx.M) error {
	record, err := sourceVoteRecord(source, "maybe")
	if err != nil {
		return err
	}
	_, err = u.DB.Pool.Exec(u.Ctx, `UPDATE source_impressions SET available_after=$4 WHERE user_id=$1 AND topic_key=$2 AND source_id=$3 AND decision=''`, u.ID, TopicNorm(u.CurrentTopic()), jx.Str(record, "source_id"), now()+int64(sourcePostponeDuration/time.Second))
	return err
}

// dvachPreviewImages собирает разные изображения для проверки содержимого треда.
func dvachPreviewImages(posts []jx.M) []jx.M {
	var images []jx.M
	seen := map[string]bool{}
	for _, post := range posts {
		for _, media := range jx.Maps(post, "media") {
			src := jx.Str(media, "src")
			if jx.Str(media, "kind") == "video" {
				src = jx.Str(media, "poster")
			}
			if src != "" && !seen[src] {
				seen[src] = true
				images = append(images, jx.M{"image": src})
			}
		}
	}
	return images
}

func (u *U) firstPreviewable(cands []jx.M, topic string) (jx.M, error) {
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
			if jx.Str(c, "platform") == PlatformDvach || jx.Str(c, "platform") == Platform4chan {
				images := dvachPreviewImages(jx.Maps(p, "posts"))
				if len(images) < 3 {
					continue
				}
				review := u.sourceImagesReview(jx.Str(c, "name"), topic, images)
				if review == nil || review.Score < config.SourceMinContentScore {
					continue
				}
				c["reason"] = "Примеры медиа треда проверены на соответствие теме: " + review.Reason
			}
			claimed, err := u.recordSourceShown(topic, jx.Str(c, "source_id"))
			if err != nil {
				return nil, err
			}
			if claimed {
				return jx.Merge(c, p), nil
			}
			continue
		}
		_ = u.rememberSourceExclusion(topic, "failed", jx.Str(c, "source_id"), sourcePreviewFailureDuration)
	}
	return nil, nil
}

// Alternate discovery and catalog candidates, avoiding duplicate source IDs.
func interleaveSourceCandidates(suggested, catalog []jx.M) []jx.M {
	out := make([]jx.M, 0, len(suggested)+len(catalog))
	seen := map[string]bool{}
	add := func(c jx.M) {
		id := jx.Str(c, "source_id")
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, c)
		}
	}
	for i := 0; i < len(suggested) || i < len(catalog); i++ {
		if i < len(suggested) {
			add(suggested[i])
		}
		if i < len(catalog) {
			add(catalog[i])
		}
	}
	return out
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
	rated, err := u.ratedSourceExclusions()
	if err != nil {
		return nil, 500, err
	}
	seen, err := u.sourceImpressionExclusions(topic)
	if err != nil {
		return nil, 500, err
	}
	for id := range seen {
		voted[id] = true
	}
	for _, kind := range []string{"postponed", "failed"} {
		entries := KV(u.App, u.Ctx, sourceSuggestionKey(u.ID, topic, kind), map[string]int64{})
		for id := range activeSourceExclusions(entries, now()) {
			voted[id] = true
		}
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
	for id := range voted {
		rated[recommendationSourceKey(id)] = true
	}
	filterRated := func(cands []jx.M) []jx.M {
		out := make([]jx.M, 0, len(cands))
		for _, c := range cands {
			if !rated[recommendationSourceKey(jx.Str(c, "source_id"))] {
				out = append(out, c)
			}
		}
		return out
	}
	suggested := filterRated(filterKind(u.suggestionSourceCandidates(d, platform, voted, topic)))
	if jx.Bool(in, "suggested_only") {
		f, err := u.firstPreviewable(suggested, topic)
		if err != nil {
			return nil, 500, err
		}
		if f != nil {
			f["quality"] = u.SourceQualityOf(u.Ctx, jx.Str(f, "source_id"), topic)
			f["board_quality"] = u.BoardQualityOf(u.Ctx, jx.Str(f, "source_id"), topic)
			return jx.M{"discovered": f}, 200, nil
		}
		return jx.M{"discovered": nil, "message": "Подбор пока ничего не нашёл."}, 200, nil
	}
	vk := wants(PlatformVK)
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
	cands = filterRated(filterKind(cands))
	if platform == PlatformTikTok && len(cands) == 0 && len(suggested) == 0 {
		if note := u.TikTokSearchPauseNote(u.Ctx); note != "" {
			return jx.M{"discovered": nil, "message": "TikTok не отдаёт поиск (похоже, капча): повторим позже. Пока добавьте аккаунт ссылкой в «Добавить источник» выше.", "action": "add_link"}, 200, nil
		}
		if cands = filterRated(u.tiktokLiveCandidates(voted, topic)); len(cands) == 0 {
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
	var catalogOrdered []jx.M
	for _, p := range platforms {
		l := by[p]
		rand.Shuffle(len(l), func(i, j int) { l[i], l[j] = l[j], l[i] })
		catalogOrdered = append(catalogOrdered, l...)
	}
	ordered := interleaveSourceCandidates(suggested, catalogOrdered)
	f, err := u.firstPreviewable(ordered, topic)
	if err != nil {
		return nil, 500, err
	}
	if f != nil {
		f["quality"] = u.SourceQualityOf(u.Ctx, jx.Str(f, "source_id"), topic)
		f["board_quality"] = u.BoardQualityOf(u.Ctx, jx.Str(f, "source_id"), topic)
		return jx.M{"discovered": f}, 200, nil
	}
	return jx.M{"discovered": nil, "message": "Не удалось загрузить посты у проверенных источников. Они временно пропущены; попробуйте ещё раз, чтобы проверить следующие."}, 502, nil
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

// Only decisions explicitly made as thread decisions enable collection.
func (a *App) approvedImageboardThreads(d *SourceDB, topic string) []jx.M {
	var out []jx.M
	for _, v := range d.Votes {
		if jx.Str(v, "scope") != "thread" || jx.Str(v, "vote") != "approve" || !a.TopicSame(jx.Str(v, "topic"), topic) {
			continue
		}
		platform, board, thread := jx.Str(v, "platform"), jx.Str(v, "board"), jx.Str(v, "thread_id")
		if (platform == PlatformDvach || platform == Platform4chan) && IsValidDvachBoard(board) && IsValidDvachID(thread) {
			out = append(out, v)
		}
	}
	return out
}

func (u *U) addApprovedSource(d *SourceDB, s jx.M, topic string) {
	id := strings.TrimLeft(strings.TrimSpace(jx.Str(s, "id")), "@")
	if jx.Str(s, "platform") == PlatformDvach || jx.Str(s, "platform") == Platform4chan {
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
	if jx.Str(record, "platform") == PlatformDvach || jx.Str(record, "platform") == Platform4chan {
		board, thread := jx.Str(record, "board"), jx.Str(record, "thread_id")
		prefix := "dvach:"
		if jx.Str(record, "platform") == Platform4chan {
			prefix = "4chan:"
		}
		if !IsValidDvachBoard(board) || !IsValidDvachID(thread) || jx.Str(record, "source_id") != prefix+board+":"+thread {
			return Bad("Некорректный тред.")
		}
		record["scope"] = "thread"
	}
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
		var remaining []jx.M
		for _, o := range d.Votes {
			if jx.Str(o, "source_id") != sid || !u.TopicSame(jx.Str(o, "topic"), topic) {
				remaining = append(remaining, o)
			}
		}
		d.Votes = append(remaining, rec)

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
	if err == nil {
		_, err = u.DB.Pool.Exec(u.Ctx, `UPDATE source_impressions SET decision=$4, available_after=0 WHERE user_id=$1 AND topic_key=$2 AND source_id=$3`, u.ID, TopicNorm(topic), sid, vote)
		if err != nil {
			return err
		}
		status := map[string]string{"approve": RegStatusOwnerYes, "reject": RegStatusOwnerNo, "maybe": RegStatusOwnerMaybe}[vote]
		u.registrySetOwner(sid, jx.Str(record, "platform"), jx.Str(record, "name"), status, topic)
	}
	return err
}

// RemoveTopicSource — убрать источник из темы; решение запоминается, автоподбор его не вернёт.
func (u *U) RemoveTopicSource(platform, id, name string) error {
	topic := u.CurrentTopic()
	id = strings.TrimLeft(strings.TrimSpace(id), "@")
	if (platform == PlatformDvach || platform == Platform4chan) && strings.Count(id, ":") == 2 {
		parts := strings.Split(id, ":")
		prefix := "dvach"
		if platform == Platform4chan {
			prefix = "4chan"
		}
		if parts[0] != prefix || !IsValidDvachBoard(parts[1]) || !IsValidDvachID(parts[2]) {
			return Bad("Некорректный тред.")
		}
		return u.SourceVote("reject", jx.M{"platform": platform, "source_id": id, "board": parts[1], "thread_id": parts[2], "name": name}, "other")
	}
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
	if err == nil {
		u.registrySetOwner(jx.Str(source, "source_id"), platform, name, RegStatusOwnerNo, topic)
	}
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
	for _, t := range u.approvedImageboardThreads(d, topic) {
		platform, board, thread, sid := jx.Str(t, "platform"), jx.Str(t, "board"), jx.Str(t, "thread_id"), jx.Str(t, "source_id")
		link := DvachPostURL(board, thread, thread)
		if platform == Platform4chan {
			link = fourchanPostURL(board, jx.ToInt(thread), jx.ToInt(thread))
		}
		cards = append(cards, jx.M{"source_id": sid, "platform": platform, "id": sid, "name": jx.Str(t, "name"), "url": link, "handle": "/" + board + "/ · #" + thread,
			"topics": []any{topic}, "kind": "Тред", "own": true, "auto": false, "score": nil, "subscribers": 0, "view_rate": nil, "frozen": false, "preview": []any{}})
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
