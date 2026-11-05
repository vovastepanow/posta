package posta

import (
	"fmt"
	"math"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Качество подбора: жёсткие правила отбора после ответа модели и разбор истории оценок.

// TopicIsHumor — тема из веток «Мемы» и «Юмор» (сами ветки, их частные темы и синонимы).
func (a *App) TopicIsHumor(topic string) bool {
	parts := TopicParts(topic)
	if len(parts) == 1 {
		topic = parts[0]
	}
	for e := a.TopicLookup(topic); e != nil; e = a.TopicParent(e) {
		k := e.Key
		if k == "humor" || k == "memes" || strings.HasPrefix(k, "humor.") || strings.HasPrefix(k, "memes.") {
			return true
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
	return QualityOpts{Threshold: config.RelevanceThreshold, Hedge: true, Format: true}
}

// QualityReasons — причина отказа или "".
func (a *App) QualityReasons(an jx.M, item jx.M, topic string, o QualityOpts) string {
	rel := int(jx.Int(an, "relevance"))
	if rel < o.Threshold {
		return fmt.Sprintf("порог %d: оценка %d", o.Threshold, rel)
	}
	why := jx.Str(an, "why_relevant")
	if o.Hedge && config.AnalysisHedgeRe.MatchString(why) {
		return "модель не уверена: „" + trimRunes(why, 60) + "…“"
	}
	if o.Format && a.TopicIsHumor(topic) && jx.Str(an, "format") == "photo" {
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

// QualityPlatformBlocked — площадка режется по теме: много оценок и большая доля жалоб.
func (a *App) QualityPlatformBlocked(h History, prefix, topic string) bool {
	total, complaints := 0, 0
	for _, c := range []string{"liked", "disliked", "complaints"} {
		for _, r := range h[c] {
			if !a.TopicSame(jx.Str(r, "topic"), topic) || !strings.HasPrefix(ContentSourceKey(r), prefix+":") {
				continue
			}
			total++
			if c == "complaints" {
				complaints++
			}
		}
	}
	return total >= config.QualityPlatformMinRatings && float64(complaints)/float64(total) >= config.QualityPlatformComplaintShare
}

type share struct {
	Share float64
	N     int
}

func (a *App) qualityShares(h History, topic string, key func(jx.M) string) map[string]share {
	type cnt struct{ n, likes int }
	counts := map[string]*cnt{}
	for _, c := range []string{"liked", "disliked", "complaints"} {
		for _, r := range h[c] {
			if !a.TopicSame(jx.Str(r, "topic"), topic) {
				continue
			}
			k := key(r)
			if k == "" {
				continue
			}
			if counts[k] == nil {
				counts[k] = &cnt{}
			}
			counts[k].n++
			if c == "liked" {
				counts[k].likes++
			}
		}
	}
	out := map[string]share{}
	for k, c := range counts {
		out[k] = share{float64(c.likes) / float64(c.n), c.n}
	}
	return out
}

type QualityContext struct {
	Sources   map[string]share
	Tags      map[string]share
	LikeShare float64
}

func (a *App) QualityContextFor(h History, topic string) QualityContext {
	src := a.qualityShares(h, topic, ContentSourceKey)
	tags := a.qualityShares(h, topic, func(r jx.M) string {
		if t := jx.Strs(r, "tags"); len(t) > 0 {
			return t[0]
		}
		return ""
	})
	total, likes := 0, 0.0
	for _, s := range src {
		total += s.N
		likes += s.Share * float64(s.N)
	}
	ls := config.QualitySourceNeutralShare
	if total > 0 {
		ls = likes / float64(total)
	}
	return QualityContext{src, tags, ls}
}

// CardScore — балл карточки: релевантность + популярность + доля лайков источника + вкус по первой метке.
func CardScore(item jx.M, ctx *QualityContext) float64 {
	score := jx.Float(item, "relevance")
	if r := QualityViewsRatio(item); r != nil {
		switch {
		case *r >= 2:
			score += 10
		case *r >= 1.3:
			score += 5
		case *r < config.QualityViewsMinRatio:
			score -= 10
		}
	}
	if ctx == nil {
		return score
	}
	if s, ok := ctx.Sources[ContentSourceKey(item)]; ok && s.N >= config.QualityCardMinRatings {
		score += math.Max(-config.QualitySourceCap, math.Min(config.QualitySourceCap, (s.Share-config.QualitySourceNeutralShare)*config.QualitySourceWeight))
	}
	if tags := jx.Strs(item, "tags"); len(tags) > 0 {
		if t, ok := ctx.Tags[tags[0]]; ok && t.N >= config.QualityTagMinRatings {
			score += math.Max(-config.QualityTagCap, math.Min(config.QualityTagCap, (t.Share-ctx.LikeShare)*config.QualityTagWeight))
		}
	}
	return score
}
