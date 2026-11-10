package posta

import (
	"fmt"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Качество подбора: жёсткие правила отбора после ответа модели и разбор истории оценок.

// TopicIsHumor — «Мемы», «Юмор» и их подтемы, а также пошлый юмор; работает и в составных темах.
func (a *App) TopicIsHumor(topic string) bool {
	for _, part := range TopicParts(topic) {
		for e := a.TopicLookup(part); e != nil; e = a.TopicParent(e) {
			k := e.Key
			if k == ContentLewdHumorTag || k == "humor" || k == "memes" || strings.HasPrefix(k, "humor.") || strings.HasPrefix(k, "memes.") {
				return true
			}
		}
	}
	return false
}

// QualityViewsRatio — просмотры поста / медиана соседних постов канала; nil — данных нет.
func QualityViewsRatio(item jx.M) *float64 {
	e := jx.Map(item, "engagement")
	med := jx.Float(e, "neighbors_median_views")
	if e == nil || med <= 0 {
		return nil
	}
	r := jx.Float(e, "views") / med
	return &r
}

type QualityOpts struct {
	Threshold                   int
	Hedge, Format, Dvach, Views bool
}

func DefaultQualityOpts() QualityOpts {
	// why_relevant содержит цитату из поста: слова «если» и «может»
	// в ней не означают неуверенность модели. Решение опирается на оценку.
	return QualityOpts{Threshold: config.RelevanceThreshold, Format: true}
}

// QualityReasons — причина отказа или "".
func (a *App) QualityReasons(an jx.M, item jx.M, topic string, o QualityOpts) string {
	if an["topic_match"] == false {
		return "не подтверждены обязательные признаки темы"
	}
	rel := int(jx.Int(an, "relevance"))
	if rel < o.Threshold {
		return fmt.Sprintf("порог %d: оценка %d", o.Threshold, rel)
	}
	why := jx.Str(an, "why_relevant")
	if o.Hedge && config.AnalysisHedgeRe.MatchString(why) {
		return "модель не уверена: „" + trimRunes(why, 60) + "…“"
	}
	if o.Format && a.TopicIsHumor(topic) && jx.Str(an, "format") == "photo" &&
		!((jx.Str(an, "meme_kind") == "finished_meme" || jx.Str(an, "meme_kind") == "funny_situation") && strings.TrimSpace(jx.Str(an, "joke")) != "") {
		return "обычное фото, а тема — мемы"
	}
	if o.Dvach && strings.HasPrefix(ContentSourceKey(item), "dvach:") {
		return "площадка Двач по этой теме отсеяна"
	}
	if r := QualityViewsRatio(item); o.Views && r != nil && *r < config.QualityViewsMinRatio {
		return fmt.Sprintf("просмотры ниже %v× медианы канала", config.QualityViewsMinRatio)
	}
	return ""
}

// PoolAdmission — причина отказа при добавлении в пул; "" — подходит.
func (a *App) PoolAdmission(an, item jx.M, topic string) string {
	o := DefaultQualityOpts()
	o.Threshold = a.LLM.ThresholdForModel(jx.Str(an, "model"), config.RelevanceThreshold)
	return a.QualityReasons(an, item, topic, o)
}
