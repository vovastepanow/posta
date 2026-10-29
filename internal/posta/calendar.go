package posta

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Календарь: охват канала по часам (медиана просмотров по t.me/s/<канал>), прогноз просмотров, повторы в опубликованном,
// история канала (посты не через Posta) и «Заполнить дыры» — план из «Предложки» на свободные слоты.

const (
	reachDays          = 30
	reachMinPosts      = 10
	reachTrendPosts    = 30
	reachPages         = 3
	reachCacheSeconds  = 43200
	reachJobSeconds    = 900
	historyDays        = 7
	historyPages       = 12
	historyCacheSecond = 900
	repeatDays         = 60
	repeatTextMin      = 20
	FillMaxPosts       = 21
	fillMaxVideoRun    = 2
	minSecondsAhead    = 60
)

type ReachPost struct {
	ID    int64
	At    int64
	Views *int64
}

var reachViewsRe = regexp.MustCompile(`tgme_widget_message_views">([^<]*)`)

func ChannelReachPostsFromHTML(page string) []ReachPost {
	var out []ReachPost
	for _, b := range TelegramPageBlocks(page) {
		id, _ := strconv.ParseInt(b.ID, 10, 64)
		p := ReachPost{ID: id, At: TelegramPostTime(b.Content)}
		if m := reachViewsRe.FindStringSubmatch(b.Content); m != nil {
			v := TelegramParseCounter(m[1])
			p.Views = &v
		}
		out = append(out, p)
	}
	return out
}

func medianInt(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2] + 1) / 2
}

