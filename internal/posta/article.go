package posta

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"posta/internal/jx"
	"posta/internal/llm"
)

// Страница статьи новостного сайта: картинка, описание и основной текст — для записей RSS без картинки или с коротким анонсом.
// vc.ru и dtf.ru — из API Osnova, остальные сайты — из страницы.

const (
	articleMaxBytes  = 2 << 20
	articleTextMax   = 4000
	articleCacheTTL  = 3 * 24 * time.Hour
	ArticleShortText = 200
	articleGapChars  = 700
)

var articleStripTags = map[string]bool{"script": true, "style": true, "noscript": true, "nav": true, "header": true, "footer": true, "aside": true,
	"form": true, "figure": true, "figcaption": true, "iframe": true, "button": true, "svg": true}

func articleMeta(page, property string) string {
	q := regexp.QuoteMeta(property)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)<meta[^>]+(?:property|name)=["']` + q + `["'][^>]*content=["']([^"']*)["']`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']*)["'][^>]*(?:property|name)=["']` + q + `["']`),
	} {
		if m := re.FindStringSubmatch(page); m != nil {
			return strings.TrimSpace(html.UnescapeString(m[1]))
		}
	}
	return ""
}

var coverStubRe = regexp.MustCompile(`^https://api\.(?:vc|dtf)\.ru/[^?]*/cover/`)

// ArticleImageIsStub — обложка-заглушка с заголовком (vc.ru, dtf.ru): не картинка.
func ArticleImageIsStub(u string) bool { return coverStubRe.MatchString(u) }

func articleImage(page string) string {
	for _, p := range []string{"og:image", "twitter:image"} {
		u := articleMeta(page, p)
		if strings.HasPrefix(u, "https://") && !ArticleImageIsStub(u) {
			if _, err := url.Parse(u); err == nil {
				return u
			}
		}
	}
	return ""
}

