package posta

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"posta/internal/jx"
)

// ---- Одноклассники: открытые группы по мобильной странице, без входа и ключей ----

const (
	OKPostsPerGroup = 20
	okNeedGroup     = "Нужна ссылка на группу Одноклассников: ok.ru/group/номер"
)

var okMonths = map[string]time.Month{"янв": 1, "фев": 2, "мар": 3, "апр": 4, "май": 5, "мая": 5, "июн": 6, "июл": 7, "авг": 8, "сен": 9, "окт": 10, "ноя": 11, "дек": 12}

var okGroupRe = regexp.MustCompile(`^[0-9]{10,20}$`)

func IsValidOKGroup(id string) bool { return okGroupRe.MatchString(id) }

func okGroupURL(id string) string {
	return "https://m.ok.ru/dk?st.cmd=altGroupMain&st.groupId=" + url.QueryEscape(id)
}

var (
	okPathGroupRe  = regexp.MustCompile(`^/group/?([0-9]{10,20})(?:/|$)`)
	okQueryGroupRe = regexp.MustCompile(`[?&]st\.groupId=([0-9]{10,20})`)
	okNamePathRe   = regexp.MustCompile(`^/[A-Za-z0-9._-]{2,64}/?$`)
	okPageGroupRe  = regexp.MustCompile(`groupId,([0-9]{10,20})|st\.groupId=([0-9]{10,20})`)
)