func isoWeekday(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

// Reach — охват по часам: hours (час → медиана), best (три лучших часа), days (1–7 → медиана), overall.
type Reach struct {
	Hours   map[int]int64 `json:"hours"`
	Best    []int         `json:"best"`
	Days    map[int]int64 `json:"days"`
	Overall int64         `json:"overall"`
	Posts   int           `json:"posts"`
	At      int64         `json:"at"`
}

func ChannelReachSummary(posts []ReachPost, tz string, at int64) *Reach {
	loc, _ := loadTZ(tz)
	byHour, byDay := map[int][]int64{}, map[int][]int64{}
	var all []int64
	for _, p := range posts {
		if p.Views == nil {
			continue
		}
		d := time.Unix(p.At, 0).In(loc)
		byHour[d.Hour()] = append(byHour[d.Hour()], *p.Views)
		byDay[isoWeekday(d)] = append(byDay[isoWeekday(d)], *p.Views)
		all = append(all, *p.Views)
	}
	if len(all) < reachMinPosts {
		return nil
	}
	r := &Reach{Hours: map[int]int64{}, Days: map[int]int64{}, Overall: medianInt(all), Posts: len(all), At: at}
	var best []int
	for h, v := range byHour {
		r.Hours[h] = medianInt(v)
		if r.Hours[h] > 0 {
			best = append(best, h)
		}
	}
	for d, v := range byDay {
		r.Days[d] = medianInt(v)
	}
	sort.Slice(best, func(i, j int) bool {
		if r.Hours[best[i]] != r.Hours[best[j]] {
			return r.Hours[best[i]] > r.Hours[best[j]]
		}
		return best[i] < best[j]
	})
	if len(best) > 3 {
		best = best[:3]
	}
	r.Best = orInts(best)
	return r
}

func orInts(l []int) []int {
	if l == nil {
		return []int{}
	}
	return l
}

// ChannelForecast — прогноз просмотров поста: медиана его часа; при ≥30 постах — поправка на день недели.
func ChannelForecast(r *Reach, at int64, tz string) *int64 {
	if r == nil {
		return nil
	}
	loc, _ := loadTZ(tz)
	d := time.Unix(at, 0).In(loc)
	hour, ok := r.Hours[d.Hour()]
	if !ok {
		return nil
	}
	factor := 1.0
	if day, ok := r.Days[isoWeekday(d)]; ok && r.Posts >= reachTrendPosts && r.Overall > 0 {
		factor = float64(day) / float64(r.Overall)
	}
	v := int64(float64(hour)*factor + 0.5)
	return &v
}

func (a *App) channelReachFetch(name string) []ReachPost {
	var posts []ReachPost
	var before int64
	cut := now() - reachDays*86400
	for page := 0; page < reachPages; page++ {
		html := a.TelegramChannelPageCached(name, before)
		if html == "" {
			break
		}
		batch := ChannelReachPostsFromHTML(html)
		if len(batch) == 0 {
			break
		}
		minID, minAt := batch[0].ID, batch[0].At
		for _, p := range batch {
			if p.At >= cut {
				posts = append(posts, p)
			}
			minID, minAt = min(minID, p.ID), min(minAt, p.At)
		}
		before = minID
		if minAt < cut {
			break
		}
	}
	return posts
}

func reachKey(name, tz string) string { return "reach:" + name + ":" + tz }

// ChannelReachRefresh — посчитать охват по сети и положить в кэш на 12 часов.
func (a *App) ChannelReachRefresh(name, tz string) *Reach {
	r := ChannelReachSummary(a.channelReachFetch(name), tz, now())
	a.KVSet(a.ctxOrBg(), reachKey(name, tz), jx.M{"at": now(), "data": r}, 7*24*time.Hour)
	return r
}

// jobDue — фоновая задача с ключом не чаще раза в period (true — пора, отметка поставлена).
func (a *App) jobDue(key string, period time.Duration) bool {
	started := false
	KVUpdate(a, a.ctxOrBg(), key, period*2, func(at int64, _ bool) *int64 {
		if at > 0 && now()-at < int64(period.Seconds()) {
			return nil
		}
		started = true
		t := now()
		return &t
	})
	return started
}

// ChannelReachCached — охват без ожидания сети: из кэша; устарел или нет — фоновое обновление.
func (a *App) ChannelReachCached(c *Channel) *Reach {
	name := strings.TrimPrefix(c.ChatUsername, "@")
	if !IsValidTelegramChannel(name) {
		return nil
	}
	tz := jx.Str(ChannelSchedule(c), "tz")
	var cached struct {
		At   int64  `json:"at"`
		Data *Reach `json:"data"`
	}
	have := a.KVGet(a.ctxOrBg(), reachKey(name, tz), &cached)
	if have && now()-cached.At < reachCacheSeconds {
		return cached.Data
	}
	if a.jobDue("reach_job:"+name+":"+tz, reachJobSeconds*time.Second) {
		a.Go("reach", func(context.Context) { a.ChannelReachRefresh(name, tz) })
	}
	return cached.Data
}

var (
	repeatURLRe   = regexp.MustCompile(`https?://\S+`)
	repeatPunctRe = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
)

// CalendarTextKey — текст для сравнения повторов: без ссылок, знаков и регистра; короткий — "".
func CalendarTextKey(text string) string {
	t := strings.ToLower(text)
	t = collapseSpaces(repeatPunctRe.ReplaceAllString(repeatURLRe.ReplaceAllString(t, " "), " "))
	if runeLen(t) < repeatTextMin {
		return ""
	}
	h := sha1.Sum([]byte(t))
	return "text:" + hex.EncodeToString(h[:])
}

func (u *U) CalendarPublishedRecent() []jx.M {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT at, data FROM published WHERE user_id = $1 AND at >= $2 ORDER BY at DESC LIMIT 500`, u.ID, now()-repeatDays*86400)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []jx.M
	for rows.Next() {
		var at int64
		var raw []byte
		if rows.Scan(&at, &raw) == nil {
			m := orEmpty(jx.Unmarshal(raw))
			m["at"] = at
			out = append(out, m)
		}
	}
	return out
}

func (a *App) calendarImageHash(src string, cache map[string]string) string {
	if h, ok := cache[src]; ok {
		return h
	}
	h := ""
	if p := a.MediaPath(src); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			h = ImagePhash(b)
		}
	}
	cache[src] = h
	return h
}

// CalendarDuplicateOf — в этом канале за 60 дней уже выходил пост с тем же текстом или той же/близкой картинкой: {at, url} или nil.
func (a *App) CalendarDuplicateOf(item jx.M, channelID string, published []jx.M, hashes map[string]string) jx.M {
	text := jx.Str(item, "text")
	if text == "" {
		text = jx.Str(item, "full_text")
	}
	key := CalendarTextKey(text)
	image := jx.Str(item, "image")
	if image == "" {
		image = jx.Str(item, "display_image")
	}
	hash := ""
	if image != "" {
		hash = a.calendarImageHash(image, hashes)
	}
	for _, p := range published {
		if jx.Str(p, "channel") != channelID {
			continue
		}
		hit := jx.M{"at": jx.Int(p, "at"), "url": jx.Str(p, "url")}
		if key != "" && key == CalendarTextKey(jx.Str(p, "text")) {
			return hit
		}
		for _, src := range jx.Strs(p, "media_src") {
			if image != "" && src == image {
				return hit
			}
			if hash != "" {
				if other := a.calendarImageHash(src, hashes); other != "" && PhashDistance(hash, other) <= config.PhashSameDistance {
					return hit
				}
			}
		}
	}
	return nil
}

var tgTextRe = regexp.MustCompile(`(?si)<div class="tgme_widget_message_text[^"]*"[^>]*>(.*?)</div>`)

func (a *App) channelHistoryFetch(name string) []jx.M {
	var posts []jx.M
	var before int64
	cut := now() - historyDays*86400
	for page := 0; page < historyPages; page++ {
		html := a.TelegramChannelPageCached(name, before)
		if html == "" {
			break
		}
		blocks := TelegramPageBlocks(html)
		if len(blocks) == 0 {
			break
		}
		var minID int64 = 1 << 62
		var minAt int64 = 1 << 62
		for i := len(blocks) - 1; i >= 0; i-- {
			b := blocks[i]
			id, _ := strconv.ParseInt(b.ID, 10, 64)
			minID = min(minID, id)
			at := TelegramPostTime(b.Content)
			if at > 0 {
				minAt = min(minAt, at)
			}
			if at < cut {
				continue
			}
			media := TelegramPostMedia(b.Content, 10)
			text := ""
			if m := tgTextRe.FindStringSubmatch(b.Content); m != nil {
				text = CleanHTMLText(m[1])
			}
			kind, thumb := "", ""
			if len(media) > 0 {
				kind, thumb = "photo", jx.Str(media[0], "image")
				for _, m := range media {
					if jx.Str(m, "video") != "" {
						kind = "video"
					}
				}
			}
			posts = append(posts, jx.M{"id": id, "at": at, "text": trimRunes(text, 600), "media": len(media), "thumb": thumb, "kind": kind,
				"url": "https://t.me/" + url.PathEscape(name) + "/" + b.ID})
		}
		if minAt == 1<<62 || minAt < cut {
			break
		}
		before = minID
	}
	return posts
}

// ChannelHistoryCached — история канала из кэша; устарела или нет — фоновое обновление. pending — данных ещё нет.
func (a *App) ChannelHistoryCached(name string, pending *bool) []jx.M {
	var cached struct {
		At    int64  `json:"at"`
		Posts []jx.M `json:"posts"`
	}
	have := a.KVGet(a.ctxOrBg(), "history:"+name, &cached)
	if have && now()-cached.At < historyCacheSecond {
		return cached.Posts
	}
	if a.jobDue("history_job:"+name, 2*time.Minute) {
		a.Go("channel-history", func(ctx context.Context) {
			a.KVSet(ctx, "history:"+name, jx.M{"at": now(), "posts": a.channelHistoryFetch(name)}, 2*24*time.Hour)
		})
	}
	if !have {
		*pending = true
	}
	return cached.Posts
}

// CalendarExternalPosts — посты, вышедшие в канале не через Posta, в виде отправленных постов календаря.
func (a *App) CalendarExternalPosts(channels []*Channel, sent []jx.M, pending *bool) []jx.M {
	out := []jx.M{}
	for _, c := range channels {
		name := strings.TrimPrefix(c.ChatUsername, "@")
		if !IsValidTelegramChannel(name) {
			continue
		}
		urls := map[string]bool{}
		var times []int64
		for _, e := range sent {
			if jx.Str(e, "channel") != c.ID {
				continue
			}
			if u := jx.Str(e, "url"); u != "" {
				urls[u] = true
			}
			t := jx.Int(e, "scheduled_for")
			if t == 0 {
				t = jx.Int(e, "at")
			}
			times = append(times, t)
		}
	posts:
		for _, p := range a.ChannelHistoryCached(name, pending) {
			if urls[jx.Str(p, "url")] {
				continue
			}
			for _, t := range times {
				if d := t - jx.Int(p, "at"); d <= 120 && d >= -120 {
					continue posts
				}
			}
			out = append(out, jx.M{"id": "", "at": jx.Int(p, "at"), "status": "sent", "text": trimRunes(jx.Str(p, "text"), 120), "full_text": jx.Str(p, "text"),
				"media": jx.Int(p, "media"), "kind": jx.Str(p, "kind"), "duration": 0, "poll": false, "url": jx.Str(p, "url"), "channel": c.ID,
				"channel_title": c.ChatTitle, "error": "", "external": true, "thumb": jx.Str(p, "thumb") != "", "thumb_url": jx.Str(p, "thumb")})
		}
	}
	return out
}

// ---- «Заполнить дыры» ----

// CalendarFreeSlots — свободные слоты расписания канала в [from, to), не раньше now+минута, без занятых очередью ±60 с.
func CalendarFreeSlots(c *Channel, queue []jx.M, from, to, nowTS int64) []int64 {
	s := ChannelSchedule(c)
	loc, _ := loadTZ(jx.Str(s, "tz"))
	slots := jx.Map(s, "slots")
	var taken []int64
	for _, p := range queue {
		if jx.Str(p, "channel") == c.ID {
			taken = append(taken, jx.Int(p, "publish_at"))
		}
	}
	earliest := max(from, nowTS+minSecondsAhead)
	var out []int64
	d := time.Unix(from, 0).In(loc)
	for day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc); day.Unix() < to; day = day.AddDate(0, 0, 1) {
		for _, hm := range jx.ToStrs(slots[strconv.Itoa(isoWeekday(day))]) {
			var h, m int
			if _, err := fmtSscanHM(hm, &h, &m); err != nil {
				continue
			}
			at := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc).Unix()
			if at < earliest || at >= to {
				continue
			}
			busy := false
			for _, t := range taken {
				if t-at <= 60 && at-t <= 60 {
					busy = true
				}
			}
			if !busy {
				out = append(out, at)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func fmtSscanHM(s string, h, m *int) (int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, Bad("время")
	}
	var err error
	if *h, err = strconv.Atoi(parts[0]); err != nil {
		return 0, err
	}
	*m, err = strconv.Atoi(parts[1])
	return 2, err
}

type FillSlot struct {
	At      int64
	Channel *Channel
	Best    bool
}

// CalendarPlanPick — на каждый слот лучший по relevance пост: без повтора в канале, без одинаковых картинок, без трёх видео подряд.
func (a *App) CalendarPlanPick(slots []FillSlot, candidates, history []jx.M, limit int) []jx.M {
	cands := append([]jx.M(nil), candidates...)
	sort.SliceStable(cands, func(i, j int) bool { return jx.Int(cands[i], "relevance") > jx.Int(cands[j], "relevance") })
	used := map[string]bool{}
	videoRun := map[string]int{}
	hashes := map[string]string{}
	plan := []jx.M{}
	for _, slot := range slots {
		if len(plan) >= limit {
			break
		}
		cid := slot.Channel.ID
		for i, c := range cands {
			if c == nil {
				continue
			}
			image := jx.Str(c, "image")
			if image == "" {
				image = jx.Str(c, "display_image")
			}
			if image == "" || used[image] {
				continue
			}
			video := jx.Str(c, "format") == "video"
			if video && videoRun[cid] >= fillMaxVideoRun {
				continue
			}
			if a.CalendarDuplicateOf(c, cid, history, hashes) != nil {
				continue
			}
			used[image] = true
			if video {
				videoRun[cid]++
			} else {
				videoRun[cid] = 0
			}
			display := jx.Str(c, "display_image")
			if display == "" {
				display = image
			}
			plan = append(plan, jx.M{"slot_at": slot.At, "channel": cid, "channel_title": slot.Channel.ChatTitle, "image": image, "display": display,
				"text": PostCaptionClean(jx.Str(c, "text")), "relevance": jx.Int(c, "relevance"), "format": jx.Str(c, "format"), "best": slot.Best})
			cands[i] = nil
			break
		}
	}
	return plan
}

// CalendarHistory — опубликованное за 60 дней и посты очереди (их картинки — по адресам источников).
func (u *U) CalendarHistory(queue []jx.M) []jx.M {
	var out []jx.M
	for _, p := range queue {
		out = append(out, jx.M{"channel": jx.Str(p, "channel"), "text": jx.Str(p, "text"), "at": jx.Int(p, "publish_at"), "url": "", "media_src": MediaSourcesOf(jx.Maps(p, "media"))})
	}
	return append(out, u.CalendarPublishedRecent()...)
}

// CalendarFillPlan — свободные слоты каналов в [from, to): лучшие часы канала первыми; с includeRest — и остальные.
func (u *U) CalendarFillPlan(channels []*Channel, from, to int64, includeRest bool, pool, queue []jx.M) []jx.M {
	history := u.CalendarHistory(queue)
	var slots []FillSlot
	for _, c := range channels {
		var best []int
		if r := u.ChannelReachCached(c); r != nil {
			best = r.Best
		}
		loc, _ := loadTZ(jx.Str(ChannelSchedule(c), "tz"))
		for _, at := range CalendarFreeSlots(c, queue, from, to, now()) {
			isBest := contains(best, time.Unix(at, 0).In(loc).Hour())
			if isBest || includeRest {
				slots = append(slots, FillSlot{at, c, isBest})
			}
		}
	}
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].Best != slots[j].Best {
			return slots[i].Best
		}
		return slots[i].At < slots[j].At
	})
	return u.CalendarPlanPick(slots, pool, history, FillMaxPosts)
}

