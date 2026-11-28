package posta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/jx"
	"posta/internal/llm"
)

// ---- Оценка ----

func (u *U) fetchCandidatePoster(item jx.M) []byte {
	img := jx.Str(item, "image")
	if jx.Bool(item, "_video_without_poster") {
		b := u.videoPosterFromPublicURL(jx.Str(item, "video"))
		if len(b) == 0 {
			u.Log("-> Не удалось получить кадр видео, пропуск.")
			return nil
		}
		local := u.MediaStoreImage("video-poster:"+jx.Str(item, "video"), b)
		if local == "" {
			return nil
		}
		item["image"], item["images"] = local, []string{local}
		delete(item, "_video_without_poster")
		for _, f := range jx.Maps(item, "gallery") {
			if jx.Str(f, "video") == jx.Str(item, "video") && jx.Str(f, "image") == "" {
				f["image"] = local
				break
			}
		}
		return b
	}
	var b []byte
	if p := u.MediaPath(img); p != "" {
		b, _ = os.ReadFile(p)
	} else if strings.HasPrefix(img, FourchanImg+"/") {
		b = u.FourchanFetchMedia(img, false)
	} else {
		b = u.HTTPGet(img, 15*time.Second)
		if b == nil {
			b = u.HTTPGet(img, 25*time.Second)
		}
	}
	if len(b) == 0 {
		u.Log("-> Превью не скачалось, пропуск.")
		return nil
	}
	if err := imgx.Validate(b); err != nil {
		u.Log("-> Пропуск: %s", err)
		return nil
	}
	return b
}

type prescreenRow struct {
	Item     jx.M
	Poster   []byte
	Pos      int
	Score    *int
	Source   string
	Phash    string
	CacheKey string
	Private  bool
}

type prescreenCtx struct {
	stats       map[string]int
	cached      map[string]int
	newCache    map[string]screenedEntry
	knownHashes map[string]string
	passedTotal int
}

// Bind a prescreen result to the actual visual evidence and editable prompts.
// A perceptual hash is useful for duplicate detection, not for classifying a
// different caption or a modified image as junk.
func prescreenEvidenceKey(topic, scope, caption, prompts string, poster []byte) string {
	sum := sha256.Sum256(poster)
	key := sha256.Sum256(jx.Marshal([]any{"prescreen-input-v2", topic, scope, caption, prompts, hex.EncodeToString(sum[:])}))
	return hex.EncodeToString(key[:])
}

func (u *U) prescreenInputKey(item jx.M, topic string, poster []byte) string {
	caption := trimRunes(PostCaptionForAI(item), 300)
	values := map[string]string{"caption": caption, "thread": ""}
	private := u.analysisPrivate(topic, item) || jx.Bool(item, "source_sensitive") || jx.Bool(item, "nsfw")
	scope := ""
	if private {
		scope = "private:" + u.ID
	} else if !u.collectorPublicEvidence(item, topic) {
		scope = "owner:" + u.ID
	}
	settings := u.LLM.Settings()
	route := settings.Roles[llm.RolePrescreen]
	prompts := string(jx.Marshal([]any{route, settings.FastOnly, u.prompt("prescreen_yesno", values), u.prompt("prescreen_score", values)}))
	return prescreenEvidenceKey(topic, scope, caption, prompts, poster)
}

func (u *U) screen(topic, stage string, item jx.M, score int, passed bool, reason, model string) {
	if !passed {
		u.rememberRejected(topic, stage, item, reason, &score)
	}
	u.CH.Screening(ch.Screening{TS: time.Now(), UserID: u.ID, Topic: topic, Stage: stage, SourceKey: ContentSourceKey(item), Platform: HotPlatform(item),
		ImageKey: ContentItemKey(item), Score: score, Passed: passed, Reason: reason, Model: model})
}

