package posta

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// «Зеркало источника»: каждый новый пост личного источника без оценки моделью — в подборку или сразу в очередь канала.

const (
	mirrorMaxPerUser = 10
	mirrorRecheck    = 900
	mirrorBudget     = 20 * time.Second
	mirrorFirstRun   = 3
	mirrorKeepKeys   = 60
	mirrorWordsMax   = 50
	mirrorReplaceMax = 20
	mirrorRegexMax   = 200
	mirrorAppendMax  = 500
)

var MirrorModes = []string{"pool", "queue"}

// MirrorRules — правила зеркала (проверенные).
type MirrorRules struct {
	Include       []string     `json:"include"`
	Exclude       []string     `json:"exclude"`
	Replace       []MirrorPair `json:"replace"`
	StripLinks    bool         `json:"strip_links"`
	StripMentions bool         `json:"strip_mentions"`
	StripHashtags bool         `json:"strip_hashtags"`
	Append        string       `json:"append"`
}

type MirrorPair struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Regex bool   `json:"regex"`
}

// MirrorRulesClean — правила из формы; плохое регулярное выражение — ошибка сохранения. Regexp в Go без возвратов (RE2): зависаний нет.
func MirrorRulesClean(in jx.M) (MirrorRules, error) {
	words := func(k string) []string {
		out := []string{}
		for _, w := range jx.Strs(in, k) {
			if w = trimRunes(strings.TrimSpace(w), 100); w != "" && len(out) < mirrorWordsMax {
				out = append(out, w)
			}
		}
		return out
	}
	r := MirrorRules{Include: words("include"), Exclude: words("exclude"), Replace: []MirrorPair{},
		StripLinks: jx.Bool(in, "strip_links"), StripMentions: jx.Bool(in, "strip_mentions"), StripHashtags: jx.Bool(in, "strip_hashtags"),
		Append: trimRunes(strings.TrimSpace(jx.Str(in, "append")), mirrorAppendMax)}
	for i, pair := range jx.Maps(in, "replace") {
		if i >= mirrorReplaceMax {
			break
		}
		raw := strings.TrimSpace(jx.Str(pair, "from"))
		if raw == "" {
			continue
		}
		re := jx.Bool(pair, "regex")
		if re {
			if _, err := regexp.Compile(raw); err != nil || runeLen(raw) > mirrorRegexMax {
				return r, Bad("Регулярное выражение не работает или длиннее %d знаков: %s", mirrorRegexMax, trimRunes(raw, 60))
			}
		}
		r.Replace = append(r.Replace, MirrorPair{trimRunes(raw, 200), trimRunes(jx.Str(pair, "to"), 200), re})
	}
	return r, nil
}

var (
	mirrorLinkRe    = regexp.MustCompile(`https?://\S+`)
	mirrorMentionRe = regexp.MustCompile(`(^|[^\w@])@[A-Za-z0-9_]{3,}`)
	mirrorTagRe     = regexp.MustCompile(`(^|[^\p{L}\p{N}_])#[\p{L}\p{N}_]+`)
	mirrorSpacesRe  = regexp.MustCompile(`[ \t]{2,}`)
)

// MirrorRulesApply — текст поста по правилам; ok=false — пост отсеян.
func MirrorRulesApply(text string, r MirrorRules) (string, bool) {
	low := strings.ToLower(text)
	if len(r.Include) > 0 {
		hit := false
		for _, w := range r.Include {
			if strings.Contains(low, strings.ToLower(w)) {
				hit = true
			}
		}
		if !hit {
			return "", false
		}
	}
	for _, w := range r.Exclude {
		if w != "" && strings.Contains(low, strings.ToLower(w)) {
			return "", false
		}
	}
	for _, p := range r.Replace {
		if p.Regex {
			if re, err := regexp.Compile(p.From); err == nil {
				text = re.ReplaceAllString(text, p.To)
			}
		} else {
			text = strings.ReplaceAll(text, p.From, p.To)
		}
	}
	if r.StripLinks {
		text = mirrorLinkRe.ReplaceAllString(text, "")
	}
	if r.StripMentions {
		text = mirrorMentionRe.ReplaceAllString(text, "$1")
	}
	if r.StripHashtags {
		text = mirrorTagRe.ReplaceAllString(text, "$1")
	}
	text = strings.TrimSpace(mirrorSpacesRe.ReplaceAllString(text, " "))
	if a := strings.TrimSpace(r.Append); a != "" {
		text = strings.TrimSpace(text + "\n" + a)
	}
	return text, true
}

type mirrorRow struct {
	ID, Platform, SourceID, Name, Mode string
	ChannelID                          *string
	Rules                              MirrorRules
	Seen                               []string
	LastChecked, Created               int64
}