func calendarFillRecord(image string, pool []jx.M, h History) jx.M {
	lists := [][]jx.M{pool}
	for _, c := range config.ProposalCollections {
		lists = append(lists, h[c])
	}
	for _, l := range lists {
		for _, r := range l {
			if ContentImageKey(r) == image {
				return r
			}
		}
	}
	return nil
}

// CalendarFillApply — каждый пост ставится той же подготовкой и очередью, что обычная публикация; частичный успех — не ошибка.
func (u *U) CalendarFillApply(items []jx.M) (jx.M, error) {
	if len(items) > FillMaxPosts {
		return nil, Bad("За раз ставится не больше %d постов.", FillMaxPosts)
	}
	posted, errs := []string{}, []jx.M{}
	pool, history := u.Pool(), u.History()
	for _, item := range items {
		at := jx.Int(item, "slot_at")
		group := randHex(6)
		chans := jx.Strs(item, "channels")
		if len(chans) == 0 {
			chans = []string{jx.Str(item, "channel")}
		}
		for _, cid := range uniqueStrings(chans) {
			id, err := u.fillOne(item, cid, at, group, pool, history)
			if err != nil {
				errs = append(errs, jx.M{"slot_at": at, "channel": cid, "error": err.Error()})
				continue
			}
			posted = append(posted, id)
		}
	}
	return jx.M{"posted": posted, "errors": errs}, nil
}

