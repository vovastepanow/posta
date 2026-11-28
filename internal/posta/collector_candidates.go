package posta

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// candidatePlatformOrder also accepts search platforms added in the future.
func candidatePlatformOrder(by map[string][]jx.M) []string {
	order := []string{PlatformTelegram, PlatformVK, PlatformRSS, PlatformOK, PlatformTikTok, PlatformDvach, Platform4chan}
	seen := map[string]bool{}
	for _, p := range order {
		seen[p] = true
	}
	var extra []string
	for p := range by {
		if !seen[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

func (a *App) freshPoolItems(pool []jx.M) []jx.M {
	var fresh []jx.M
	for _, item := range pool {
		if a.poolItemFresh(item) {
			fresh = append(fresh, item)
		}
	}
	return fresh
}

type queueSkip map[string]int

// buildCandidateQueue — без виденных, устаревших, отсеянных раньше; лучшие источники первыми, горячие — в шортлист. До 500.
func (u *U) buildCandidateQueue(byPlatform map[string][]jx.M, seen *knownKeys, rejected map[string]bool, rates map[string][2]int, format *TopicFormat, filters jx.M, topic string) ([]jx.M, queueSkip) {
	skipped := queueSkip{}
	sourcePolicy, policyErr := u.collectorSourcePolicy(topic)
	if policyErr != nil {
		u.Log("Не удалось применить фильтр источников: %v", policyErr)
	}
	busy := u.sourceBusy(topic)
	unique := map[string]jx.M{}
	var order, deferred []string
	uaHits, uaNames, uaAll := map[string]int{}, map[string]string{}, map[string]bool{}
	maxAge := u.TInt("content_max_age")
	adult := u.AdultAllowed()
	frozen := map[string]bool{}
	if jx.Bool(filters, "active_sources_only") {
		frozen = u.FrozenSourceLabels()
	}
	platOrder := candidatePlatformOrder(byPlatform)
	recovery := newAttachmentRecovery(u)
	for _, p := range platOrder {
		var ready []jx.M
		for _, raw := range byPlatform[p] {
			if (len(jx.Maps(raw, "source_attachments")) > 0 || telegramRepairChannel(raw) != "") && policyErr == nil && sourcePolicy(raw) == "" &&
				!candidateIsKnown(raw, seen) && !IsPostOutdated(jx.Int(raw, "posted_at"), 0, maxAge) &&
				!PostIsAdvertisement(raw) && (adult || !u.ContentIsAdult(raw)) && u.UAItemBlocked(raw) == "" {
				raw = recovery.post(raw)
			}
			if jx.Bool(raw, "media_incomplete") || jx.Bool(raw, "text_incomplete") || jx.Bool(raw, "video_unavailable") {
				skipped["incomplete_source_post"]++
				continue
			}
			ready = append(ready, raw)
		}
		for _, item := range ExpandPostImages(ready) {
			if policyErr != nil {
				skipped["source_policy_unavailable"]++
				continue
			}
			if reason := sourcePolicy(item); reason != "" {
				skipped[reason]++
				continue
			}
			if reason := u.collectorDisplayReason(item, filters, frozen, adult); reason != "" {
				notes := map[string]string{"filter": "Не прошёл фильтры выдачи", "advertising": "Явная рекламная маркировка", "adult": "18+ и чувствительный контент выключены"}
				u.rememberRejected(topic, "filter", item, notes[reason], nil)
				skipped[reason]++
				continue
			}
			if ua := u.UAItemBlocked(item); ua != "" {
				u.rememberRejected(topic, "filter", item, "Фильтр украинского контента: "+ua, nil)
				skipped["ukraine"]++
				src := ContentSourceKey(item)
				uaAll[src] = true
				if !strings.HasPrefix(ua, "источник:") {
					uaHits[src]++
					uaNames[src] = jx.Str(item, "source")
				}
				continue
			}
			// Distinct videos can share a cover. Short text records can have
			// no lexical fingerprint but still have a valid record identity.
			ukey := ContentItemKey(item)
			if ukey == "" {
				if format != nil && jx.Str(item, "image") == "" {
					skipped["format"]++
				} else {
					skipped["no_key"]++
				}
				continue
			}
			if candidateIsKnown(item, seen) {
				skipped["seen"]++
				continue
			}
			if unique[ukey] != nil {
				skipped["duplicate"]++
				continue
			}
			src := ContentSourceKey(item)
			days := maxAge
			if strings.HasPrefix(src, "dvach:") {
				days = config.DvachMaxAgeDays
			}
			reason := ""
			switch {
			case IsPostOutdated(jx.Int(item, "posted_at"), 0, days):
				reason = "outdated"
			case rejected[jx.Str(item, "image")]:
				reason = "rejected_before"
			case format != nil && jx.Str(item, "image") == "" && format.Media == "required":
				reason = "format"
			case format != nil && format.Text == "required" && runeLen(strings.TrimSpace(jx.Str(item, "text"))) < format.MinText:
				reason = "format"
			}
			if reason != "" {
				switch reason {
				case "rejected_before":
					u.rememberRejected(topic, "prescreen", item, "Отсеян быстрой оценкой ранее", nil)
				case "outdated":
					u.rememberRejected(topic, "filter", item, "Устаревший пост", nil)
				case "format":
					u.rememberRejected(topic, "filter", item, "Не подходит по формату", nil)
				}
				skipped[reason]++
				continue
			}
			unique[ukey] = item
			if busy[src] {
				deferred = append(deferred, ukey)
			} else {
				order = append(order, ukey)
			}
		}
	}
	// The source cap and cooldown are for variety, not a reason to leave a
	// topic empty.  If the normal queue is thin, bring back enough of the
	// otherwise valid posts from resting/full sources to give the analyzer a
	// realistic chance of filling the feed.  They remain interleaved by source.
	queueGoal := min(500, max(config.PrescreenBatch, 2*max(1, min(u.TInt("added_per_run"), u.TInt("pool_target")-u.PoolTopicCount(topic)))))
	returned := 0
	if len(order) < queueGoal && len(deferred) > 0 {
		fallback := collectorDeferredKeys(unique, deferred, queueGoal-len(order))
		order = append(order, fallback...)
		returned = len(fallback)
		skipped["source_busy_returned"] = returned
	}
	skipped["source_busy"] = len(deferred) - returned
	skipped["ukraine_sources"] = len(uaAll)
	for src, n := range uaHits {
		if n >= uaSourcePostsMax {
			u.UABlockSource(u.Ctx, src, uaNames[src], fmt.Sprintf("за проход украинских постов: %d", n))
		}
	}
	u.UACountSkipped(u.Ctx, skipped["ukraine"])
	all := make([]jx.M, 0, len(order))
	for _, k := range order {
		all = append(all, unique[k])
	}
	collectorSortCandidates(all, rates, u.CurrentQuery())
	short := u.hotShortlist(all, rates, topic)
	return short[:min(len(short), 500)], skipped
}

// Calculate lexical matches and quality hints once, not on every comparison.
func collectorSortCandidates(items []jx.M, rates map[string][2]int, query string) {
	type rank struct {
		item        jx.M
		named, cold bool
		score       float64
		postedAt    int64
	}
	ranked := make([]rank, len(items))
	for i, item := range items {
		r := rates[ContentSourceKey(item)]
		rate := float64(r[0]+1) / float64(r[1]+2)
		ranked[i] = rank{
			item:     item,
			named:    !ImageboardItem(item) && query != "" && (QueryTextHit(jx.Str(item, "text"), query) || inventoryPriority(item, query)),
			cold:     r[1] >= config.SourceVerdictMin && rate < config.SourceVerdictLow,
			score:    rate * queryRerankMultiplier(item, query),
			postedAt: jx.Int(item, "posted_at"),
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.named != b.named {
			return a.named
		}
		if a.cold != b.cold {
			return !a.cold
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.postedAt > b.postedAt
	})
	for i, r := range ranked {
		items[i] = r.item
	}
}

// Restore exactly the identities used when building the unique map. Image
// URLs and long text fingerprints are not the keys of videos/short posts.
func collectorDeferredKeys(unique map[string]jx.M, deferred []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	fallback := make([]jx.M, 0, len(deferred))
	for _, key := range deferred {
		if it := unique[key]; it != nil {
			fallback = append(fallback, it)
		}
	}
	var keys []string
	for _, it := range InterleaveBySource(fallback) {
		key := ContentItemKey(it)
		if key == "" || unique[key] == nil {
			continue
		}
		keys = append(keys, key)
		if len(keys) >= limit {
			break
		}
	}
	return keys
}

// InterleaveBySource — по одному с каждого источника по очереди.
func InterleaveBySource(items []jx.M) []jx.M {
	return interleaveByKey(items, ContentSourceKey)
}

// Preserve first-seen source order and item order within each source.
// Removing exhausted groups keeps total work linear even for skewed inputs.
func interleaveByKey[T any](items []T, key func(T) string) []T {
	if len(items) == 0 {
		return nil
	}
	var groups [][]T
	byKey := make(map[string]int)
	for _, item := range items {
		k := key(item)
		i, exists := byKey[k]
		if !exists {
			i = len(groups)
			byKey[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], item)
	}
	out := make([]T, 0, len(items))
	visitInterleaved(groups, func(item T) bool {
		out = append(out, item)
		return true
	})
	return out
}

// Visit one item per group per round, without mutating the caller's slices.
// Exhausted groups are removed; false ends traversal early.
func visitInterleaved[T any](groups [][]T, visit func(T) bool) {
	active := make([][]T, 0, len(groups))
	for _, group := range groups {
		if len(group) > 0 {
			active = append(active, group)
		}
	}
	for len(active) > 0 {
		remaining := active[:0]
		for _, group := range active {
			if !visit(group[0]) {
				return
			}
			if len(group) > 1 {
				remaining = append(remaining, group[1:])
			}
		}
		active = remaining
	}
}

// HotPlatform — площадка поста для горячих.
func HotPlatform(item jx.M) string {
	e := jx.Map(item, "engagement")
	src := jx.Str(item, "source")
	switch {
	case jx.Str(e, "source") == "4chan" || strings.HasPrefix(src, Platform4chan):
		return "4chan"
	case jx.Has(e, "thread_posts") || strings.HasPrefix(src, PlatformDvach):
		return "Двач"
	case jx.Str(e, "source") == "telegram":
		return "Telegram"
	case strings.HasPrefix(src, PlatformTikTok):
		return "TikTok"
	case strings.HasPrefix(src, PlatformVK):
		return "ВКонтакте"
	case strings.HasPrefix(src, PlatformOK):
		return "ОК"
	case jx.Has(item, "views"):
		return "YouTube"
	}
	return "other"
}

// PostHeat — горячесть поста за час жизни: показатель площадки / (часов + 2); nil — данных нет.
func PostHeat(item jx.M) *float64 {
	e := jx.Map(item, "engagement")
	var v *float64
	set := func(x float64) { v = &x }
	switch HotPlatform(item) {
	case "Telegram":
		views := e["views"]
		if views == nil {
			views = item["post_views"]
		}
		if views != nil {
			sum := 0.0
			for _, r := range jx.Map(e, "reactions") {
				sum += jx.ToFloat(r)
			}
			set(jx.ToFloat(views) + 5*sum + 10*jx.Float(e, "forwards"))
		}
	case "Двач":
		if jx.Has(e, "thread_posts") {
			set(jx.Float(e, "thread_posts"))
		}
	case "4chan":
		if jx.Has(e, "replies") || jx.Has(e, "images") {
			set(jx.Float(e, "replies") + jx.Float(e, "images"))
		}
	case "YouTube":
		set(jx.Float(item, "views"))
	case "TikTok":
		if jx.Has(e, "view_count") || jx.Has(e, "like_count") {
			set(jx.Float(e, "view_count") + 5*jx.Float(e, "like_count"))
		}
	case "ВКонтакте":
		if jx.Has(e, "likes") || jx.Has(e, "views") {
			set(jx.Float(e, "views") + 5*jx.Float(e, "likes") + 10*jx.Float(e, "reposts"))
		}
	}
	if v == nil {
		return nil
	}
	hours := math.Max(1, float64(now()-jx.Int(item, "posted_at"))/3600)
	r := *v / (hours + 2)
	return &r
}

func hotRelativeHeat(items []jx.M) []float64 {
	type sample struct {
		heat             *float64
		source, platform string
	}
	samples := make([]sample, len(items))
	bySrc, byPlat := map[string][]float64{}, map[string][]float64{}
	for i, item := range items {
		if heat := PostHeat(item); heat != nil {
			source, platform := ContentSourceKey(item), HotPlatform(item)
			samples[i] = sample{heat, source, platform}
			bySrc[source] = append(bySrc[source], *heat)
			byPlat[platform] = append(byPlat[platform], *heat)
		}
	}
	// Median sorts a copy of the input. Reuse each median throughout
	// this batch rather than sorting the same group for every post.
	sourceMedian, platformMedian := map[string]*float64{}, map[string]*float64{}
	out := make([]float64, len(items))
	for i, s := range samples {
		out[i] = 1
		if s.heat == nil {
			continue
		}
		cache, key, values := platformMedian, s.platform, byPlat[s.platform]
		if source := bySrc[s.source]; len(source) >= 3 {
			cache, key, values = sourceMedian, s.source, source
		}
		base, ok := cache[key]
		if !ok {
			base = Median(values)
			cache[key] = base
		}
		if base != nil && *base > 0 {
			out[i] = *s.heat / *base
		}
	}
	return out
}

func hotWords(text string) []string {
	var out []string
	for _, w := range nonWordRe.Split(strings.ToLower(text), -1) {
		if runeLen(w) >= 4 {
			out = append(out, w)
		}
	}
	return uniqueStrings(out)
}

func hotHasWord(text string, words []string) bool {
	t := strings.ToLower(text)
	for _, w := range words {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

func (u *U) imageboardNativeTopic(topic string) bool {
	if topic == "" {
		return false
	}
	first := topic
	if p := TopicParts(topic); len(p) > 0 {
		first = p[0]
	}
	e := u.TopicLookup(first)
	if e == nil || e.Key == "" {
		return false
	}
	for _, n := range config.ImageboardNativeTopicKeys {
		if e.Key == n || strings.HasPrefix(e.Key, n+".") {
			return true
		}
	}
	return false
}

// hotShortlist — горячие первыми: уточнение, затем общий отбор по горячести × доля принятых × бонусы; хвост — по кругу источников.
func (u *U) hotShortlist(all []jx.M, rates map[string][2]int, topic string) []jx.M {
	if topic == "" && u.ID != "" {
		topic = u.SearchTopicKey()
	}
	query := u.CurrentQuery()
	var keywords []string
	exclude := []string{}
	if p := u.TopicProfileForPrompts(topic); p != nil {
		keywords, exclude = p.Keywords, hotWords(p.Exclude)
	}
	shortN := u.TInt("hot_shortlist")
	var first, indexed, rest, softTail []jx.M
	var boost []float64
	for _, it := range all {
		text := jx.Str(it, "text")
		if ImageboardItem(it) {
			text = ""
		}
		// Profile exclusions are model-generated prose, not user bans.
		// A single word can appear in a comparison, negation or quotation;
		// postpone to full analysis instead of discarding the publication.
		if collectorExcludeHint(text, exclude) {
			softTail = append(softTail, it)
			continue
		}
		named := query != "" && QueryTextHit(text, query)
		if inventoryPriority(it, query) && len(indexed) < shortN/4 {
			indexed = append(indexed, it)
			continue
		}
		if named && len(first) < shortN/2 {
			first = append(first, it)
			continue
		}
		b := 1.0
		if named {
			b *= config.HotQueryBoost
		}
		boost = append(boost, b)
		rest = append(rest, it)
	}
	// Indexed candidates have passed the same source, media and safety filters.
	// Do not let popularity or literal word matches consume their entire budget.
	first = append(InterleaveBySource(indexed), first...)
	heat := hotRelativeHeat(rest)
	score := make([]float64, len(rest))
	for i, it := range rest {
		text := jx.Str(it, "text")
		if ImageboardItem(it) {
			text = ""
		}
		r := rates[ContentSourceKey(it)]
		rate := float64(r[0]+1) / float64(r[1]+2)
		mult := math.Min(1.5, math.Max(0.5, 0.5+rate))
		if r[1] >= config.SourceVerdictMin && rate < config.SourceVerdictLow {
			mult = 0.5
		}
		kw := 1.0
		if TopicWordsHit(text, keywords) {
			kw = config.HotKeywordBoost
		}
		// Ограничиваем влияние вирусности: популярность не заменяет попадание в тему.
		score[i] = math.Min(3, math.Max(0.25, heat[i])) * mult * kw * boost[i] * queryRerankMultiplier(it, query)
	}
	bySrc := map[string][]int{}
	var srcOrder []string
	for i := range rest {
		k := ContentSourceKey(rest[i])
		if bySrc[k] == nil {
			srcOrder = append(srcOrder, k)
		}
		bySrc[k] = append(bySrc[k], i)
	}
	var pool []int
	for _, k := range srcOrder {
		idx := bySrc[k]
		sort.SliceStable(idx, func(a, b int) bool { return score[idx[a]] > score[idx[b]] })
		pool = append(pool, idx[:min(len(idx), config.QueuePerSource)]...)
	}
	perSourceCut := len(score) - len(pool)
	sort.SliceStable(pool, func(a, b int) bool { return score[pool[a]] > score[pool[b]] })
	room := max(0, shortN-len(first))
	capN := int(math.Floor(config.HotPlatformShare * float64(shortN)))
	var picked []int
	per := map[string]int{}
	for _, i := range pool {
		if len(picked) >= room {
			break
		}
		p := HotPlatform(rest[i])
		if per[p] >= capN {
			continue
		}
		per[p]++
		picked = append(picked, i)
	}
	top := -1
	trusted := 0
	for _, i := range picked {
		if top < 0 || heat[i] > heat[top] {
			top = i
		}
		if jx.Bool(rest[i], "thread_trust") {
			trusted++
		}
	}
	if u.run != nil {
		hs := jx.M{"total": len(all), "short": len(picked) + len(first), "per": per, "excluded": 0, "keyword_demoted": len(softTail), "trusted": trusted, "imageboard_tail": 0, "top": nil,
			"per_source_cut": perSourceCut, "shortlist_cut": len(pool) - len(picked)}
		if top >= 0 {
			hs["top"] = jx.M{"source": jx.Str(rest[top], "source"), "times": math.Round(heat[top]*10) / 10}
		}
		u.run.HotStats = hs
	}
	sort.SliceStable(picked, func(a, b int) bool { return score[picked[a]] > score[picked[b]] })
	isPicked := map[int]bool{}
	var hot []jx.M
	for _, i := range picked {
		isPicked[i] = true
		hot = append(hot, rest[i])
	}
	var tailItems []jx.M
	for i, it := range rest {
		if !isPicked[i] {
			tailItems = append(tailItems, it)
		}
	}
	return append(append(append(first, InterleaveBySource(hot)...), InterleaveBySource(tailItems)...), InterleaveBySource(softTail)...)
}

func collectorExcludeHint(text string, exclude []string) bool {
	return len(exclude) > 0 && hotHasWord(text, exclude)
}

// A caption is incomplete evidence when the post also contains media.
func captionVerdictCanReject(item jx.M, judge string) bool {
	return judge == "text" && !PostItemHasMedia(item)
}

// hotTextJudge — пакетная проверка подписей: слабые подписи медиа откладываются до полного анализа.
func (u *U) hotTextJudge(items []jx.M, topic string) []jx.M {
	judge := u.TopicFormatOf(topic).Judge
	if judge != "text" && judge != "both" {
		return items
	}
	// Keep actual text posts and media captions in different batches. Only
	// the former produce a complete verdict that can be reused at admission.
	type textGroup struct {
		indices         []int
		full, ownerOnly bool
	}
	groups := []textGroup{{full: true}, {full: true, ownerOnly: true}, {}, {ownerOnly: true}}
	for i, item := range items {
		if u.analysisPrivate(topic, item) {
			continue // private records are analyzed individually when reached
		}
		hasMedia := PostItemHasMedia(item)
		text := AnalysisText(item)
		if hasMedia {
			text = PostCaptionForAI(item)
		}
		if runeLen(text) < config.HotTextMinChars {
			continue
		}
		group := 0
		if hasMedia {
			group += 2
		}
		if !u.collectorPublicEvidence(item, topic) {
			group++
		}
		groups[group].indices = append(groups[group].indices, i)
	}
	drop, late := map[int]bool{}, map[int]bool{}
	h := u.History()
	for _, group := range groups {
		// Token budget in AnalyzeTextBatch covers at most six records.
		batchSize := min(config.HotTextBatch, 6)
		for off := 0; off < len(group.indices) && u.Alive(); off += batchSize {
			var texts []string
			ids := group.indices[off:min(len(group.indices), off+batchSize)]
			for _, i := range ids {
				if group.full {
					texts = append(texts, AnalysisText(items[i]))
				} else {
					texts = append(texts, PostCaptionForAI(items[i]))
				}
			}
			res := u.cachedCollectorTextBatch(texts, topic, h, false, group.full, group.ownerOnly)
			for k, r := range res {
				if r == nil || jx.Int(r, "relevance") >= config.HotTextRejectScore {
					continue
				}
				i := ids[k]
				if captionVerdictCanReject(items[i], judge) {
					drop[i] = true
				} else {
					late[i] = true
				}
			}
		}
	}
	var kept, tail []jx.M
	for i, it := range items {
		switch {
		case drop[i]:
		case late[i]:
			tail = append(tail, it)
		default:
			kept = append(kept, it)
		}
	}
	if u.run != nil {
		u.run.TextPrefilterDropped += len(drop)
		u.run.TextPrefilterLate += len(late)
	}
	u.Log("Проверка подписей моделью: отброшено %d, в конец шортлиста %d.", len(drop), len(late))
	return append(kept, tail...)
}
