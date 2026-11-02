package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

var SourceNamePrefixes = map[string]string{PlatformTelegram: "Telegram | ", PlatformVK: "ВКонтакте | ", PlatformRSS: "RSS | ", PlatformOK: "Одноклассники | ",
	PlatformTikTok: "TikTok | ", Platform4chan: "4chan | "}

var trustedSourceHosts = []string{"2ch.hk", "t.me", "vk.com", "ok.ru", "www.tiktok.com", "x.com", "boards.4chan.org"}

const EmptyPostText = "Пост без текста"

var NoTextPlaceholders = []string{EmptyPostText, "Текст к посту отсутствует"}

// PostCaptionClean — текст поста без заглушек.
func PostCaptionClean(text string) string {
	t := strings.TrimSpace(text)
	if contains(NoTextPlaceholders, t) {
		return ""
	}
	return t
}

var telegramReservedPaths = []string{"joinchat", "addlist", "addstickers", "addemoji", "addtheme", "setlanguage", "share", "proxy", "socks", "boost", "contact", "login", "premium", "username", "telegram", "durov", "iv"}

var tagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// CleanHTMLText — текст без тегов и сущностей, пробелы схлопнуты.
func CleanHTMLText(raw string) string {
	return collapseSpaces(html.UnescapeString(tagRe.ReplaceAllString(raw, "")))
}

func htmlAttr(v string) string { return html.UnescapeString(v) }

var (
	dvachBoardRe  = regexp.MustCompile(`(?i)^[a-z0-9_-]{1,30}$`)
	dvachIDRe     = regexp.MustCompile(`^[0-9]{1,20}$`)
	tgChannelRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
	vkDomainRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

func IsValidDvachBoard(b string) bool      { return dvachBoardRe.MatchString(b) }
func IsValidDvachID(id string) bool        { return dvachIDRe.MatchString(id) }
func IsValidTelegramChannel(c string) bool { return tgChannelRe.MatchString(c) }
func IsValidVKDomain(d string) bool        { return vkDomainRe.MatchString(d) }

func IsValidRSSURL(raw string) bool {
	if len(raw) > 500 || !strings.HasPrefix(raw, "https://") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Host != ""
}

func IsValidSourceID(platform, id string) bool {
	switch platform {
	case PlatformTelegram:
		return IsValidTelegramChannel(id)
	case PlatformVK:
		return IsValidVKDomain(id)
	case PlatformRSS:
		return IsValidRSSURL(id)
	case PlatformOK:
		return IsValidOKGroup(id)
	case PlatformTikTok:
		return IsValidTikTokUser(id)
	}
	return false
}

// SourceIDFor — ключ источника «telegram:имя».
func SourceIDFor(platform, id string) string {
	prefix := map[string]string{PlatformTelegram: "telegram:", PlatformVK: "vk:", PlatformRSS: "rss:", PlatformOK: "ok:", PlatformTikTok: "tiktok:"}[platform]
	if prefix == "" {
		prefix = "source:"
	}
	return prefix + strings.ToLower(id)
}

func SourceDisplayName(platform, name string) string { return SourceNamePrefixes[platform] + name }

func IsTrustedSourceURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && contains(trustedSourceHosts, strings.ToLower(u.Host))
}

func DvachPostURL(board, thread, post string) string {
	return "https://2ch.hk/" + url.PathEscape(board) + "/res/" + url.PathEscape(thread) + ".html#" + url.PathEscape(post)
}

// ContentPlatform — площадка записи по префиксу source.
func ContentPlatform(item jx.M) string {
	src := jx.Str(item, "source")
	for p, prefix := range SourceNamePrefixes {
		if strings.HasPrefix(src, prefix) {
			return p
		}
	}
	return PlatformDvach
}

