package posta

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
)

// The content-only text verdict can be shared across posts and users only
// when the rendered prompt, model configuration and exact full text agree.
// Personal feedback/private evidence receives an owner-scoped key.
func textVerdictKey(scope, mode, prompt, text, settings string) string {
	sum := sha256.Sum256(jx.Marshal([]any{"collector-text-v1", config.AnalysisPromptVersion, scope, mode, prompt, text, settings}))
	return hex.EncodeToString(sum[:])
}

func (u *U) collectorTextKey(text, topic string, h History, private, full bool) string {
	scope := ""
	if private || u.complaintPrompt(topic, h) != "" {
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

func (u *U) cachedCollectorTextBatch(texts []string, topic string, h History, private, full bool) []Analysis {
	out := make([]Analysis, len(texts))
	if len(texts) == 0 {
		return out
	}
	indices := map[string][]int{}
	var unique []string
	for i, text := range texts {
		key := u.collectorTextKey(text, topic, h, private, full)
		if len(indices[key]) == 0 {
			unique = append(unique, key)
		}
		indices[key] = append(indices[key], i)
	}
	cached := u.readAnalysisCache(unique)
	var missingKeys, missingTexts []string
	for _, key := range unique {
		if a := cached[key]; a != nil {
			for _, i := range indices[key] {
				out[i] = jx.Clone(a)
			}
			continue
		}
		missingKeys = append(missingKeys, key)
		missingTexts = append(missingTexts, texts[indices[key][0]])
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
		res, err := u.analyzeTextBatch(missingTexts[off:end], topic, h, private, full)
		if err != nil {
			u.Log("-> Не удалось оценить текст: %v", err)
			continue
		}
		for i, a := range res {
			if i >= end-off || !validModelContentVerdict(a) {
				continue
			}
			key := missingKeys[off+i]
			for _, pos := range indices[key] {
				out[pos] = jx.Clone(a)
			}
			// Relevance remains valid even if optional classification is
			// pending. Retry only the classifier when the post reaches admission.
			if !private && (u.ContentIsAdult(a) || len(u.contentSafetyTags(a["tags"])) > 0) {
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
	for key, a := range entries {
		batch.Queue(`INSERT INTO analysis_cache(key,analysis,at) VALUES($1,$2,$3) ON CONFLICT(key) DO UPDATE SET analysis=EXCLUDED.analysis,at=EXCLUDED.at`, key, jx.Marshal(a), at)
	}
	batch.Queue(`DELETE FROM analysis_cache WHERE at<$1`, at-config.AnalysisCacheDays*config.DaySeconds)
	batch.Queue(`DELETE FROM analysis_cache WHERE key IN (SELECT key FROM analysis_cache ORDER BY at DESC,key OFFSET $1)`, config.AnalysisCacheLimit)
	if err := u.DB.Pool.SendBatch(u.Ctx, batch).Close(); err != nil {
		u.Log("Не удалось сохранить кэш анализа: %v", err)
	}
}