var compactTimeRe = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})$`)

func articlePublished(page string) int64 {
	raw := articleMeta(page, "article:published_time")
	if m := compactTimeRe.FindStringSubmatch(raw); m != nil {
		raw = m[1] + "-" + m[2] + "-" + m[3] + "T" + m[4] + ":" + m[5] + ":00Z"
	}
	return ParseFeedTime(raw)
}

var blockClassRe = regexp.MustCompile(`(?i)(text|content|body|article|paragraph)`)

// articleMainText — листовые блоки текста по порядку; соседние (разрыв до articleGapChars) — группа; берётся самая длинная.
func articleMainText(page string) string {
	z := xhtml.NewTokenizer(strings.NewReader(page))
	type open struct {
		tag      string
		start    int
		keep     bool
		hasChild bool
		text     strings.Builder
	}
	var stack []*open
	skip := 0
	offset := 0
	var groups [][]string
	var group []string
	lastEnd := -1
	for {
		tt := z.Next()
		raw := len(z.Raw())
		start := offset
		offset += raw
		if tt == xhtml.ErrorToken {
			break
		}
		switch tt {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if articleStripTags[tag] && tt == xhtml.StartTagToken {
				skip++
				continue
			}
			if tag == "br" && len(stack) > 0 && skip == 0 {
				stack[len(stack)-1].text.WriteString("\n")
			}
			if (tag == "p" || tag == "div") && tt == xhtml.StartTagToken {
				class := ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "class" {
						class = string(v)
					}
				}
				for _, o := range stack {
					o.hasChild = true
				}
				stack = append(stack, &open{tag: tag, start: start, keep: tag == "p" || blockClassRe.MatchString(class)})
			}
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if articleStripTags[tag] && skip > 0 {
				skip--
				continue
			}
			if (tag == "p" || tag == "div") && len(stack) > 0 {
				o := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if !o.keep || o.hasChild {
					continue
				}
				t := strings.TrimSpace(regexp.MustCompile(`[ \t\r\f\v]+`).ReplaceAllString(o.text.String(), " "))
				if runeLen(t) < 40 {
					continue
				}
				if len(group) > 0 && o.start-lastEnd > articleGapChars {
					groups = append(groups, group)
					group = nil
				}
				group = append(group, t)
				lastEnd = offset
			}
		case xhtml.TextToken:
			if skip == 0 && len(stack) > 0 {
				stack[len(stack)-1].text.WriteString(html.UnescapeString(string(z.Text())))
			}
		}
	}
	if len(group) > 0 {
		groups = append(groups, group)
	}
	var best []string
	bestLen := 0
	for _, g := range groups {
		n := 0
		for _, t := range g {
			n += runeLen(t)
		}
		if n > bestLen {
			best, bestLen = g, n
		}
	}
	return trimRunes(strings.Join(best, "\n\n"), articleTextMax)
}

var ldJSONRe = regexp.MustCompile(`(?is)<script[^>]+application/ld\+json[^>]*>(.*?)</script>`)

func articleJSONLD(page string) (headline, body, image string) {
	for _, m := range ldJSONRe.FindAllStringSubmatch(page, -1) {
		var data any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &data) != nil {
			continue
		}
		nodes := []any{data}
		if dm, ok := data.(jx.M); ok && dm["@graph"] != nil {
			nodes = jx.List(dm, "@graph")
		} else if dl, ok := data.([]any); ok {
			nodes = dl
		}
		for _, n := range nodes {
			node, ok := n.(jx.M)
			if !ok {
				continue
			}
			types := jx.ToStrs(node["@type"])
			if s, ok := node["@type"].(string); ok {
				types = []string{s}
			}
			isArticle := false
			for _, t := range types {
				if t == "Article" || t == "NewsArticle" || t == "BlogPosting" || t == "ReportageNewsArticle" {
					isArticle = true
				}
			}
			if !isArticle {
				continue
			}
			img := ""
			switch v := node["image"].(type) {
			case string:
				img = v
			case []any:
				if len(v) > 0 {
					if s, ok := v[0].(string); ok {
						img = s
					} else if mm, ok := v[0].(jx.M); ok {
						img = jx.Str(mm, "url")
					}
				}
			case jx.M:
				img = jx.Str(v, "url")
			}
			if !strings.HasPrefix(img, "https://") || ArticleImageIsStub(img) {
				img = ""
			}
			text := func(v any) string {
				if l, ok := v.([]any); ok {
					var parts []string
					for _, x := range l {
						if s, ok := x.(string); ok {
							parts = append(parts, s)
						}
					}
					return strings.Join(parts, "\n")
				}
				s, _ := v.(string)
				return s
			}
			return strings.TrimSpace(text(node["headline"])), strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(text(node["articleBody"]), ""))), img
		}
	}
	return "", "", ""
}

var endsSentenceRe = regexp.MustCompile(`[.!?…]$`)

// ArticleIsKeywordList — SEO-список через запятую, а не предложение (og:description vc.ru).
func ArticleIsKeywordList(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || endsSentenceRe.MatchString(text) {
		return false
	}
	var parts []string
	for _, p := range strings.Split(text, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 4 {
		return false
	}
	words := 0
	for _, p := range parts {
		words += len(strings.Fields(p))
	}
	return float64(words)/float64(len(parts)) < 6
}

type articleData struct {
	Image       string `json:"image"`
	Description string `json:"description"`
	Published   int64  `json:"published"`
	ArticleText string `json:"article_text"`
}

func articleParse(page string) articleData {
	_, body, ldImage := articleJSONLD(page)
	text := articleMainText(page)
	if runeLen(body) > runeLen(text) {
		text = trimRunes(body, articleTextMax)
	}
	img := articleImage(page)
	if img == "" {
		img = ldImage
	}
	desc := articleMeta(page, "og:description")
	if desc == "" {
		desc = articleMeta(page, "description")
	}
	return articleData{img, trimRunes(desc, 1000), articlePublished(page), text}
}

func articleBodyText(blocks []jx.M) string {
	var parts []string
	for _, b := range blocks {
		switch jx.Str(b, "type") {
		case "list":
			var items []string
			for _, it := range jx.Maps(b, "items") {
				items = append(items, jx.Str(it, "text"))
			}
			parts = append(parts, strings.Join(items, "\n"))
		case "p", "h", "quote":
			parts = append(parts, jx.Str(b, "text"))
		}
	}
	return trimRunes(strings.Join(parts, "\n\n"), articleTextMax)
}

func articlePlainBlocks(text string) []jx.M {
	out := []jx.M{}
	for _, p := range regexp.MustCompile(`\n{2,}`).Split(text, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, jx.M{"type": "p", "text": p, "links": []any{}})
		}
	}
	return out
}

func articleApplyOsnova(post jx.M, a jx.M, link string) jx.M {
	post["article"] = jx.M{"title": a["title"], "lead": a["lead"], "cover": a["cover"], "rubric": a["rubric"], "author": a["author"],
		"minutes": a["minutes"], "words": a["words"], "site": hostOf(link)}
	text := jx.Str(a, "title")
	if l := jx.Str(a, "lead"); l != "" {
		text += "\n" + l
	}
	post["text"] = strings.TrimSpace(text)
	post["article_text"] = articleBodyText(jx.Maps(a, "blocks"))
	eng := jx.M{"source": "osnova"}
	for k, v := range jx.Map(a, "counters") {
		eng[k] = v
	}
	post["engagement"] = eng
	cover := jx.Str(jx.Map(a, "cover"), "src")
	img := jx.Str(post, "image")
	if cover != "" && (img == "" || ArticleImageIsStub(img)) {
		post["image"], post["images"] = cover, []any{cover}
	} else if ArticleImageIsStub(img) {
		post["image"], post["images"] = "", []any{}
	}
	if jx.Int(post, "posted_at") == 0 && jx.Int(a, "published") > 0 {
		post["posted_at"] = jx.Int(a, "published")
	}
	post["enriched"] = true
	return post
}

// ArticleEnrich — картинка и текст со страницы статьи (кэш 3 суток, неудача — час). Не вышло — запись как была.
func (a *App) ArticleEnrich(post jx.M, force bool) jx.M {
	link := jx.Str(post, "url")
	if !strings.HasPrefix(link, "https://") {
		return post
	}
	if os := a.OsnovaArticle(link, force); os != nil {
		return articleApplyOsnova(jx.Copy(post), os, link)
	}
	key := "article:" + sha1Short(link, 40)
	var data *articleData
	var cached struct {
		Data *articleData `json:"data"`
	}
	if !force && a.CacheGet(a.ctxOrBg(), key, &cached) {
		data = cached.Data
	} else {
		HostThrottle(hostOf(link), time.Second)
		r, err := a.FetchPublic(link, FetchOpts{MaxBytes: articleMaxBytes, Timeout: 10 * time.Second})
		ttl := time.Hour
		if err == nil && r.OK {
			d := articleParse(string(r.Bytes))
			data, ttl = &d, articleCacheTTL
		}
		a.CacheSet(a.ctxOrBg(), key, jx.M{"data": data}, ttl)
	}
	if data == nil {
		return post
	}
	post = jx.Copy(post)
	if jx.Str(post, "image") == "" && data.Image != "" {
		post["image"], post["images"] = data.Image, []any{data.Image}
	}
	if data.ArticleText != "" {
		post["article_text"] = data.ArticleText
	}
	desc := data.Description
	if ArticleIsKeywordList(desc) {
		desc = ""
	}
	if t := jx.Str(post, "text"); t == "" || runeLen(t) < ArticleShortText {
		nt := strings.TrimSpace(strings.TrimSpace(t+"\n") + desc)
		if t == "" {
			nt = strings.TrimSpace(desc)
		}
		nt = trimRunes(nt, 1000)
		if nt == "" {
			nt = EmptyPostText
		}
		post["text"] = nt
	}
	if jx.Int(post, "posted_at") == 0 && data.Published > 0 {
		post["posted_at"] = data.Published
	}
	post["enriched"] = true
	return post
}

// ArticleBlocksFor — статья для читалки: у vc.ru/dtf.ru — блоки API, у остальных — абзацы текста.
func (a *App) ArticleBlocksFor(link string) jx.M {
	if os := a.OsnovaArticle(link, false); os != nil {
		return os
	}
	text := strings.TrimSpace(jx.Str(a.ArticleEnrich(jx.M{"url": link}, false), "article_text"))
	if text == "" {
		return nil
	}
	return jx.M{"title": "", "lead": "", "cover": nil, "rubric": "", "author": "", "published": 0, "words": 0, "minutes": 1, "counters": jx.M{}, "blocks": articlePlainBlocks(text)}
}

// ---- Osnova (vc.ru, dtf.ru): данные из открытого API content?id= ----

var osnovaHosts = map[string]string{"vc.ru": "api.vc.ru", "dtf.ru": "api.dtf.ru"}

var (
	osnovaIDRe     = regexp.MustCompile(`/(\d{5,12})(?:-|/|$)`)
	osnovaWWWRe    = regexp.MustCompile(`^(?:www|m)\.`)
	osnovaLinkRe   = regexp.MustCompile(`(?is)<a\b[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	osnovaRedirRe  = regexp.MustCompile(`redirect\?(?:[^"]*&)?to=([^&]+)`)
	osnovaHashOnly = regexp.MustCompile(`^(?:#\S+\s*)+$`)
	uuidRe         = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	pCloseRe       = regexp.MustCompile(`(?i)</p>`)
	nbspRe         = regexp.MustCompile("[ \t\r\f\v ]+")
)

