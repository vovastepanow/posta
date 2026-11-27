package posta

import "posta/internal/jx"

// ParserLastPass — воронка последнего прохода пользователя; пусто, если проходов ещё не было.
func (a *App) ParserLastPass(uid string) jx.M {
	return KV(a, a.ctxOrBg(), parserLastPassKey(uid), jx.M(nil))
}

// parserLastPassKey — ключ kv с воронкой последнего прохода: конвейер подбора показывает её вне работы.
func parserLastPassKey(uid string) string { return "parser_funnel:" + uid }

// passFunnel — счётчики воронки прохода для конвейера подбора: сбор → фильтры → ПК-фильтр → быстрая оценка → анализ → пул.
// skipped — причины отсева кандидатов; ключ ukraine_sources считает источники, а не посты, поэтому в сумму не входит.
func passFunnel(collected, candidates int, skipped queueSkip, prescreen map[string]int, analyzed, added int) jx.M {
	byReason, filtered := jx.M{}, 0
	for k, n := range skipped {
		if k == "ukraine_sources" || k == "source_busy_returned" || n == 0 {
			continue
		}
		byReason[k] = n
		filtered += n
	}
	return jx.M{"collected": collected, "candidates": candidates, "filtered": filtered, "filtered_by": byReason,
		"prescreen_rejected": prescreen["rejected"], "duplicates": prescreen["duplicates"], "analyzed": analyzed, "added": added}
}