// prescreenBatch — быстрая оценка пачки: прошедшие в порядке очереди.
func (u *U) prescreenBatch(batch []jx.M, topic string, c *prescreenCtx, deadline time.Time) []prescreenRow {
	var passed, toScore []prescreenRow
	primary, fallback, _ := u.LLM.Resolve(u.Ctx, llm.RolePrescreen)
	prescreenEnabled := !primary.Empty() || !fallback.Empty()
	for pos, item := range batch {
		if time.Now().After(deadline) || !u.Alive() {
			break
		}
		src := ContentSourceKey(item)
		if c.passedTotal > 0 && u.run != nil && u.run.SourceMisses[src] >= config.PrescreenSourceMissLimit {
			c.stats["source_skipped"]++
			continue
		}
		poster := u.fetchCandidatePoster(item)
		if poster == nil {
			continue
		}
		hash := ImagePhash(poster)
		if repeatFileIsVideo(item) {
			hash = ""
		}
		if hash != "" {
			if seen, ok := PhashFind(hash, c.knownHashes); ok {
				c.stats["duplicates"]++
				from := ""
				if c.knownHashes[seen] != "" {
					from = " (из «" + c.knownHashes[seen] + "»)"
				}
				u.Log("-> %s: повтор уже виденной картинки%s — пропуск.", candidateLogLabel(item), from)
				continue
			}
			c.knownHashes[hash] = jx.Str(item, "source")
		}
		row := prescreenRow{Item: item, Poster: poster, Pos: pos, Source: src, Phash: hash, Private: u.analysisPrivate(topic, item)}
		if !prescreenEnabled || !collectorPrescreenComplete(item) {
			// No extra AI call for evidence that cannot justify a rejection.
			c.stats["prescreen_bypassed_media"]++
			c.passedTotal++
			passed = append(passed, row)
			continue
		}
		row.CacheKey = u.prescreenInputKey(item, topic, poster)
		if s, ok := c.cached[row.CacheKey]; ok {
			sc := s
			c.stats["prescreen_cache_hits"]++
			if u.prescreenIsJunk(&sc, item) {
				u.rememberRejected(topic, "prescreen", item, "Оценка из кеша", &sc)
				c.stats["rejected"]++
			} else {
				c.passedTotal++
				row.Score = &sc
				passed = append(passed, row)
			}
		} else {
			toScore = append(toScore, row)
		}
	}
	if len(toScore) > 0 {
		u.Log("Быстрая оценка %d кандидатов (до %d одновременно)...", len(toScore), min(5, len(toScore)))
		scores := u.ParallelQuickScores(toScore)
		model := u.LLM.ModelFor(u.Ctx, "prescreen")
		for i, q := range scores {
			row := toScore[i]
			item := row.Item
			if q.Score == nil {
				u.Log("-> %s: ошибка оценки, идёт на полный анализ", candidateLogLabel(item))
				passed = append(passed, row)
				continue
			}
			junk := u.prescreenIsJunk(q.Score, item)
			if junk {
				u.Log("-> %s: мусор %d%% — пропуск", candidateLogLabel(item), *q.Score)
			} else {
				u.Log("-> %s: мусора %d%%", candidateLogLabel(item), *q.Score)
			}
			c.newCache[row.CacheKey] = screenedEntry{Topic: topic, Source: row.Source, Score: *q.Score}
			c.cached[row.CacheKey] = *q.Score
			c.stats["prescreened"]++
			reason := ""
			if junk {
				reason = "быстрая оценка: мусор"
			}
			u.screen(topic, "prescreen", item, *q.Score, !junk, reason, model)
			if junk {
				c.stats["rejected"]++
			} else {
				c.passedTotal++
				row.Score = q.Score
				passed = append(passed, row)
			}
		}
	}
	sort.SliceStable(passed, func(i, j int) bool { return passed[i].Pos < passed[j].Pos })
	return passed
}