func osnovaText(h string) string {
	t := html.UnescapeString(tagRe.ReplaceAllString(brRe.ReplaceAllString(h, "\n"), ""))
	return strings.TrimSpace(nbspRe.ReplaceAllString(t, " "))
}

func osnovaLinks(h string) []any {
	out := []any{}
	for _, m := range osnovaLinkRe.FindAllStringSubmatch(h, -1) {
		href := html.UnescapeString(m[1])
		if r := osnovaRedirRe.FindStringSubmatch(href); r != nil {
			if d, err := url.QueryUnescape(r[1]); err == nil {
				href = d
			}
		}
		label := osnovaText(m[2])
		if label != "" && strings.HasPrefix(href, "https://") {
			out = append(out, []any{label, href})
		}
	}
	return out
}

func osnovaParagraphs(h string) []jx.M {
	var out []jx.M
	for _, c := range pCloseRe.Split(h, -1) {
		t := osnovaText(c)
		if t == "" || osnovaHashOnly.MatchString(t) {
			continue
		}
		out = append(out, jx.M{"type": "p", "text": t, "links": osnovaLinks(c)})
	}
	return out
}

func osnovaImage(img jx.M) jx.M {
	d := jx.Map(img, "data")
	uuid := jx.Str(d, "uuid")
	if !uuidRe.MatchString(uuid) {
		return nil
	}
	return jx.M{"src": "https://leonardo.osnova.io/" + uuid + "/-/resize/1300/", "width": jx.Int(d, "width"), "height": jx.Int(d, "height")}
}

