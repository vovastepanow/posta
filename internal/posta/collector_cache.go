package posta

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// ---- Кэши оценок и итоги каналов ----

func SourcePassRates(verdicts map[string][2]int) map[string]float64 {
	out := map[string]float64{}
	for k, c := range verdicts {
		out[k] = float64(c[0]+1) / float64(c[1]+2)
	}
	return out
}

// A cover or first album image is not enough to reject the entire post.
func collectorPrescreenComplete(item jx.M) bool {
	return !PostItemHasVideo(item) && jx.Str(item, "video_via") == "" && len(ContentGallery(item)) < 2
}

func (u *U) prescreenIsJunk(score *int, item jx.M) bool {
	return collectorPrescreenComplete(item) && score != nil && *score >= u.prescreenThreshold(item)
}

func (a *App) prescreenThreshold(item jx.M) int {
	t := a.TInt("prescreen_junk")
	if !ImageboardItem(item) && jx.Bool(item, "thread_trust") {
		t += config.ThreadTrustPrescreenBonus
	}
	return t
}

func (u *U) sourceMissNote(src string, passed bool) {
	if u.run == nil {
		return
	}
	if passed {
		u.run.SourceMisses[src] = 0
	} else {
		u.run.SourceMisses[src]++
	}
}

// SourceVerdictRecord — итог полного анализа по каналу и теме (скользящее окно SourceVerdictWindow).
func (a *App) SourceVerdictRecord(topic, src string, passed bool) {
	if topic == "" || src == "" {
		return
	}
	p := 0
	if passed {
		p = 1
	}
	w := config.SourceVerdictWindow
	a.DB.Pool.Exec(a.ctxOrBg(), `INSERT INTO source_verdicts (topic, source, passed, total, at) VALUES ($1, $2, $3, 1, $4)
		ON CONFLICT (topic, source) DO UPDATE SET
		  passed = (CASE WHEN source_verdicts.total >= $5 THEN round(source_verdicts.passed * ($5 - 1)::numeric / $5)::int ELSE source_verdicts.passed END) + $3,
		  total = (CASE WHEN source_verdicts.total >= $5 THEN $5 - 1 ELSE source_verdicts.total END) + 1,
		  at = $4`, topic, src, p, now(), w)
}

func (a *App) TopicVerdictStats(topic string) map[string][2]int {
	out := map[string][2]int{}
	rows, err := a.DB.Pool.Query(a.ctxOrBg(), `SELECT source, passed, total FROM source_verdicts WHERE topic = $1`, topic)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var p, t int
		if rows.Scan(&s, &p, &t) == nil {
			out[s] = [2]int{p, t}
		}
	}
	return out
}

type screenedEntry struct {
	URL, Topic, Source, Phash string
	Score                     int
}

func (a *App) screenedCacheLoad(topicKey string) []screenedEntry {
	rows, err := a.DB.Pool.Query(a.ctxOrBg(), `SELECT url, topic, source, score, phash FROM screened_cache WHERE ver = $1 AND lower(topic) = lower($2) AND at > $3`,
		config.ScreenedCacheVersion, topicKey, now()-config.ScreenedCacheDays*config.DaySeconds)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []screenedEntry
	for rows.Next() {
		var e screenedEntry
		if rows.Scan(&e.URL, &e.Topic, &e.Source, &e.Score, &e.Phash) == nil {
			out = append(out, e)
		}
	}
	return out
}

func (a *App) cacheMerge(entries map[string]screenedEntry) {
	if len(entries) == 0 {
		return
	}
	ctx := a.ctxOrBg()
	for url, e := range entries {
		a.DB.Pool.Exec(ctx, `INSERT INTO screened_cache (url, topic, source, score, phash, at, ver) VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (url) DO UPDATE SET topic=EXCLUDED.topic, source=EXCLUDED.source, score=EXCLUDED.score, phash=EXCLUDED.phash, at=EXCLUDED.at, ver=EXCLUDED.ver`,
			url, e.Topic, e.Source, e.Score, e.Phash, now(), config.ScreenedCacheVersion)
	}
	a.DB.Pool.Exec(ctx, `DELETE FROM screened_cache WHERE at < $1`, now()-config.ScreenedCacheDays*config.DaySeconds)
	a.DB.Pool.Exec(ctx, `DELETE FROM screened_cache WHERE url IN (SELECT url FROM screened_cache ORDER BY at DESC OFFSET $1)`, config.ScreenedCacheLimit)
}

// AnalysisCacheKey — ключ общего кэша анализа: версия промпта, запись, тема.
func AnalysisCacheKey(item jx.M, topic string) string {
	t := strings.ToLower(strings.TrimSpace(topic))
	suffix := ""
	if ImageboardItem(item) {
		suffix = "|imageboard-media-only-v1"
	}
	if contains(config.SubtopicCacheTopics, t) {
		suffix += "|subtopic2"
	}
	// The same poster may belong to distinct videos, and short text posts
	// have no CandidateTextKey. Bind the verdict to the actual model input.
	var media []any
	for _, f := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		media = append(media, []any{ContentImageKey(f), jx.Strs(f, "images"), jx.Str(f, "video"), jx.Str(f, "video_page"), jx.Str(f, "video_via")})
	}
	caption := AnalysisText(item)
	if ImageboardItem(item) {
		caption = ""
	}
	h := sha1.Sum(jx.Marshal([]any{config.AnalysisPromptVersion, "v5content", t, suffix,
		CandidatePostKey(item), media, caption, PostCaptionForAI(item), AnalysisContextBlock(item)}))
	return hex.EncodeToString(h[:])
}

// A user's complaint examples affect the vision prompt and must never be
// reused for another user or after those examples change.
func (u *U) analysisCacheKey(item jx.M, topic string, h History) string {
	base := AnalysisCacheKey(item, topic)
	if u.openProfilesEnabled() {
		sum := sha256.Sum256([]byte("open-editorial-v1|" + base))
		base = hex.EncodeToString(sum[:])
	}
	if h == nil {
		h = EmptyHistory()
	}
	complaints := TopicComplaintTexts(u.TopicComplaints(topic, h), TopicComplaintExamples)
	if len(complaints) == 0 && !u.analysisPrivate(topic, item) {
		return base
	}
	sum := sha1.Sum([]byte(base + "|" + u.ID + "|" + string(jx.Marshal(complaints))))
	return hex.EncodeToString(sum[:])
}

// A cached poster-only assessment must never replace a storyboard verdict.
// Hash the actual frames plus the exact rendered prompt and configured route.
func visionEvidenceKey(base, prompt, settings string, frames [][]byte) string {
	hashes := make([]string, len(frames))
	for i, frame := range frames {
		sum := sha256.Sum256(frame)
		hashes[i] = hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(jx.Marshal([]any{"vision-evidence-v1", base, prompt, settings, hashes}))
	return hex.EncodeToString(sum[:])
}

func (u *U) collectorVisionKey(item jx.M, topic string, h History, frames [][]byte, video *Storyboard, caption, context string) string {
	s := u.LLM.Settings()
	settings := string(jx.Marshal([]any{s.Roles, s.FastOnly, s.ThinkInParser, u.contentAnalysisSchemaForTopic(TopicQueryOf(topic), topic)}))
	base := u.analysisCacheKey(item, topic, h)
	if !u.collectorPublicEvidence(item, topic) {
		base += "|owner:" + u.ID
	}
	return visionEvidenceKey(base, u.editableVisionPrompt(topic, strings.TrimSpace(caption), h, video, context, visualEvidenceDescription(item, len(frames), video)), settings, frames)
}