func FormatSubscribers(n int64) string {
	switch {
	case n >= 1_000_000:
		return trimFloat(math.Round(float64(n)/100_000)/10) + "M"
	case n >= 1000:
		return trimFloat(math.Round(float64(n)/100)/10) + "K"
	}
	return strconv.FormatInt(n, 10)
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// Median — медиана; nil — пусто.
func Median(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	v := append([]float64{}, values...)
	sort.Float64s(v)
	n := len(v)
	m := v[n/2]
	if n%2 == 0 {
		m = (v[n/2-1] + v[n/2]) / 2
	}
	return &m
}

// ---- Двач ----

// Fetch2chJSON — каталог доски или тред.
func (a *App) Fetch2chJSON(board, thread string) jx.M {
	if !IsValidDvachBoard(board) || (thread != "" && !IsValidDvachID(thread)) {
		return nil
	}
	u := "https://2ch.hk/" + url.PathEscape(board) + "/catalog.json"
	if thread != "" {
		u = "https://2ch.hk/" + url.PathEscape(board) + "/res/" + url.PathEscape(thread) + ".json"
	}
	var m jx.M
	if !a.HTTPGetJSON(u, 8*time.Second, &m) {
		return nil
	}
	return m
}

func DvachThreadPosts(data jx.M) []jx.M {
	threads := jx.Maps(data, "threads")
	if len(threads) == 0 {
		return nil
	}
	return jx.Maps(threads[0], "posts")
}

// DvachPostMedia — медиа поста: у видео image — превью; без превью видео пропускается.
func DvachPostMedia(board string, post jx.M, withVideo bool) []jx.M {
	q := regexp.QuoteMeta(board)
	img := regexp.MustCompile(`(?i)^/` + q + `/src/[0-9]+/[^/]+\.(?:jpe?g|png|gif|webp)$`)
	vid := regexp.MustCompile(`(?i)^/` + q + `/src/[0-9]+/[^/]+\.(?:webm|mp4)$`)
	thumb := regexp.MustCompile(`(?i)^/` + q + `/thumb/[0-9]+/[^/]+\.(?:jpe?g|png|gif|webp)$`)
	var media []jx.M
	for _, f := range jx.Maps(post, "files") {
		p, t := jx.Str(f, "path"), jx.Str(f, "thumbnail")
		if img.MatchString(p) {
			media = append(media, jx.M{"image": "https://2ch.hk" + p, "video": ""})
		} else if withVideo && vid.MatchString(p) && thumb.MatchString(t) {
			media = append(media, jx.M{"image": "https://2ch.hk" + t, "video": "https://2ch.hk" + p})
		}
	}
	return media
}

var dvachRefRe = regexp.MustCompile(`>>\d+(\s*\((?:OP|You)\))?`)

func DvachPostText(post jx.M) string {
	raw := jx.Str(post, "comment")
	if !jx.Has(post, "comment") {
		raw = jx.Str(post, "subject")
	}
	return collapseSpaces(dvachRefRe.ReplaceAllString(CleanHTMLText(raw), ""))
}

func DvachThreadTitle(subject, thread string) string {
	t := strings.NewReplacer("[", "", "]", "").Replace(CleanHTMLText(subject))
	if t == "" {
		return "#" + thread
	}
	if runeLen(t) > config.DvachThreadTitleMax {
		return strings.TrimRight(trimRunes(t, config.DvachThreadTitleMax-1), " ") + "…"
	}
	return t
}

// BuildDvachPreviewPosts — превью треда: count файлов через равные промежутки.
func (a *App) BuildDvachPreviewPosts(src jx.M, count int) []jx.M {
	board, thread := jx.Str(src, "board"), jx.Str(src, "thread_id")
	type pair struct{ post, media jx.M }
	var items []pair
	for _, p := range DvachThreadPosts(a.Fetch2chJSON(board, thread)) {
		for _, m := range DvachPostMedia(board, p, true) {
			items = append(items, pair{p, m})
		}
	}
	picked := items
	if len(items) > count {
		picked = nil
		for i := 0; i < count; i++ {
			picked = append(picked, items[int(math.Floor((float64(i)+0.5)*float64(len(items))/float64(count)))])
		}
	}
	out := []jx.M{}
	for _, p := range picked {
		text := DvachPostText(p.post)
		if text == "" {
			text = EmptyPostText
		}
		num := jx.Str(p.post, "num")
		out = append(out, jx.M{"image": p.media["image"], "images": []any{p.media["image"]}, "video": p.media["video"], "text": text,
			"source": jx.Str(src, "name"), "post_id": num, "posted_at": jx.Int(p.post, "timestamp"), "url": DvachPostURL(board, thread, num)})
	}
	return out
}

// DvachThreadState — alive | gone (только 404) | unknown; posts — посты живого треда.
func (a *App) DvachThreadState(board, thread string) (string, []jx.M) {
	if !IsValidDvachBoard(board) || !IsValidDvachID(thread) {
		return "unknown", nil
	}
	body, status, _ := a.HTTPGetStatus("https://2ch.hk/"+url.PathEscape(board)+"/res/"+url.PathEscape(thread)+".json", 20*time.Second, nil)
	if status == 404 {
		return "gone", nil
	}
	if status != 200 {
		return "unknown", nil
	}
	list := DvachThreadPosts(jx.Unmarshal(body))
	if len(list) == 0 {
		return "unknown", nil
	}
	return "alive", list
}

// DvachThreadDeadReason — тред закрыт, пустой или молчит дольше SourceStaleDays.
func DvachThreadDeadReason(t jx.M, at int64) string {
	if at == 0 {
		at = now()
	}
	if jx.Bool(t, "closed") {
		return "тред закрыт"
	}
	if n := jx.Int(t, "posts_count"); n < config.DvachThreadMinPosts {
		return fmt.Sprintf("в треде всего %d постов", n)
	}
	if last := jx.Int(t, "lasthit"); last > 0 {
		if days := (at - last) / config.DaySeconds; days > config.SourceStaleDays {
			return fmt.Sprintf("в тред не писали %d дн", days)
		}
	}
	return ""
}

func DvachThreadIsAlive(t jx.M, at int64) bool {
	if at == 0 {
		at = now()
	}
	last := jx.Int(t, "lasthit")
	return DvachThreadDeadReason(t, at) == "" && last > 0 && at-last <= config.DvachThreadAliveHours*3600
}

var dataNumRe = regexp.MustCompile(`data-num="(\d+)"`)

// DvachThreadEngagement — активность постов треда: ответы, позиция, размер и скорость треда против медиан доски.
func DvachThreadEngagement(posts []jx.M, catalogThreads []jx.M) map[string]jx.M {
	if len(posts) == 0 {
		return map[string]jx.M{}
	}
	replies := map[string]int{}
	for _, p := range posts {
		if jx.Bool(p, "op") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range dataNumRe.FindAllStringSubmatch(jx.Str(p, "comment"), -1) {
			if !seen[m[1]] && m[1] != jx.Str(p, "num") {
				seen[m[1]] = true
				replies[m[1]]++
			}
		}
	}
	t := now()
	var vel, counts []float64
	for _, th := range catalogThreads {
		hours := 24.0
		if ts := jx.Int(th, "timestamp"); ts > 0 {
			hours = math.Max(1, float64(t-ts)/3600)
		}
		vel = append(vel, float64(jx.Int(th, "posts_count"))/hours)
		counts = append(counts, float64(jx.Int(th, "posts_count")))
	}
	first, last := jx.Int(posts[0], "timestamp"), jx.Int(posts[len(posts)-1], "timestamp")
	var hours any
	if first > 0 && last > 0 {
		hours = math.Round(math.Max(1, float64(last-first)/3600)*10) / 10
	}
	files := 0
	for _, p := range posts {
		files += len(jx.List(p, "files"))
	}
	var medVel, medPosts any
	if m := Median(vel); m != nil {
		medVel = math.Round(*m*100) / 100
	}
	if m := Median(counts); m != nil {
		medPosts = *m
	}
	out := map[string]jx.M{}
	for i, p := range posts {
		n := jx.Str(p, "num")
		var views any
		if jx.Has(p, "views") {
			views = jx.Int(p, "views")
		}
		out[n] = jx.M{"v": 1, "source": "dvach", "at": t, "is_op": i == 0, "index": i, "posts_after": len(posts) - i - 1, "replies": replies[n],
			"post_views": views, "thread_posts": len(posts), "thread_hours": hours, "thread_files": files,
			"board_median_thread_posts": medPosts, "board_median_velocity": medVel}
	}
	return out
}

// ---- Картинки в нескольких размерах: один ключ, оставляем самый большой вариант ----

var sizeSuffixRe = regexp.MustCompile(`[_-](?:s|m|x|y|w|z|small|medium|large|thumb|preview)(\.[a-z0-9]+)$`)
var sizeLetterRe = regexp.MustCompile(`[_-](s|m|x|y|z|w)\.[a-z0-9]+$`)
var thumbWordRe = regexp.MustCompile(`thumb|small|preview`)

func MediaFileKey(raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return raw
	}
	p, _ := url.PathUnescape(u.Path)
	p = strings.ToLower(p)
	name := sizeSuffixRe.ReplaceAllString(path.Base(p), "$1")
	if len(name) >= 16 {
		return name
	}
	return strings.ToLower(u.Host) + sizeSuffixRe.ReplaceAllString(p, "$1")
}

func MediaSizeScore(raw string) int {
	u, _ := url.Parse(raw)
	if u == nil {
		return 0
	}
	q := u.Query()
	for _, k := range []string{"w", "width", "size"} {
		if v := q.Get(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return 1000 + n
			}
		}
	}
	name := strings.ToLower(path.Base(u.Path))
	if m := sizeLetterRe.FindStringSubmatch(name); m != nil {
		return map[string]int{"s": 100, "m": 200, "x": 300, "y": 400, "z": 500, "w": 600}[m[1]]
	}
	if thumbWordRe.MatchString(name) {
		return 10
	}
	return 1000
}

// MediaDedupeURLs — адреса без повторов файлов: остаётся самый большой вариант, порядок — первого вхождения.
func MediaDedupeURLs(urls []string) []string {
	best := map[string]string{}
	var order []string
	for _, u := range uniqueStrings(urls) {
		if u == "" {
			continue
		}
		k := MediaFileKey(u)
		cur, ok := best[k]
		if !ok {
			order = append(order, k)
			best[k] = u
			continue
		}
		a, b := MediaSizeScore(u), MediaSizeScore(cur)
		if a > b || (a == b && len(u) > len(cur)) {
			best[k] = u
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, best[k])
	}
	return out
}

// ---- Telegram: публичные страницы t.me/s/<канал> ----

// FetchTelegramChannelPage — страница канала (не чаще одного запроса к t.me в 1,5 с).
func (a *App) FetchTelegramChannelPage(channel string, before int64) string {
	if !IsValidTelegramChannel(channel) {
		return ""
	}
	HostThrottle("t.me", 1500*time.Millisecond)
	u := "https://t.me/s/" + url.PathEscape(channel)
	if before > 0 {
		u += "?before=" + strconv.FormatInt(before, 10)
	}
	b, _, _ := a.HTTPGetStatus(u, 12*time.Second, map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Posta/1.0)"})
	return string(b)
}