func (u *U) fillOne(item jx.M, cid string, at int64, group string, pool []jx.M, history History) (string, error) {
	if at < now()+minSecondsAhead {
		return "", Bad("Слот уже прошёл — выберите другой.")
	}
	c, err := u.PublishChannelResolve(cid)
	if err != nil {
		return "", err
	}
	image := jx.Str(item, "image")
	record := calendarFillRecord(image, pool, history)
	var tags []string
	if record != nil {
		tags = u.ChannelHashtags(c, u.PostHashtags(record))
	}
	text := TextWithHashtags(strings.TrimSpace(jx.Str(item, "text")), tags, config.TelegramCaptionLimit)
	if d := u.BillingPostDenied(true); d != "" {
		return "", Bad("%s", d)
	}
	if d := u.PublishMediaDenied([]jx.M{{"src": image}}); d != "" {
		return "", Bad("%s", d)
	}
	if err := TelegramCheckText(text, 1); err != nil {
		return "", err
	}
	src := image
	if record != nil {
		if local := u.MediaExisting(jx.Str(record, "image_local")); local != "" {
			src = local
		}
	}
	work, err := TelegramWorkDir()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	prepared, err := u.TelegramPrepareMedia([]MediaItem{{Src: src, Name: "фото"}}, nil, work, "bot", nil)
	if err != nil {
		return "", err
	}
	r, err := u.OutboxQueuePost(c, text, prepared, at, &at, work, nil, nil, group, "")
	if err != nil {
		return "", err
	}
	u.BillingPostUsed()
	u.KickPublisher()
	return jx.Str(r, "id"), nil
}
