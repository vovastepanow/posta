<?php
// Фильтры подбора «Только с медиа» и «Только медиа» (MEDIA_FILTERS_PROMPT.md): правила, очередь парсера, выдача пула.
// Запуск: php tools/test_media_filters.php. Создаёт временного пользователя и удаляет его записи в конце.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';   // pool_for_current_topic

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

$uid = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);   // дальше все функции работают от имени этого пользователя
$topic = 'тест-медиа-фильтры';
save_settings(['topic' => $topic]);

echo "A. Текст поста:\n";
$text = fn($t, $extra = []) => ['text' => $t, 'image' => '', 'text_post' => true] + $extra;
$check("пусто — не текст", !post_item_has_text(['text' => '', 'image' => 'x.jpg']));
$check('заглушка EMPTY_POST_TEXT — не текст', !post_item_has_text(['text' => EMPTY_POST_TEXT]));
$check('одни хэштеги — не текст', !post_item_has_text($text('#смех #funny #топ')));
$check('эмодзи, ссылка и @упоминание — не текст', !post_item_has_text($text('😂😂 https://t.me/x/1 @chan')));
$check('подпись с хэштегом — текст', post_item_has_text($text('Когда пятница 13-е #смех')));
$check('две буквы — текст', post_item_has_text($text('Ок')));
$check('текст в подписи (captions[0]) — текст', post_item_has_text(['text' => '#смех', 'captions' => ['Настоящая подпись']]));

echo "B. Медиа:\n";
$check('текстовый пост — не медиа', !post_item_has_media($text('Просто текст')));
$check('TikTok в очереди (обложка, видео пустое) — медиа', post_item_has_media(['image' => 'https://cdn.test/cover.jpg', 'video' => '', 'video_via' => 'ytdlp']));
$check('альбом с gallery (два файла) — медиа', post_item_has_media(['image' => '', 'gallery' => [['image' => 'https://cdn.test/1.jpg', 'video' => ''], ['image' => 'https://cdn.test/2.jpg', 'video' => '']]]));
$check('видео без обложки — медиа', post_item_has_media(['image' => '', 'video' => 'https://cdn.test/v.mp4']));

echo "C. Очередь парсера:\n";
$post = fn($tag, $extra = []) => array_merge(['image' => "https://cdn.test/{$tag}.jpg", 'text' => "Запись {$tag}", 'text_post' => false, 'source' => 'test', 'source_key' => 'source:test',
    'source_url' => "https://t.me/test/{$tag}", 'posted_at' => time() - 3600, 'tags' => []], $extra);
$textpost = ['image' => '', 'text' => 'Длинный текст поста, который точно проходит минимум для анализа', 'text_post' => true, 'source' => 'test', 'source_key' => 'source:test',
    'source_url' => 'https://t.me/test/text', 'posted_at' => time() - 3600, 'tags' => []];
$skipped = [];
$queue = build_candidate_queue(['test' => [$post('m1'), $textpost]], [], [], [], $skipped, [], null, ['media_only' => true]);
$check('«Только с медиа»: текстовый пост в очередь не попал', count($queue) === 1 && ($queue[0]['image'] ?? '') !== '');
$check('«Только с медиа»: отсеян с причиной filter', ($skipped['filter'] ?? 0) === 1);
$skipped = [];
$queue = build_candidate_queue(['test' => [$post('m2', ['text' => '#смех #funny']), $post('m3', ['text' => 'Когда пятница 13-е #смех'])]], [], [], [], $skipped, [], null, ['media_no_text' => true]);
$check('«Только медиа»: пост с подписью отсеян, без подписи — проходит', count($queue) === 1 && str_contains($queue[0]['image'], 'm2'));
$skipped = [];
build_candidate_queue(['test' => [$post('m4', ['text' => 'Подпись'])]], [], [], [], $skipped, [], null, []);
$check('без фильтров пост с подписью проходит', ($skipped['filter'] ?? 0) === 0);

echo "D. Пул:\n";
$pooled = fn($tag, $text) => ['image' => "https://cdn.test/pool-{$tag}.jpg", 'text' => $text, 'captions' => [$text], 'topic' => $topic, 'pooled_at' => time(), 'source' => 'test', 'source_key' => 'source:test', 'source_url' => "https://t.me/test/p{$tag}"];
pool_update(fn($p) => array_merge($p, [$pooled('a', 'Подпись с текстом'), $pooled('b', '#смех #funny')]));
save_settings(['media_only' => true, 'media_no_text' => true]);
$shown = pool_for_current_topic();
$check('«Только медиа»: запись с подписью скрыта при выдаче', !array_filter($shown, fn($i) => ($i['text'] ?? '') === 'Подпись с текстом'));
$check('«Только медиа»: запись без подписи показана', count(array_filter($shown, fn($i) => ($i['text'] ?? '') === '#смех #funny')) === 1);
$check('«Только медиа»: запись с подписью осталась в пуле', count(array_filter(pool_load(), fn($i) => ($i['text'] ?? '') === 'Подпись с текстом')) === 1);
save_settings(['media_only' => false, 'media_no_text' => false]);
$check('после снятия фильтров скрытая запись снова видна', count(array_filter(pool_for_current_topic(), fn($i) => ($i['text'] ?? '') === 'Подпись с текстом')) === 1);

db_exec('DELETE FROM history_items WHERE user_id = ?', [$uid]);
db_exec('DELETE FROM pool_items WHERE user_id = ?', [$uid]);
db_exec('DELETE FROM users WHERE id = ?', [$uid]);
echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