// textCandidatesPriority — текстовые записи: сначала со словами темы, потом по лайкам.
func (u *U) textCandidatesPriority(items []jx.M, topic string) []jx.M {
	words := u.DiscoveryTopicKeywords(topic)
	score := func(it jx.M) int64 {
		s := jx.Int(it, "likes")
		t := strings.ToLower(jx.Str(it, "text"))
		for _, w := range words {
			if strings.Contains(t, strings.ToLower(w)) {
				return s + 1_000_000
			}
		}
		return s
	}
	sort.SliceStable(items, func(i, j int) bool { return score(items[i]) > score(items[j]) })
	return items
}

// admissionReason contains the same content decisions for collection and diagnostics.
func (u *U) admissionReason(a jx.M, item jx.M, topic, text string) string {
	if reason := u.contentAdmissionReason(a, item, topic, text); reason != "" {
		return reason
	}
	if u.SearchTopicKey() != topic {
		return "тема поиска сменилась"
	}
	return ""
}

// contentAdmissionReason is shared by collection and read-only replay diagnostics.
func (u *U) contentAdmissionReason(a jx.M, item jx.M, topic, text string) string {
	if ad, ok := a["advertising"].(bool); !ok || ad {
		return "реклама или не проверена на рекламу"
	}
	if PostIsAdvertisement(item) || IsSpamContent(CleanPostAttribution(text, item), jx.Str(a, "description")) {
		return "спам"
	}
	o := DefaultQualityOpts()
	o.Threshold = u.LLM.ThresholdForModel(jx.Str(a, "model"), u.TInt("relevance_threshold"))
	if reason := u.QualityReasons(a, item, topic, o); reason != "" {
		return reason
	}
	if u.professionalValueTopic(topic) && professionalAnalysisLowValue(a) {
		return "нет конкретной пользы, фактов или интересного вывода"
	}
	if u.ContentAdultBlocked(jx.Merge(item, jx.M{"tags": a["tags"]}), topic) {
		return "18+ и чувствительный контент выключены"
	}
	if a["ukraine"] == true && u.UAFilterOn() {
		return "украинский контент"
	}
	return ""
}

func (u *U) admit(a jx.M, item jx.M, topic, verdict, stage, text, evidence string, h History) bool {
	reason := u.admissionReason(a, item, topic, text)
	if u.run != nil {
		incrementCollectorCount(&u.run.AdmissionOutcomes, collectorAdmissionCode(reason))
	}
	if u.DB != nil && reason != "тема поиска сменилась" {
		if err := recordAutopilotAdmission(u.Ctx, u.DB.Pool, u.ID, topic, item, reason == "", now()); err != nil {
			u.Log("Не удалось сохранить наблюдение автопилота: %v", err)
		}
	}
	if passed, known := collectorSourceVerdict(a, reason, evidence); known {
		// A user's filter or complaint is not a global source-quality signal.
		if u.DB != nil && !u.analysisPrivate(topic, item) && len(u.TopicComplaints(topic, h)) == 0 {
			u.SourceVerdictRecord(topic, ContentSourceKey(item), passed)
		}
		u.sourceMissNote(ContentSourceKey(item), passed)
	}
	if reason != "" {
		u.Log("-> Отброшено: %s (%s).", reason, verdict)
		u.screen(topic, stage, item, int(jx.Int(a, "relevance")), false, reason, jx.Str(a, "model"))
		return false
	}
	u.screen(topic, stage, item, int(jx.Int(a, "relevance")), true, "", jx.Str(a, "model"))
	return true
}

// analyzeCandidateVision is the common uncached image evaluation used by the collector and the AI test.
func (u *U) analyzeCandidateVision(frames [][]byte, item jx.M, h History, topic string, video *Storyboard) (Analysis, string, error) {
	context := AnalysisContextBlock(item)
	caption := ""
	if context == "" {
		caption = PostCaptionForAI(item)
	}
	a, err := u.AnalyzeContentWithVision(frames, caption, h, topic, video, u.LLM.Settings().ThinkInParser, context, item)
	return a, caption, err
}