func (u *U) mirrorRows() []mirrorRow {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT id, platform, source_id, name, channel_id, mode, rules, last_post_key, last_checked_at, created_at FROM mirrors
		WHERE user_id = $1 ORDER BY created_at, id`, u.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []mirrorRow
	for rows.Next() {
		var m mirrorRow
		var rules, seen []byte
		if rows.Scan(&m.ID, &m.Platform, &m.SourceID, &m.Name, &m.ChannelID, &m.Mode, &rules, &seen, &m.LastChecked, &m.Created) == nil {
			jsonInto(rules, &m.Rules)
			jsonInto(seen, &m.Seen)
			out = append(out, m)
		}
	}
	return out
}

func jsonInto(b []byte, dest any) { _ = jsonUnmarshal(b, dest) }

// MirrorList — зеркала для страницы.
func (u *U) MirrorList() []jx.M {
	out := []jx.M{}
	for _, m := range u.mirrorRows() {
		title := ""
		var ch any
		if m.ChannelID != nil {
			ch = *m.ChannelID
			if c := u.Channel(*m.ChannelID); c != nil {
				title = c.ChatTitle
			}
		}
		out = append(out, jx.M{"id": m.ID, "name": m.Name, "platform": m.Platform, "mode": m.Mode, "channel_id": ch, "channel_title": title, "rules": m.Rules,
			"last_checked_at": m.LastChecked})
	}
	return out
}

// MirrorSave — новое зеркало: ссылка → источник, режим и (для очереди) свой канал с расписанием.
func (u *U) MirrorSave(in jx.M) (jx.M, error) {
	rows := u.mirrorRows()
	if len(rows) >= mirrorMaxPerUser {
		return nil, Bad("Зеркал не больше %d: удалите одно, чтобы добавить новое.", mirrorMaxPerUser)
	}
	platform, id, err := u.SourceParseLink(jx.Str(in, "link"))
	if err != nil {
		return nil, err
	}
	mode := jx.Str(in, "mode")
	if mode == "" {
		mode = "pool"
	}
	if !contains(MirrorModes, mode) {
		return nil, Bad("Неизвестный режим зеркала.")
	}
	var channelID *string
	if mode == "queue" {
		c := u.Channel(jx.Str(in, "channel"))
		if c == nil {
			return nil, Bad("Выберите свой канал для очереди.")
		}
		if len(jx.Map(ChannelSchedule(c), "slots")) == 0 {
			return nil, Bad("У канала не настроено расписание: «Соцсети» → канал → «Расписание».")
		}
		channelID = &c.ID
	}
	rules, err := MirrorRulesClean(jx.Map(in, "rules"))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		same := (r.ChannelID == nil && channelID == nil) || (r.ChannelID != nil && channelID != nil && *r.ChannelID == *channelID)
		if r.Platform == platform && r.SourceID == id && r.Mode == mode && same {
			return nil, Bad("Такое зеркало уже есть.")
		}
	}
	mid := randHex(6)
	name := trimRunes(SourceDisplayName(platform, id), 160)
	if _, err := u.DB.Pool.Exec(u.Ctx, `INSERT INTO mirrors (id, user_id, platform, source_id, name, channel_id, mode, rules, last_post_key, last_checked_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '[]', 0, $9)`, mid, u.ID, platform, id, name, channelID, mode, jx.Marshal(rules), now()); err != nil {
		return nil, err
	}
	return jx.M{"id": mid, "name": name, "platform": platform, "mode": mode, "channel_id": channelID, "rules": rules}, nil
}

func (u *U) MirrorDelete(id string) {
	u.DB.Pool.Exec(u.Ctx, `DELETE FROM mirrors WHERE id = $1 AND user_id = $2`, id, u.ID)
}

func (u *U) mirrorFetchPosts(m mirrorRow) []jx.M {
	if u.UASourceBlocked(SourceIDFor(m.Platform, m.SourceID), nil) != "" {
		return nil
	}
	var posts []jx.M
	switch m.Platform {
	case PlatformTelegram:
		posts = u.BestPostsFromTelegram(m.SourceID, 2)
	case PlatformVK:
		posts = u.BestPostsFromVK(u.Ctx, m.SourceID, 2)
	case PlatformOK:
		posts = u.BestPostsFromOK(m.SourceID)
	default:
		posts = u.BestPostsFromFeed(m.SourceID)
	}
	posts = u.UAFilterPosts(posts)
	sort.SliceStable(posts, func(i, j int) bool { return jx.Int(posts[i], "posted_at") < jx.Int(posts[j], "posted_at") })
	return posts
}

// mirrorQueuePost — тот же путь, что ручная постановка в очередь (расписание, медиа, журнал очереди).
func (u *U) mirrorQueuePost(m mirrorRow, post jx.M) error {
	if m.ChannelID == nil {
		return Bad("канал зеркала не найден")
	}
	c := u.Channel(*m.ChannelID)
	if c == nil {
		return Bad("канал зеркала не найден")
	}
	if len(jx.Map(ChannelSchedule(c), "slots")) == 0 {
		return Bad("у канала не настроено расписание")
	}
	if u.BillingPostDenied(true) != "" {
		return Bad("лимит постов по тарифу исчерпан")
	}
	item, err := u.BestPostPoolItem(post, true)
	if err != nil {
		return err
	}
	var media []MediaItem
	if v := jx.Str(item, "video_local"); v != "" {
		media = []MediaItem{{Kind: "video", Src: v, Name: "видео"}}
	} else {
		gallery := jx.Maps(item, "gallery")
		if len(gallery) == 0 {
			gallery = []jx.M{{"image": item["image"], "image_local": item["image_local"]}}
		}
		for _, g := range gallery {
			src := jx.Str(g, "image_local")
			if src == "" {
				src = jx.Str(g, "image")
			}
			media = append(media, MediaItem{Kind: "photo", Src: src, Name: "фото"})
		}
	}
	limit := config.TelegramTextLimit
	if len(media) > 0 {
		limit = config.TelegramCaptionLimit
	}
	text := TextWithHashtags(jx.Str(item, "text"), u.ChannelHashtags(c, u.PostHashtags(item)), limit)
	if err := TelegramCheckText(text, len(media)); err != nil {
		return err
	}
	slot := u.ChannelNextSlot(c, now(), u.Outbox())
	if slot == nil {
		return Bad("в расписании канала нет свободного слота")
	}
	work, err := TelegramWorkDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	prepared, err := u.TelegramPrepareMedia(media, nil, work, "bot", nil)
	if err != nil {
		return err
	}
	slotAt := jx.Int(slot, "slot_at")
	if _, err := u.OutboxQueuePost(c, text, prepared, jx.Int(slot, "publish_at"), &slotAt, work, nil, nil, "", ""); err != nil {
		return err
	}
	u.BillingPostUsed()
	return nil
}

func (u *U) mirrorRunOne(m mirrorRow) map[string]int {
	posts := u.mirrorFetchPosts(m)
	var fresh []jx.M
	if len(m.Seen) > 0 {
		for _, p := range posts {
			if !contains(m.Seen, jx.Str(p, "post_id")) {
				fresh = append(fresh, p)
			}
		}
	} else if len(posts) > mirrorFirstRun {
		fresh = posts[len(posts)-mirrorFirstRun:]
	} else {
		fresh = posts
	}
	keep := append([]string{}, m.Seen...)
	for _, p := range posts {
		keep = append(keep, jx.Str(p, "post_id"))
	}
	keep = uniqueStrings(keep)
	if len(keep) > mirrorKeepKeys {
		keep = keep[len(keep)-mirrorKeepKeys:]
	}
	counts := map[string]int{"new": len(fresh), "pooled": 0, "queued": 0, "skipped": 0}
	var items []jx.M
	videos := 0
	for _, post := range fresh {
		post["source_key"] = SourceIDFor(m.Platform, m.SourceID)
		text, ok := MirrorRulesApply(jx.Str(post, "text"), m.Rules)
		if !ok {
			counts["skipped"]++
			continue
		}
		post["text"] = text
		var err error
		if m.Mode == "queue" {
			if err = u.mirrorQueuePost(m, post); err == nil {
				counts["queued"]++
			}
		} else {
			withVideo := jx.Str(post, "video") != "" && videos < BestTakeVideos
			var it jx.M
			if it, err = u.BestPostPoolItem(post, withVideo); err == nil {
				items = append(items, it)
				if jx.Str(it, "video_local") != "" {
					videos++
				}
			}
		}
		if err != nil {
			counts["skipped"]++
			u.Log("-> Зеркало «%s»: пост пропущен — %s", m.Name, err)
		}
	}
	if len(items) > 0 {
		counts["pooled"] = u.OwnAddToPool(items)
	}
	u.DB.Pool.Exec(u.Ctx, `UPDATE mirrors SET last_post_key = $1, last_checked_at = $2 WHERE id = $3`, jx.Marshal(keep), now(), m.ID)
	return counts
}

// MirrorCollectUser — все давно проверенные зеркала пользователя; общий бюджет — 20 с.
func (u *U) MirrorCollectUser() map[string]int {
	started := time.Now()
	total := map[string]int{"mirrors": 0, "new": 0, "pooled": 0, "queued": 0, "skipped": 0}
	for _, m := range u.mirrorRows() {
		if now()-m.LastChecked < mirrorRecheck {
			continue
		}
		if time.Since(started) > mirrorBudget {
			u.Log("Зеркала: время на проверку вышло, остальные — в следующий проход.")
			break
		}
		r := u.mirrorRunOne(m)
		u.Log("Зеркало «%s»: новых постов %d, в подборку %d, в очередь %d, пропущено %d.", m.Name, r["new"], r["pooled"], r["queued"], r["skipped"])
		total["mirrors"]++
		for k, v := range r {
			total[k] += v
		}
	}
	if total["queued"] > 0 {
		u.KickPublisher()
	}
	return total
}
