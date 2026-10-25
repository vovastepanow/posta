<?php
// Качество подбора: жёсткие правила отбора (после ответа модели) и разбор истории оценок. Правила — одни и те же функции
// для парсера и для отчёта tools/quality_report.php; отчёт передаёт свои наборы правил через $opts.

/** Тема из веток юмора «Мемы» и «Юмор»: сами ветки, их частные (memes.*, humor.*) и синонимы. */
function topic_is_humor($topic) {
    for ($e = topic_lookup((string)$topic); $e; $e = topic_parent($e)) {
        $key = (string)($e['key'] ?? '');
        if (in_array($key, ['humor', 'memes'], true) || str_starts_with($key, 'humor.') || str_starts_with($key, 'memes.')) return true;
    }
    return false;
}

/** Просмотры поста / медиана просмотров соседних постов канала (Telegram); null — данных нет. */
function quality_views_ratio(array $item): ?float {
    $e = $item['engagement'] ?? null;
    if (!is_array($e) || (int)($e['neighbors_median_views'] ?? 0) <= 0) return null;
    return (int)($e['views'] ?? 0) / (int)$e['neighbors_median_views'];
}

/**
 * Жёсткие правила отбора: причина отказа или null. $opts переопределяет, какие правила включены:
 * threshold (порог «по теме»), hedge (оговорки), format (обычное фото в мемах), dvach (площадка), views (просмотры).
 */
function quality_reasons(array $a, array $item, string $topic, array $opts = []) {
    $opts += ['threshold' => RELEVANCE_THRESHOLD, 'hedge' => true, 'format' => true, 'dvach' => false, 'views' => false];
    $relevance = (int)($a['relevance'] ?? 0);
    if ($relevance < $opts['threshold']) return "порог {$opts['threshold']}: оценка {$relevance}";
    $why = (string)($a['why_relevant'] ?? '');
    if ($opts['hedge'] && preg_match(ANALYSIS_HEDGE_RE, $why)) return 'модель не уверена: „' . mb_substr($why, 0, 60) . '…“';
    if ($opts['format'] && topic_is_humor($topic) && ($a['format'] ?? '') === 'photo') return 'обычное фото, а тема — мемы';
    if ($opts['dvach'] && str_starts_with(content_source_key($item), 'dvach:')) return 'площадка Двач по этой теме отсеяна';
    $ratio = quality_views_ratio($item);
    if ($opts['views'] && $ratio !== null && $ratio < QUALITY_VIEWS_MIN_RATIO) return 'просмотры ниже ' . QUALITY_VIEWS_MIN_RATIO . '× медианы канала';
    return null;
}

/**
 * Частная тема подбора, а среди меток записи её нет — оценка ниже на TOPIC_TAG_MISS_PENALTY (часть D).
 * Общие темы и записи с меткой темы не трогаем.
 */
function quality_tag_penalty(array $a, string $topic): array {
    if (array_key_exists('subtopic', $a) && $a['subtopic'] !== null) return $a;   // выбор среди соседей уже решил (SUBTOPIC_PROMPT.md, часть C)
    $entry = topic_lookup($topic);
    if (!$entry || $entry['parent'] === null || ($entry['key'] ?? '') === '') return $a;
    $tags = array_map('strval', (array)($a['tags'] ?? []));
    $key = (string)$entry['key'];
    $has = in_array($key, $tags, true) || array_filter($tags, fn($t) => str_starts_with($t, $key . '.'));
    if (!$has) $a['relevance'] = max(0, (int)($a['relevance'] ?? 0) - TOPIC_TAG_MISS_PENALTY);
    return $a;
}

/** Причина отказа при добавлении в пул (правила части B); null — запись подходит. */
function pool_admission(array $a, array $item, string $topic): ?string {
    return quality_reasons($a, $item, $topic);
}