func osnovaCover(raw []jx.M) jx.M {
	for _, onlyCover := range []bool{true, false} {
		for _, b := range raw {
			if jx.Str(b, "type") != "media" || jx.Bool(b, "cover") != onlyCover {
				continue
			}
			for _, it := range jx.Maps(jx.Map(b, "data"), "items") {
				if img := osnovaImage(jx.Map(it, "image")); img != nil {
					return img
				}
			}
		}
	}
	return nil
}

func osnovaBlocks(raw []jx.M, link string) []jx.M {
	out := []jx.M{}
	for _, b := range raw {
		d := jx.Map(b, "data")
		switch jx.Str(b, "type") {
		case "text":
			out = append(out, osnovaParagraphs(jx.Str(d, "text"))...)
		case "header":
			if t := osnovaText(jx.Str(d, "text")); t != "" {
				out = append(out, jx.M{"type": "h", "text": t})
			}
		case "list":
			items := []any{}
			for _, i := range jx.List(d, "items") {
				if t := osnovaText(jx.ToStr(i)); t != "" {
					items = append(items, jx.M{"text": t, "links": osnovaLinks(jx.ToStr(i))})
				}
			}
			out = append(out, jx.M{"type": "list", "items": items})
		case "quote":
			if t := osnovaText(jx.Str(d, "text")); t != "" {
				out = append(out, jx.M{"type": "quote", "text": t})
			}
		case "media":
			for _, it := range jx.Maps(d, "items") {
				if img := osnovaImage(jx.Map(it, "image")); img != nil {
					img["type"], img["caption"] = "image", osnovaText(jx.Str(it, "title"))
					out = append(out, img)
				}
			}
		case "video":
			out = append(out, jx.M{"type": "video", "title": osnovaText(jx.Str(d, "title")), "url": link})
		case "osnovaEmbed":
			e := jx.Map(jx.Map(d, "osnovaEmbed"), "data")
			if t := osnovaText(jx.Str(e, "title")); t != "" {
				out = append(out, jx.M{"type": "embed", "title": t, "description": osnovaText(jx.Str(e, "description"))})
			}
		}
	}
	return out
}

