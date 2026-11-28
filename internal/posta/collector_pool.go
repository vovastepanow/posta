package posta

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
)

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
	sourceKey := ContentSourceKey(item)
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
		var fresh []jx.M
		if visible != nil {
			fresh = u.visibleFreshPool(pool, topicKey, visible)
		} else {
			fresh = u.freshPoolItems(pool)
		}
		if candidateIsKnown(item, recordKnownKeys(pool)) {
			autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_duplicate", 1)
			return nil, nil
		}
		sourceCount, freshCount := 0, 0
		for _, record := range fresh {
			if visible == nil && TopicNorm(jx.Str(record, "topic")) != topic {
				continue
			}
			freshCount++
			if ContentSourceKey(record) == sourceKey {
				sourceCount++
			}
		}
		if sourceCount >= sourcePoolLimit(u.TInt("per_source_pool"), target, freshCount) {
			autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_source_limit", 1)
			return nil, nil
		}
		if _, _, _, valid := autopilotSourceParts(sourceKey); valid {
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
			if !autopilotProbationPoolAllowed(fresh, topicKey, sourceKey, probation, target, u.TInt("autopilot_probation_pool_percent")) {
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
			count, oldest := 0, -1
			for i, it := range pool {
				if TopicNorm(jx.Str(it, "topic")) != topic {
					continue
				}
				count++
				if !u.poolItemFresh(it) && (oldest < 0 || jx.Int(it, "pooled_at") < jx.Int(pool[oldest], "pooled_at")) {
					oldest = i
				}
			}
			if count >= target {
				if oldest < 0 {
					autopilotDeliveryAuditCount(u.Ctx, u.ID, "pool_full", 1)
					return nil, nil
				}
				pool = append(pool[:oldest], pool[oldest+1:]...)
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
