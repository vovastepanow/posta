<?php
// Горячие посты первыми и автоподбор с минимумом источников (HOT_FIRST_PROMPT.md). Без сети и модели, подставные посты.
// Запуск — на копии базы: см. HOT_FIRST_PROMPT.md, «Ограничения».
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
$fails = 0;
$ok = function ($cond, $name) use (&$fails) { echo ($cond ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$cond) $fails++; };

// Пользователь нужен для текущей темы и уточнения; тема — по умолчанию
app_user_bind(bin2hex(random_bytes(16)));

$now = time();
// Подставной пост: источник, площадка (по подписи источника), вовлечённость, часы жизни
$post = function ($source_key, $source, $posted_hours, $engagement, $text = 'пост про тему, достаточно длинная подпись для проверки') use ($now) {
    return ['source_key' => $source_key, 'source' => $source, 'posted_at' => $now - (int)($posted_hours * 3600), 'image' => "img-$source_key-" . mt_rand(), 'text' => $text,
        'engagement' => $engagement];
};
$tg = fn($views, $reactions = []) => ['source' => 'telegram', 'views' => $views, 'reactions' => $reactions];

// 1. heat_rel: пост в 3 раза горячее медианы своего канала → ≈ 3
$chan = [];
foreach ([100, 100, 100, 300] as $v) $chan[] = $post('tg:one', 'Telegram | one', 2, $tg($v));
$rel = hot_relative_heat($chan);
$ok(abs($rel[3] - 3.0) < 0.01, 'heat_rel: пост в 3 раза горячее медианы канала ≈ 3 (' . round($rel[3], 2) . ')');

// 2. Канал на 1K подписчиков с горячим постом обгоняет канал на 100K с обычным (подписчики не входят в горячесть — только относительная)
$small = [$post('tg:small', 'Telegram | small', 2, $tg(100)), $post('tg:small', 'Telegram | small', 2, $tg(100)), $post('tg:small', 'Telegram | small', 2, $tg(100)),
    $post('tg:small', 'Telegram | small', 2, $tg(400))];
$big = [$post('tg:big', 'Telegram | big', 2, $tg(50000)), $post('tg:big', 'Telegram | big', 2, $tg(50000)), $post('tg:big', 'Telegram | big', 2, $tg(50000)),
    $post('tg:big', 'Telegram | big', 2, $tg(50000))];
$short = hot_shortlist(array_merge($big, $small), []);
$ok(($short[0]['source_key'] ?? '') === 'tg:small' && ($short[0]['engagement']['views'] ?? 0) === 400, 'малый канал с горячим постом идёт первым, большой с обычным — после него');

// 3. Двач в шортлисте ≤ 40% при 165 постах Двача из 203
$all = [];
for ($t = 0; $t < 55; $t++) for ($k = 0; $k < 3; $k++) $all[] = $post("dvach:b:$t", 'Двач /b/ · тред', 5, ['source' => 'dvach', 'thread_posts' => 50 + $t]);   // 165 постов Двача
for ($c = 0; $c < 38; $c++) $all[] = $post("tg:c$c", "Telegram | c$c", 5, $tg(200 + $c));   // 38 постов Telegram
$out = hot_shortlist($all, [], 'Мемы');   // родная тема имиджбордов: посты Двача без признаков темы не уходят в хвост (CONTENT_FLOW_PROMPT.md, B2)
$sh = array_slice($out, 0, $GLOBALS['hot_stats']['short']);   // шортлист — горячая часть списка; хвост идёт в конец очереди
$dvach = count(array_filter($sh, fn($i) => str_starts_with((string)$i['source'], 'Двач')));
// Ограничение — в местах шортлиста: 40% от HOT_SHORTLIST. При двух площадках этого мало для полного шортлиста: доля от фактической длины не выполнима
$ok(count($all) === 203 && $dvach <= HOT_PLATFORM_SHARE * HOT_SHORTLIST, "Двач в шортлисте не больше 40% от " . HOT_SHORTLIST . " мест ({$dvach} из " . count($sh) . ' в шортлисте)');

// 4. Не больше HOT_PER_SOURCE с одного источника
$per_source = array_count_values(array_map(fn($i) => $i['source_key'], $sh));
$ok(max($per_source) <= HOT_PER_SOURCE, 'не больше ' . HOT_PER_SOURCE . ' с одного источника (максимум ' . max($per_source) . ')');

// 5. topic_usable_sources: высокие предложения считаются, доски Двача — нет
$topic = 'Города';
$db = ['telegram_channels' => [['id' => 'gorod', 'topics' => [$topic]]], 'vk_publics' => [], 'rss_feeds' => [], 'ok_groups' => [], 'tiktok_accounts' => [],
    'dvach_topic_boards' => [mb_strtolower($topic) => ['b', 'soc']], 'source_suggestions' => [
        ['platform' => PLATFORM_TELEGRAM, 'id' => 'high', 'topic' => $topic, 'score' => 70],
        ['platform' => PLATFORM_TELEGRAM, 'id' => 'low', 'topic' => $topic, 'score' => 40]]];
$ok(topic_usable_sources($db, $topic) === 2, 'topic_usable_sources: 1 свой канал + 1 высокое предложение, низкое и доски не считаются (' . topic_usable_sources($db, $topic) . ')');

// 6. Отбор по тексту: слова из exclude отбрасывают пост
$excluded = $post('tg:x', 'Telegram | x', 1, $tg(900), 'реклама магазина одежды со скидками');
$ok(hot_has_word('реклама магазина одежды', hot_words('магазина одежды')), 'hot_has_word: слово из exclude находится в подписи');

// 7. Дешёвые фильтры автоподбора на именах из журнала (без модели)
foreach (['filmviral2025terbaru233', 'smieszne_filmy', 'la_memoria_del_cuore'] as $name) {
    $ok(discovery_language_reject($name), "язык: @{$name} — отказ без модели");
}
$ok(!discovery_language_reject('memchiky Мемчики'), 'язык: @memchiky «Мемчики» — проходит');

echo "Провалов: $fails\n";
exit($fails ? 1 : 0);
