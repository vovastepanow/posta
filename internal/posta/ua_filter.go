package posta

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Фильтр украинского контента: источники из Украины, тексты на украинском и посты имиджборд с флагом UA.
// Переключатель — kv admin:ua_filter (по умолчанию включён). Работает по уже скачанным данным: ни сети, ни модели.
// Посты российских источников, где просто упоминается Украина, не трогаем: фильтр по происхождению и языку, а не по теме.

var uaFilterWords = []string{"що", "це", "який", "яка", "які", "та", "але", "від", "для", "щоб", "також", "вже", "дуже", "ще", "тут", "бути", "буде",
	"немає", "україни", "україна", "київ", "гривень"}

const uaSourcePostsMax = 3

var uaZones = map[string]string{"ua": "", "укр": "xn--j1amh"}

// Список блокировки по умолчанию; правки админки (kv admin:ua_blocklist) главнее.
var uaDefaultBlocklist = UABlocklist{
	Domains: []string{"ua", "укр", "pravda.com.ua", "unian.net", "unian.ua", "censor.net", "korrespondent.net", "rbc.ua", "tsn.ua", "nv.ua", "liga.net",
		"segodnya.ua", "ukrinform.ua", "obozrevatel.com", "strana.today", "strana.ua", "focus.ua", "24tv.ua", "hromadske.ua", "suspilne.media", "epravda.com.ua"},
	Sources: []string{},
}

type UABlocklist struct {
	Domains []string `json:"domains"`
	Sources []string `json:"sources"`
}

type uaBlocked struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
}

type uaStateT struct {
	On        bool
	Blocklist UABlocklist
	Blocked   map[string]uaBlocked
	Allowed   []string
	Read      time.Time
}

var uaCache struct {
	sync.Mutex
	s *uaStateT
}

var (
	schemeRe = regexp.MustCompile(`^https?://`)
	pathTail = regexp.MustCompile(`/.*$`)
)

func uaNormEntry(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = schemeRe.ReplaceAllString(v, "")
	v = strings.TrimPrefix(strings.TrimPrefix(v, "www."), "@")
	return strings.TrimSpace(pathTail.ReplaceAllString(v, ""))
}

func uaClean(l UABlocklist) UABlocklist {
	out := UABlocklist{Domains: []string{}, Sources: []string{}}
	for _, d := range l.Domains {
		if d = uaNormEntry(d); d != "" {
			out.Domains = append(out.Domains, d)
		}
	}
	for _, s := range l.Sources {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out.Sources = append(out.Sources, s)
		}
	}
	out.Domains, out.Sources = uniqueStrings(out.Domains), uniqueStrings(out.Sources)
	return out
}

// uaState — переключатель, список, запомненные решения; перечитывается не чаще раза в 30 с.
func (a *App) uaState(fresh bool) *uaStateT {
	uaCache.Lock()
	defer uaCache.Unlock()
	if !fresh && uaCache.s != nil && time.Since(uaCache.s.Read) < 30*time.Second {
		return uaCache.s
	}
	ctx := a.ctxOrBg()
	bl := uaDefaultBlocklist
	a.KVGet(ctx, "admin:ua_blocklist", &bl)
	s := &uaStateT{On: KV(a, ctx, "admin:ua_filter", true), Blocklist: uaClean(bl), Blocked: KV(a, ctx, "admin:ua_blocked_sources", map[string]uaBlocked{}),
		Allowed: KV(a, ctx, "admin:ua_allowed_sources", []string{}), Read: time.Now()}
	uaCache.s = s
	return s
}

func (a *App) UAFilterOn() bool { return a.uaState(false).On }

func (a *App) UAFilterSet(ctx context.Context, on bool) {
	a.KVSet(ctx, "admin:ua_filter", on, 0)
	a.uaState(true)
}

func (a *App) UABlocklistLoad() UABlocklist { return a.uaState(false).Blocklist }

func (a *App) UABlocklistSave(ctx context.Context, l UABlocklist) UABlocklist {
	c := uaClean(l)
	a.KVSet(ctx, "admin:ua_blocklist", c, 0)
	a.uaState(true)
	return c
}

var (
	uaLinkRe    = regexp.MustCompile(`https?://\S+`)
	uaMentionRe = regexp.MustCompile(`[@#]\S+`)
	uaLettersRe = regexp.MustCompile(`[їєґЇЄҐ]`)
	uaIRe       = regexp.MustCompile(`[іІ]`)
	uaBelRe     = regexp.MustCompile(`[ўЎ]`)
	uaWordRes   = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, w := range uaFilterWords {
			out = append(out, regexp.MustCompile(`(?i)(?:^|[^\p{L}])`+regexp.QuoteMeta(w)+`(?:$|[^\p{L}])`))
		}
		return out
	}()
)

// TextIsUkrainian — есть ї, є или ґ; либо і (без белорусского ў) и два и больше частых украинских слова.
func TextIsUkrainian(text string) bool {
	clean := uaMentionRe.ReplaceAllString(uaLinkRe.ReplaceAllString(text, " "), " ")
	if strings.TrimSpace(clean) == "" {
		return false
	}
	if uaLettersRe.MatchString(clean) {
		return true
	}
	if !uaIRe.MatchString(clean) || uaBelRe.MatchString(clean) {
		return false
	}
	found := 0
	for _, re := range uaWordRes {
		if re.MatchString(clean) {
			if found++; found >= 2 {
				return true
			}
		}
	}
	return false
}

