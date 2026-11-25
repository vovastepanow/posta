package posta

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
)

// The content-only text verdict can be shared across posts and users only
// when the rendered prompt, model configuration and exact full text agree.
// Personal feedback/private evidence receives an owner-scoped key.
func textVerdictKey(scope, mode, prompt, text, settings string) string {
	sum := sha256.Sum256(jx.Marshal([]any{"collector-text-v2", config.AnalysisPromptVersion, scope, mode, prompt, text, settings}))
	return hex.EncodeToString(sum[:])
}

func (u *U) collectorTextKey(text, topic string, h History, private, full, ownerOnly bool) string {
	scope := ""
	if private {
		scope = "private:" + u.ID
	} else if ownerOnly || u.complaintPrompt(topic, h) != "" {
		scope = "owner:" + u.ID
	}
	mode := "caption"
	if full {
		mode = "full"
	}
	s := u.LLM.Settings()
	// Include the configured text and classifier routes; a change of model
	// or prompt cannot reuse a verdict from the former setup.
	settings := string(jx.Marshal([]any{s.Roles, s.FastOnly}))
	return textVerdictKey(scope, mode, u.editableTextPrompt(topic, []string{text}, h), text, settings)
}

// A model can return a fluent but invented quotation, or copy evidence from
// another row of a batch. Check it against the actual submitted text before
// using the result as a positive OR negative decision. Punctuation and case
// differences are harmless; translated or invented evidence is not a quote.
func collectorTextEvidenceValid(a jx.M, text string) bool {
	if !validModelContentVerdict(a) {
		return false
	}
	normalize := func(s string) string {
		return collapseSpaces(ctkPunctRe.ReplaceAllString(strings.ReplaceAll(strings.ToLower(s), "ё", "е"), " "))
	}
	evidence := normalize(jx.Str(a, "evidence"))
	if evidence == "" {
		return jx.Int(a, "relevance") <= config.QualityNoEvidenceCap
	}
	return strings.Contains(normalize(textAnalysisRecord(text)), evidence)
}

func (u *U) cachedCollectorTextBatch(texts []string, topic string, h History, private, full, ownerOnly bool) []Analysis {
	out := make([]Analysis, len(texts))
	if len(texts) == 0 {
		return out
	}
	indices := map[string][]int{}
	var unique []string
	for i, text := range texts {
		key := u.collectorTextKey(text, topic, h, private, full, ownerOnly)
		if len(indices[key]) == 0 {
			unique = append(unique, key)
		}
		indices[key] = append(indices[key], i)
	}
	cached := u.readAnalysisCache(unique)
	var missingKeys, missingTexts []string
	for _, key := range unique {
		if a := cached[key]; collectorTextEvidenceValid(a, texts[indices[key][0]]) {
			for _, i := range indices[key] {
				out[i] = jx.Clone(a)
			}
			continue
		}
		missingKeys = append(missingKeys, key)
		missingTexts = append(missingTexts, texts[indices[key][0]])
	}
	if u.run != nil {
		u.run.TextCacheHits += len(unique) - len(missingKeys)
	}
	if len(missingKeys) < len(unique) {
		u.Log("-> Текстовый анализ из кэша: %d из %d уникальных записей.", len(unique)-len(missingKeys), len(unique))
	}
	if len(missingKeys) == 0 {
		return out
	}
	fresh := map[string]jx.M{}
	for off := 0; off < len(missingKeys); off += 6 {
		end := min(len(missingKeys), off+6)
		if u.run != nil {
			u.run.TextBatchCalls++
		}
		res, err := u.analyzeTextBatch(missingTexts[off:end], topic, h, private, full)
		if err != nil {
			u.Log("-> Не удалось оценить текст: %v", err)
			continue
		}
		for i, a := range res {
			if i >= end-off {
				break
			}
			text := missingTexts[off+i]
			if !collectorTextEvidenceValid(a, text) && a != nil && end-off > 1 && u.Alive() {
				// A conflicting batch quotation gets one independent retry.
				if u.run != nil {
					u.run.TextBatchCalls++
				}
				single, err := u.analyzeTextBatch([]string{text}, topic, h, private, full)
				if err == nil && len(single) == 1 {
					a = single[0]
				}
			}
			if !collectorTextEvidenceValid(a, text) {
				if a != nil {
					u.Log("-> Цитата модели не подтверждается исходным текстом: оценка не сохранена, запись отложена.")
				}
				continue
			}
			key := missingKeys[off+i]
			for _, pos := range indices[key] {
				out[pos] = jx.Clone(a)
			}
			// Relevance remains valid even if optional classification is
			// pending. Retry only the classifier when the post reaches admission.
			if !private && !ownerOnly && (u.ContentIsAdult(a) || len(u.contentSafetyTags(a["tags"])) > 0) {
				continue
			}
			fresh[key] = a
		}
	}
	u.writeAnalysisCache(fresh)
	return out
}