func osnovaLead(raw, blocks []jx.M) string {
	lead := ""
	for _, b := range raw {
		if jx.Str(b, "type") == "text" && jx.Bool(b, "cover") {
			if ps := osnovaParagraphs(jx.Str(jx.Map(b, "data"), "text")); len(ps) > 0 {
				if lead = jx.Str(ps[0], "text"); lead != "" {
					break
				}
			}
		}
	}
	if lead == "" {
		for _, b := range blocks {
			if jx.Str(b, "type") == "p" {
				lead = jx.Str(b, "text")
				break
			}
		}
	}
	if runeLen(lead) <= 400 {
		return lead
	}
	cut := trimRunes(lead, 400)
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ") + "…"
}

func osnovaWordCount(blocks []jx.M) int {
	n := 0
	for _, b := range blocks {
		t := ""
		switch jx.Str(b, "type") {
		case "list":
			var items []string
			for _, it := range jx.Maps(b, "items") {
				items = append(items, jx.Str(it, "text"))
			}
			t = strings.Join(items, " ")
		case "image", "video", "embed":
		default:
			t = jx.Str(b, "text")
		}
		n += len(strings.Fields(t))
	}
	return n
}

// OsnovaFromAPI — ответ content?id= → статья общего вида; nil — не статья.
func OsnovaFromAPI(body []byte, link string) jx.M {
	res := jx.Map(jx.Unmarshal(body), "result")
	if res == nil || res["blocks"] == nil {
		return nil
	}
	raw := jx.Maps(res, "blocks")
	blocks := osnovaBlocks(raw, link)
	words := osnovaWordCount(blocks)
	reactions := int64(0)
	for _, c := range jx.Maps(jx.Map(res, "reactions"), "counters") {
		reactions += jx.Int(c, "count")
	}
	counters := jx.Map(res, "counters")
	return jx.M{"title": osnovaText(jx.Str(res, "title")), "lead": osnovaLead(raw, blocks), "cover": osnovaCover(raw),
		"rubric": osnovaText(jx.Str(jx.Map(res, "subsite"), "name")), "author": osnovaText(jx.Str(jx.Map(res, "author"), "name")),
		"published": jx.Int(res, "date"), "words": words, "minutes": max(1, (words+90)/180),
		"counters": jx.M{"views": jx.Int(counters, "views"), "comments": jx.Int(counters, "comments"), "likes": reactions}, "blocks": blocks}
}