func analysisRecord(a, item jx.M, topic, defFormat string) jx.M {
	desc := jx.Str(a, "description")
	if desc == "" {
		desc = "Описание не получено"
	}
	why := jx.Str(a, "why_relevant")
	if why == "" {
		why = "Нейросеть не сформулировала объяснение."
	}
	format := jx.Str(a, "format")
	if format == "" {
		format = defFormat
	}
	rec := jx.Copy(item)
	delete(rec, "_query_rank_for")
	delete(rec, "_query_rank_score")
	delete(rec, "_inventory_rank_for")
	delete(rec, "_inventory_rank")
	for k, v := range (jx.M{"description": desc, "why_relevant": why, "relevance": a["relevance"], "rating": nil, "format": format, "tags": orList(jx.List(a, "tags")),
		"tags_classification_pending": jx.Bool(a, "tags_classification_pending"), "tags_classification_reason": jx.Str(a, "tags_classification_reason"),
		"quote": jx.Bool(a, "quote"), "entity": jx.Str(a, "entity"), "evidence": jx.Str(a, "evidence"), "source_hashtags": PostSourceHashtags(item), "topic": topic, "analysis_model": jx.Str(a, "model"),
		"interest": a["interest"], "practical_value": a["practical_value"], "specificity": a["specificity"]}) {
		rec[k] = v
	}
	// Метка — по содержанию поста (place от модели); нет места — поля geo в записи нет
	if g := GeoClean(a["geo"]); g != nil {
		rec["geo"] = g
	} else {
		delete(rec, "geo")
	}
	return rec
}

// analyzeTextCandidate — пост без картинки: оценка текста, те же проверки, запись без картинки.
func (u *U) analyzeTextCandidate(item jx.M, topic string) bool {
	text := AnalysisText(item)
	if !u.TBool("text_posts") || text == "" {
		return false
	}
	if u.run != nil {
		if u.run.TextAnalysesLeft <= 0 {
			u.run.TextAnalysesDeferred++
			u.Log("-> Текстовых: проанализировано %d, отложено %d (лимит за проход).", u.TInt("text_per_pass"), u.run.TextAnalysesDeferred)
			return false
		}
		u.run.TextAnalysesLeft--
	}
	u.Log("Полный анализ текстовой записи %s (без картинки)", candidateLogLabel(item))
	h := u.History()
	ownerOnly := !u.collectorPublicEvidence(item, topic)
	res := u.cachedCollectorTextBatch([]string{text}, topic, h, u.analysisPrivate(topic, item), true, ownerOnly)
	if len(res) == 0 || res[0] == nil {
		if u.run != nil {
			u.run.UnknownAnalyses++
		}
		u.Log("-> ИИ не вернул подтверждённую оценку текста, откладываю.")
		return false
	}
	a := res[0]
	// A valid relevance verdict is reusable even when the optional tag
	// classifier failed. Retry only tags, and persist successful completion.
	if jx.Bool(a, "tags_classification_pending") && !PostItemHasVideo(item) {
		private := u.analysisPrivate(topic, item)
		u.applyContentClassification(a, "", text, private)
		if !jx.Bool(a, "tags_classification_pending") && (private || ownerOnly || len(u.contentSafetyTags(a["tags"])) == 0) {
			u.writeAnalysisCache(map[string]jx.M{u.collectorTextKey(text, topic, h, private, true, ownerOnly): a})
		}
	}
	if PostItemHasVideo(item) {
		u.applyVideoClassification(a, item, "", PostCaptionForAI(item), u.analysisPrivate(topic, item))
	}
	verdict := fmt.Sprintf("соответствие теме %d%%", jx.Int(a, "relevance"))
	u.classifyPublicPost(item, a, topic, h)
	evidence := "text"
	if PostItemHasMedia(item) {
		evidence = "caption_only"
	}
	if !u.admit(a, item, topic, verdict, "text", text, evidence, h) {
		return false
	}
	u.Log("-> Принято: %s.", verdict)
	u.PostSearchSuggest(item, topic)
	rec := analysisRecord(a, item, topic, "text")
	u.GeoFill(rec)
	rec["image_local"], rec["phash"], rec["image"], rec["images"], rec["captions"] = "", "", "", []any{}, []any{text}
	return u.AddItemToPoolSafely(rec)
}

