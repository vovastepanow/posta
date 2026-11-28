package posta

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Сборщик: посты всех источников темы → очередь кандидатов → быстрая оценка пачками → полный анализ лучших → пул.

func PostCaptionForAI(item jx.M) string {
	if ImageboardItem(item) {
		return ""
	}
	t := strings.TrimSpace(jx.Str(item, "text"))
	if t == EmptyPostText {
		t = ""
	}
	if jx.Bool(item, "forwarded") {
		from := jx.Str(item, "forwarded_from")
		if from == "" {
			from = "неизвестен"
		}
		t = "[Это пересланное сообщение из «" + from + "». Оценивай содержание самого пересланного сообщения и авторский комментарий отдельно; не приписывай пересланный текст текущему каналу и не считай сам факт репоста доказательством релевантности.]\n" + t
	}
	return t
}

func candidateLogLabel(item jx.M) string {
	u := jx.Str(item, "source_url")
	s := "[" + jx.Str(item, "source") + "]"
	if IsTrustedSourceURL(u) {
		s += "(" + u + ")"
	}
	return s
}

func (u *U) collectorStopReason(started time.Time, analyzed, added int) string {
	switch {
	case !u.Alive():
		return ParserSessionEnded
	case time.Since(started) > time.Duration(u.TInt("parser_minutes"))*time.Minute:
		return "Достигнут лимит времени одного запуска."
	case analyzed >= u.TInt("analyze_per_run"):
		return "Достигнут лимит полных анализов за запуск."
	case added >= u.TInt("added_per_run"):
		return "Достигнут лимит новых записей за запуск."
	}
	key, target := u.SearchTopicKey(), u.TInt("pool_target")
	available, allowed, err := u.poolRefillState(key, target)
	if err != nil {
		return "Не удалось проверить фильтры подборки."
	}
	if available >= target {
		return fmt.Sprintf("Пул заполнен до %d записей.", target)
	}
	if !allowed {
		return "Резерв скрытых записей заполнен. Подбор возобновится после освобождения места."
	}
	return ""
}

func (u *U) parserPhaseWithTopic(phase, topic string) {
	if topic == "" {
		topic = u.CurrentTopic()
	}
	if topic != "" {
		phase += " — тема: «" + topic + "»"
	}
	u.ParserPhase(phase)
}

// RunCollector — один проход. Возвращает true, если тема сменилась и сбор нужно начать заново.
// When fresh topic supply is low, search posts directly instead of
// blocking the collector on source vetting. The existing candidate, full
// analysis, safety, source-override and deduplication checks remain in place.
func shouldFastTopicSearch(hasQuery, discoveryEnabled, restricted bool, sources, requiredSources, available, target int) bool {
	return !hasQuery && discoveryEnabled && !restricted && target > 0 && available < target &&
		((requiredSources > 0 && sources < requiredSources) || available < max(1, (target+3)/4))
}

