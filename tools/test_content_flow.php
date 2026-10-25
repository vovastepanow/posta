<?php
// Мало контента при большом числе постов (CONTENT_FLOW_PROMPT.md): шортлист, имиджборды, слова темы по основам.
// Без сети и модели, подставные посты. Запуск — на копии базы, см. CONTENT_FLOW_PROMPT.md, «Ограничения».
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
$fails = 0;
$ok = function ($cond, $name) use (&$fails) { echo ($cond ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$cond) $fails++; };

app_user_bind(bin2hex(random_bytes(16)));

$now = time();
$post = function ($source_key, $source, $posted_hours, $engagement, $extra = []) use ($now) {
    return $extra + ['source_key' => $source_key, 'source' => $source, 'posted_at' => $now - (int)($posted_hours * 3600),
        'image' => "img-$source_key-" . mt_rand(), 'text' => 'пост про тему, достаточно длинная подпись для проверки', 'engagement' => $engagement];
};
$tg = fn($views) => ['source' => 'telegram', 'views' => $views, 'reactions' => []];
$topic = 'Города';
$keywords = ['архитектура'];

// A. 90 постов Двача с доверенными тредами + 20 Telegram + 16 YouTube
$all = [];
for ($t = 0; $t < 30; $t++) for ($k = 0; $k < 3; $k++) {
    $all[] = $post("dvach:trv:$t", 'Двач /trv/ · тред ' . $t, 3, ['source' => 'dvach', 'thread_posts' => 40 + $t], ['thread_trust' => true, 'thread_title' => "тред $t", 'board' => 'trv', 'thread_id' => (string)$t]);
}
for ($c = 0; $c < 20; $c++) $all[] = $post("tg:c$c", "Telegram | c$c", 3, $tg(300 + $c));
for ($y = 0; $y < 16; $y++) $all[] = $post("yt:y$y", 'RSS | youtube.com', 3, null, ['views' => 500 + $y]);
$out = hot_shortlist($all, [], $topic, $keywords);
$short = $GLOBALS['hot_stats']['short'];
$sh = array_slice($out, 0, $short);
$count = fn($pred) => count(array_filter($sh, $pred));
$dvach = $count(fn($i) => str_starts_with((string)$i['source'], 'Двач'));
$chan = $count(fn($i) => str_starts_with((string)$i['source'], 'four') || str_starts_with((string)$i['source'], '4chan'));
$tgn = $count(fn($i) => str_starts_with((string)$i['source'], 'Telegram'));
$ytn = $count(fn($i) => str_starts_with((string)$i['source'], 'RSS'));
$ok($short > 0, "A: шортлист не пуст ({$short} из " . count($all) . ')');
$ok($tgn > 0 && $ytn > 0, "A: Telegram ({$tgn}) и YouTube ({$ytn}) в шортлисте");
$ok($dvach <= HOT_PLATFORM_SHARE * HOT_SHORTLIST, "A: Двача не больше 40% мест шортлиста ({$dvach} из {$short})");
$ok($chan <= HOT_PLATFORM_SHARE * HOT_SHORTLIST, "A: 4chan не больше 40% мест шортлиста ({$chan} из {$short})");
$per_source = array_count_values(array_map(fn($i) => $i['source_key'], $sh));
$ok(max($per_source) <= HOT_PER_SOURCE, 'A: с одного источника не больше HOT_PER_SOURCE (' . max($per_source) . ')');
$ok(($GLOBALS['hot_stats']['trusted'] ?? -1) > 0, 'A: в журнале учтены посты из доверенных тредов (' . ($GLOBALS['hot_stats']['trusted'] ?? '—') . ')');

// B. Имиджборды в узкой теме — только с признаками темы
$board_all = [];
$board_all[] = $post('4chan:em:1', '4chan /em/ · ФИЛИППИНОТРЕД', 2, ['source' => '4chan', 'replies' => 80, 'images' => 40],
    ['thread_title' => 'ФИЛИППИНОТРЕД', 'thread_op' => 'обсуждаем', 'text' => 'обсуждаем разное без нужных слов в подписи этого поста']);
$board_all[] = $post('4chan:trv:2', '4chan /trv/ · Архитектура Праги', 2, ['source' => '4chan', 'replies' => 30, 'images' => 12],
    ['thread_title' => 'Архитектура Праги', 'thread_op' => '', 'text' => 'подпись без слова в тексте']);
for ($c = 0; $c < 6; $c++) $board_all[] = $post("tg:b$c", "Telegram | b$c", 2, $tg(200 + $c));
$short_b = hot_stats_short_after($board_all, $topic, $keywords);
$ok(!in_array('ФИЛИППИНОТРЕД', $short_b, true) && $GLOBALS['hot_stats']['imageboard_tail'] >= 1, 'B: /em/ «ФИЛИППИНОТРЕД» без слов темы — в хвосте');
$ok(in_array('Архитектура Праги', $short_b, true), 'B: /trv/ «Архитектура Праги» — в шортлисте');

$native = hot_shortlist($board_all, [], 'Мемы', $keywords);
$native_short = array_column(array_slice($native, 0, $GLOBALS['hot_stats']['short']), 'thread_title');
$ok(imageboard_native_topic('Мемы') && !imageboard_native_topic($topic), 'B: тема «Мемы» (ключ memes) — родная для имиджбордов, «Города» — нет');
$ok(in_array('ФИЛИППИНОТРЕД', $native_short, true), 'B: в теме с ключом memes правило не действует — пост в шортлисте');
$ok(imageboard_native_topic('Мемы') === true && imageboard_native_topic('Города') === false, 'B: проверка по ключу темы');

// C. Слова темы по основам
$ok(topic_word_stem('архитектура') === 'архитектур', 'C: архитектура → архитектур');
$ok(topic_word_stem('здания') === 'здани', 'C: здания → здани');
$ok(topic_word_stem('город') === 'город', 'C: город → город');
$ok(topic_word_stem('мост') === 'мост', 'C: мост → мост');
$ok(topic_words_hit('Архитектурные излишества', ['архитектура']), 'C: «архитектура» находит «Архитектурные излишества»');
$ok(topic_words_hit('новых зданий', ['здания']), 'C: «здания» находит «зданий»');
$ok(topic_words_hit('Архитектура Праги', ['архитектура']), 'C: «Архитектура Праги» находит «архитектура»');
$ok(!topic_words_hit('Раньше всех. Ну почти.', ['архитектура', 'город', 'здания']), 'C: «Раньше всех. Ну почти.» — не находит');
$ok(!topic_words_hit('ФИЛИППИНОТРЕД', ['архитектура', 'город']), 'C: без слов темы — не находит');

echo "Провалов: $fails\n";
exit($fails ? 1 : 0);

/** Названия шортлиста для проверки: что попало в первые места после прохода. */
function hot_stats_short_after(array $items, string $topic, array $keywords): array {
    $out = hot_shortlist($items, [], $topic, $keywords);
    return array_column(array_slice($out, 0, $GLOBALS['hot_stats']['short']), 'thread_title');
}