// OsnovaArticle — статья vc.ru / dtf.ru по адресу; nil — не Osnova или не вышло.
func (a *App) OsnovaArticle(link string, force bool) jx.M {
	u, err := url.Parse(link)
	if err != nil {
		return nil
	}
	host := osnovaWWWRe.ReplaceAllString(strings.ToLower(u.Hostname()), "")
	api, ok := osnovaHosts[host]
	if !ok {
		return nil
	}
	m := osnovaIDRe.FindStringSubmatch(u.Path)
	if m == nil {
		return nil
	}
	key := "osnova:" + m[1]
	var cached struct {
		Data jx.M `json:"data"`
	}
	if !force && a.CacheGet(a.ctxOrBg(), key, &cached) {
		return cached.Data
	}
	HostThrottle(api, time.Second)
	r, err := a.FetchPublic("https://"+api+"/v2.10/content?id="+m[1], FetchOpts{MaxBytes: articleMaxBytes, Timeout: 10 * time.Second})
	var data jx.M
	ttl := time.Hour
	if err == nil && r.OK {
		if data = OsnovaFromAPI(r.Bytes, link); data != nil {
			ttl = articleCacheTTL
		}
	}
	a.CacheSet(a.ctxOrBg(), key, jx.M{"data": data}, ttl)
	return data
}

// ---- Новости: дообогащение, дубли, подпись ----

const (
	rssEnrichPerFeed   = 10
	NewsEnrichPerPass  = 40
	newsDuplicateShare = 0.6
	NewsDuplicateHours = 48
	newsKeyWords       = 6
	NewsSiteMaxAgeHours = 48
)

var newsStopwords = []string{"это", "как", "что", "при", "для", "над", "под", "про", "его", "её", "их", "или", "так", "уже", "еще", "ещё", "там", "тут",
	"the", "and", "for", "with", "from", "that", "this"}

var nonLetterRe = regexp.MustCompile(`[^\p{L}]+`)

func NewsStoryWords(title string) []string {
	var out []string
	for _, w := range nonLetterRe.Split(strings.ToLower(title), -1) {
		if runeLen(w) >= 3 && !contains(newsStopwords, w) {
			out = append(out, w)
		}
	}
	return uniqueStrings(out)
}

// NewsSimilar — доля общих основ (5 букв) не меньше newsDuplicateShare.
func NewsSimilar(x, y []string) bool {
	st := func(l []string) map[string]bool {
		m := map[string]bool{}
		for _, w := range l {
			m[trimRunes(w, 5)] = true
		}
		return m
	}
	a, b := st(x), st(y)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	common := 0
	union := map[string]bool{}
	for k := range a {
		union[k] = true
		if b[k] {
			common++
		}
	}
	for k := range b {
		union[k] = true
	}
	return float64(common)/float64(len(union)) >= newsDuplicateShare
}

func newsNeedsArticle(p jx.M) bool {
	if jx.Bool(p, "enriched") {
		return false
	}
	return jx.Str(p, "image") == "" || runeLen(jx.Str(p, "text")) < ArticleShortText
}

// NewsPostOutdated — у сайта свой max_age_hours, у остальных — ContentMaxAgeDays.
func NewsPostOutdated(p, entry jx.M) bool {
	if h := jx.Int(entry, "max_age_hours"); h > 0 {
		at := jx.Int(p, "posted_at")
		return at > 0 && now()-at > h*3600
	}
	return IsPostOutdated(jx.Int(p, "posted_at"), 0, ContentMaxAgeDays(false))
}

// NewsEnrichPosts — свежие записи дообогащаются со страницы статьи; без картинки после этого отбрасываются (кроме текстовых тем).
func (u *U) NewsEnrichPosts(posts []jx.M, entry jx.M) []jx.M {
	sortByPostedDesc(posts)
	budget := rssEnrichPerFeed
	var out []jx.M
	for _, p := range posts {
		if ArticleImageIsStub(jx.Str(p, "image")) {
			p["image"], p["images"] = "", []any{}
		}
		if newsNeedsArticle(p) && budget > 0 && (u.run == nil || u.run.NewsEnrichLeft > 0) {
			budget--
			if u.run != nil {
				u.run.NewsEnrichLeft--
			}
			p = u.ArticleEnrich(p, false)
		}
		if jx.Str(p, "image") == "" && !(jx.Bool(p, "article") && u.TopicsAcceptText(jx.Strs(entry, "topics"))) {
			continue
		}
		if jx.Bool(entry, "news") {
			p["news"] = true
			site := hostOf(jx.Str(p, "url"))
			if site == "" {
				site = hostOf(jx.Str(entry, "id"))
			}
			p["news_site"] = site
			first := strings.TrimSpace(strings.SplitN(jx.Str(p, "text"), "\n", 2)[0])
			w := NewsStoryWords(first)
			p["news_words"] = w[:min(len(w), newsKeyWords)]
		}
		out = append(out, p)
	}
	return out
}

