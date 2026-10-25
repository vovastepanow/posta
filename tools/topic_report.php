<?php
// Отчёт по темам (только чтение): php tools/topic_report.php [--json]
// По каждой теме: источники каталога, предложения и голоса, проверка постов (screened_cache), карточки и лучшие/худшие источники.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

const TOPIC_REPORT_SCREEN_OK = 60;     // балл быстрой проверки, с которого пост «проходит» (как в TOPICS_SEARCH_PROMPT.md)
const TOPIC_REPORT_MIN_RATINGS = 5;    // оценок источника по теме, с которых он попадает в топ

/** Все темы, которые встречаются в данных: история, каталог, предложения, кэш проверки. */
function topic_report_names(array $db, array $cache, array $history_cards): array {
    $names = [];
    foreach ($history_cards as $c) $names[topic_norm((string)($c['card']['topic'] ?? ''))] = (string)($c['card']['topic'] ?? '');
    foreach ($cache as $e) if (trim((string)($e['topic'] ?? '')) !== '') $names[topic_norm($e['topic'])] = $e['topic'];
    foreach ($db['source_suggestions'] as $s) if (trim((string)($s['topic'] ?? '')) !== '') $names[topic_norm($s['topic'])] = $s['topic'];
    foreach (CATALOG_KEYS as $key) foreach ($db[$key] as $e) foreach ((array)($e['topics'] ?? []) as $t) $names[topic_norm((string)$t)] = (string)$t;
    unset($names['']);
    return $names;
}

/** Строка по теме. */
function topic_report_row(string $topic, array $db, array $cache, array $cards): array {
    $sources = [];
    foreach (CATALOG_KEYS as $platform => $key) {
        $count = count(array_filter($db[$key], fn($e) => catalog_entry_matches_topic($e, $topic, true)));
        if ($count) $sources[$platform] = $count;
    }
    $suggestions = array_filter($db['source_suggestions'], fn($s) => topic_same((string)($s['topic'] ?? ''), $topic));
    $votes = array_filter($db['source_votes'], fn($v) => isset($v['topic']) && topic_same((string)$v['topic'], $topic));
    $checked = screened_scores_for_topic($cache, $topic);
    $passed = count(array_filter($checked, fn($s) => (int)$s >= TOPIC_REPORT_SCREEN_OK));
    $count_by = fn($c) => count(array_filter($cards, fn($x) => $x['outcome'] === $c));
    return [
        'источники каталога' => $sources ?: ['всего' => 0],
        'предложено' => count($suggestions),
        'одобрено' => count(array_filter($votes, fn($v) => ($v['vote'] ?? '') === 'approve')),
        'отклонено' => count(array_filter($votes, fn($v) => ($v['vote'] ?? '') === 'reject')),
        'проверено постов' => count($checked),
        'прошло проверку' => $checked ? round(100 * $passed / count($checked), 1) : null,
        'карточек' => count($cards),
        'лайков' => $count_by('like'), 'дизлайков' => $count_by('dislike'), 'жалоб' => $count_by('complaint'),
        'доля лайков' => $cards ? round(100 * $count_by('like') / count($cards), 1) : null,
        'источники по лайкам' => topic_report_sources($cards),
    ];
}

/** Топ-5 лучших и худших источников по доле лайков (от TOPIC_REPORT_MIN_RATINGS оценок). */
function topic_report_sources(array $cards): array {
    $by = [];
    foreach ($cards as $c) {
        $key = content_source_key($c['card']);
        $by[$key]['n'] = ($by[$key]['n'] ?? 0) + 1;
        $by[$key]['likes'] = ($by[$key]['likes'] ?? 0) + ($c['outcome'] === 'like' ? 1 : 0);
    }
    $rated = array_filter($by, fn($s) => $s['n'] >= TOPIC_REPORT_MIN_RATINGS);
    uasort($rated, fn($a, $b) => $b['likes'] / $b['n'] <=> $a['likes'] / $a['n']);
    $fmt = fn($k, $s) => ['источник' => $k, 'оценок' => $s['n'], 'доля лайков' => round(100 * $s['likes'] / $s['n'], 1)];
    return ['лучшие' => array_map(fn($k, $s) => $fmt($k, $s), array_keys(array_slice($rated, 0, 5, true)), array_slice($rated, 0, 5, true)),
        'худшие' => array_map(fn($k, $s) => $fmt($k, $s), array_keys(array_slice(array_reverse($rated, true), 0, 5, true)), array_slice(array_reverse($rated, true), 0, 5, true))];
}

/** Все оценённые карточки всех пользователей: лайк, дизлайк, жалоба. */
function history_load_all_cards(): array {
    $outcome = ['liked' => 'like', 'disliked' => 'dislike', 'complaints' => 'complaint'];
    return array_map(fn($r) => ['outcome' => $outcome[$r['collection']], 'card' => db_unjson($r['data'], [])],
        db_all("SELECT collection, data FROM history_items WHERE collection IN ('liked', 'disliked', 'complaints')"));
}

$opt = getopt('', ['json']);
$db = load_source_db();
$cache = screened_cache_load();
$history = history_load_all_cards();
$rows = [];
foreach (topic_report_names($db, $cache, $history) as $topic) {
    $cards = array_values(array_filter($history, fn($c) => topic_same((string)($c['card']['topic'] ?? ''), $topic)));
    $row = topic_report_row($topic, $db, $cache, $cards);
    if (!$row['источники каталога'] && !$row['предложено'] && !$row['проверено постов'] && !$cards) continue;
    $rows[$topic] = $row;
}
uksort($rows, fn($a, $b) => $rows[$b]['карточек'] <=> $rows[$a]['карточек']);

if (isset($opt['json'])) { echo json_encode($rows, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT), "\n"; exit(0); }
foreach ($rows as $topic => $r) {
    echo "\n== {$topic}\n";
    foreach ($r as $k => $v) {
        if ($k === 'источники по лайкам') {
            foreach (['лучшие', 'худшие'] as $part) foreach ($v[$part] as $s) printf("   %-7s %-28s оценок %3d, лайков %5.1f%%\n", $part, $s['источник'], $s['оценок'], $s['доля лайков']);
            continue;
        }
        echo '  ', $k, ': ', is_array($v) ? json_encode($v, JSON_UNESCAPED_UNICODE) : ($v === null ? '—' : $v), "\n";
    }
}
