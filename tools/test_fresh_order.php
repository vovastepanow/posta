<?php
// Порядок «Подбора» по свежести без пачек с одного источника (FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть A).
// Запуск: php tools/test_fresh_order.php. Без базы и без сети: только чистые функции.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$now = time();
$post = fn($src, $ago, $topic = 'тест') => ['source_key' => "source:$src", 'posted_at' => $now - $ago, 'topic' => $topic, 'text' => "$src $ago"];
$keys = fn($pool) => array_map(fn($i) => $i['text'], $pool);

echo "A. Порядок показа:\n";
$pool = [$post('a', 100), $post('a', 200), $post('a', 300), $post('a', 400), $post('b', 150), $post('b', 250), $post('b', 350), $post('b', 450), $post('c', 120), $post('c', 220), $post('c', 320), $post('c', 420)];
$ordered = pool_order($pool);
$sources = array_map(fn($i) => $i['source_key'], $ordered);
$check('все записи на месте', count($ordered) === count($pool));
$check('нет двух постов одного источника подряд, пока в окне есть другой', !array_filter(range(1, count($sources) - 1), fn($k) => $sources[$k] === $sources[$k - 1] && count(array_unique(array_slice($sources, $k, POOL_DIVERSITY_WINDOW))) > 1));
$check('первый — самый свежий пост пула', $ordered[0]['text'] === 'a 100');
$times = array_map(fn($i) => $i['posted_at'], $ordered);
$check('по свежести в целом: время не растёт (с учётом чередования)', $times[0] >= $times[count($times) - 1]);

echo "B. Один источник и пустой пул:\n";
$one = [$post('a', 300), $post('a', 100), $post('a', 200)];
$check('один источник — просто по свежести', $keys(pool_order($one)) === ['a 100', 'a 200', 'a 300']);
$check('пустой пул — пустой порядок', pool_order([]) === []);

echo "C. Темы не смешиваются:\n";
$mixed = [$post('a', 100, 'тема 1'), $post('b', 50, 'тема 2'), $post('a', 200, 'тема 1'), $post('b', 60, 'тема 2')];
$topics = array_map(fn($i) => $i['topic'], pool_order($mixed));
$check('сначала все записи первой темы, потом второй', $topics === ['тема 1', 'тема 1', 'тема 2', 'тема 2']);

echo "D. Дата:\n";
$nodate = [['source_key' => 'source:a', 'pooled_at' => $now - 10, 'topic' => 'тест', 'text' => 'pooled'], $post('b', 500)];
$check('без posted_at берётся pooled_at', pool_order($nodate)[0]['text'] === 'pooled');

echo "E. Анализ по кругу:\n";
$cands = [];
foreach (['a', 'b', 'c'] as $src) for ($k = 0; $k < 4; $k++) $cands[] = ['image' => "https://cdn.test/$src$k.jpg", 'text' => "$src$k", 'source' => "test $src", 'source_key' => "source:$src", 'posted_at' => $now - 100 - $k * 60, 'tags' => []];
$skipped = [];
$queue = build_candidate_queue(['test' => $cands], [], [], [], $skipped, [], null, []);
$first3 = array_map(fn($i) => $i['source_key'], array_slice($queue, 0, 3));
$check('первые кандидаты — из трёх разных источников', count(array_unique($first3)) === 3);
$check('внутри источника порядок сохранён (свежие первыми)', array_values(array_filter(array_column($queue, 'text'), fn($t) => $t[0] === 'a')) === ['a0', 'a1', 'a2', 'a3']);
$check('все кандидаты на месте', count($queue) === 12);

echo "F. Интервалы опроса:\n";
$check('100 постов в неделю — около 50 минут', abs(source_poll_interval(['per_week' => 100]) - 3024) < 60);
$check('10 постов в неделю — 6 часов (потолок)', source_poll_interval(['per_week' => 10]) === SOURCE_POLL_MAX);
$check('500 постов в неделю — 20 минут (пол)', source_poll_interval(['per_week' => 500]) === SOURCE_POLL_MIN);
$check('per_week = 0 — максимум', source_poll_interval(['per_week' => 0]) === SOURCE_POLL_MAX);
$check('нет активности — максимум', source_poll_interval(null) === SOURCE_POLL_MAX);
$check('нет записи активности — просрочен', source_poll_overdue(['id' => 'x'], $now));
$check('проверен минуту назад — не просрочен', !source_poll_overdue(['activity' => ['per_week' => 500, 'checked_at' => $now - 60]], $now));
$check('per_week 500 — просрочен через 20 минут', source_poll_overdue(['activity' => ['per_week' => 500, 'checked_at' => $now - 1200]], $now));
$check('per_week 10 — через 20 минут ещё не просрочен', !source_poll_overdue(['activity' => ['per_week' => 10, 'checked_at' => $now - 1200]], $now));
$entries = [];
$cached = [];
for ($k = 0; $k < 15; $k++) { $entries["s$k"] = ['activity' => ['per_week' => 50, 'checked_at' => $now - 3600 * ($k + 1)]]; $cached["s$k"] = true; }
$picked = source_poll_fetch_set($entries, $cached, $now);
$check('в сеть не больше SOURCE_FETCHES_PER_PASS источников', count($picked) === SOURCE_FETCHES_PER_PASS);
$check('первыми идут самые просроченные', $picked[0] === 's14' && $picked[count($picked) - 1] === 's5');
$check('источник без кэша идёт в сеть, даже если не просрочен', in_array('new', source_poll_fetch_set(['new' => ['activity' => ['per_week' => 10, 'checked_at' => $now]]], [], $now), true));
$fresh = ['f' => ['activity' => ['per_week' => 10, 'checked_at' => $now - 60]]];
$check('свежий источник с кэшем — не в сеть', source_poll_fetch_set($fresh, ['f' => true], $now) === []);
$check('second pass сразу после первого — никто не просрочен', source_poll_fetch_set($entries, array_fill_keys(array_keys($entries), true), $now) !== [] && source_poll_fetch_set(array_map(fn($e) => ['activity' => ['per_week' => 50, 'checked_at' => $now]], $entries), $cached, $now) === []);

echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
