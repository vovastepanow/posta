<?php
// Прогон новой оценки на истории (QUALITY_PROMPT.md, часть C): php tools/quality_eval.php --user ID [--seed N] [--dry-run]
// 60 карточек мемов: 20 лайков, 20 дизлайков, 20 жалоб, только картинки с файлом в кэше. Ничего не пишет в кэш анализа и пул.
// Лимит — 60 вызовов умной модели за запуск; --dry-run выбирает выборку и вызовов не делает.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

const QUALITY_EVAL_PER_OUTCOME = 20;
const QUALITY_EVAL_MAX_CALLS = 60;

$opt = getopt('', ['user:', 'seed:', 'dry-run', 'only:']);   // --only=complaint — только жалобы
$uid = (string)($opt['user'] ?? '');
if ($uid === '') exit("Укажите --user ID (пользователь с оценками мемов).\n");
$seed = (int)($opt['seed'] ?? time());
mt_srand($seed);

$history = history_load($uid);
$collections = ['liked' => 'like', 'disliked' => 'dislike', 'complaints' => 'complaint'];
$eligible = [];
foreach ($collections as $collection => $outcome) {
    foreach ($history[$collection] ?? [] as $record) {
        if (!topic_is_humor((string)($record['topic'] ?? '')) || trim((string)($record['video'] ?? '')) !== '') continue;
        $path = media_cache_path((string)($record['image_local'] ?? ''));
        if ($path === null || !is_file($path)) continue;
        $eligible[$outcome][] = $record;
    }
}

$sample = [];
$outcomes = isset($opt['only']) ? [(string)$opt['only']] : ['like', 'dislike', 'complaint'];
foreach ($outcomes as $outcome) {
    $pool = $eligible[$outcome] ?? [];
    shuffle($pool);
    foreach (array_slice($pool, 0, QUALITY_EVAL_PER_OUTCOME) as $record) $sample[] = [$outcome, $record];
    printf("доступно %s: %d, в выборке: %d\n", $outcome, count($pool), min(QUALITY_EVAL_PER_OUTCOME, count($pool)));
}
printf("seed=%d, вызовов модели: %d (лимит %d)\n", $seed, count($sample), QUALITY_EVAL_MAX_CALLS);
if (count($sample) > QUALITY_EVAL_MAX_CALLS) exit("Больше лимита вызовов.\n");
if (isset($opt['dry-run'])) exit(0);

$results = [];
foreach ($sample as $i => [$outcome, $record]) {
    $path = media_cache_path((string)$record['image_local']);
    $caption = post_caption_for_ai($record);
    $topic = (string)$record['topic'];
    try {
        $a = analyze_content_with_vision([file_get_contents($path)], $caption, $history, $topic, null, PARSER_THINKING, (string)($record['source'] ?? ''));
        $reason = pool_admission($a, $record, $topic);
    } catch (Throwable $e) {
        $a = ['relevance' => null, 'evidence' => '', 'joke' => '', 'why_relevant' => ''];
        $reason = 'ошибка модели: ' . $e->getMessage();
    }
    $results[] = ['outcome' => $outcome, 'old' => (int)($record['relevance'] ?? 0), 'new' => $a['relevance'], 'evidence' => $a['evidence'] ?? '', 'joke' => $a['joke'] ?? '',
        'why' => $a['why_relevant'] ?? '', 'reason' => $reason, 'record' => mb_substr((string)($record['description'] ?? ''), 0, 120)];
    printf("[%02d/%d] %-8s было %s → стало %s; %s\n", $i + 1, count($sample), $outcome, $record['relevance'] ?? '—', $a['relevance'] ?? '—', $reason ?? 'прошла');
    @flush();
}

// Итог по выборке: сколько жалоб отсеяно, сколько лайков сохранено
foreach ($outcomes as $outcome) {
    $rows = array_filter($results, fn($r) => $r['outcome'] === $outcome);
    $kept = count(array_filter($rows, fn($r) => $r['reason'] === null));
    printf("%-9s n=%d, прошли %d (%.0f%%), отсеяны %d (%.0f%%)\n", $outcome, count($rows), $kept, 100 * $kept / max(1, count($rows)), count($rows) - $kept, 100 * (count($rows) - $kept) / max(1, count($rows)));
}
echo json_encode(['seed' => $seed, 'results' => $results], JSON_UNESCAPED_UNICODE), "\n";