var sentSplitRe = regexp.MustCompile(`([.!?])\s+`)

// NewsCaption — заголовок, выжимка (до трёх предложений) и «Источник: сайт — ссылка».
func NewsCaption(p jx.M) string {
	var lines []string
	for _, l := range strings.Split(jx.Str(p, "text"), "\n") {
		if l = strings.TrimSpace(l); l != "" && l != EmptyPostText {
			lines = append(lines, l)
		}
	}
	title := ""
	if len(lines) > 0 {
		title = lines[0]
	}
	excerpt := ""
	if len(lines) > 1 {
		excerpt = trimRunes(strings.TrimSpace(strings.Join(lines[1:], " ")), 400)
	}
	sents := strings.Split(sentSplitRe.ReplaceAllString(excerpt, "$1\x00"), "\x00")
	excerpt = strings.Join(sents[:min(3, len(sents))], " ")
	site := jx.Str(p, "news_site")
	if site == "" {
		site = hostOf(jx.Str(p, "url"))
	}
	link := jx.Str(p, "url")
	var b strings.Builder
	if title != "" {
		b.WriteString("**" + title + "**\n")
	}
	if excerpt != "" {
		b.WriteString(excerpt + "\n")
	}
	b.WriteString("Источник: " + site)
	if link != "" {
		b.WriteString(" — " + link)
	}
	return strings.TrimSpace(b.String())
}

// NewsExcerpt — выжимка новости своими словами (2–3 предложения) по тексту статьи.
func (u *U) NewsExcerpt(link string) (string, error) {
	if !strings.HasPrefix(link, "https://") {
		return "", Bad("Нужна ссылка на статью https://.")
	}
	body := strings.TrimSpace(jx.Str(u.ArticleEnrich(jx.M{"url": link}, false), "article_text"))
	if body == "" {
		return "", Bad("Не удалось прочитать статью, выжимка не сделана. Напишите текст вручную.")
	}
	if err := u.CaptionQuotaTake(); err != nil {
		return "", err
	}
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleText, MaxTokens: 240, Temperature: 0.3, UserID: u.ID,
		Prompt: "Напиши выжимку новости из текста ниже: 2–3 коротких предложения по-русски, своими словами, без цитат и без вступления. Не копируй фразы из текста. Ответь только выжимкой.\n\nТекст статьи:\n" + trimRunes(body, 4000)})
	if err != nil {
		return "", Bad("Модель не написала выжимку: попробуйте ещё раз.")
	}
	t := strings.Trim(strings.TrimSpace(r.Text), " \t\n\r\"«»")
	if t == "" {
		return "", Bad("Модель не написала выжимку: попробуйте ещё раз.")
	}
	return trimRunes(t, 400), nil
}

