<?php
// Бережный опрос источников: у каждого свой интервал по активности (FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть B).

/**
 * Интервал опроса источника в секундах: средний промежуток между постами / 2, в пределах SOURCE_POLL_MIN..SOURCE_POLL_MAX.
 * Промежуток = 7 суток / постов в неделю. Нет активности или постов нет — MAX (такой источник опрашиваем редко).
 */
function source_poll_interval(?array $activity): int {
    $per_week = (float)($activity['per_week'] ?? 0);
    if ($per_week <= 0) return SOURCE_POLL_MAX;
    return (int)max(SOURCE_POLL_MIN, min(SOURCE_POLL_MAX, 7 * DAY_SECONDS / $per_week / 2));
}

/** Просрочен ли опрос: нет записи активности или прошёл интервал с последней проверки. */
function source_poll_overdue(?array $entry, int $now): bool {
    if (!is_array($entry) || !is_array($entry['activity'] ?? null)) return true;
    return $now - (int)($entry['activity']['checked_at'] ?? 0) >= source_poll_interval($entry['activity']);
}

/** Насколько просрочен: во сколько раз прошло больше интервала (1.0 — ровно срок). Без активности — очень много, чтобы ехал первым. */
function source_poll_lateness(?array $entry, int $now): float {
    if (!is_array($entry) || !is_array($entry['activity'] ?? null)) return INF;
    return ($now - (int)($entry['activity']['checked_at'] ?? 0)) / source_poll_interval($entry['activity']);
}

/** Ключ кэша постов источника в kv: по площадке и адресу без регистра. */
function source_poll_cache_key($platform, $id): string {
    return 'source_posts:' . sha1($platform . '|' . strtolower((string)$id));
}

/**
 * Кому идти в сеть на этом проходе: у кого нет кэша (новая тема, первый раз) или просрочен опрос. Самые просроченные первыми,
 * не больше SOURCE_FETCHES_PER_PASS. $entries — [ключ => запись], $cached — [ключ => true] для тех, у кого есть кэш.
 */
function source_poll_fetch_set(array $entries, array $cached, int $now): array {
    $need = [];
    foreach ($entries as $key => $entry) {
        if (!isset($cached[$key])) $need[$key] = INF;
        elseif (source_poll_overdue($entry, $now)) $need[$key] = source_poll_lateness($entry, $now);
    }
    arsort($need);
    return array_slice(array_keys($need), 0, SOURCE_FETCHES_PER_PASS);
}
