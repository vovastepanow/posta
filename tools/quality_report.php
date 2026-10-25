<?php
// Отчёт качества подбора по истории оценок (только чтение): php tools/quality_report.php [--topic "мемы"] [--user ID] [--json]
// Без --topic — все темы с не меньше 30 оценками. Симуляция правил — те же функции, что в парсере (lib/quality.php).
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

const QUALITY_REPORT_MIN_TOPIC = 30;

/** Карточки с оценкой: лайк, дизлайк, жалоба «не по теме» (и прочие жалобы). */
function quality_load_cards(?string $user): array {
    $sql = "SELECT user_id, collection, data FROM history_items WHERE collection IN ('liked', 'disliked', 'complaints')";
    $rows = $user ? db_all($sql . ' AND user_id = ?', [$user]) : db_all($sql);
    $outcome = ['liked' => 'like', 'disliked' => 'dislike', 'complaints' => 'complaint'];
    return array_map(fn($r) => ['outcome' => $outcome[$r['collection']], 'card' => db_unjson($r['data'], []) + ['user' => $r['user_id']]], $rows);
}

/** Доли лайк / дизлайк / жалоба среди карточек группы. */
function quality_summary(array $cards): array {
    $n = count($cards);
    $count = array_count_values(array_column($cards, 'outcome'));
    $share = fn($k) => $n ? round(100 * ($count[$k] ?? 0) / $n, 1) : 0.0;
    return ['n' => $n, 'like' => $share('like'), 'dislike' => $share('dislike'), 'complaint' => $share('complaint')];
}

/** Разрез по ключу: [значение => сводка]; сортировка по значению. */
function quality_cut(array $cards, callable $key): array {
    $groups = [];
    foreach ($cards as $c) $groups[(string)$key($c['card'])][] = $c;
    ksort($groups);
    return array_map('quality_summary', $groups);
}

/** Корзины для оценки: точные значения у 60–65 и 75–90 — остальное группами. */
function quality_bucket(array $card): string {
    $r = (int)($card['relevance'] ?? 0);
    return match (true) {
        $r < 50 => '<50', $r <= 59 => '50–59', $r <= 65 => '60–65', $r <= 74 => '66–74', $r === 75 => '75', $r <= 79 => '76–79',
        $r === 80 => '80', $r <= 84 => '81–84', $r === 85 => '85', $r <= 89 => '86–89', default => '90+',
    };
}

/** Правила симуляции — накопительно, как в таблице QUALITY_PROMPT.md. */
function quality_rule_rows(): array {
    $base = ['threshold' => 61, 'hedge' => false, 'format' => false, 'dvach' => false, 'views' => false];
    return [
        'сейчас (оценка ≥ 61)' => $base,
        'оценка ≥ 80' => ['threshold' => 80] + $base,
        '≥ 80 + без оговорок' => ['threshold' => 80, 'hedge' => true] + $base,
        '+ не format=photo в мемах' => ['threshold' => 80, 'hedge' => true, 'format' => true] + $base,
        '+ не Двач' => ['threshold' => 80, 'hedge' => true, 'format' => true, 'dvach' => true] + $base,
        '+ просмотры ≥ 0,7× медианы' => ['threshold' => 80, 'hedge' => true, 'format' => true, 'dvach' => true, 'views' => true] + $base,
        // как в парсере: Двач режется до 1 из QUALITY_PLATFORM_SAMPLE (остальные — в полный анализ), это ближе к реальности
        'как в парсере (Двач 1 из 10)' => ['threshold' => 80, 'hedge' => true, 'format' => true, 'dvach_sample' => QUALITY_PLATFORM_SAMPLE] + $base,
    ];
}

/** Сколько карточек и лайков остаётся при правилах $opts. */
function quality_simulate(array $cards, array $opts): array {
    $dvach_seen = 0;
    $kept = array_values(array_filter($cards, function ($c) use ($opts, &$dvach_seen) {
        if (isset($opts['dvach_sample']) && str_starts_with(content_source_key($c['card']), 'dvach:') && ++$dvach_seen % $opts['dvach_sample'] !== 1) return false;   // пропущенные — не Двач-сэмпл
        return quality_reasons($c['card'], $c['card'], (string)($c['card']['topic'] ?? ''), array_diff_key($opts, ['dvach_sample' => 1])) === null;
    }));
    $likes_total = count(array_filter($cards, fn($c) => $c['outcome'] === 'like'));
    $likes_kept = count(array_filter($kept, fn($c) => $c['outcome'] === 'like'));
    $summary = quality_summary($kept);
    return $summary + ['share_kept' => round(100 * count($kept) / max(1, count($cards)), 1), 'likes_saved' => round(100 * $likes_kept / max(1, $likes_total), 1)];
}

/** Часть D: верхняя и нижняя треть карточек по баллу card_score — лайкаются ли верхние заметно чаще.
 * $honest — доли источников считаются только по оценкам, сделанным раньше карточки (без подглядывания). */