// NewsSites — проверенные ленты RSS новостных и тематических сайтов (темы — названия новой карты тем).
var NewsSites = []struct {
	ID, Name string
	Topics   []string
}{
	{"https://vc.ru/rss", "vc.ru", []string{"Предпринимательство", "Маркетинг и реклама", "Технологии"}},
	{"https://dtf.ru/rss", "dtf.ru", []string{"ПК и консоли", "Мобильные игры", "Игры"}},
	{"https://habr.com/ru/rss/articles/", "habr.com", []string{"Программирование", "Нейросети", "Технологии"}},
	{"https://rssexport.rbc.ru/rbcnews/news/30/full.rss", "rbc.ru", []string{"Экономика", "Личные финансы и налоги", "Мировые новости"}},
	{"https://lenta.ru/rss", "lenta.ru", []string{"Новости России и городов", "Мировые новости", "Политика"}},
	{"https://ria.ru/export/rss2/archive/index.xml", "ria.ru", []string{"Политика", "Мировые новости"}},
	{"https://tass.ru/rss/v2.xml", "tass.ru", []string{"Мировые новости", "Политика", "Экономика"}},
	{"https://3dnews.ru/news/rss/", "3dnews.ru", []string{"Гаджеты", "Железо и сборки ПК"}},
	{"https://www.sports.ru/rss/main.xml", "sports.ru", []string{"Футбол", "Спорт"}},
	{"https://www.vedomosti.ru/rss/news", "vedomosti.ru", []string{"Экономика", "Инвестиции и трейдинг"}},
	{"https://nplus1.ru/rss", "nplus1.ru", []string{"Космос", "Интересные факты", "Физика и математика"}},
	{"https://forklog.com/feed", "forklog.com", []string{"Криптовалюта"}},
	{"https://lifehacker.ru/feed/", "lifehacker.ru", []string{"Психология", "Медицина"}},
	{"https://realty.ria.ru/export/rss2/index.xml", "realty.ria.ru", []string{"Недвижимость"}},
	{"https://www.kolesa.ru/export/rss.xml", "kolesa.ru", []string{"Автомобили"}},
	{"https://www.zr.ru/rss-news/", "zr.ru", []string{"Автомобили"}},
	{"https://smart-lab.ru/rss/index/", "smart-lab.ru", []string{"Инвестиции и трейдинг"}},
	{"https://www.ixbt.com/export/rss.xml", "ixbt.com", []string{"Гаджеты", "Технологии"}},
	{"https://naked-science.ru/feed", "naked-science.ru", []string{"Космос", "История", "Интересные факты"}},
	{"https://www.kinonews.ru/rss/", "kinonews.ru", []string{"Фильмы", "Сериалы"}},
	{"https://1000.menu/feed/news", "1000.menu", []string{"Еда", "Рецепты"}},
}

// NewsSitesSeed — засев каталога лентами сайтов; повторный запуск ничего не дублирует.
func (a *App) NewsSitesSeed() (jx.M, error) {
	counts := jx.M{"added": 0, "topics": 0, "updated": 0}
	_, err := a.UpdateCatalog(a.ctxOrBg(), func(d *SourceDB) bool {
		for _, s := range NewsSites {
			found := false
			for _, e := range d.RSS {
				if !strings.EqualFold(jx.Str(e, "id"), s.ID) {
					continue
				}
				found = true
				have := jx.Strs(e, "topics")
				var missing []string
				for _, t := range s.Topics {
					if !contains(have, t) {
						missing = append(missing, t)
					}
				}
				e["topics"] = append(have, missing...)
				e["news"], e["max_age_hours"] = true, NewsSiteMaxAgeHours
				counts["topics"] = jx.ToInt(counts["topics"]) + int64(len(missing))
				if len(missing) > 0 {
					counts["updated"] = jx.ToInt(counts["updated"]) + 1
				}
			}
			if !found {
				d.RSS = append(d.RSS, jx.M{"id": s.ID, "name": s.Name, "topics": s.Topics, "news": true, "max_age_hours": NewsSiteMaxAgeHours})
				counts["added"] = jx.ToInt(counts["added"]) + 1
			}
		}
		return true
	})
	return counts, err
}

func sortByPostedDesc(posts []jx.M) {
	for i := 1; i < len(posts); i++ {
		for j := i; j > 0 && jx.Int(posts[j], "posted_at") > jx.Int(posts[j-1], "posted_at"); j-- {
			posts[j], posts[j-1] = posts[j-1], posts[j]
		}
	}
}

var _ = strconv.Itoa
