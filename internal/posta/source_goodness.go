package posta

import (
	"fmt"
	"sort"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Годнота источника (0–100) — отдельно от балла темы: активность, отклик, оригинальность и доля рекламы.

// GoodnessInput — метрики источника. Компонента без данных не считается, её вес уходит остальным.
type GoodnessInput struct {
	ViewRate    *float64 // доля просмотров от подписчиков
	ViewMedian  float64  // медиана доли просмотров по площадке (по реестру); 0 — медианы ещё нет
	LastPostAge int64    // секунд с последнего поста; -1 — неизвестно
	PerWeek     float64  // постов в неделю
	AdShare     float64  // доля рекламных постов, 0–1
	LinkShare   float64  // доля постов-ссылок без текста, 0–1
	HasPosts    bool     // есть посты для оценки оригинальности и рекламы
	Taste       *int     // оценка вкуса владельца моделью, 0–100
}

// GoodnessResult — итог: балл, баллы по компонентам и строка для причины предложения.
type GoodnessResult struct {
	Score int
	Parts map[string]int
	Note  string
}

// goodnessAdCap — рекламы больше этой доли достаточно, чтобы годнота не выше GoodnessAdCap.
const (
	goodnessAdLimit  = 0.3
	goodnessAdCap    = 40  // потолок годноты: реклама сверх лимита или мёртвый канал
	goodnessDeadLife = 30  // компонент «жизнь» ниже — канал мёртв
	goodnessAggr     = 200 // постов в неделю: выше — агрегатор
)

func clampScore(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 100 {
		return 100
	}
	return x
}

// SourceGoodness — годнота по метрикам; компоненты с весами из config.GoodnessWeights, итог — взвешенное среднее по имеющимся.
func SourceGoodness(in GoodnessInput) GoodnessResult {
	parts := map[string]float64{}
	if in.ViewRate != nil {
		if in.ViewMedian > 0 {
			parts["отклик"] = clampScore(50 * *in.ViewRate / in.ViewMedian)
		} else {
			parts["отклик"] = clampScore(*in.ViewRate / 0.3 * 100)
		}
	}
	if in.LastPostAge >= 0 {
		life := 100.0
		maxAge := int64(config.ContentMaxAgeDays * config.DaySeconds)
		if in.LastPostAge > maxAge {
			life = clampScore(100 - 100*float64(in.LastPostAge-maxAge)/float64(maxAge))
		}
		if in.PerWeek > goodnessAggr {
			life = min(life, 30)
		} else if in.PerWeek > 0 && in.PerWeek < 1 {
			life = min(life, 50)
		}
		parts["жизнь"] = life
	}
	if in.HasPosts {
		parts["оригинальность"] = clampScore(100 * (1 - in.LinkShare))
		parts["реклама"] = clampScore(100 * (1 - in.AdShare/goodnessAdLimit))
	}
	if in.Taste != nil {
		parts["вкус"] = clampScore(float64(*in.Taste))
	}
	var sum, wsum float64
	for name, p := range parts {
		w := config.GoodnessWeights[name]
		sum += w * p
		wsum += w
	}
	score := 0.0
	if wsum > 0 {
		score = sum / wsum
	}
	if in.HasPosts && in.AdShare > goodnessAdLimit {
		score = min(score, goodnessAdCap)
	}
	if life, ok := parts["жизнь"]; ok && life < goodnessDeadLife {
		score = min(score, goodnessAdCap) // мёртвый канал не бывает годным, что бы ни говорили остальные компоненты
	}
	res := GoodnessResult{Score: int(score + 0.5), Parts: map[string]int{}}
	for name, p := range parts {
		res.Parts[name] = int(p + 0.5)
	}
	res.Note = goodnessNote(res.Parts)
	return res
}

// goodnessNote — короткая строка по компонентам для причины: «отклик 70%, жизнь 90%, …».
func goodnessNote(parts map[string]int) string {
	order := []string{"отклик", "жизнь", "оригинальность", "реклама", "вкус"}
	var out []string
	for _, name := range order {
		if p, ok := parts[name]; ok {
			out = append(out, fmt.Sprintf("%s %d%%", name, p))
		}
	}
	return strings.Join(out, ", ")
}

// goodnessPostShares — доля рекламных постов и доля постов-ссылок без текста.
func goodnessPostShares(posts []jx.M) (ad, link float64) {
	if len(posts) == 0 {
		return 0, 0
	}
	var nAd, nLink int
	for _, p := range posts {
		if PostSearchIsAd(p) {
			nAd++
		}
		if strings.TrimSpace(jx.Str(p, "text")) == "" && jx.Str(p, "url") != "" {
			nLink++
		}
	}
	n := float64(len(posts))
	return float64(nAd) / n, float64(nLink) / n
}

// DiscoveryBudget — делит проверки прогона между видами находки: доля «да» владельца по виду, с минимумом goodnessFloor
// (чтобы новые виды тоже пробовались). kinds — сколько кандидатов каждого вида в очереди; виды без кандидатов не получают долю.
func DiscoveryBudget(limit int, kinds map[string]int, report map[string]map[string]int) map[string]int {
	const floor = 0.1
	caps := map[string]int{}
	weights := map[string]float64{}
	var total float64
	for kind, n := range kinds {
		if n <= 0 {
			continue
		}
		r := report[kind]
		found, yes := float64(r["found"]), float64(r["approve"])
		// сглаживание: без данных — треть «да»
		w := (yes + 1) / (found + 3)
		weights[kind] = w
		total += w
	}
	if total == 0 || limit <= 0 {
		return caps
	}
	// минимум — виды ниже floor фиксируются на нём, остаток делится между остальными пропорционально весам
	shares := map[string]float64{}
	fixed := map[string]bool{}
	for {
		var restW float64
		for kind, w := range weights {
			if !fixed[kind] {
				restW += w
			}
		}
		left := 1 - floor*float64(len(fixed))
		raised := false
		for kind, w := range weights {
			if fixed[kind] {
				continue
			}
			if s := left * w / restW; s < floor {
				fixed[kind], shares[kind] = true, floor
				raised = true
			}
		}
		if !raised {
			for kind, w := range weights {
				if !fixed[kind] {
					shares[kind] = left * w / restW
				}
			}
			break
		}
	}
	for kind, s := range shares {
		caps[kind] = max(1, int(float64(limit)*s+0.5))
	}
	return caps
}

// discoveryBudgetLines — распределение бюджета для журнала: «вид — N из M».
func discoveryBudgetLines(caps map[string]int, names map[string]string) string {
	kinds := make([]string, 0, len(caps))
	for k := range caps {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var parts []string
	for _, k := range kinds {
		label := names[k]
		if label == "" {
			label = k
		}
		parts = append(parts, fmt.Sprintf("%s %d", label, caps[k]))
	}
	return strings.Join(parts, ", ")
}

// registryViewMedian — медиана доли просмотров по площадке (по реестру); 0 — данных ещё нет.
func (u *U) registryViewMedian(platform string) float64 {
	var m *float64
	if u.DB.Pool.QueryRow(u.Ctx, `SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY (metrics->>'view_rate')::float)
		FROM source_registry WHERE platform = $1 AND jsonb_typeof(metrics->'view_rate') = 'number'`, platform).Scan(&m) != nil || m == nil {
		return 0
	}
	return *m
}

// candidateGoodness — годнота кандидата по общим метрикам источника.
func (u *U) candidateGoodness(c DiscoveryCandidate, posts []jx.M, rate *float64, activity jx.M) GoodnessResult {
	in := GoodnessInput{ViewRate: rate, LastPostAge: -1, PerWeek: jx.Float(activity, "per_week"), HasPosts: len(posts) > 0}
	if lp := jx.Int(activity, "last_post"); lp > 0 {
		in.LastPostAge = now() - lp
	}
	in.ViewMedian = u.registryViewMedian(c.Platform)
	in.AdShare, in.LinkShare = goodnessPostShares(posts)
	res := SourceGoodness(in)
	return res
}

// kindLabels — подписи видов находки для журнала.
var discoveryKindLabels = map[string]string{"post_search": "посты", "similar": "похожие", "mentioned": "упоминания", "model_handle": "модель",
	"tg_name": "поиск по имени", "deferred": "отложенные", "keyword": "слова темы"}
