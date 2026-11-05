package posta

import (
	"context"
	"math"
	"regexp"
	"strings"

	"posta/internal/jx"
)

// Индекс качества источника (0–100): (лайки + 0,5·«не знаю» + 2) / (лайки + 0,5·«не знаю» + дизлайки + 4). Без оценок — 50; каждая оценка сдвигает его
// (первый лайк — 60, «не знаю» — 55,6, дизлайк — 40), а чем больше оценок, тем меньше сдвиг и тем точнее значение.
// Для досок оценки постов считаются отдельно по каждой теме; жалоба «ошибка подбора» — дизлайк.

const SourceQualityStart = 50.0

var boardThreadKeyRe = regexp.MustCompile(`^((?:4chan|dvach):[^:]+):.*$`)

// SourceQualityKey — ключ источника для индекса: тред имиджборда — доска; свой контент и пустой ключ индекса не имеют.
func SourceQualityKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "own:") {
		return ""
	}
	return boardThreadKeyRe.ReplaceAllString(key, "$1")
}

// sourceQualityValue — «не знаю» (maybes) — лайк с весом 0,5.
func sourceQualityValue(likes, maybes, dislikes int) float64 {
	good := float64(likes) + 0.5*float64(maybes)
	return math.Round(1000*(good+2)/(good+float64(dislikes)+4)) / 10
}

// SourceQualityUpdate — пересчёт индекса источника записи после оценки. Считается заново по истории, поэтому смена оценки
// одного поста (лайк → дизлайк) учитывается без дублей.
func (a *App) SourceQualityUpdate(ctx context.Context, record jx.M) {
	key := SourceQualityKey(ContentSourceKey(record))
	if key == "" {
		return
	}
	var likes, maybes, dislikes int
	err := a.DB.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE collection = 'liked'), count(*) FILTER (WHERE collection = 'undecided'),
			count(*) FILTER (WHERE collection = 'disliked' OR (collection = 'complaints' AND data->>'complaint_reason' = 'match_error'))
		FROM history_items WHERE collection IN ('liked', 'disliked', 'undecided', 'complaints')
		AND regexp_replace(data->>'source_key', '^((4chan|dvach):[^:]+):.*$', '\1') = $1`, key).Scan(&likes, &maybes, &dislikes)
	if err != nil {
		return
	}
	a.DB.Pool.Exec(ctx, `INSERT INTO source_quality (source_key, likes, dislikes, maybes, quality, updated_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source_key) DO UPDATE SET likes = $2, dislikes = $3, maybes = $4, quality = $5, updated_at = $6`, key, likes, dislikes, maybes, sourceQualityValue(likes, maybes, dislikes), now())
}

// SourceQualityOf — {quality, likes, maybes, dislikes} источника по ключу; без оценок — nil (в интерфейсе тогда нейтральные 50).
func (a *App) SourceQualityOf(ctx context.Context, key, topic string) jx.M {
	raw := strings.TrimSpace(key)
	key = SourceQualityKey(raw)
	if key == "" {
		return nil
	}
	if strings.HasPrefix(key, "dvach:") || strings.HasPrefix(key, "4chan:") {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 || !IsValidDvachID(parts[2]) {
			return nil
		}
		return a.imageboardQualityOf(ctx, key, topic, parts[2])
	}
	var q float64
	var likes, maybes, dislikes int
	if a.DB.Pool.QueryRow(ctx, `SELECT quality, likes, maybes, dislikes FROM source_quality WHERE source_key = $1`, key).Scan(&q, &likes, &maybes, &dislikes) != nil {
		return nil
	}
	return jx.M{"quality": q, "likes": likes, "maybes": maybes, "dislikes": dislikes}
}

// boardQualityOf counts only ratings assigned to this exact topic (including its aliases).
// Records without a topic cannot safely be attributed and are omitted.
func (a *App) BoardQualityOf(ctx context.Context, key, topic string) jx.M {
	key = SourceQualityKey(key)
	if !strings.HasPrefix(key, "dvach:") && !strings.HasPrefix(key, "4chan:") {
		return nil
	}
	return a.imageboardQualityOf(ctx, key, topic, "")
}

func (a *App) imageboardQualityOf(ctx context.Context, key, topic, thread string) jx.M {
	if TopicNorm(topic) == "" {
		return nil
	}
	rows, err := a.DB.Pool.Query(ctx, `SELECT collection, COALESCE(data->>'topic', ''), COALESCE(data->>'complaint_reason', ''), COALESCE(NULLIF(data->>'thread_id', ''), split_part(data->>'source_key', ':', 3))
        FROM history_items WHERE collection IN ('liked', 'disliked', 'undecided', 'complaints')
        AND regexp_replace(data->>'source_key', '^((4chan|dvach):[^:]+):.*$', '\1') = $1`, key)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var likes, maybes, dislikes int
	for rows.Next() {
		var collection, recordTopic, reason, recordThread string
		if rows.Scan(&collection, &recordTopic, &reason, &recordThread) != nil || (thread != "" && recordThread != thread) || TopicNorm(recordTopic) == "" || !a.TopicSame(recordTopic, topic) {
			continue
		}
		switch collection {
		case "liked":
			likes++
		case "undecided":
			maybes++
		case "disliked":
			dislikes++
		case "complaints":
			if reason == "match_error" {
				dislikes++
			}
		}
	}
	if rows.Err() != nil || likes+maybes+dislikes == 0 {
		return nil
	}
	return jx.M{"quality": sourceQualityValue(likes, maybes, dislikes), "likes": likes, "maybes": maybes, "dislikes": dislikes}
}