func (u *U) RunCollector() bool {
	u.run = newRun(u.App)
	started := time.Now()
	topic := u.CurrentTopic()
	u.KickVideoWorker()
	u.ParserProgress(jx.M{"deadline": started.Unix() + int64(u.TInt("parser_minutes")*60)}, true)
	u.Log("")
	u.Log("⏱️ Начинается подбор контента — тема «%s», не дольше %d мин.", topic, u.TInt("parser_minutes"))
	u.Log("")
	u.ParserStage("collect")
	u.parserPhaseWithTopic("сбор постов", topic)
	u.MirrorCollectUser()
	if n := u.MediaCleanup(u.AllUsersPoolItems(u.Ctx), false, false); n > 0 {
		u.Log("Кэш медиа: удалено устаревших файлов: %d.", n)
	}
	if n := u.prunePoolAlreadyRated(); n > 0 {
		u.Log("Пул: убрано записей, пост которых уже оценён: %d.", n)
	}
	u.PoolStampLegacy()
	u.CachePoolGalleries(4)
	u.PruneDeadPoolItems(false, false, true)
	seen := u.collectKnown()
	topic = u.CurrentTopic()
	key := u.SearchTopicKey()
	if q := u.CurrentQuery(); q != "" {
		u.Log("Поиск: «%s» — ищу посты по площадкам (тема «%s» — только для оценки).", q, topic)
	}
	u.Log("Модели: быстрая оценка — %s, полный анализ — %s.", u.LLM.ModelFor(u.Ctx, "prescreen"), u.LLM.ModelFor(u.Ctx, "analysis"))
	u.TopicEnsure(u.Ctx, topic)
	u.TopicClassifyPending(1, topic)
	u.TopicProfileEnsure(topic)
	u.DvachBoardsForTopic(topic, true)
	d := u.Catalog()
	usableSources := u.TopicUsableSources(d, topic)
	// The public source autopilot now owns source discovery. The content
	// parser can still explore individual posts without running the legacy
	// scanner or waiting for manual source approvals.
	fastTopicSearch := shouldFastTopicSearch(u.CurrentQuery() != "", u.TBool("discovery"), u.TopicIsRestricted(topic),
		usableSources, u.TInt("min_sources"), u.PoolTopicCount(key), u.TInt("pool_target"))
	if usableSources < u.TInt("min_sources") {
		u.Log("Источников по теме %d из %d — проверкой новых источников занимается автопилот.", usableSources, u.TInt("min_sources"))
	}
	if reason := u.collectorStopReason(started, 0, 0); reason != "" {
		u.Log("%s", reason)
		return false
	}
	cache := u.screenedCacheLoad(key)
	scores := map[string]int{}
	// A prescreen verdict is only valid after the actual image bytes and
	// caption have been checked. Never reject a record by its URL alone.
	for _, e := range cache {
		scores[e.URL] = e.Score
	}
	format := u.TopicFormatOf(key)
	var byPlatform map[string][]jx.M
	if fastTopicSearch && u.PoolTopicCount(key) == 0 {
		u.Log("У новой темы пока нет источников — не жду общий опрос, ищу публикации напрямую.")
	} else {
		u.parserPhaseWithTopic("ожидание общего сбора", topic)
		u.ParserProgress(jx.M{"shared_wait": true}, false)
		u.waitSharedCollection(u.Ctx, topic)
		u.ParserProgress(jx.M{"shared_wait": false}, false)
	}
	if !u.Alive() {
		return false
	}
	fastPostKeys := map[string]bool{}
	fastPostFound := 0
	fastSearchSeconds := 0.0
	if u.CurrentQuery() != "" {
		byPlatform = u.QuerySearchCollect(key) // поиск по запросу сам включает каталог темы
	} else {
		byPlatform = u.collectPostsByPlatform(key)
		if byPlatform == nil {
			byPlatform = map[string][]jx.M{}
		}
		if fastTopicSearch && u.Alive() && u.CurrentTopic() == topic {
			fastStarted := time.Now()
			fastPosts := u.TopicFastSearchCollect(topic)
			fastSearchSeconds = time.Since(fastStarted).Seconds()
			for platform, posts := range fastPosts {
				fastPostFound += len(posts)
				for _, post := range posts {
					if postKey := CandidatePostKey(post); postKey != "" {
						fastPostKeys[postKey] = true
					}
				}
				byPlatform[platform] = append(byPlatform[platform], posts...)
			}
		}
	}
	u.ParserStage("filter")
	candidates, skipped := u.buildCandidateQueue(byPlatform, seen, nil, u.CombinedSourceVerdicts(u.Ctx, key), &format, u.ContentFilterValues(), key)
	if hs := u.run.HotStats; hs != nil {
		var per []string
		for p, n := range hs["per"].(map[string]int) {
			per = append(per, fmt.Sprintf("%s %d", p, n))
		}
		sort.Strings(per)
		line := fmt.Sprintf("Горячие: %d из %d", jx.Int(hs, "short"), jx.Int(hs, "total"))
		if len(per) > 0 {
			line += " (" + strings.Join(per, ", ") + ")"
		}
		if top := jx.Map(hs, "top"); top != nil {
			line += fmt.Sprintf("; самый горячий — %s ×%v к обычному", jx.Str(top, "source"), top["times"])
		}
		if x := jx.Int(hs, "keyword_demoted"); x > 0 {
			line += fmt.Sprintf(". Отложено в хвост по словам «не подходит» (не отброшено): %d", x)
		}
		line += fmt.Sprintf(". Срез очереди на источник: %d, срез шортлиста: %d", jx.Int(hs, "per_source_cut"), jx.Int(hs, "shortlist_cut"))
		u.Log("%s, из доверенных тредов %d; имиджборд без признаков темы — в хвост: %d.", line, jx.Int(hs, "trusted"), jx.Int(hs, "imageboard_tail"))
	}
	// Evaluate captions only for the block about to be processed. A large
	// shortlist must not hold back the first usable result.
	collected := 0
	for _, posts := range byPlatform {
		collected += len(posts)
	}
	notes := fmt.Sprintf("📌 Тема: «%s». Собрано постов: %d. Кандидатов к проверке: %d.", topic, collected, len(candidates))
	for _, s := range []struct{ k, t string }{{"rejected_before", " Уже отсеяны в прошлых запусках: %d."}, {"outdated", " Устаревших: %d."},
		{"ukraine", " Украинский контент отсеян: %d."}, {"format", " Отсеяно форматом темы: %d."}, {"filter", " Отсеяно фильтрами выдачи: %d."}, {"source_excluded", " Исключённые источники: %d."}, {"personal_blog", " Фильтр личных блогов: %d."}, {"advertising", " Явная реклама: %d."}, {"adult", " Ограничение 18+: %d."},
		{"source_busy", " Источники в ленте или на отдыхе: %d."}, {"seen", " Уже видели: %d."}, {"duplicate", " Дубли: %d."}, {"no_key", " Без текста и картинки: %d."}} {
		if skipped[s.k] > 0 {
			notes += fmt.Sprintf(s.t, skipped[s.k])
		}
	}
	if n := skipped["source_busy_returned"]; n > 0 {
		notes += fmt.Sprintf(" Вернул из них в очередь из-за нехватки кандидатов: %d.", n)
	}
	u.Log("%s Быстрая оценка по картинке %d px: отсев мусора при уверенности от %d%%, тему не проверяет.", notes, config.PrescreenMaxSide, u.TInt("prescreen_junk"))
	pc := &prescreenCtx{stats: map[string]int{}, cached: scores, newCache: map[string]screenedEntry{}, knownHashes: u.collectKnownHashes()}
	analyzed, added, done := 0, 0, 0
	fastPostAdded := 0
	firstAddedSeconds := 0.0
	funnel := func() jx.M {
		f := passFunnel(collected, len(candidates), skipped, pc.stats, analyzed, added)
		f["fast_search_found"] = fastPostFound
		f["fast_search_added"] = fastPostAdded
		f["fast_search_seconds"] = fastSearchSeconds
		f["prescreen_cache_hits"] = pc.stats["prescreen_cache_hits"]
		f["prescreen_bypassed_media"] = pc.stats["prescreen_bypassed_media"]
		f["text_cache_hits"] = u.run.TextCacheHits
		f["text_batch_calls"] = u.run.TextBatchCalls
		f["vision_cache_hits"] = u.run.VisionCacheHits
		f["vision_calls"] = u.run.VisionCalls
		f["admission_outcomes"] = collectorCountSnapshot(u.run.AdmissionOutcomes)
		f["visual_evidence"] = collectorCountSnapshot(u.run.VisualEvidenceKinds)
		f["analysis_unknown"] = u.run.UnknownAnalyses
		f["text_evidence_failures"] = u.run.TextEvidenceFailures
		f["text_prefilter_rejected"] = u.run.TextPrefilterDropped
		f["text_prefilter_deferred"] = u.run.TextPrefilterLate
		if added > 0 {
			f["first_added_seconds"] = firstAddedSeconds
		}
		return f
	}
	stop := ""
	restart := false
	var queue []prescreenRow
	var tAnalyze time.Duration
	need := max(1, min(u.TInt("added_per_run"), u.TInt("pool_target")-u.PoolTopicCount(key)))
	u.ParserProgress(jx.M{"total": len(candidates), "queue_at": now(), "need": need, "funnel": funnel()}, false)
	lastProgress := time.Now()
	deadline := started.Add(time.Duration(u.TInt("parser_minutes")) * time.Minute)
	nBatches := (len(candidates) + config.PrescreenBatch - 1) / config.PrescreenBatch
	takenSrc := map[string]bool{}
outer:
	for bi := 0; bi < nBatches; bi++ {
		batch := dropTakenSources(candidates[bi*config.PrescreenBatch:min(len(candidates), (bi+1)*config.PrescreenBatch)], takenSrc)
		if time.Since(lastProgress) >= 30*time.Second {
			left := time.Until(deadline)
			u.Log("⏳ Прогресс: %d%% (%d/%d пачек). Прошло: %.1f мин. Осталось до лимита: ≈ %dм %dс", (bi+1)*100/max(1, nBatches), bi, nBatches,
				time.Since(started).Minutes(), int(left.Minutes()), int(left.Seconds())%60)
			lastProgress = time.Now()
		}
		if stop = u.collectorStopReason(started, analyzed, added); stop != "" {
			break
		}
		if u.CurrentTopic() != topic {
			stop, restart = "Тема поиска изменилась на «"+u.CurrentTopic()+"» — начинаю заново с новой темой.", true
			break
		}
		if u.SearchTopicKey() != key {
			stop, restart = "Вторая тема изменилась — собираю заново.", true
			break
		}
		originalCount := len(batch)
		batch = u.hotTextJudge(batch, key)
		done += originalCount - len(batch)
		if stop = u.collectorStopReason(started, analyzed, added); stop != "" {
			break
		}
		var texts, media []jx.M
		for _, it := range batch {
			if jx.Str(it, "image") == "" {
				texts = append(texts, it)
			} else {
				media = append(media, it)
			}
		}
		for _, t := range u.textCandidatesPriority(texts, topic) {
			queue = append(queue, prescreenRow{Item: t})
		}
		u.ParserStage("prescreen")
		u.parserPhaseWithTopic("быстрая оценка", topic)
		var passed []prescreenRow
		if len(media) > 0 {
			passed = u.prescreenBatch(media, key, pc, deadline)
		}
		u.cacheMerge(pc.newCache)
		pc.newCache = map[string]screenedEntry{}
		done += len(batch) - len(passed) - len(texts)
		u.ParserProgress(jx.M{"done": done, "funnel": funnel()}, false)
		queue = append(queue, passed...)
		if len(queue) < config.AnalyzeBlock && bi < nBatches-1 {
			continue
		}
		u.ParserStage("analysis")
		u.parserPhaseWithTopic("полный анализ", topic)
		for _, c := range queue {
			if stop = u.collectorStopReason(started, analyzed, added); stop != "" {
				break outer
			}
			if takenSrc[ContentSourceKey(c.Item)] {
				done++
				continue
			}
			analyzed++
			t0 := time.Now()
			if u.analyzeCandidate(c.Item, c.Poster, u.History(), key) {
				added++
				if added == 1 {
					firstAddedSeconds = time.Since(started).Seconds()
				}
				if fastPostKeys[CandidatePostKey(c.Item)] {
					fastPostAdded++
				}
				if src := ContentSourceKey(c.Item); u.sourceVisibleQuotaReached(key, src) {
					takenSrc[src] = true
				}
			}
			tAnalyze += time.Since(t0)
			done++
			u.ParserProgress(jx.M{"done": done, "analyzed": analyzed, "added": added, "funnel": funnel()}, false)
		}
		queue = nil
	}
	u.cacheMerge(pc.newCache)
	u.ParserProgress(jx.M{"done": len(candidates)}, false)
	if !restart {
		u.parserExpectedSeconds(u.ID, int64(time.Since(started).Seconds()))
	}
	if stop != "" {
		u.Log("%s", stop)
	}
	u.ParserStage("pool")
	u.KVSet(u.Ctx, parserLastPassKey(u.ID), jx.M{"at": now(), "funnel": funnel()}, 0) // воронка последнего прохода для главной панели
	u.Log("")
	u.Log("✅ Подбор завершён за %.1f мин: добавлено %d, проанализировано %d, отсеяно быстрой оценкой %d, повторов %d.", time.Since(started).Minutes(), added, analyzed,
		pc.stats["rejected"], pc.stats["duplicates"])
	total := time.Since(started)
	u.Log("Время прохода: сбор %.1f мин, анализ %.1f мин.", max(0, (total-tAnalyze).Minutes()), tAnalyze.Minutes())
	u.Log("Кэш: быстрый отсев %d, текст %d, визуальный анализ %d; запуски анализа: текстовые пачки %d, визуальные %d.", pc.stats["prescreen_cache_hits"], u.run.TextCacheHits, u.run.VisionCacheHits, u.run.TextBatchCalls, u.run.VisionCalls)
	u.Log("Отбор: %s; без достоверной оценки %d; до полного анализа отброшено текстов %d.", string(jx.Marshal(u.run.AdmissionOutcomes)), u.run.UnknownAnalyses, u.run.TextPrefilterDropped)
	u.Log("Визуальные данные: %s; неподтверждённых цитат текста %d.", string(jx.Marshal(u.run.VisualEvidenceKinds)), u.run.TextEvidenceFailures)
	if fastTopicSearch {
		u.Log("Быстрый поиск: найдено %d кандидатов, после полного отбора добавлено %d.", fastPostFound, fastPostAdded)
	}
	u.Log("")
	return restart
}