// analyzeCandidate — полный анализ (видео — раскадровкой), порог, кэш медиа и запись в пул.
func (u *U) analyzeCandidate(item jx.M, poster []byte, h History, topic string) bool {
	if jx.Str(item, "image") == "" {
		return u.analyzeTextCandidate(item, topic)
	}
	isVideo := PostItemHasVideo(item)
	kind := "картинка"
	if isVideo {
		kind = "видео"
	}
	u.Log("Полный анализ %s %s %s", candidateLogLabel(item), kind, jx.Str(item, "image"))
	var video *Storyboard
	mixed := isVideo && len(ContentGallery(item)) > 1
	frames := [][]byte{poster}
	var galleryBytes map[string][]byte
	cachedVideo := ""
	if mixed {
		var ok bool
		item, frames, galleryBytes, ok = u.prepareMixedAlbumVision(item, poster)
		if !ok {
			u.Log("-> Альбом ещё не удалось исследовать целиком; отложен без оценки.")
			return false
		}
	} else if direct := jx.Str(item, "video"); direct != "" {
		cachedVideo = u.MediaExisting(jx.Str(item, "video_local"))
		if cachedVideo == "" {
			cachedVideo = u.MediaExisting(direct)
		}
		if cachedVideo != "" {
			video = u.VideoStoryboard(u.MediaPath(cachedVideo))
			if video == nil {
				u.Log("-> Не удалось извлечь кадры сохранённого видео; отложено.")
				return false
			}
		} else {
			video = u.VideoAnalysisInput(direct, poster)
			if video.File != "" {
				defer os.Remove(video.File)
			}
		}
	} else if jx.Str(item, "video_via") == "ytdlp" && jx.Int(item, "duration") <= ytdlpWatchMaxSeconds {
		var cleanup func()
		video, cleanup = u.VideoPageAnalysisInput(jx.Str(item, "video_page"), poster)
		defer cleanup()
	}
	if video != nil {
		frames = video.Frames
	} else if !isVideo {
		frames, galleryBytes = galleryVisionFrames(item, poster, u.discoveryImageBytes)
	}
	storyboard := video != nil && len(frames) > 1
	context := AnalysisContextBlock(item)
	caption := ""
	if context == "" {
		caption = PostCaptionForAI(item)
	}
	var sb *Storyboard
	if storyboard {
		sb = video
	}
	cacheKey := u.collectorVisionKey(item, topic, h, frames, sb, caption, context)
	private := u.analysisPrivate(topic, item)
	a := u.readAnalysisCache([]string{cacheKey})[cacheKey]
	if a != nil {
		if u.run != nil {
			u.run.VisionCacheHits++
		}
		u.Log("-> Анализ взят из кэша: совпадают содержание, кадры и настройки модели.")
		if !isVideo && jx.Bool(a, "tags_classification_pending") {
			tagCaption := PostCaptionForAI(item)
			if tagCaption == "" {
				tagCaption = caption
			}
			u.applyContentClassification(a, jx.Str(a, "description"), tagCaption, private)
			if !jx.Bool(a, "tags_classification_pending") && (private || len(u.contentSafetyTags(a["tags"])) == 0) {
				u.writeAnalysisCache(map[string]jx.M{cacheKey: a})
			}
		}
	} else {
		var err error
		if u.run != nil {
			u.run.VisionCalls++
		}
		a, caption, err = u.analyzeCandidateVision(frames, item, h, topic, sb)
		if err != nil {
			if u.run != nil {
				u.run.UnknownAnalyses++
			}
			u.Log("-> ИИ не вернул корректную оценку: %s", err)
			return false
		}
		if private || (!u.ContentIsAdult(a) && len(u.contentSafetyTags(a["tags"])) == 0) {
			u.writeAnalysisCache(map[string]jx.M{cacheKey: a})
		}
	}
	if isVideo {
		localVideo := u.MediaPath(cachedVideo)
		if video != nil && video.File != "" {
			localVideo = video.File
		}
		u.applyVideoClassification(a, item, localVideo, PostCaptionForAI(item), u.analysisPrivate(topic, item))
	}
	if u.run != nil {
		incrementCollectorCount(&u.run.VisualEvidenceKinds, visualEvidenceKind(item, len(frames), sb))
	}
	verdict := fmt.Sprintf("соответствие теме %d%%", jx.Int(a, "relevance"))
	u.classifyPublicPost(item, a, topic, h)
	if !u.admit(a, item, topic, verdict, "analysis", caption, visualEvidenceKind(item, len(frames), sb), h) {
		return false
	}
	tags := u.ContentTagsPublic(a["tags"])
	tagNote := ""
	if len(tags) > 0 {
		var paths []string
		for _, t := range tags {
			paths = append(paths, jx.Str(t, "path"))
		}
		tagNote = " Метки: " + strings.Join(paths, ", ") + "."
	}
	u.Log("-> Принято: %s.%s", verdict, tagNote)
	u.PostSearchSuggest(item, topic)
	rec := analysisRecord(a, item, topic, "")
	delete(rec, "_album_frame_counts")
	rec["analysis_evidence"] = visualEvidenceKind(item, len(frames), sb)
	u.GeoFill(rec)
	rec["image_local"] = u.MediaStoreImage(jx.Str(item, "image"), poster)
	rec["phash"] = ImagePhash(poster)
	if isVideo && !mixed {
		direct, page := jx.Str(item, "video"), jx.Str(item, "video_page")
		if cachedVideo != "" {
			rec["video_local"] = cachedVideo
		} else if video != nil && video.File != "" {
			mediaKey := direct
			if mediaKey == "" {
				mediaKey = page
			}
			rec["video_local"] = u.CacheDownloadedVideo(mediaKey, video.File)
			if direct == "" && jx.Str(rec, "video_local") != "" {
				// The video was already downloaded through yt-dlp for
				// analysis. Do not enqueue the same download again.
				rec["video"] = rec["video_local"]
			}
		} else if direct != "" {
			rec["video_local"] = u.MediaStoreVideo(direct, "", false, nil)
		}
		if jx.Str(rec, "video_local") != "" {
			u.Log("-> Видео сохранено в кэш сайта.")
		} else {
			u.Log("-> Видео не удалось сохранить в кэш, на сайте будет исходная ссылка.")
		}
	}
	if g := jx.Maps(rec, "gallery"); len(g) > 0 {
		for i, f := range g {
			image := jx.Str(f, "image")
			if b := galleryBytes[image]; len(b) > 0 {
				g[i]["image_local"] = u.MediaStoreImage(image, galleryPrepareImage(b))
				g[i]["phash"] = ImagePhash(b)
			}
		}
		rec["gallery"] = u.GalleryCacheFiles(g, jx.Str(item, "image"))
		if mixed {
			for _, f := range g {
				if jx.Str(f, "image") == jx.Str(item, "image") && jx.Str(f, "video") == jx.Str(item, "video") {
					rec["video_local"] = jx.Str(f, "video_local")
					break
				}
			}
		}
	}
	if jx.Str(rec, "image_local") == "" {
		u.Log("-> Картинку не удалось сохранить в кэш, на сайте будет исходная ссылка.")
	}
	added := u.AddItemToPoolSafely(rec)
	if added {
		u.Log("[!] Запись сохранена и выведена на сайт.")
	} else {
		u.Log("-> Запись уже в пуле или оценена пользователем, пропускаю.")
	}
	return added
}