type tgBlock struct{ ID, Content string }

var dataPostRe = regexp.MustCompile(`data-post="([^"]+)"`)

// TelegramPageBlocks — блоки постов страницы: [id поста, разметка до следующего поста].
func TelegramPageBlocks(page string) []tgBlock {
	locs := dataPostRe.FindAllStringSubmatchIndex(page, -1)
	var out []tgBlock
	for i, l := range locs {
		full := page[l[2]:l[3]]
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		parts := strings.SplitN(full, "/", 2)
		if len(parts) == 2 && dvachIDRe.MatchString(parts[1]) {
			out = append(out, tgBlock{parts[1], page[l[1]:end]})
		}
	}
	return out
}

var counterRe = regexp.MustCompile(`([\d.,]+)\s*([KkMm]?)`)

func TelegramParseCounter(text string) int64 {
	m := counterRe.FindStringSubmatch(strings.ReplaceAll(text, " ", ""))
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	mult := map[string]float64{"k": 1000, "m": 1_000_000}[strings.ToLower(m[2])]
	if mult == 0 {
		mult = 1
	}
	return int64(math.Round(f * mult))
}

var timeRe = regexp.MustCompile(`(?i)<time[^>]*\sdatetime="([^"]+)"`)

func TelegramPostTime(content string) int64 {
	m := timeRe.FindStringSubmatch(content)
	if m == nil {
		return 0
	}
	t, err := time.Parse(time.RFC3339, htmlAttr(m[1]))
	if err != nil {
		return 0
	}
	return t.Unix()
}

var (
	reactionRe = regexp.MustCompile(`<i class="emoji"[^>]*><b>([^<]+)</b></i>([\d.,KkMm ]*)</span>`)
	viewsRe    = regexp.MustCompile(`tgme_widget_message_views">([^<]*)`)
)

type tgEngagement struct {
	Views     *int64
	Reactions map[string]int64
}

func TelegramPageEngagement(page string) (map[string]tgEngagement, []string) {
	out := map[string]tgEngagement{}
	var order []string
	for _, b := range TelegramPageBlocks(page) {
		r := map[string]int64{}
		for _, m := range reactionRe.FindAllStringSubmatch(b.Content, -1) {
			r[m[1]] += TelegramParseCounter(m[2])
		}
		e := tgEngagement{Reactions: r}
		if m := viewsRe.FindStringSubmatch(b.Content); m != nil {
			v := TelegramParseCounter(m[1])
			e.Views = &v
		}
		out[b.ID] = e
		order = append(order, b.ID)
	}
	return out, order
}

// telegramEngagementSnapshot — активность поста и медианы соседних постов канала.
func telegramEngagementSnapshot(page map[string]tgEngagement, postID string, postedAt int64) jx.M {
	p, ok := page[postID]
	if !ok || p.Views == nil {
		return nil
	}
	var views, rates []float64
	n := 0
	for id, o := range page {
		if id == postID || o.Views == nil {
			continue
		}
		n++
		views = append(views, float64(*o.Views))
		sum := int64(0)
		for _, v := range o.Reactions {
			sum += v
		}
		rates = append(rates, float64(sum)/math.Max(1, float64(*o.Views)))
	}
	var age any
	if postedAt > 0 {
		age = math.Round(float64(now()-postedAt)/360) / 10
	}
	snap := jx.M{"v": 1, "source": "telegram", "at": now(), "age_hours": age, "views": *p.Views, "reactions": p.Reactions, "neighbors": n,
		"neighbors_median_views": nil, "neighbors_median_reaction_rate": nil}
	if m := Median(views); m != nil {
		snap["neighbors_median_views"] = *m
	}
	if m := Median(rates); m != nil {
		snap["neighbors_median_reaction_rate"] = *m
	}
	return snap
}

var (
	tgMediaSplitRe = regexp.MustCompile(`<a class="tgme_widget_message_(?:photo_wrap|video_player|roundvideo_player)\b`)
	videoSrcRe     = regexp.MustCompile(`(?i)<video[^>]*?\ssrc="([^"]+)"`)
	videoPosterRe  = regexp.MustCompile(`(?i)<video[^>]*?\sposter="([^"]+)"`)
	bgImageRe      = regexp.MustCompile(`(?i)background-image:\s*url\(['"]?(https://[^'")\s]+)`)
	linkPreviewRe  = regexp.MustCompile(`(?is)tgme_widget_message_link_preview.*?background-image:\s*url\(['"]?(https://[^'")\s]+)`)
	msgTextRe      = regexp.MustCompile(`(?is)<div class="tgme_widget_message_text[^"]*"[^>]*>(.*?)</div>`)
	videoPlayerRe  = regexp.MustCompile(`tgme_widget_message_(?:round)?video_player`)
)

// TelegramPostMedia — медиа сообщения по плиткам альбома.
func TelegramPostMedia(content string, maxN int) []jx.M {
	locs := tgMediaSplitRe.FindAllStringIndex(content, -1)
	var media []jx.M
	for i, l := range locs {
		end := len(content)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		part := content[l[0]:end]
		if e := strings.Index(part, "</a>"); e >= 0 {
			part = part[:e]
		}
		video := ""
		if m := videoSrcRe.FindStringSubmatch(part); m != nil {
			video = htmlAttr(m[1])
		}
		img := ""
		if m := videoPosterRe.FindStringSubmatch(part); m != nil {
			img = htmlAttr(m[1])
		} else if m := bgImageRe.FindStringSubmatch(part); m != nil {
			img = htmlAttr(m[1])
		}
		if img != "" {
			media = append(media, jx.M{"image": img, "video": video})
		}
		if len(media) >= maxN {
			break
		}
	}
	return media
}

// TelegramPostsFromHTML — посты с медиа (и текстовые, если withText) со страницы канала, свежие первыми.
func TelegramPostsFromHTML(page string, src jx.M, count int, withText bool, minText int) []jx.M {
	channel := strings.TrimLeft(jx.Str(src, "id"), "@")
	eng, _ := TelegramPageEngagement(page)
	blocks := TelegramPageBlocks(page)
	posts := []jx.M{}
	for i := len(blocks) - 1; i >= 0 && len(posts) < count; i-- {
		b := blocks[i]
		text := ""
		if m := msgTextRe.FindStringSubmatch(b.Content); m != nil {
			text = CleanHTMLText(m[1])
		}
		media := TelegramPostMedia(b.Content, 10)
		var imgs []string
		for _, m := range media {
			imgs = append(imgs, jx.Str(m, "image"))
		}
		images := MediaDedupeURLs(imgs)
		video := ""
		if len(media) > 0 {
			video = jx.Str(media[0], "video")
		}
		unavailable := video == "" && videoPlayerRe.MatchString(b.Content)
		textOnly := len(images) == 0 && withText && runeLen(text) >= minText && text != ""
		if len(images) == 0 && !textOnly {
			continue
		}
		t := TelegramPostTime(b.Content)
		gallery := []jx.M{}
		if len(media) > 1 {
			gallery = media
		}
		name := jx.Str(src, "name")
		if name == "" {
			name = SourceDisplayName(PlatformTelegram, channel)
		}
		if text == "" {
			text = EmptyPostText
		}
		main := ""
		if len(images) > 0 {
			main = images[0]
		}
		linkImage := ""
		if m := linkPreviewRe.FindStringSubmatch(b.Content); m != nil {
			linkImage = htmlAttr(m[1])
		}
		posts = append(posts, jx.M{"image": main, "images": images[:min(10, len(images))], "link_image": linkImage, "gallery": gallery, "video": video,
			"text": text, "text_post": textOnly, "source": name, "post_id": b.ID, "posted_at": t, "url": "https://t.me/" + url.PathEscape(channel) + "/" + b.ID,
			"video_unavailable": unavailable, "engagement": telegramEngagementSnapshot(eng, b.ID, t)})
	}
	return posts
}