// OKGroupID — id группы из ссылки.
func (a *App) OKGroupID(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (strings.ToLower(u.Host) != "ok.ru" && strings.ToLower(u.Host) != "m.ok.ru") {
		return "", Bad(okNeedGroup)
	}
	if m := okPathGroupRe.FindStringSubmatch(u.Path); m != nil {
		return m[1], nil
	}
	if m := okQueryGroupRe.FindStringSubmatch("?" + u.RawQuery); m != nil {
		return m[1], nil
	}
	if !okNamePathRe.MatchString(u.Path) {
		return "", Bad(okNeedGroup)
	}
	r := a.RSSGet("https://ok.ru"+strings.TrimRight(u.Path, "/"), 3<<20, 15*time.Second, BrowserUA)
	m := okPageGroupRe.FindStringSubmatch(string(r.Bytes))
	if !r.OK || m == nil {
		return "", Bad(okNeedGroup)
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}

func (a *App) OKGroupHTML(id string) (string, error) {
	r := a.RSSGet(okGroupURL(id), 2<<20, 15*time.Second, BrowserUA)
	if !r.OK {
		msg := "Страница группы Одноклассников не загрузилась"
		if r.Error != "" {
			msg += ": " + r.Error
		}
		return "", Bad("%s", msg)
	}
	return string(r.Bytes), nil
}

var (
	ogTitleRe  = regexp.MustCompile(`property="og:title" content="([^"]+)"`)
	titleTagRe = regexp.MustCompile(`<title>([^<]+)</title>`)
	okSuffixRe = regexp.MustCompile(`(?i)\s*\|\s*OK\.RU\s*$`)
	okImgRe    = regexp.MustCompile(`<img class="[^"]*\bimage_inner_img\b[^"]*"[^>]*?\ssrc="([^"]+)"`)
	okCDNRe    = regexp.MustCompile(`(?i)^https://(?:i\.okcdn\.ru/|[a-z0-9-]+\.mycdn\.me/)`)
	okTopicRe  = regexp.MustCompile(`topicId&quot;:&quot;(\d+)`)
	okTextRe   = regexp.MustCompile(`(?s)topic-text_content[^"]*"[^>]*>(.*?)(?:<a [^>]*topic-more|<div class="widget-list)`)
	okDateRe   = regexp.MustCompile(`class="tstmp feed_descr">([^<]*)<`)
	okCountRe  = regexp.MustCompile(`widget_action_count">\s*([^<]*)<`)
	hhmmRe     = regexp.MustCompile(`(\d{1,2}):(\d{2})`)
	onlyHHMMRe = regexp.MustCompile(`^\d{1,2}:\d{2}$`)
	dayMonthRe = regexp.MustCompile(`(\d{1,2})\s+([а-яё]+)\.?(?:\s+(\d{4}))?`)
)

func OKTitleFromHTML(page, id string) string {
	raw := ""
	if m := ogTitleRe.FindStringSubmatch(page); m != nil {
		raw = m[1]
	} else if m := titleTagRe.FindStringSubmatch(page); m != nil {
		raw = m[1]
	}
	t := strings.TrimSpace(okSuffixRe.ReplaceAllString(CleanHTMLText(htmlAttr(raw)), ""))
	if t == "" {
		t = id
	}
	return trimRunes(t, 120)
}

func (a *App) OKGroupTitle(id string) string {
	page, err := a.OKGroupHTML(id)
	if err != nil {
		return id
	}
	return OKTitleFromHTML(page, id)
}

func okCardImages(card string) []string {
	var out []string
	for _, m := range okImgRe.FindAllStringSubmatch(card, -1) {
		u := htmlAttr(m[1])
		if strings.HasPrefix(strings.ToLower(u), "http://") {
			u = "https://" + u[7:]
		}
		if okCDNRe.MatchString(u) {
			out = append(out, u)
		}
	}
	return uniqueStrings(out)
}

// OKParseDate — «сегодня 14:05», «вчера», «12 окт», «12 окт 2025 в 14:05», «14:05»; не разобрал — 0.
func OKParseDate(raw string, at time.Time) int64 {
	s := strings.ToLower(strings.TrimSpace(CleanHTMLText(raw)))
	h, mi := 0, 0
	if m := hhmmRe.FindStringSubmatch(s); m != nil {
		h, mi = int(jx.ToInt(m[1])), int(jx.ToInt(m[2]))
	}
	today := at.In(time.Local)
	var day time.Time
	switch {
	case strings.Contains(s, "сегодня") || onlyHHMMRe.MatchString(s):
		day = today
	case strings.Contains(s, "вчера"):
		day = today.AddDate(0, 0, -1)
	default:
		m := dayMonthRe.FindStringSubmatch(s)
		if m == nil {
			return 0
		}
		mon, ok := okMonths[trimRunes(m[2], 3)]
		if !ok {
			return 0
		}
		year := today.Year()
		if m[3] != "" {
			year = int(jx.ToInt(m[3]))
		}
		day = time.Date(year, mon, int(jx.ToInt(m[1])), 0, 0, 0, 0, time.Local)
		if m[3] == "" && day.After(today.AddDate(0, 0, 1)) {
			day = day.AddDate(-1, 0, 0)
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, time.Local).Unix()
}

func okCardCounts(card string) jx.M {
	out := jx.M{"likes": nil, "comments": nil, "reshares": nil}
	for _, seg := range strings.Split(card, "widget-list_i ") {
		key := ""
		switch {
		case strings.HasPrefix(seg, "__like"):
			key = "likes"
		case strings.HasPrefix(seg, "__comment"):
			key = "comments"
		case strings.HasPrefix(seg, "__reshare"):
			key = "reshares"
		}
		if key == "" {
			continue
		}
		out[key] = int64(0)
		if m := okCountRe.FindStringSubmatch(seg); m != nil {
			out[key] = TelegramParseCounter(m[1])
		}
	}
	return out
}

// OKParseFeed — посты из мобильной страницы группы, свежие первыми; без картинки не берутся.
func OKParseFeed(page, id string, count int, name string) []jx.M {
	src := name
	if src == "" {
		src = SourceDisplayName(PlatformOK, id)
	}
	cards := strings.Split(page, `<li class="item it feed-card`)
	posts := []jx.M{}
	for _, card := range cards[min(1, len(cards)):] {
		t := okTopicRe.FindStringSubmatch(card)
		if t == nil {
			continue
		}
		images := okCardImages(card)
		if len(images) == 0 {
			continue
		}
		text := ""
		if m := okTextRe.FindStringSubmatch(card); m != nil {
			text = CleanHTMLText(m[1])
		}
		if text == "" {
			text = EmptyPostText
		}
		var date int64
		if m := okDateRe.FindStringSubmatch(card); m != nil {
			date = OKParseDate(m[1], time.Now())
		}
		p := jx.M{"image": images[0], "images": images, "gallery": []any{}, "video": "", "video_unavailable": false, "engagement": nil,
			"text": trimRunes(text, 1000), "source": src, "post_id": t[1], "posted_at": date, "url": "https://ok.ru/group/" + id + "/topic/" + t[1]}
		for k, v := range okCardCounts(card) {
			p[k] = v
		}
		posts = append(posts, p)
	}
	sort.SliceStable(posts, func(i, j int) bool { return jx.Int(posts[i], "posted_at") > jx.Int(posts[j], "posted_at") })
	return posts[:min(len(posts), max(0, count))]
}

func (u *U) OKPreviewPosts(src jx.M, count int) []jx.M {
	p, err := u.OKFetchPosts(jx.Str(src, "id"), count, jx.Str(src, "name"))
	if err != nil {
		return []jx.M{}
	}
	return p
}

func (a *App) OKFetchPosts(id string, count int, name string) ([]jx.M, error) {
	if !IsValidOKGroup(id) {
		return []jx.M{}, nil
	}
	page, err := a.OKGroupHTML(id)
	if err != nil {
		return nil, err
	}
	return OKParseFeed(page, id, count, name), nil
}

// ---- TikTok: ролики аккаунта через yt-dlp; обложки кэшируются сразу (адреса протухают) ----

const (
	TikTokPostsPerAccount = 12
	tiktokMaxSeconds      = 180
	tiktokNeedAccount     = "Нужна ссылка на аккаунт TikTok: tiktok.com/@имя"
)

var (
	tiktokUserRe     = regexp.MustCompile(`^[A-Za-z0-9_.]{2,24}$`)
	tiktokAtRe       = regexp.MustCompile(`^@([A-Za-z0-9_.]{2,24})$`)
	tiktokPathUserRe = regexp.MustCompile(`^/@([A-Za-z0-9_.]{2,24})(?:/|$)`)
	tiktokIDRe       = regexp.MustCompile(`^[0-9]{5,25}$`)
)

func IsValidTikTokUser(u string) bool { return tiktokUserRe.MatchString(u) }

// TikTokUserFromLink — имя аккаунта из ссылки или «@имя» (короткие vm./vt. — через перенаправление).
func (a *App) TikTokUserFromLink(input string) (string, error) {
	input = strings.TrimSpace(input)
	if m := tiktokAtRe.FindStringSubmatch(input); m != nil {
		return m[1], nil
	}
	u, err := url.Parse(input)
	if err != nil || u.Scheme != "https" {
		return "", Bad(tiktokNeedAccount)
	}
	host := strings.ToLower(u.Host)
	if host == "vm.tiktok.com" || host == "vt.tiktok.com" {
		r, err := a.FetchPublic(input, FetchOpts{MaxBytes: 4096})
		if err != nil || !strings.HasPrefix(r.Location, "https://") {
			return "", Bad(tiktokNeedAccount)
		}
		return a.TikTokUserFromLink(r.Location)
	}
	if host != "tiktok.com" && host != "www.tiktok.com" && host != "m.tiktok.com" {
		return "", Bad(tiktokNeedAccount)
	}
	m := tiktokPathUserRe.FindStringSubmatch(u.Path)
	if m == nil {
		return "", Bad(tiktokNeedAccount)
	}
	return m[1], nil
}

func tiktokBestThumbnail(thumbs []jx.M) string {
	best, size := "", int64(-1)
	for _, t := range thumbs {
		u := jx.Str(t, "url")
		if !strings.HasPrefix(u, "https://") {
			continue
		}
		if s := jx.Int(t, "width") * jx.Int(t, "height"); s >= size {
			best, size = u, s
		}
	}
	return best
}

// mediaProtect — файлы кэша, которые нельзя удалять до конца прохода (обложки TikTok).
var mediaProtect sync.Map

func (a *App) tiktokCacheCover(u string) string {
	if u == "" {
		return ""
	}
	b, err := a.FetchPublicImage(u)
	if err != nil {
		return ""
	}
	cached := a.MediaStoreImage(u, b)
	if cached != "" {
		mediaProtect.Store(filepath.Base(cached), time.Now())
	}
	return cached
}

func (a *App) tiktokPostFromItem(user string, it jx.M, src string) jx.M {
	id := jx.Str(it, "id")
	if !tiktokIDRe.MatchString(id) || jx.Int(it, "duration") > tiktokMaxSeconds {
		return nil
	}
	cover := a.tiktokCacheCover(tiktokBestThumbnail(jx.Maps(it, "thumbnails")))
	if cover == "" {
		return nil
	}
	page := "https://www.tiktok.com/@" + user + "/video/" + id
	text := strings.TrimSpace(jx.Str(it, "description"))
	if text == "" {
		text = strings.TrimSpace(jx.Str(it, "title"))
	}
	if text == "" {
		text = EmptyPostText
	}
	return jx.M{"image": cover, "images": []any{cover}, "gallery": []any{}, "video": "", "video_unavailable": false, "video_via": "ytdlp", "video_page": page,
		"engagement": nil, "text": trimRunes(text, 1000), "source": src, "post_id": id, "posted_at": jx.Int(it, "timestamp"), "url": page,
		"views": jx.Int(it, "view_count"), "likes": jx.Int(it, "like_count"), "comments": jx.Int(it, "comment_count"), "reshares": jx.Int(it, "repost_count"),
		"duration": jx.Int(it, "duration")}
}

// TikTokFetchPosts — последние короткие видео аккаунта.
func (a *App) TikTokFetchPosts(ctx context.Context, user string, count int, name string) ([]jx.M, error) {
	if !IsValidTikTokUser(user) {
		return []jx.M{}, nil
	}
	src := name
	if src == "" {
		src = SourceDisplayName(PlatformTikTok, user)
	}
	items, err := a.YtdlpList(ctx, "https://www.tiktok.com/@"+user, max(1, min(count, TikTokPostsPerAccount)))
	if err != nil {
		return nil, err
	}
	posts := []jx.M{}
	for _, it := range items {
		if p := a.tiktokPostFromItem(user, it, src); p != nil {
			posts = append(posts, p)
		}
		if len(posts) >= count {
			break
		}
	}
	return posts, nil
}

func (u *U) TikTokPreviewPosts(src jx.M, count int) []jx.M {
	p, err := u.TikTokFetchPosts(u.Ctx, jx.Str(src, "id"), count, jx.Str(src, "name"))
	if err != nil {
		return []jx.M{}
	}
	return p
}

// ---- Поиск аккаунтов TikTok: страница поиска через headless Chrome (без Node), разбор ссылок «/@имя» ----

const (
	tiktokSearchTimeout  = 60 * time.Second
	tiktokSearchMaxUsers = 10
	tiktokSearchUA       = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

var tiktokPauseSteps = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

var (
	tiktokVideoAuthorRe = regexp.MustCompile(`/@([A-Za-z0-9_.]{2,24})/video/\d+`)
	tiktokHrefRe        = regexp.MustCompile(`href="/@([A-Za-z0-9_.]{2,24})"`)
	chromeMu            sync.Mutex
)

func pickNames(re *regexp.Regexp, page string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		if IsValidTikTokUser(m[1]) {
			out = append(out, m[1])
		}
	}
	out = uniqueStrings(out)
	return out[:min(len(out), tiktokSearchMaxUsers)]
}

type tiktokPause struct {
	Until int64 `json:"until"`
	Level int   `json:"level"`
}

func (u *U) tiktokSearchPaused() bool {
	p := KV(u.App, u.Ctx, "tiktok_search_pause", tiktokPause{})
	if p.Until > now() {
		u.Log("-> TikTok: поиск на паузе после пустых ответов (капча?) — до %s.", time.Unix(p.Until, 0).Format("02.01 15:04"))
		return true
	}
	return false
}

// TikTokSearchPauseNote — строка для сводки или "".
func (a *App) TikTokSearchPauseNote(ctx context.Context) string {
	p := KV(a, ctx, "tiktok_search_pause", tiktokPause{})
	if p.Until <= now() {
		return ""
	}
	return "пауза до " + time.Unix(p.Until, 0).Format("02.01 15:04") + ", пустых ответов подряд: " + jx.ToStr(p.Level)
}

func (u *U) tiktokSearchNote(found bool) {
	if found {
		u.KVDelete(u.Ctx, "tiktok_search_pause")
		return
	}
	p, _ := KVUpdate(u.App, u.Ctx, "tiktok_search_pause", 0, func(p tiktokPause, _ bool) *tiktokPause {
		p.Level++
		p.Until = time.Now().Add(tiktokPauseSteps[min(p.Level, len(tiktokPauseSteps))-1]).Unix()
		return &p
	})
	u.Log("-> TikTok: пустой ответ — поиск на паузе до %s (пустых подряд: %d).", time.Unix(p.Until, 0).Format("02.01 15:04"), p.Level)
}

func (u *U) tiktokSearchPage(path, query string) string {
	if !chromeMu.TryLock() {
		u.Log("-> TikTok: другой поиск уже идёт — пропускаю.")
		return ""
	}
	defer chromeMu.Unlock()
	profile := filepath.Join(u.Cfg.DataDir, "chrome-tiktok")
	home := profile + "-home"
	os.MkdirAll(profile, 0o775)
	os.MkdirAll(home, 0o775)
	args := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-sandbox", "--no-zygote", "--disable-dev-shm-usage",
		"--user-data-dir=" + profile, "--virtual-time-budget=15000", "--user-agent=" + tiktokSearchUA, "--dump-dom",
		"https://www.tiktok.com/search/" + path + "?q=" + url.QueryEscape(query)}
	r := Exec(u.Ctx, u.Cfg.Chrome, args, tiktokSearchTimeout, append(os.Environ(), "HOME="+home), nil)
	if !r.OK || !strings.Contains(strings.ToLower(r.Stdout), "</html>") {
		what := "Chrome не запустился"
		if r.Code == -1 {
			what = "Chrome не ответил вовремя"
		}
		u.Log("-> %s: %s", what, YtdlpErrorText(r))
		return ""
	}
	return r.Stdout
}

func (u *U) tiktokSearchAllowed(q string) bool {
	return q != "" && runeLen(q) <= 50 && !u.tiktokSearchPaused()
}

// TikTokSearchVideos — авторы свежих популярных роликов по запросу (живые аккаунты).
func (u *U) TikTokSearchVideos(query string) []string {
	query = strings.TrimSpace(query)
	if !u.tiktokSearchAllowed(query) {
		return nil
	}
	page := u.tiktokSearchPage("video", query)
	if page == "" {
		return nil
	}
	names := pickNames(tiktokVideoAuthorRe, page)
	if len(names) == 0 && strings.Contains(strings.ToLower(page), "captcha") {
		u.Log("-> TikTok не отдал поиск видео (капча?).")
	} else if len(names) == 0 {
		u.Log("-> TikTok: по видео «%s» авторов не нашлось.", query)
	}
	u.tiktokSearchNote(len(names) > 0)
	return names
}

// TikTokSearchUsers — аккаунты по нику (запасной путь).
func (u *U) TikTokSearchUsers(query string) []string {
	query = strings.TrimSpace(query)
	if !u.tiktokSearchAllowed(query) {
		return nil
	}
	page := u.tiktokSearchPage("user", query)
	if page == "" {
		return nil
	}
	names := pickNames(tiktokHrefRe, page)
	u.tiktokSearchNote(len(names) > 0)
	return names
}