function quality_score_tertiles(array $cards, bool $honest = false): array {
    $history = ['liked' => [], 'disliked' => [], 'complaints' => []];
    $collection = ['like' => 'liked', 'dislike' => 'disliked', 'complaint' => 'complaints'];
    foreach ($cards as $c) $history[$collection[$c['outcome']]][] = $c['card'];
    $shares = [];
    $scored = [];
    foreach ($cards as $c) {
        $topic = (string)($c['card']['topic'] ?? '');
        if ($honest) {
            $before = ['liked' => [], 'disliked' => [], 'complaints' => []];
            foreach ($history as $name => $list) foreach ($list as $r) if ((int)($r['timestamp'] ?? 0) < (int)($c['card']['timestamp'] ?? 0)) $before[$name][] = $r;
            $ctx = quality_context($before, $topic);
        } else {
            $ctx = $shares[$topic] ??= quality_context($history, $topic);
        }
        $scored[] = ['score' => card_score($c['card'], $topic, $ctx), 'card' => $c];
    }
    usort($scored, fn($a, $b) => $b['score'] <=> $a['score']);
    $third = intdiv(count($scored), 3);
    $part = fn($rows) => quality_summary(array_map(fn($r) => $r['card'], $rows));
    return ['верхняя треть' => $part(array_slice($scored, 0, $third)), 'нижняя треть' => $part(array_slice($scored, -$third))];
}

/** Оговорка в why_relevant (есть ли): доли у такой карточки. */
function quality_hedged(array $card): string {
    return preg_match(ANALYSIS_HEDGE_RE, (string)($card['why_relevant'] ?? '')) ? 'да' : 'нет';
}

function quality_report_for(array $cards): array {
    $cut = fn($fn) => quality_cut($cards, $fn);
    return [
        'итог' => quality_summary($cards),
        'оценка relevance (точно)' => $cut(fn($c) => (int)($c['relevance'] ?? 0)),
        'оценка relevance (корзины)' => $cut(fn($c) => quality_bucket($c)),
        'format' => $cut(fn($c) => (string)($c['format'] ?? '') ?: '—'),
        'подпись' => $cut(fn($c) => trim((string)($c['text'] ?? '')) !== '' && ($c['text'] ?? '') !== EMPTY_POST_TEXT ? 'есть' : 'нет'),
        'видео / картинка' => $cut(fn($c) => trim((string)($c['video'] ?? '')) !== '' ? 'видео' : 'картинка'),
        'площадка' => $cut(fn($c) => explode(':', content_source_key($c))[0]),
        'первая метка' => $cut(fn($c) => (string)(($c['tags'] ?? [])[0] ?? '—')),
        'оговорка в why_relevant' => $cut(fn($c) => quality_hedged($c)),
        'просмотры / медиана канала' => $cut(function ($c) {
            $r = quality_views_ratio($c);
            return $r === null ? 'нет данных' : ($r >= 1.3 ? '≥1,3×' : ($r >= QUALITY_VIEWS_MIN_RATIO ? '0,7–1,3×' : '<0,7×'));
        }),
        'балл карточки (части D+E, с подглядыванием)' => quality_score_tertiles($cards),
        'балл карточки (части D+E, честно: только прошлые оценки)' => quality_score_tertiles($cards, true),
        'правила (симуляция)' => array_map(fn($opts) => quality_simulate($cards, $opts), quality_rule_rows()),
    ];
}

function quality_print(string $title, array $report): void {
    echo "\n== {$title}\n";
    foreach ($report as $section => $body) {
        echo "-- {$section}\n";
        if ($section === 'правила (симуляция)') {
            foreach ($body as $name => $r) printf("  %-30s осталось %5.1f%%  лайк %5.1f  дизлайк %5.1f  жалоба %5.1f  сохранено лайков %5.1f%%\n", $name, $r['share_kept'], $r['like'], $r['dislike'], $r['complaint'], $r['likes_saved']);
            continue;
        }
        if (isset($body['n'])) { printf("  n=%d  лайк %.1f%%  дизлайк %.1f%%  жалоба %.1f%%\n", $body['n'], $body['like'], $body['dislike'], $body['complaint']); continue; }
        foreach ($body as $value => $r) printf("  %-22s n=%-4d лайк %5.1f%%  дизлайк %5.1f%%  жалоба %5.1f%%\n", $value, $r['n'], $r['like'], $r['dislike'], $r['complaint']);
    }
}

$opt = getopt('', ['topic:', 'user:', 'json', 'humor']);
$all = quality_load_cards($opt['user'] ?? null);

// Группы: по заданной теме или все темы с не меньше QUALITY_REPORT_MIN_TOPIC оценками
$groups = [];
if (isset($opt['humor'])) {   // все темы ветки юмора одной группой (как в QUALITY_PROMPT.md: 610 карточек мемов)
    $groups['ветки «Мемы» и «Юмор» (все темы юмора)'] = array_values(array_filter($all, fn($c) => topic_is_humor((string)($c['card']['topic'] ?? ''))));
} elseif (isset($opt['topic'])) {
    $groups[$opt['topic']] = array_values(array_filter($all, fn($c) => topic_same((string)($c['card']['topic'] ?? ''), $opt['topic'])));
} else {
    foreach ($all as $c) $groups[topic_norm((string)($c['card']['topic'] ?? ''))][] = $c;
    $groups = array_filter($groups, fn($g) => count($g) >= QUALITY_REPORT_MIN_TOPIC);
    uasort($groups, fn($a, $b) => count($b) <=> count($a));
}

$out = [];
foreach ($groups as $title => $cards) $out[$title] = quality_report_for($cards);
if (isset($opt['json'])) { echo json_encode($out, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT), "\n"; exit(0); }
foreach ($out as $title => $report) quality_print($title, $report);