var subscribersRe = regexp.MustCompile(`(?i)<span class="counter_value">([^<]+)</span>\s*<span class="counter_type">subscribers?</span>`)

type TelegramFacts struct {
	Subscribers int64
	PostTimes   []int64
	PostViews   []*int64 // от новых к старым
}

func TelegramPageFacts(page string) TelegramFacts {
	f := TelegramFacts{}
	if m := subscribersRe.FindStringSubmatch(page); m != nil {
		f.Subscribers = TelegramParseCounter(m[1])
	}
	for _, b := range TelegramPageBlocks(page) {
		if t := TelegramPostTime(b.Content); t > 0 {
			f.PostTimes = append(f.PostTimes, t)
		}
	}
	eng, order := TelegramPageEngagement(page)
	for i := len(order) - 1; i >= 0; i-- {
		f.PostViews = append(f.PostViews, eng[order[i]].Views)
	}
	return f
}

const (
	telegramPreviewPages       = 4
	telegramPreviewBudget      = 10 * time.Second
	telegramPageCacheSeconds   = 600
	telegramTextPostMin        = 80
	telegramTextPostsMax       = 6
)

// TelegramChannelPageCached — страница канала из кэша (10 минут, в памяти) или из сети.
func (a *App) TelegramChannelPageCached(channel string, before int64) string {
	key := "tgpage:" + strings.ToLower(channel) + "|" + strconv.FormatInt(before, 10)
	if s, ok := Mem[string](a.Mem, key); ok {
		return s
	}
	page := a.FetchTelegramChannelPage(channel, before)
	if page != "" {
		a.Mem.Set(key, page, telegramPageCacheSeconds*time.Second)
	}
	return page
}

// TelegramChannelMediaPosts — посты с медиа и текстовые посты канала со страниц t.me/s/ (листание через ?before=).
func (a *App) TelegramChannelMediaPosts(channel string, want, maxPages int) jx.M {
	started := time.Now()
	src := jx.M{"id": channel, "name": SourceDisplayName(PlatformTelegram, channel)}
	posts, textPosts := []jx.M{}, []jx.M{}
	pages, total := 0, 0
	var before int64
	for pages < maxPages && len(posts) < want {
		if pages > 0 && time.Since(started) > telegramPreviewBudget {
			break
		}
		page := a.TelegramChannelPageCached(channel, before)
		blocks := TelegramPageBlocks(page)
		if page == "" || len(blocks) == 0 {
			break
		}
		pages++
		total += len(blocks)
		for _, p := range TelegramPostsFromHTML(page, src, math.MaxInt, true, telegramTextPostMin) {
			if jx.Bool(p, "text_post") {
				textPosts = append(textPosts, p)
			} else {
				posts = append(posts, p)
			}
		}
		oldest := int64(math.MaxInt64)
		for _, b := range blocks {
			if n, _ := strconv.ParseInt(b.ID, 10, 64); n < oldest {
				oldest = n
			}
		}
		if before != 0 && oldest >= before {
			break
		}
		before = oldest
	}
	mediaTotal, textTotal := len(posts), len(textPosts)
	return jx.M{"posts": posts[:min(want, len(posts))], "text_posts": textPosts[:min(telegramTextPostsMax, len(textPosts))], "pages": pages,
		"posts_total": total, "media_total": mediaTotal, "text_total": textTotal}
}

var (
	channelTitleRe = regexp.MustCompile(`(?is)class="tgme_channel_info_header_title"[^>]*>(.*?)</div>`)
	channelDescRe  = regexp.MustCompile(`(?is)class="tgme_channel_info_description"[^>]*>(.*?)</div>`)
	ogImageRe      = regexp.MustCompile(`<meta property="og:image" content="([^"]+)"`)
)

// TelegramChannelInfo — канал по странице t.me/s/; nil — канала нет или он закрыт.
func (a *App) TelegramChannelInfo(channel string, postCount int) jx.M {
	channel = strings.TrimLeft(strings.TrimSpace(channel), "@")
	page := a.TelegramChannelPageCached(channel, 0)
	if page == "" || !strings.Contains(page, `data-post="`) {
		return nil
	}
	title := ""
	if m := channelTitleRe.FindStringSubmatch(page); m != nil {
		title = CleanHTMLText(m[1])
	}
	desc := ""
	if m := channelDescRe.FindStringSubmatch(page); m != nil {
		desc = CleanHTMLText(m[1])
	}
	if title == "" {
		title = channel
	}
	f := TelegramPageFacts(page)
	return jx.M{"id": channel, "title": title, "description": trimRunes(desc, 500),
		"posts":       TelegramPostsFromHTML(page, jx.M{"id": channel, "name": SourceDisplayName(PlatformTelegram, title)}, postCount, false, 120),
		"linked":      TelegramLinkedChannels(page, channel),
		"subscribers": f.Subscribers, "post_times": f.PostTimes, "post_views": f.PostViews}
}

var tmeLinkRe = regexp.MustCompile(`https?://t\.me/(?:s/)?([A-Za-z][A-Za-z0-9_]{4,31})([/?"#'<\s]|$)`)

// TelegramLinkedChannels — каналы, на которые ссылаются посты.
func TelegramLinkedChannels(page, own string) []string {
	found := map[string]string{}
	var order []string
	for _, m := range tmeLinkRe.FindAllStringSubmatch(page, -1) {
		k := strings.ToLower(m[1])
		if k == strings.ToLower(own) || contains(telegramReservedPaths, k) || strings.HasSuffix(k, "bot") {
			continue
		}
		if _, ok := found[k]; !ok {
			order = append(order, k)
		}
		found[k] = m[1]
	}
	out := []string{}
	for _, k := range order {
		out = append(out, found[k])
	}
	return out
}

// ---- ВКонтакте ----

const vkIDTokenURL = "https://id.vk.ru/oauth2/auth"

func (a *App) VKOAuthConfigured() bool { return a.Cfg.VKClientID != "" && a.Cfg.VKRedirectURI != "" }

// vkTokens — токены VK сайта (kv vk_tokens, зашифрованы ключом сервера).
func (a *App) vkTokens(ctx context.Context) jx.M {
	var enc string
	if !a.KVGet(ctx, "vk_tokens", &enc) || enc == "" {
		return jx.M{}
	}
	plain, ok := a.Box.Decrypt(enc, "vk_tokens")
	if !ok {
		plain, ok = a.Box.Decrypt(enc, "")
	}
	if m := jx.Unmarshal([]byte(plain)); ok && m != nil {
		return m
	}
	return jx.M{}
}

func (a *App) SaveVKTokens(ctx context.Context, t jx.M) {
	a.KVSet(ctx, "vk_tokens", a.Box.Encrypt(string(jx.Marshal(t)), "vk_tokens"), 0)
}

