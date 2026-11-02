package posta

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"html"
	"io"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/jx"
)

// RSS 2.0, RSS 1.0 и Atom: посты с картинками в том же виде, что у Telegram. Запросы — только через FetchPublic.

const (
	rssMaxBytes = 2 << 20
	rssMaxItems = 100
)

type FetchResultURL struct {
	FetchResult
	URL string
}

// RSSGet — загрузка с переходами по перенаправлениям (до 3); каждый адрес проверяется FetchPublic.
func (a *App) RSSGet(raw string, maxBytes int64, timeout time.Duration, ua string) FetchResultURL {
	for hop := 0; hop <= 3; hop++ {
		r, err := a.FetchPublic(raw, FetchOpts{MaxBytes: maxBytes, Timeout: timeout, UserAgent: ua})
		if err != nil {
			return FetchResultURL{FetchResult{Error: err.Error()}, raw}
		}
		if (r.Status == 301 || r.Status == 302 || r.Status == 303 || r.Status == 307 || r.Status == 308) && r.Location != "" {
			raw = RSSAbsoluteURL(raw, r.Location)
			continue
		}
		return FetchResultURL{r, raw}
	}
	return FetchResultURL{FetchResult{Error: "слишком много перенаправлений"}, raw}
}

// RSSAbsoluteURL — адрес href относительно страницы base.
func RSSAbsoluteURL(base, href string) string {
	href = htmlAttr(strings.TrimSpace(href))
	if strings.HasPrefix(strings.ToLower(href), "http://") || strings.HasPrefix(strings.ToLower(href), "https://") {
		return href
	}
	b, err := url.Parse(base)
	if err != nil {
		return href
	}
	r, err := b.Parse(href)
	if err != nil {
		return href
	}
	return r.String()
}

// xnode — узел XML: имя с привычным префиксом (media:content), атрибуты, дети, текст, сырая разметка внутри.
type xnode struct {
	N    string
	A    map[string]string
	K    []*xnode
	T    string
	Raw  string
}

var nsPrefix = map[string]string{
	"http://search.yahoo.com/mrss/":              "media",
	"http://purl.org/rss/1.0/modules/content/":   "content",
	"http://purl.org/dc/elements/1.1/":           "dc",
	"http://www.youtube.com/xml/schemas/2015":    "yt",
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#": "rdf",
	"http://www.w3.org/2005/Atom":                "",
	"http://purl.org/rss/1.0/":                   "",
}

func xname(n xml.Name) string {
	p, ok := nsPrefix[n.Space]
	if !ok && n.Space != "" && !strings.Contains(n.Space, "/") {
		p = n.Space // необъявленный префикс остаётся как есть
	}
	if p == "" {
		return n.Local
	}
	return p + ":" + n.Local
}

// rssLoad — дерево XML или nil (не XML).
func rssLoad(data []byte) *xnode {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	// HTMLAutoClose здесь не нужен: он закрывал <link> сразу после открытия, и лента RSS 2.0 обрывалась на первом же <link>
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(label string, input io.Reader) (io.Reader, error) { return input, nil }
	root := &xnode{N: "#root"}
	stack := []*xnode{root}
	starts := []int64{0}
	for {
		off := d.InputOffset()
		tok, err := d.Token()
		if err != nil {
			break
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) > 60 {
				return nil
			}
			n := &xnode{N: xname(t.Name), A: map[string]string{}}
			for _, at := range t.Attr {
				n.A[xname(at.Name)] = at.Value
				if at.Name.Space == "" {
					n.A[at.Name.Local] = at.Value
				}
			}
			top.K = append(top.K, n)
			stack = append(stack, n)
			starts = append(starts, d.InputOffset())
		case xml.EndElement:
			if len(stack) > 1 {
				s := starts[len(starts)-1]
				if off >= s && int(off) <= len(data) {
					top.Raw = string(data[s:off])
				}
				stack = stack[:len(stack)-1]
				starts = starts[:len(starts)-1]
			}
		case xml.CharData:
			top.T += string(t)
		}
	}
	if len(root.K) == 0 {
		return nil
	}
	return root.K[0]
}

func rssIsFeed(doc *xnode) bool {
	return doc != nil && (doc.N == "rss" || doc.N == "feed" || doc.N == "rdf:RDF")
}

