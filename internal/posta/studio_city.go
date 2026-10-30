package posta

import (
	"math"
	"time"

	"posta/internal/jx"
)

// studioWeeks — сколько недель показывает «город» студии.
const studioWeeks = 52

// studioDayRow — число записей за день по одной теме; день — studioDayOf.
type studioDayRow struct {
	Day   int64
	Topic string
	N     int
}

// StudioStart — первый день «города»: воскресенье studioWeeks недель назад, полночь по часовому поясу сервера.
func StudioStart(now time.Time) time.Time {
	today := studioToday(now)
	return today.AddDate(0, 0, -studioWeeks*7-int(today.Weekday()))
}

// studioDays — число дней от start до сегодня включительно.
func studioDays(start, now time.Time) int {
	return int(math.Round(studioToday(now).Sub(start).Hours()/24)) + 1
}

func studioToday(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// studioDayOf — день записи: unix-время со смещением часового пояса, делённое на сутки.
func studioDayOf(unix int64, off int) int64 { return (unix + int64(off)) / 86400 }

// studioCounts — раскладывает строки по дням от start; keep решает, считать ли тему (nil — считать всё).
func studioCounts(start time.Time, days, off int, rows []studioDayRow, keep func(topic string) bool) []int {
	out := make([]int, days)
	base := studioDayOf(start.Unix(), off)
	for _, r := range rows {
		i := r.Day - base
		if i < 0 || i >= int64(days) || (keep != nil && !keep(r.Topic)) {
			continue
		}
		out[i] += r.N
	}
	return out
}

// StudioCity — по дням за studioWeeks недель: записи пула по текущей теме и публикации пользователя.
// Пул: pool_items и оценённые записи истории — по pooled_at (когда попал в пул). У старых оценённых записей
// pooled_at нет: берём timestamp, это момент решения, а не попадания в пул.
func (u *U) StudioCity(now time.Time) (start time.Time, pool, published []int, err error) {
	start = StudioStart(now)
	days := studioDays(start, now)
	_, off := now.Zone()
	from, to := start.Unix(), start.AddDate(0, 0, days).Unix()

	rows, err := u.DB.Pool.Query(u.Ctx, `
		SELECT (ts + $2::bigint) / 86400 AS day, topic, COUNT(*) AS n
		FROM (
			SELECT CASE WHEN data->>'pooled_at' ~ '^[0-9]+$' THEN (data->>'pooled_at')::bigint END AS ts,
				COALESCE(data->>'topic', '') AS topic
			FROM pool_items WHERE user_id = $1
			UNION ALL
			SELECT COALESCE(CASE WHEN data->>'pooled_at' ~ '^[0-9]+$' THEN (data->>'pooled_at')::bigint END,
					CASE WHEN data->>'timestamp' ~ '^[0-9]+$' THEN (data->>'timestamp')::bigint END),
				COALESCE(data->>'topic', '')
			FROM history_items WHERE user_id = $1 AND collection IN ('liked', 'disliked', 'undecided', 'complaints')
		) e
		WHERE ts >= $3::bigint AND ts < $4::bigint
		GROUP BY day, topic`, u.ID, off, from, to)
	if err != nil {
		return start, nil, nil, err
	}
	poolRows, err := scanStudioRows(rows, true)
	if err != nil {
		return start, nil, nil, err
	}

	rows, err = u.DB.Pool.Query(u.Ctx, `
		SELECT (at + $2::bigint) / 86400 AS day, COUNT(*) AS n
		FROM published WHERE user_id = $1 AND at >= $3::bigint AND at < $4::bigint
		GROUP BY day`, u.ID, off, from, to)
	if err != nil {
		return start, nil, nil, err
	}
	pubRows, err := scanStudioRows(rows, false)
	if err != nil {
		return start, nil, nil, err
	}

	topic, key := u.CurrentTopic(), u.SearchTopicKey()
	keep := func(t string) bool { return u.poolItemMatchesTopic(jx.M{"topic": t}, topic, key) }
	return start, studioCounts(start, days, off, poolRows, keep), studioCounts(start, days, off, pubRows, nil), nil
}

// scanStudioRows читает (день, тема, число) или (день, число) — без темы.
func scanStudioRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}, withTopic bool) ([]studioDayRow, error) {
	defer rows.Close()
	var out []studioDayRow
	for rows.Next() {
		r := studioDayRow{}
		var err error
		if withTopic {
			err = rows.Scan(&r.Day, &r.Topic, &r.N)
		} else {
			err = rows.Scan(&r.Day, &r.N)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ParserLastPass — воронка последнего прохода пользователя; пусто, если проходов ещё не было.
func (a *App) ParserLastPass(uid string) jx.M {
	return KV(a, a.ctxOrBg(), parserLastPassKey(uid), jx.M(nil))
}

// parserLastPassKey — ключ kv с воронкой последнего прохода: конвейер студии показывает её вне работы.
func parserLastPassKey(uid string) string { return "parser_funnel:" + uid }

// passFunnel — счётчики воронки прохода для конвейера студии: сбор → фильтры → ПК-фильтр → быстрая оценка → анализ → пул.
// skipped — причины отсева кандидатов; ключ ukraine_sources считает источники, а не посты, поэтому в сумму не входит.
func passFunnel(collected, candidates int, skipped queueSkip, prescreen map[string]int, analyzed, added int) jx.M {
	byReason, filtered := jx.M{}, 0
	for k, n := range skipped {
		if k == "ukraine_sources" || n == 0 {
			continue
		}
		byReason[k] = n
		filtered += n
	}
	return jx.M{"collected": collected, "candidates": candidates, "filtered": filtered, "filtered_by": byReason,
		"prescreen_rejected": prescreen["rejected"], "duplicates": prescreen["duplicates"], "analyzed": analyzed, "added": added}
}