func (a *App) vkOAuthPost(params url.Values) (jx.M, error) {
	b, status, err := a.HTTPPost(vkIDTokenURL, []byte(params.Encode()), "", nil, 20*time.Second)
	m := jx.Unmarshal(b)
	if err != nil || m == nil || status < 200 || status >= 300 || jx.Bool(m, "error") {
		msg := jx.Str(m, "error_description")
		if msg == "" {
			msg = jx.Str(m, "error")
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = fmt.Sprintf("Ошибка запроса к VK ID (HTTP %d).", status)
		}
		return nil, Bad("%s", msg)
	}
	return m, nil
}

// VKAccessToken — рабочий токен VK (обновляется по refresh_token) или "".
func (a *App) VKAccessToken(ctx context.Context) string {
	t := a.vkTokens(ctx)
	if jx.Str(t, "access_token") == "" {
		return ""
	}
	if jx.Int(t, "expires_at") > now()+60 {
		return jx.Str(t, "access_token")
	}
	if jx.Str(t, "refresh_token") == "" || a.Cfg.VKClientID == "" || jx.Str(t, "device_id") == "" {
		return ""
	}
	state := randHex(16)
	p := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {jx.Str(t, "refresh_token")}, "client_id": {a.Cfg.VKClientID},
		"device_id": {jx.Str(t, "device_id")}, "state": {state}}
	if a.Cfg.VKServiceToken != "" {
		p.Set("service_token", a.Cfg.VKServiceToken)
	}
	nt, err := a.vkOAuthPost(p)
	if err != nil || jx.Str(nt, "access_token") == "" || jx.Str(nt, "refresh_token") == "" {
		return ""
	}
	if s := jx.Str(nt, "state"); s != "" && s != state {
		return ""
	}
	t = jx.Merge(t, nt)
	t["expires_at"] = now() + max(0, jx.Int(nt, "expires_in"))
	a.SaveVKTokens(ctx, t)
	return jx.Str(t, "access_token")
}

func (a *App) VKIsConnected(ctx context.Context) bool { return a.VKAccessToken(ctx) != "" }

// VKTokenForRead — OAuth-токен, иначе сервисный ключ приложения.
func (a *App) VKTokenForRead(ctx context.Context) string {
	if t := a.VKAccessToken(ctx); t != "" {
		return t
	}
	return a.Cfg.VKServiceToken
}

func (a *App) VKCanRead(ctx context.Context) bool { return a.VKTokenForRead(ctx) != "" }

// VKAPI — запрос к VK API; service — метод читается сервисным ключом.
func (a *App) VKAPI(ctx context.Context, method string, params url.Values, service bool) (jx.M, error) {
	token := a.VKAccessToken(ctx)
	if service {
		token = a.VKTokenForRead(ctx)
	}
	if token == "" {
		return jx.M{}, nil
	}
	params.Set("access_token", token)
	if params.Get("v") == "" {
		params.Set("v", "5.199")
	}
	b, status, err := a.HTTPPost("https://api.vk.com/method/"+url.PathEscape(method), []byte(params.Encode()), "", nil, 15*time.Second)
	var data struct {
		Response json.RawMessage `json:"response"`
		Error    *struct {
			Msg string `json:"error_msg"`
		} `json:"error"`
	}
	if err != nil || status < 200 || status >= 300 || json.Unmarshal(b, &data) != nil {
		return nil, Bad("Не удалось получить ответ VK API.")
	}
	if data.Error != nil {
		return nil, Bad("%s", data.Error.Msg)
	}
	var m jx.M
	if json.Unmarshal(data.Response, &m) != nil {
		// groups.getById в старых версиях отдаёт список
		var list []any
		if json.Unmarshal(data.Response, &list) == nil {
			return jx.M{"groups": list}, nil
		}
		return jx.M{}, nil
	}
	return m, nil
}

func (a *App) VKWallItems(ctx context.Context, domain string, count, offset int) []jx.M {
	p := url.Values{"domain": {domain}, "count": {strconv.Itoa(max(1, min(100, count)))}}
	if offset > 0 {
		p.Set("offset", strconv.Itoa(offset))
	}
	r, err := a.VKAPI(ctx, "wall.get", p, true)
	if err != nil {
		return nil
	}
	return jx.Maps(r, "items")
}

// VKWallPost — запись стены → пост с картинками или nil.
func VKWallPost(post jx.M, sourceName, domain string, withText bool, minText int) jx.M {
	var images []string
	hasVideo := false
	for _, a := range jx.Maps(post, "attachments") {
		switch jx.Str(a, "type") {
		case "photo":
			sizes := jx.Maps(jx.Map(a, "photo"), "sizes")
			sort.SliceStable(sizes, func(i, j int) bool {
				return jx.Int(sizes[i], "width")*jx.Int(sizes[i], "height") > jx.Int(sizes[j], "width")*jx.Int(sizes[j], "height")
			})
			if len(sizes) > 0 && strings.HasPrefix(jx.Str(sizes[0], "url"), "https://") {
				images = append(images, jx.Str(sizes[0], "url"))
			}
		case "video":
			hasVideo = true
		}
	}
	text := CleanHTMLText(jx.Str(post, "text"))
	textOnly := len(images) == 0 && !hasVideo && withText && runeLen(text) >= minText
	if len(images) == 0 && !textOnly {
		return nil
	}
	owner, id := jx.Str(post, "owner_id"), jx.Str(post, "id")
	link := "https://vk.com/" + url.PathEscape(domain)
	if owner != "" && id != "" {
		link = "https://vk.com/wall" + owner + "_" + id
	}
	if text == "" {
		text = EmptyPostText
	}
	images = uniqueStrings(images)
	main := ""
	if len(images) > 0 {
		main = images[0]
	}
	var views any
	if v := jx.Map(post, "views"); v != nil && jx.Has(v, "count") {
		views = jx.Int(v, "count")
	}
	return jx.M{"image": main, "images": images, "text": text, "text_post": textOnly, "source": sourceName, "post_id": id, "posted_at": jx.Int(post, "date"),
		"likes": jx.Int(jx.Map(post, "likes"), "count"), "reposts": jx.Int(jx.Map(post, "reposts"), "count"), "comments": jx.Int(jx.Map(post, "comments"), "count"),
		"views": views, "url": link}
}

func (a *App) BuildVKPreviewPosts(ctx context.Context, src jx.M, count int, withText bool, minText int) []jx.M {
	domain := strings.TrimSpace(jx.Str(src, "id"))
	if !IsValidVKDomain(domain) {
		return []jx.M{}
	}
	name := jx.Str(src, "name")
	if name == "" {
		name = SourceDisplayName(PlatformVK, domain)
	}
	posts := []jx.M{}
	for _, it := range a.VKWallItems(ctx, domain, count, 0) {
		if p := VKWallPost(it, name, domain, withText, minText); p != nil {
			posts = append(posts, p)
		}
		if len(posts) >= count {
			break
		}
	}
	return posts
}