func (n *xnode) kids(name string) []*xnode {
	if n == nil {
		return nil
	}
	var out []*xnode
	for _, k := range n.K {
		if k.N == name {
			out = append(out, k)
		}
	}
	return out
}

func (n *xnode) kid(name string) *xnode {
	if k := n.kids(name); len(k) > 0 {
		return k[0]
	}
	return nil
}

func (n *xnode) text() string {
	if n == nil {
		return ""
	}
	return n.T
}

// html — текст (в том числе из CDATA) или, у XHTML-контента Atom, вложенная разметка.
func (n *xnode) html() string {
	if n == nil {
		return ""
	}
	if strings.TrimSpace(n.T) != "" || len(n.K) == 0 {
		return n.T
	}
	return n.Raw
}

func rssHTTPS(raw, base string) string {
	u := htmlAttr(strings.TrimSpace(raw))
	if base != "" && u != "" {
		u = RSSAbsoluteURL(base, u)
	}
	if !strings.HasPrefix(u, "https://") || len(u) > 1000 {
		return ""
	}
	if p, err := url.Parse(u); err != nil || p.Host == "" {
		return ""
	}
	return u
}

func rssItemLink(item *xnode) string {
	for _, l := range item.kids("link") {
		if strings.TrimSpace(l.T) != "" {
			return strings.TrimSpace(l.T)
		}
		if rel := l.A["rel"]; rel == "" || rel == "alternate" {
			return strings.TrimSpace(l.A["href"])
		}
	}
	return ""
}

type rssFile struct{ URL, Type, Medium string }

func rssItemMedia(item *xnode) []rssFile {
	var files []rssFile
	add := func(n *xnode, medium string) {
		u := n.A["url"]
		if u == "" {
			u = n.A["href"]
		}
		if u == "" {
			return
		}
		m := strings.ToLower(n.A["medium"])
		if m == "" {
			m = medium
		}
		files = append(files, rssFile{u, strings.ToLower(n.A["type"]), m})
	}
	for _, e := range item.kids("enclosure") {
		add(e, "")
	}
	for _, l := range item.kids("link") {
		if l.A["rel"] == "enclosure" {
			add(l, "")
		}
	}
	for _, c := range item.kids("media:content") {
		add(c, "")
	}
	for _, g := range item.kids("media:group") {
		for _, c := range g.kids("media:content") {
			add(c, "")
		}
		for _, t := range g.kids("media:thumbnail") {
			add(t, "thumbnail")
		}
	}
	for _, t := range item.kids("media:thumbnail") {
		add(t, "thumbnail")
	}
	return files
}

var badImageExtRe = regexp.MustCompile(`(?i)\.(tiff?|svg|heic|heif|avif|ico|psd)$`)
var imageExtRe = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|webp)(\?|$)`)

func rssImageOK(raw string) bool {
	u, err := url.Parse(raw)
	return err != nil || !badImageExtRe.MatchString(u.Path)
}

func rssIsImage(f rssFile) bool {
	return strings.HasPrefix(f.Type, "image/") || f.Medium == "image" || (f.Type == "" && f.Medium == "" && imageExtRe.MatchString(f.URL))
}

var pinimgRe = regexp.MustCompile(`^https://i\.pinimg\.com/(?:136x136|236x|474x|564x)/`)

func rssUpgradeImageURL(u string) string { return pinimgRe.ReplaceAllString(u, "https://i.pinimg.com/736x/") }

var imgSrcRe = regexp.MustCompile(`(?i)<img\b[^>]*?\ssrc\s*=\s*["']([^"']+)["']`)

func rssHTMLImage(h, base string) string {
	if m := imgSrcRe.FindStringSubmatch(h); m != nil {
		return rssHTTPS(m[1], base)
	}
	return ""
}

var timeLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05", "2006-01-02",
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700"}