func (u *U) readAnalysisCache(keys []string) map[string]jx.M {
	out := map[string]jx.M{}
	if len(keys) == 0 || u.DB == nil {
		return out
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT key,analysis FROM analysis_cache WHERE key=ANY($1::text[]) AND at>$2`, keys, now()-config.AnalysisCacheDays*config.DaySeconds)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if rows.Scan(&key, &raw) == nil {
			a := jx.Unmarshal(raw)
			if validModelContentVerdict(a) {
				out[key] = a
			}
		}
	}
	return out
}

func (u *U) writeAnalysisCache(entries map[string]jx.M) {
	if len(entries) == 0 || u.DB == nil {
		return
	}
	batch := &pgx.Batch{}
	at := now()
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		batch.Queue(`INSERT INTO analysis_cache(key,analysis,at) VALUES($1,$2,$3) ON CONFLICT(key) DO UPDATE SET analysis=EXCLUDED.analysis,at=EXCLUDED.at`, key, jx.Marshal(entries[key]), at)
	}
	batch.Queue(`DELETE FROM analysis_cache WHERE at<$1`, at-config.AnalysisCacheDays*config.DaySeconds)
	batch.Queue(`DELETE FROM analysis_cache WHERE key IN (SELECT key FROM analysis_cache ORDER BY at DESC,key OFFSET $1)`, config.AnalysisCacheLimit)
	if err := u.DB.Pool.SendBatch(u.Ctx, batch).Close(); err != nil {
		u.Log("Не удалось сохранить кэш анализа: %v", err)
	}
}

// The global catalog is the only positive evidence that a post was collected
// publicly. Private integrations and unverified search hits remain owner scoped.
// Compare the normalized model inputs, ignoring poll counters and timestamps.
func publicEvidenceMatches(item, stored jx.M) bool {
	if item == nil || stored == nil || ContentSourceKey(item) != ContentSourceKey(stored) ||
		jx.Bool(stored, "source_sensitive") || jx.Bool(stored, "nsfw") || len(jx.ToStrs(stored["safety"])) != 0 {
		return false
	}
	key := CandidatePostKey(item)
	if key == "" || key != CandidatePostKey(stored) {
		return false
	}
	if AnalysisText(item) != AnalysisText(stored) || PostCaptionForAI(item) != PostCaptionForAI(stored) ||
		AnalysisContextBlock(item) != AnalysisContextBlock(stored) {
		return false
	}
	if ContentImageKey(item) != ContentImageKey(stored) || jx.Str(item, "video") != jx.Str(stored, "video") ||
		jx.Str(item, "video_page") != jx.Str(stored, "video_page") || jx.Str(item, "video_via") != jx.Str(stored, "video_via") {
		return false
	}
	return string(jx.Marshal(ContentGallery(item))) == string(jx.Marshal(ContentGallery(stored)))
}

func (u *U) collectorPublicEvidence(item jx.M, topic string) bool {
	if u.DB == nil || u.analysisPrivate(topic, item) || len(jx.ToStrs(item["safety"])) != 0 {
		return false
	}
	source, post := ContentSourceKey(item), CandidatePostKey(item)
	if post == "" || strings.HasPrefix(source, "own:") || strings.HasPrefix(source, "source:") {
		return false
	}
	fingerprint := source + "|" + AnalysisCacheKey(item, topic)
	if u.run != nil && u.run.PublicCacheScopes != nil {
		if ok, found := u.run.PublicCacheScopes[fingerprint]; found {
			return ok
		}
	}
	var raw []byte
	ok := false
	if err := u.DB.Pool.QueryRow(u.Ctx, `SELECT data FROM public_posts WHERE source_key=$1 AND post_key=$2`, source, post).Scan(&raw); err == nil {
		stored := jx.Unmarshal(raw)
		if publicEvidenceMatches(item, stored) {
			ok = true
		} else {
			for _, normalized := range ExpandPostImages([]jx.M{stored}) {
				if publicEvidenceMatches(item, normalized) {
					ok = true
					break
				}
			}
		}
	}
	if u.run != nil {
		if u.run.PublicCacheScopes == nil {
			u.run.PublicCacheScopes = map[string]bool{}
		}
		u.run.PublicCacheScopes[fingerprint] = ok
	}
	return ok
}