// VKSearchGroups — открытые сообщества по запросу, крупные первыми.
func (a *App) VKSearchGroups(ctx context.Context, query string, count int) ([]jx.M, error) {
	r, err := a.VKAPI(ctx, "groups.search", url.Values{"q": {query}, "count": {strconv.Itoa(max(1, min(100, count)))}, "sort": {"0"}}, false)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range jx.Maps(r, "items") {
		if jx.Int(g, "is_closed") == 0 && jx.Str(g, "type") != "event" {
			ids = append(ids, jx.Str(g, "id"))
		}
	}
	if len(ids) == 0 {
		return []jx.M{}, nil
	}
	d, err := a.VKAPI(ctx, "groups.getById", url.Values{"group_ids": {strings.Join(ids, ",")}, "fields": {"members_count,description,country"}}, true)
	if err != nil {
		return nil, err
	}
	var groups []jx.M
	for _, g := range jx.Maps(d, "groups") {
		name := jx.Str(g, "screen_name")
		if name == "" {
			name = "club" + jx.Str(g, "id")
		}
		if !IsValidVKDomain(name) {
			continue
		}
		title := CleanHTMLText(jx.Str(g, "name"))
		if title == "" {
			title = name
		}
		groups = append(groups, jx.M{"id": name, "title": title, "description": trimRunes(CleanHTMLText(jx.Str(g, "description")), 500),
			"subscribers": jx.Int(g, "members_count"), "country": g["country"]})
	}
	sort.SliceStable(groups, func(i, j int) bool { return jx.Int(groups[i], "subscribers") > jx.Int(groups[j], "subscribers") })
	return groups, nil
}

// BuildSourcePreviewPosts — превью источника любой площадки.
func (u *U) BuildSourcePreviewPosts(src jx.M, count int) []jx.M {
	switch jx.Str(src, "platform") {
	case PlatformTelegram:
		if page := u.FetchTelegramChannelPage(strings.TrimLeft(jx.Str(src, "id"), "@"), 0); page != "" {
			return TelegramPostsFromHTML(page, src, count, false, 120)
		}
	case PlatformVK:
		return u.BuildVKPreviewPosts(u.Ctx, src, count, false, 120)
	case PlatformRSS:
		return u.RSSPreviewPosts(src, count)
	case PlatformOK:
		return u.OKPreviewPosts(src, count)
	case PlatformTikTok:
		return u.TikTokPreviewPosts(src, count)
	case PlatformDvach:
		return u.BuildDvachPreviewPosts(src, count)
	}
	return []jx.M{}
}

// ---- Активность: свежесть постов, частота, заморозка ----

func IsPostOutdated(postedAt, at int64, maxDays int) bool {
	if at == 0 {
		at = now()
	}
	return postedAt > 0 && at-postedAt > int64(maxDays)*config.DaySeconds
}