// ParseFeedTime — дата из RSS/Atom (strtotime-подобно для частых форматов).
func ParseFeedTime(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if t, err := mail.ParseDate(raw); err == nil {
		return t.Unix()
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, raw); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func rssItemTime(item *xnode) int64 {
	for _, tag := range []string{"pubDate", "published", "updated", "dc:date"} {
		if t := ParseFeedTime(item.kid(tag).text()); t > 0 {
			return t
		}
	}
	return 0
}

func rssItems(doc *xnode) []*xnode {
	if doc.N == "feed" {
		return doc.kids("entry")
	}
	if it := doc.kid("channel").kids("item"); len(it) > 0 {
		return it
	}
	return doc.kids("item")
}

var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// RSSParse — посты ленты, самые свежие первыми: с картинкой (у видео — постер); YouTube — с ссылкой на ролик.
func RSSParse(data []byte, count int, name, feedURL string, keepNoImage, withText bool, minText int) ([]jx.M, error) {
	doc := rssLoad(data)
	if !rssIsFeed(doc) {
		return nil, Bad("Это не RSS и не Atom.")
	}
	src := name
	if src == "" {
		src = SourceDisplayName(PlatformRSS, hostOf(feedURL))
	}
	kind := RSSFeedKind(feedURL)
	youtube := kind == "YouTube" || kind == "YouTube Shorts"
	shortsFeed := strings.Contains(feedURL, "playlist_id=UUSH")
	items := rssItems(doc)
	if len(items) > rssMaxItems {
		items = items[:rssMaxItems]
	}
	posts := []jx.M{}
	for _, item := range items {
		link := rssHTTPS(rssItemLink(item), "")
		base := link
		if base == "" {
			base = feedURL
		}
		var body []string
		for _, tag := range []string{"content:encoded", "description", "content", "summary", "media:description"} {
			body = append(body, item.kid(tag).html())
		}
		files := rssItemMedia(item)
		videoID := ""
		if youtube {
			videoID = strings.TrimSpace(item.kid("yt:videoId").text())
			if !videoIDRe.MatchString(videoID) {
				videoID = ""
			}
		}
		img := ""
		for _, f := range files {
			if rssIsImage(f) && f.Medium != "thumbnail" && rssImageOK(f.URL) {
				if img = rssHTTPS(f.URL, base); img != "" {
					break
				}
			}
		}
		if img == "" {
			for _, f := range files {
				if f.Medium == "thumbnail" && rssImageOK(f.URL) {
					if img = rssHTTPS(f.URL, base); img != "" {
						break
					}
				}
			}
		}
		if img == "" {
			for _, h := range body {
				if i := rssHTMLImage(h, base); i != "" && rssImageOK(i) {
					img = i
					break
				}
			}
		}
		if img == "" && videoID != "" {
			img = "https://i.ytimg.com/vi/" + videoID + "/hqdefault.jpg"
		}
		title := CleanHTMLText(item.kid("title").html())
		desc := ""
		for _, h := range body {
			if h != "" {
				desc = CleanHTMLText(h)
				break
			}
		}
		text := title
		if desc != "" && desc != title {
			text = strings.TrimSpace(title + "\n" + desc)
		}
		text = trimRunes(strings.TrimSpace(text), 1000)
		textOnly := img == "" && withText && runeLen(text) >= minText
		if img == "" && !keepNoImage && !textOnly {
			continue
		}
		video := ""
		for _, f := range files {
			if f.Type == "video/mp4" {
				if video = rssHTTPS(f.URL, base); video != "" {
					break
				}
			}
		}
		short := videoID != "" && (shortsFeed || strings.Contains(link, "/shorts/"))
		if videoID != "" && !short {
			text = strings.TrimSpace(text + "\nhttps://www.youtube.com/watch?v=" + videoID)
		}
		img = rssUpgradeImageURL(img)
		guid := strings.TrimSpace(item.kid("guid").text())
		if guid == "" {
			guid = strings.TrimSpace(item.kid("id").text())
		}
		if guid == "" {
			guid = link
		}
		if guid == "" {
			guid = img
		}
		h := sha1.Sum([]byte(guid))
		if text == "" {
			text = EmptyPostText
		}
		images := []string{}
		if img != "" {
			images = []string{img}
		}
		p := jx.M{"image": img, "images": images, "gallery": []any{}, "video": video, "text": text, "source": src, "post_id": hex.EncodeToString(h[:])[:12],
			"posted_at": rssItemTime(item), "url": link, "video_unavailable": false, "engagement": nil, "text_post": textOnly}
		if videoID != "" {
			p["video_via"] = "ytdlp"
			if short {
				p["video_page"] = "https://www.youtube.com/shorts/" + videoID
			} else {
				p["video_page"] = "https://www.youtube.com/watch?v=" + videoID
			}
		}
		posts = append(posts, p)
	}
	sort.SliceStable(posts, func(i, j int) bool { return jx.Int(posts[i], "posted_at") > jx.Int(posts[j], "posted_at") })
	if len(posts) > count {
		posts = posts[:max(0, count)]
	}
	return posts, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// RSSFetchPosts — свежие посты ленты по адресу (YouTube без ленты — через yt-dlp).
func (u *U) RSSFetchPosts(feedURL string, count int, name string, keepNoImage, withText bool) ([]jx.M, error) {
	r := u.RSSGet(feedURL, rssMaxBytes, 15*time.Second, HTTPUA)
	if !r.OK {
		if cid, tab, ok := YouTubeFeedChannel(feedURL); ok {
			u.Log("YouTube: лента недоступна (%d), список роликов взят через yt-dlp", r.Status)
			posts, err := u.YouTubeChannelPosts(cid, tab, count)
			if len(posts) > count {
				posts = posts[:count]
			}
			return posts, err
		}
		msg := "Лента не загрузилась"
		if r.Error != "" {
			msg += ": " + r.Error
		}
		return nil, Bad("%s", msg)
	}
	return RSSParse(r.Bytes, count, name, r.URL, keepNoImage, withText, 120)
}

// RSSFeedTitle — название ленты, иначе хост.
func (a *App) RSSFeedTitle(feedURL string) string {
	host := hostOf(feedURL)
	r := a.RSSGet(feedURL, rssMaxBytes, 15*time.Second, HTTPUA)
	title := ""
	if r.OK {
		if doc := rssLoad(r.Bytes); rssIsFeed(doc) {
			n := doc
			if doc.N != "feed" {
				n = doc.kid("channel")
			}
			title = CleanHTMLText(n.kid("title").html())
		}
	}
	if title == "" {
		title = host
	}
	return trimRunes(title, 120)
}

var linkTagRe = regexp.MustCompile(`(?i)<link\b[^>]*>`)

func attrOf(tag, name string) string {
	re := regexp.MustCompile(`(?i)\b` + name + `\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	m := re.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return htmlAttr(m[1])
	}
	return htmlAttr(m[2])
}

// RSSDiscover — адрес ленты по адресу сайта: сам адрес или <link rel="alternate">.
func (a *App) RSSDiscover(raw string) (string, error) {
	r := a.RSSGet(raw, 3<<20, 15*time.Second, HTTPUA)
	if !r.OK {
		msg := "Страница не загрузилась"
		if r.Error != "" {
			msg += ": " + r.Error
		}
		return "", Bad("%s", msg)
	}
	if rssIsFeed(rssLoad(r.Bytes)) {
		return r.URL, nil
	}
	altRe := regexp.MustCompile(`(?i)\balternate\b`)
	typeRe := regexp.MustCompile(`(?i)^application/(rss|atom)\+xml`)
	for _, tag := range linkTagRe.FindAllString(string(r.Bytes), -1) {
		if !altRe.MatchString(attrOf(tag, "rel")) || !typeRe.MatchString(strings.TrimSpace(attrOf(tag, "type"))) {
			continue
		}
		if href := attrOf(tag, "href"); href != "" {
			abs := RSSAbsoluteURL(r.URL, href)
			if strings.HasPrefix(strings.ToLower(abs), "http://") {
				abs = "https://" + abs[7:]
			}
			return abs, nil
		}
	}
	return "", Bad("На этой странице нет RSS-ленты")
}

var (
	tumblrNameRe   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	pinUserRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	pinBoardRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	ytChannelIDRe  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	ytFeedIDRe     = regexp.MustCompile(`channel_id=(UC[A-Za-z0-9_-]{22})`)
	ytPageIDRe     = regexp.MustCompile(`"channelId":"(UC[A-Za-z0-9_-]{22})"|youtube\.com/channel/(UC[A-Za-z0-9_-]{22})`)
	pinterestHostRe = regexp.MustCompile(`(^|\.)pinterest\.(com|ru)$`)
)

var pinterestReserved = []string{"pin", "search", "ideas", "today", "explore", "categories", "login", "settings", "business"}

// RSSKnownFeed — ссылка YouTube, Pinterest или Tumblr → адрес ленты; "" — обычный сайт.
func (a *App) RSSKnownFeed(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", nil
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var parts []string
	for _, s := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	switch {
	case host == "tumblr.com" || strings.HasSuffix(host, ".tumblr.com"):
		name := strings.TrimSuffix(host, ".tumblr.com")
		if host == "tumblr.com" {
			name = ""
			if len(parts) > 0 {
				name = parts[0]
			}
		}
		name = strings.ToLower(name)
		if !tumblrNameRe.MatchString(name) {
			return "", Bad("Нужна ссылка на блог Tumblr: адрес вида имя.tumblr.com")
		}
		return "https://" + name + ".tumblr.com/rss", nil
	case pinterestHostRe.MatchString(host):
		user, board := "", ""
		if len(parts) > 0 {
			user = parts[0]
		}
		if len(parts) > 1 {
			board = parts[1]
		}
		if !pinUserRe.MatchString(user) || contains(pinterestReserved, strings.ToLower(user)) {
			return "", Bad("Нужна ссылка на профиль или доску Pinterest")
		}
		if board == "" {
			return "https://www.pinterest.com/" + user + "/feed.rss", nil
		}
		if !pinBoardRe.MatchString(board) {
			return "", Bad("Нужна ссылка на профиль или доску Pinterest")
		}
		return "https://www.pinterest.com/" + user + "/" + board + ".rss", nil
	case host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		if host == "youtu.be" {
			parts = nil
		}
		return a.rssYouTubeFeed(raw, parts)
	}
	return "", nil
}

func (a *App) rssYouTubeFeed(raw string, parts []string) (string, error) {
	head := ""
	if len(parts) > 0 {
		head = strings.ToLower(parts[0])
	}
	shorts := len(parts) > 0 && strings.ToLower(parts[len(parts)-1]) == "shorts"
	if head == "channel" && len(parts) > 1 && ytChannelIDRe.MatchString(parts[1]) {
		if shorts {
			return RSSYouTubeShortsFeed(parts[1]), nil
		}
		return YouTubeChannelFeedURL(parts[1]), nil
	}
	if strings.HasPrefix(head, "@") || ((head == "c" || head == "user") && len(parts) > 1) {
		r := a.RSSGet(raw, 3<<20, 15*time.Second, HTTPUA)
		if !r.OK {
			return "", Bad("Страница YouTube не загрузилась")
		}
		m := ytPageIDRe.FindStringSubmatch(string(r.Bytes))
		if m == nil {
			return "", Bad("Не нашёл канал YouTube по этой ссылке")
		}
		cid := m[1]
		if cid == "" {
			cid = m[2]
		}
		if shorts {
			return RSSYouTubeShortsFeed(cid), nil
		}
		return YouTubeChannelFeedURL(cid), nil
	}
	return "", Bad("Нужна ссылка на канал YouTube")
}

func RSSYouTubeShortsFeed(cid string) string {
	return "https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH" + cid[2:]
}

func YouTubeChannelFeedURL(cid string) string {
	return "https://www.youtube.com/feeds/videos.xml?channel_id=" + cid
}

// RSSFeedKind — YouTube, YouTube Shorts, Pinterest, Tumblr или RSS.
func RSSFeedKind(feedURL string) string {
	host := strings.ToLower(hostOf(feedURL))
	switch {
	case strings.Contains(feedURL, "playlist_id=UUSH"):
		return "YouTube Shorts"
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		return "YouTube"
	case pinterestHostRe.MatchString(host):
		return "Pinterest"
	case strings.HasSuffix(host, ".tumblr.com"):
		return "Tumblr"
	}
	return "RSS"
}

func (u *U) RSSPreviewPosts(src jx.M, count int) []jx.M {
	posts, err := u.RSSFetchPosts(jx.Str(src, "id"), count, jx.Str(src, "name"), false, false)
	if err != nil {
		return []jx.M{}
	}
	return posts
}

// RSSPostURLTrusted — ссылка записи RSS-источника безопасна: https, хост совпадает с хостом ленты или их поддомены.
func RSSPostURLTrusted(item jx.M) bool {
	key, link := jx.Str(item, "source_key"), jx.Str(item, "source_url")
	if !strings.HasPrefix(key, "rss:") || !strings.HasPrefix(link, "https://") {
		return false
	}
	h := func(u string) string { return strings.TrimPrefix(strings.ToLower(hostOf(u)), "www.") }
	feed, post := h(key[4:]), h(link)
	return feed != "" && post != "" && (feed == post || strings.HasSuffix(feed, "."+post) || strings.HasSuffix(post, "."+feed))
}

var _ = html.UnescapeString