/**
 * Площадка у пользователя режется по теме: не меньше QUALITY_PLATFORM_MIN_RATINGS оценок и жалоб не меньше
 * QUALITY_PLATFORM_COMPLAINT_SHARE. $history — history_load(); $prefix — 'dvach', 'telegram', …
 */
function quality_platform_blocked(array $history, string $prefix, string $topic): bool {
    $total = 0;
    $complaints = 0;
    foreach (['liked', 'disliked', 'complaints'] as $collection) {
        foreach ((array)($history[$collection] ?? []) as $record) {
            if (!is_array($record) || !topic_same((string)($record['topic'] ?? ''), $topic) || !str_starts_with(content_source_key($record), $prefix . ':')) continue;
            $total++;
            if ($collection === 'complaints') $complaints++;
        }
    }
    return $total >= QUALITY_PLATFORM_MIN_RATINGS && $complaints / $total >= QUALITY_PLATFORM_COMPLAINT_SHARE;
}

/** Доля лайков и число оценок по ключу (источник, первая метка) среди карточек темы: key => ['share', 'n']. */
function quality_shares(array $history, string $topic, callable $key): array {
    $counts = [];
    foreach (['liked' => 'like', 'disliked' => 'dislike', 'complaints' => 'complaint'] as $collection => $outcome) {
        foreach ((array)($history[$collection] ?? []) as $record) {
            if (!is_array($record) || !topic_same((string)($record['topic'] ?? ''), $topic)) continue;
            $k = $key($record);
            if ($k === null) continue;
            $counts[$k]['n'] = ($counts[$k]['n'] ?? 0) + 1;
            $counts[$k]['likes'] = ($counts[$k]['likes'] ?? 0) + ($outcome === 'like' ? 1 : 0);
        }
    }
    return array_map(fn($c) => ['share' => $c['likes'] / $c['n'], 'n' => $c['n']], $counts);
}

/** Контекст балла для темы: доли источников, вкус по первой метке, доля лайков темы. */
function quality_context(array $history, string $topic): array {
    $shares = quality_shares($history, $topic, fn($r) => content_source_key($r));
    $tags = quality_shares($history, $topic, fn($r) => (string)(($r['tags'] ?? [])[0] ?? '') ?: null);
    $total = array_sum(array_column($shares, 'n'));
    $likes = array_sum(array_map(fn($s) => $s['share'] * $s['n'], $shares));
    return ['sources' => $shares, 'tags' => $tags, 'like_share' => $total ? $likes / $total : QUALITY_SOURCE_NEUTRAL_SHARE];
}

/**
 * Балл карточки для порядка пула (части D и E): релевантность + популярность + доля лайков источника + вкус по первой метке.
 * $ctx — quality_context(); без него только релевантность и популярность.
 */
function card_score(array $item, string $topic, array $ctx = []): float {
    $score = (float)($item['relevance'] ?? 0);
    $ratio = quality_views_ratio($item);
    if ($ratio !== null) $score += match (true) { $ratio >= 2 => 10, $ratio >= 1.3 => 5, $ratio < QUALITY_VIEWS_MIN_RATIO => -10, default => 0 };
    $source = $ctx['sources'][content_source_key($item)] ?? null;
    if ($source && $source['n'] >= QUALITY_CARD_MIN_RATINGS) {
        $score += max(-QUALITY_SOURCE_CAP, min(QUALITY_SOURCE_CAP, ($source['share'] - QUALITY_SOURCE_NEUTRAL_SHARE) * QUALITY_SOURCE_WEIGHT));
    }
    $tag = $ctx['tags'][(string)(($item['tags'] ?? [])[0] ?? '')] ?? null;
    if ($tag && $tag['n'] >= QUALITY_TAG_MIN_RATINGS) {
        $score += max(-QUALITY_TAG_CAP, min(QUALITY_TAG_CAP, ($tag['share'] - ($ctx['like_share'] ?? QUALITY_SOURCE_NEUTRAL_SHARE)) * QUALITY_TAG_WEIGHT));
    }
    return $score;
}