// SourceActivity — последний пост и постов в неделю; nil — дат нет.
func SourceActivity(times []int64, at int64) jx.M {
	if at == 0 {
		at = now()
	}
	var ok []int64
	for _, t := range times {
		if t > 0 && t <= at+config.DaySeconds {
			ok = append(ok, t)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	mn, mx := ok[0], ok[0]
	for _, t := range ok {
		mn, mx = min(mn, t), max(mx, t)
	}
	perWeek := math.Round(float64(len(ok))/math.Max(1, float64(at-mn)/config.DaySeconds)*7*10) / 10
	return jx.M{"last_post": mx, "per_week": perWeek, "checked_at": at}
}

func SourceInactivityReason(activity jx.M, at int64) string {
	if at == 0 {
		at = now()
	}
	if activity == nil {
		return "нет постов с датой"
	}
	days := (at - jx.Int(activity, "last_post")) / config.DaySeconds
	if days > config.SourceStaleDays {
		return fmt.Sprintf("последний пост %d дн назад", days)
	}
	if pw := jx.Float(activity, "per_week"); pw < config.SourceMinPostsPerWeek {
		return fmt.Sprintf("редко публикует: %s постов в неделю", trimFloat(pw))
	}
	return ""
}

func DescribeSourceActivity(activity jx.M) string {
	if activity == nil {
		return "дат постов нет"
	}
	h := (now() - jx.Int(activity, "last_post")) / 3600
	when := "меньше часа назад"
	if h >= 1 && h < 48 {
		when = fmt.Sprintf("%d ч назад", h)
	} else if h >= 48 {
		when = fmt.Sprintf("%d дн назад", h/24)
	}
	return "последний пост " + when + ", ~" + trimFloat(jx.Float(activity, "per_week")) + " в неделю"
}

func IsSourceFrozen(e jx.M, at int64) bool {
	if at == 0 {
		at = now()
	}
	return jx.Int(e, "frozen_until") > at
}

// ApplySourceActivity — заморозить неактивный / разморозить оживший: (запись, frozen|still_frozen|revived|"").
func ApplySourceActivity(entry jx.M, activity jx.M, at int64, extraReason string) (jx.M, string) {
	if at == 0 {
		at = now()
	}
	e := jx.Copy(entry)
	_, was := e["frozen_until"]
	if activity != nil {
		e["activity"] = activity
	}
	reason := extraReason
	if reason == "" {
		reason = SourceInactivityReason(activity, at)
	}
	if reason != "" {
		e["frozen_until"] = at + config.SourceFrozenRecheckDays*config.DaySeconds
		e["frozen_reason"] = reason
		if was {
			return e, "still_frozen"
		}
		return e, "frozen"
	}
	delete(e, "frozen_until")
	delete(e, "frozen_reason")
	if was {
		return e, "revived"
	}
	return e, ""
}

// ---- Качество: аудитория и доля просмотров ----

func qualityCurve(x float64, pts [][2]float64) float64 {
	if x <= pts[0][0] {
		return pts[0][1]
	}
	for i := 1; i < len(pts); i++ {
		if x <= pts[i][0] {
			return pts[i-1][1] + (pts[i][1]-pts[i-1][1])*(x-pts[i-1][0])/(pts[i][0]-pts[i-1][0])
		}
	}
	return pts[len(pts)-1][1]
}

func SourceAudienceScore(subs int64) *float64 {
	if subs <= 0 {
		return nil
	}
	v := math.Round(qualityCurve(math.Log10(float64(subs)), [][2]float64{{2.5, 0}, {3.0, 30}, {4.0, 65}, {5.0, 90}, {6.0, 100}})*10) / 10
	return &v
}

// SourceViewRate — медиана просмотров свежих постов ÷ подписчики (два самых новых пропускаются).
func SourceViewRate(subs int64, views []*int64) *float64 {
	var v []float64
	for _, x := range views {
		if x != nil && *x >= 0 {
			v = append(v, float64(*x))
		}
	}
	if subs < config.SourceMinSubscribersForRate {
		return nil
	}
	if len(v) >= 7 {
		v = v[2:]
	}
	if len(v) > 12 {
		v = v[:12]
	}
	if len(v) < 4 {
		return nil
	}
	r := math.Round(*Median(v)/float64(subs)*10000) / 10000
	return &r
}

func SourceViewRateScore(rate *float64, subs int64) *float64 {
	if rate == nil {
		return nil
	}
	scale := 1.0
	if subs >= 100000 {
		scale = 0.6
	}
	pts := [][2]float64{{0.015 * scale, 0}, {0.05 * scale, 20}, {0.10 * scale, 45}, {0.25 * scale, 80}, {0.45 * scale, 100}}
	v := math.Round(qualityCurve(*rate, pts)*10) / 10
	return &v
}

func SourceDeadAudienceReason(subs int64, rate *float64) string {
	if rate == nil || *rate >= config.SourceDeadViewRate {
		return ""
	}
	return fmt.Sprintf("мёртвая аудитория: посты видят %.1f%% из %s подписчиков", *rate*100, FormatSubscribers(subs))
}

// SourceQualityScore — итог: по теме + аудитория + доля просмотров (неизвестное не штрафует).
func SourceQualityScore(content float64, subs int64, rate *float64) jx.M {
	aud := SourceAudienceScore(subs)
	rs := SourceViewRateScore(rate, subs)
	weighted, weight := content*config.SourceWeightContent, config.SourceWeightContent
	if aud != nil {
		weighted += *aud * config.SourceWeightAudience
		weight += config.SourceWeightAudience
	}
	if rs != nil {
		weighted += *rs * config.SourceWeightViewRate
		weight += config.SourceWeightViewRate
	}
	score := weighted / weight
	if content < config.SourceMinContentScore {
		score = math.Min(content, config.SourceSuggestScore-1)
	}
	out := jx.M{"score": int(math.Round(score)), "content": int(math.Round(content)), "audience": nil, "view_rate_score": nil}
	if aud != nil {
		out["audience"] = *aud
	}
	if rs != nil {
		out["view_rate_score"] = *rs
	}
	return out
}

func DescribeSourceAudience(subs int64, rate *float64, q jx.M) string {
	var parts []string
	if subs > 0 {
		p := FormatSubscribers(subs) + " подписчиков"
		if jx.Has(q, "audience") {
			p += fmt.Sprintf(" (оценка %d)", int(math.Round(jx.Float(q, "audience"))))
		}
		parts = append(parts, p)
	}
	if rate != nil {
		p := fmt.Sprintf("просмотры %d%% подписчиков", int(math.Round(*rate*100)))
		if jx.Has(q, "view_rate_score") {
			p += fmt.Sprintf(" (оценка %d)", int(math.Round(jx.Float(q, "view_rate_score"))))
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "аудитория неизвестна"
	}
	return strings.Join(parts, ", ")
}

// ---- Отзывы пользователей об источниках ----

type SourceFeedback struct {
	Likes, Dislikes, Votes int
	Score                  float64
}

// SourceFeedbackStats — лайки и дизлайки всех пользователей по источникам темы (жалоба «ошибка подбора» — дизлайк).
func (a *App) SourceFeedbackStats(ctx context.Context, topic string) map[string]*SourceFeedback {
	stats := map[string]*SourceFeedback{}
	rows, err := a.DB.Pool.Query(ctx, `SELECT collection, data->>'source_key', data->>'topic', COALESCE(data->>'complaint_reason', '')
		FROM history_items WHERE collection IN ('liked', 'disliked', 'complaints')`)
	if err != nil {
		return stats
	}
	defer rows.Close()
	for rows.Next() {
		var coll, reason string
		var key, t *string
		if rows.Scan(&coll, &key, &t, &reason) != nil || key == nil || *key == "" || strings.HasPrefix(*key, "own:") {
			continue
		}
		if coll == "complaints" && reason != "match_error" {
			continue
		}
		tt := ""
		if t != nil {
			tt = *t
		}
		if !a.TopicsAreEquivalent(tt, topic) {
			continue
		}
		s := stats[*key]
		if s == nil {
			s = &SourceFeedback{}
			stats[*key] = s
		}
		if coll == "liked" {
			s.Likes++
		} else {
			s.Dislikes++
		}
	}
	for _, s := range stats {
		s.Votes = s.Likes + s.Dislikes
		s.Score = float64(s.Likes+1) / float64(s.Likes+s.Dislikes+2)
	}
	return stats
}

// SourceUserMutes — источники, которые пользователь не хочет по теме (доля лайков ниже порога при достаточных оценках).
func (a *App) SourceUserMutes(h History, topic string) map[string]jx.M {
	counts := map[string]map[string]int{}
	for coll, field := range map[string]string{"liked": "likes", "disliked": "dislikes", "complaints": "complaints"} {
		for _, r := range h[coll] {
			key := jx.Str(r, "source_key")
			if key == "" || strings.HasPrefix(key, "own:") || !a.TopicSame(jx.Str(r, "topic"), topic) {
				continue
			}
			if coll == "complaints" && jx.Str(r, "complaint_reason") != "match_error" {
				continue
			}
			if counts[key] == nil {
				counts[key] = map[string]int{}
			}
			counts[key][field]++
		}
	}
	out := map[string]jx.M{}
	for key, c := range counts {
		ratings := c["likes"] + c["dislikes"] + c["complaints"]
		if ratings < config.SourceMuteMinRatings {
			continue
		}
		share := float64(c["likes"]) / math.Max(1, float64(c["likes"]+c["dislikes"]+config.SourceMuteComplaintWeight*c["complaints"]))
		if share < config.SourceMuteLikeShare {
			out[key] = jx.M{"likes": c["likes"], "ratings": ratings, "share": share}
		}
	}
	return out
}

// CombinedSourceRates — доля прошедших, смешанная с долей лайков; стабильно плохой источник — 0.
func (a *App) CombinedSourceRates(ctx context.Context, pass map[string]float64, topic string) map[string]float64 {
	for key, s := range a.SourceFeedbackStats(ctx, topic) {
		if s.Votes >= config.SourceFeedbackBanVotes && s.Score < config.SourceFeedbackBanScore {
			pass[key] = 0
		} else if s.Votes >= config.SourceFeedbackMinVotes {
			if v, ok := pass[key]; ok {
				pass[key] = (v + s.Score) / 2
			} else {
				pass[key] = s.Score
			}
		}
	}
	return pass
}

// CatalogEntryMatchesTopic — подходит ли источник теме: без topics — всем; strict — только сама тема и синонимы.
func (a *App) CatalogEntryMatchesTopic(entry jx.M, topic string, strict bool) bool {
	if q := strings.TrimSpace(jx.Str(entry, "query")); q != "" && strings.ToLower(q) != strings.ToLower(TopicQueryOf(topic)) {
		return false
	}
	topic = TopicBase(topic)
	topics := jx.Strs(entry, "topics")
	if len(topics) == 0 {
		return true
	}
	for _, t := range topics {
		if strict && a.TopicSame(topic, t) || !strict && a.TopicsAreEquivalent(t, topic) {
			return true
		}
	}
	return false
}

// QueryOwnSourcesCount — своих источников у пары «тема + уточнение».
func QueryOwnSourcesCount(d *SourceDB, key string) int {
	q := strings.ToLower(TopicQueryOf(key))
	if q == "" {
		return 0
	}
	n := 0
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if strings.ToLower(strings.TrimSpace(jx.Str(e, "query"))) == q {
				n++
			}
		}
	}
	return n
}

// SaveCatalogEntryUpdates — записи каталога [«вид:id» → запись] одной операцией.
func (a *App) SaveCatalogEntryUpdates(ctx context.Context, updates map[string]jx.M) {
	if len(updates) == 0 {
		return
	}
	a.UpdateCatalog(ctx, func(d *SourceDB) bool {
		changed := false
		for _, c := range CatalogKinds {
			l := d.List(c.Kind)
			for i, e := range *l {
				if n, ok := updates[c.Kind+":"+strings.ToLower(jx.Str(e, "id"))]; ok {
					(*l)[i] = n
					changed = true
				}
			}
		}
		return changed
	})
}

// SourcePreviewImages — до 4 картинок самых свежих постов (карточка на вкладке «Источники»).
func SourcePreviewImages(posts []jx.M) []string {
	p := append([]jx.M{}, posts...)
	sort.SliceStable(p, func(i, j int) bool { return jx.Int(p[i], "posted_at") > jx.Int(p[j], "posted_at") })
	var imgs []string
	for _, x := range p {
		if s := jx.Str(x, "image"); strings.HasPrefix(s, "https://") {
			imgs = append(imgs, s)
		}
	}
	imgs = uniqueStrings(imgs)
	return imgs[:min(4, len(imgs))]
}

// PopularTopics — самые частые темы пользователей.
func (a *App) PopularTopics(ctx context.Context, limit int) []string {
	rows, err := a.DB.Pool.Query(ctx, `SELECT settings->>'topic' AS t, count(*) FROM users WHERE settings->>'topic' IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		var n int
		if rows.Scan(&t, &n) == nil && NormalizeTopic(t) != "" {
			out = append(out, NormalizeTopic(t))
		}
	}
	return out
}

// ---- Тематические теги досок Двача ----

const DvachAdultTag = "18+"

var dvachBoardTags = map[string][]string{
	"b": {"юмор", "мемы", "приколы", "бред", "треш"}, "a": {"аниме", "японская культура", "манга"}, "bi": {"велосипеды", "спорт", "транспорт"},
	"biz": {"бизнес", "деньги", "финансы", "предпринимательство"}, "bo": {"книги", "литература", "чтение"}, "c": {"комиксы", "мультфильмы", "мультики", "анимация"},
	"dev": {"девушки", "общение", "отношения"}, "em": {"путешествия", "эмиграция", "другие страны"}, "ew": {"апокалипсис", "конец света", "выживание"},
	"fa": {"мода", "стиль", "одежда", "красота"}, "fiz": {"спорт", "фитнес", "тренировки", "бодибилдинг", "здоровье"}, "fl": {"языки", "иностранные языки", "образование"},
	"ftb": {"футбол", "спорт"}, "hh": {"хип-хоп", "рэп", "музыка"}, "hi": {"история", "наука"}, "hw": {"железо", "компьютеры", "техника", "гаджеты"},
	"me": {"медицина", "здоровье"}, "mg": {"магия", "эзотерика", "мистика"}, "mlp": {"my little pony", "пони", "мультфильмы", "фэндом"},
	"mo": {"мотоциклы", "байки", "транспорт"}, "ne": {"животные", "природа", "кошки", "собаки", "питомцы", "котики"}, "ph": {"философия"},
	"po": {"политика", "новости"}, "pr": {"программирование", "it", "айти", "разработка"}, "psy": {"психология", "психиатрия", "психическое здоровье"},
	"ra": {"радиотехника", "электроника", "техника"}, "re": {"религия"}, "s": {"программы", "софт", "компьютеры", "it"},
	"sf": {"фантастика", "научная фантастика", "книги", "кино"}, "sci": {"наука", "научпоп"}, "sn": {"паранормальное", "мистика", "крипота", "ужасы"},
	"sp": {"спорт"}, "spc": {"космос", "астрономия", "наука"}, "tr": {"транспорт", "авиация", "самолёты", "поезда"}, "tv": {"сериалы", "кино", "тв"},
	"un": {"образование", "учёба", "университет"}, "w": {"оружие"}, "wh": {"warhammer", "настольные игры", "миниатюры"},
	"wm": {"военная техника", "армия", "оружие", "война"}, "di": {"еда", "кулинария", "готовка"}, "de": {"дизайн", "графика"},
	"diy": {"хобби", "рукоделие", "своими руками", "мастерская"}, "dom": {"дом", "ремонт", "строительство", "интерьер"}, "izd": {"творчество", "литература", "графомания"},
	"mus": {"музыка", "музыканты", "музыкальные инструменты"}, "pa": {"живопись", "рисование", "искусство", "арт"}, "p": {"фотография", "фото"},
	"wp": {"обои", "красивые картинки", "пейзажи", "арт"}, "td": {"3d", "трёхмерная графика", "графика"}, "bg": {"настольные игры", "игры"},
	"cg": {"консоли", "видеоигры", "игры"}, "mc": {"minecraft", "майнкрафт", "видеоигры", "игры"}, "mmo": {"mmo", "онлайн-игры", "видеоигры", "игры"},
	"tes": {"the elder scrolls", "skyrim", "видеоигры", "игры"}, "vg": {"видеоигры", "игры", "гейминг"}, "wr": {"ролевые игры", "текстовые игры", "игры"},
	"aa": {"аниме", "арт", "аниме-арт"}, "fd": {"фэндом", "аниме"}, "ja": {"японская культура", "япония", "аниме"}, "ma": {"манга", "аниме", "комиксы"},
	"to": {"touhou", "аниме", "видеоигры"}, "d": {"обсуждение двача"}, "fag": {"18+", "лгбт"}, "soc": {"общение", "знакомства"}, "r": {"просьбы"},
	"gg": {"18+", "девушки", "эротика", "фап"}, "sex": {"18+", "секс", "отношения", "фап", "эротика"}, "web": {"веб-разработка", "сайты", "it"},
	"gd": {"геймдев", "разработка игр", "видеоигры"}, "mov": {"кино", "фильмы"}, "rf": {"общение"}, "br": {"барахолка", "покупки"}, "wrk": {"работа", "карьера"},
	"mu": {"музыка"}, "trv": {"путешествия", "туризм", "отдых"}, "au": {"автомобили", "машины", "авто", "транспорт"}, "asylum": {"общение"},
	"fs": {"фэнтези", "книги"}, "hc": {"18+", "порно"}, "mobi": {"смартфоны", "мобильные приложения", "гаджеты"}, "out": {"активный отдых", "туризм", "походы", "природа"},
	"cc": {"криптовалюта", "биткоин", "финансы"}, "media": {"анимация", "видео", "медиа"}, "zog": {"теории заговора", "конспирология"},
	"jsf": {"японская мода", "мода", "стиль"}, "sw": {"звёздные войны", "star wars", "кино", "фантастика"}, "law": {"право", "закон", "юриспруденция"},
	"m": {"меха", "роботы", "аниме"}, "wow": {"world of warcraft", "видеоигры", "игры"}, "cute": {"милое", "милота", "котики", "животные"},
	"se": {"самообразование", "образование", "саморазвитие"}, "alco": {"18+", "алкоголь", "напитки"}, "brg": {"браузерные игры", "игры"}, "asmr": {"asmr", "релакс"},
	"dr": {"дневники", "личное"}, "socionics": {"соционика", "психология"}, "fi": {"рыбалка", "хобби", "природа"}, "vape": {"18+", "вейп", "курение"},
	"gsg": {"стратегии", "grand strategy", "видеоигры"}, "kpop": {"k-pop", "кпоп", "музыка", "корея"}, "news": {"новости", "политика"}, "v": {"видеоигры", "игры", "гейминг"},
	"o": {"рисование", "арт", "творчество"}, "2d": {"аниме", "общение"}, "ch": {"чаты", "общение"}, "int": {"международное общение", "иностранцы"},
	"math": {"математика", "наука"}, "es": {"бесконечное лето", "визуальные новеллы", "аниме"}, "hry": {"ютуб", "блогеры", "интернет-культура"},
	"whn": {"warhammer", "настольные игры"}, "ai": {"искусственный интеллект", "нейросети", "it", "технологии"}, "gacha": {"гача-игры", "мобильные игры", "видеоигры", "аниме"},
	"man": {"мужское", "саморазвитие", "отношения"},
}

func DvachBoardTags(board string) []string { return dvachBoardTags[board] }

// DvachBoardTagMap — все теги досок (для подбора досок по теме).
func DvachBoardTagMap() map[string][]string { return dvachBoardTags }