// UAURLBlocked — адрес на украинском домене или зоне.
func (a *App) UAURLBlocked(raw string) bool {
	if !a.UAFilterOn() || raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "" {
		return false
	}
	for _, d := range a.uaState(false).Blocklist.Domains {
		if puny, zone := uaZones[d]; zone {
			if strings.HasSuffix(host, "."+d) || (puny != "" && strings.HasSuffix(host, "."+puny)) {
				return true
			}
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

var uaMarksRe = regexp.MustCompile(`(?i)🇺🇦|(?:украин|україн)\p{L}*[^.!?\n]{0,40}(?:наш|наша|новост|канал|зсу|всу)|(?:наш|наша|новост|канал|зсу|всу)[^.!?\n]{0,40}(?:украин|україн)\p{L}*`)

// UASourceBlocked — источник из Украины: причина или "".
func (a *App) UASourceBlocked(sourceID string, info jx.M) string {
	if !a.UAFilterOn() {
		return ""
	}
	s := a.uaState(false)
	sid := strings.ToLower(sourceID)
	if contains(s.Allowed, sid) {
		return ""
	}
	if contains(s.Blocklist.Sources, sid) {
		return "в списке блокировки"
	}
	if b, ok := s.Blocked[sid]; ok {
		return b.Reason
	}
	if strings.HasPrefix(sid, "rss:") && a.UAURLBlocked(sid[4:]) {
		return "сайт или лента на украинском домене"
	}
	if u := jx.Str(info, "url"); u != "" && a.UAURLBlocked(u) {
		return "адрес на украинском домене"
	}
	text := strings.TrimSpace(jx.Str(info, "title") + "\n" + jx.Str(info, "description"))
	if text != "" && TextIsUkrainian(text) {
		return "украинский текст в названии или описании"
	}
	if text != "" && uaMarksRe.MatchString(text) {
		return "украинская символика в названии или описании"
	}
	country := info["country"]
	cid := jx.ToInt(country)
	if m, ok := country.(jx.M); ok {
		cid = jx.Int(m, "id")
	}
	if cid == 2 && strings.HasPrefix(sid, "vk:") {
		return "сообщество из Украины (VK)"
	}
	cc := jx.Str(info, "channel_country")
	if cc == "" {
		cc = jx.Str(info, "location")
	}
	if strings.ToUpper(cc) == "UA" {
		return "страна канала — Украина"
	}
	return ""
}

// UAItemBlocked — пост из украинского источника или на украинском: причина или "".
func (a *App) UAItemBlocked(item jx.M) string {
	if !a.UAFilterOn() {
		return ""
	}
	if strings.ToUpper(jx.Str(item, "country")) == "UA" {
		return "флаг Украины на имиджборде"
	}
	if r := a.UASourceBlocked(ContentSourceKey(item), nil); r != "" {
		return "источник: " + r
	}
	for _, f := range []string{"url", "link", "article_url"} {
		if a.UAURLBlocked(jx.Str(item, f)) {
			return "адрес поста из Украины"
		}
	}
	texts := []string{AnalysisText(item), jx.Str(item, "text"), jx.Str(item, "title")}
	for _, c := range jx.List(item, "captions") {
		texts = append(texts, jx.ToStr(c))
	}
	for _, t := range texts {
		if TextIsUkrainian(t) {
			return "текст на украинском"
		}
	}
	return ""
}

func (a *App) UABlockSource(ctx context.Context, sid, name, reason string) {
	KVUpdate(a, ctx, "admin:ua_blocked_sources", 0, func(m map[string]uaBlocked, _ bool) *map[string]uaBlocked {
		if m == nil {
			m = map[string]uaBlocked{}
		}
		m[strings.ToLower(sid)] = uaBlocked{name, reason, now()}
		return &m
	})
	a.uaState(true)
}

func (a *App) UAUnblockSource(ctx context.Context, sid string) {
	sid = strings.ToLower(sid)
	KVUpdate(a, ctx, "admin:ua_blocked_sources", 0, func(m map[string]uaBlocked, _ bool) *map[string]uaBlocked {
		delete(m, sid)
		return &m
	})
	KVUpdate(a, ctx, "admin:ua_allowed_sources", 0, func(l []string, _ bool) *[]string {
		l = uniqueStrings(append(l, sid))
		return &l
	})
	a.uaState(true)
}

func (a *App) UABlockedSources() map[string]uaBlocked { return a.uaState(false).Blocked }

// UACountSkipped — отсеяно постов за проход (статистика за сутки).
func (a *App) UACountSkipped(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	KVUpdate(a, ctx, "admin:ua_stats", 0, func(l [][2]int64, _ bool) *[][2]int64 {
		var out [][2]int64
		for _, e := range append(l, [2]int64{now(), int64(n)}) {
			if now()-e[0] < config.DaySeconds {
				out = append(out, e)
			}
		}
		return &out
	})
}

func (a *App) UASkippedLastDay(ctx context.Context) int {
	n := 0
	for _, e := range KV(a, ctx, "admin:ua_stats", [][2]int64{}) {
		if now()-e[0] < config.DaySeconds {
			n += int(e[1])
		}
	}
	return n
}

func (a *App) UAFilterPosts(posts []jx.M) []jx.M {
	out := []jx.M{}
	for _, p := range posts {
		if a.UAItemBlocked(p) == "" {
			out = append(out, p)
		}
	}
	return out
}

// UARecordHidden — запись не показываем и не публикуем.
func (a *App) UARecordHidden(r jx.M) bool {
	return a.UAFilterOn() && (jx.Bool(r, "ua_blocked") || a.UAItemBlocked(r) != "")
}
