<?php
// Проверка системы хэштегов: php tools/test_hashtags.php. Без сети и без модели; kv hashtag_candidates восстанавливается.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$saved_candidates = kv_get('hashtag_candidates', null);

echo "Словарь:\n";
$check('taxonomy-check без ошибок', taxonomy_check()['errors'] === []);
$dict = hashtag_dictionary();
$long = array_filter(array_keys($dict['tags']), fn($t) => mb_strlen($t) > HASHTAG_MAX_LEN);
$check('нет тегов длиннее ' . HASHTAG_MAX_LEN . ' символов', $long === []);
$check('синоним ведёт к каноническому: кринж → фейлы', (hashtag_lookup('кринж')['tag'] ?? '') === 'фейлы');
$check('#ЧёрныйЮмор → черныйюмор', (hashtag_lookup('#ЧёрныйЮмор')['tag'] ?? '') === 'черныйюмор');
$check('мусорные слова не распознаются (горячо, двусмысленно)', hashtag_lookup('горячо') === null && hashtag_lookup('двусмысленно') === null);
$check('жиза — рубрика своей темы', (hashtag_lookup('жиза')['kind'] ?? '') === 'rubric' && hashtag_rubric('humor.relatable') === 'жиза');
$check('у темы нет форматов: brainrot = рубрика и имена', hashtags_for_topic('internet.brainrot') === ['брейнрот', 'тралалеро', 'бомбардиро']);
$check('прочее ветки делит тег соседа: food.other → еда', hashtag_rubric('food.other') === 'еда');

echo "Хэштеги поста:\n";
$tags = fn(array $r) => post_hashtags($r);
$check('кот в видео → #котики #видео', $tags(['tags' => ['animals.cats'], 'image' => 'a', 'video' => 'https://x/v.mp4']) === ['#котики', '#видео']);
$check('ролик в очереди скачивания — тоже видео', $tags(['tags' => ['animals.dogs'], 'image' => 'a', 'video' => '', 'video_via' => 'ytdlp']) === ['#собаки', '#видео']);
$check('статья про WB → #маркетплейсы #wildberries #статья',
    $tags(['tags' => ['money.ecom'], 'entity' => 'Вайлдберриз', 'article' => ['title' => 'x'], 'image' => 'a']) === ['#маркетплейсы', '#wildberries', '#статья']);
$check('синоним имени не выходит наружу: dota2 → #дота', $tags(['tags' => ['games.esports'], 'entity' => 'dota2', 'image' => 'a']) === ['#киберспорт', '#дота']);
$check('humor.other без признаков → #юмор', $tags(['tags' => ['humor.other'], 'image' => 'a']) === ['#юмор']);
$check('альбом из 3 картинок → #подборка', $tags(['tags' => ['travel.nature'], 'image' => 'a', 'images' => ['a', 'b', 'c']]) === ['#природа', '#подборка']);
$check('имя-рубрика не дублируется именем: entity «мемы»', $tags(['tags' => ['memes.other'], 'entity' => 'мемы', 'image' => 'a']) === ['#мемы']);
$check('без меток — рубрика по теме записи', $tags(['tags' => [], 'topic' => 'Абсурдный юмор', 'image' => 'a']) === ['#абсурд']);
kv_update('hashtag_candidates', fn() => [], []);
$check('новое имя «Хаги Ваги» → #хагиваги', post_hashtags(['tags' => ['internet.characters'], 'entity' => 'Хаги Ваги', 'image' => 'a'], true) === ['#мемперсонажи', '#хагиваги']);
$check('новое имя попало в кандидаты словаря', (kv_get('hashtag_candidates', [])['хагиваги']['count'] ?? 0) === 1);

echo "Текст и канал:\n";
$check('теги — последней строкой', text_with_hashtags('Привет', ['#котики', '#видео'], 1024) === "Привет\n\n#котики #видео");
$check('тег, уже стоящий в тексте, не повторяется', text_with_hashtags('Привет #котики', ['#котики', '#видео'], 1024) === "Привет #котики\n\n#видео");
$check('не влезает — снимается с конца (формат первым)', text_with_hashtags(str_repeat('а', 1005), ['#котики', '#дота', '#видео'], 1024) === str_repeat('а', 1005) . "\n\n#котики #дота");
$check('совсем не влезает — текст как был', text_with_hashtags(str_repeat('а', 1020), ['#котики'], 1024) === str_repeat('а', 1020));
$post = ['#котики', '#дота', '#видео'];
$check('канал off — без тегов', channel_hashtags(['hashtags' => 'off'], $post) === []);
$check('канал rubric — только рубрика', channel_hashtags(['hashtags' => 'rubric'], $post) === ['#котики']);
$check('канал full — все три', channel_hashtags(['hashtags' => 'full'], $post) === $post);
$check('ввод редактора чистится: «#Чёрный юмор», мусор, повторы, не больше трёх',
    publish_hashtags_input(json_encode(['#Чёрный юмор', '<b>', '#котики', 'котики', '#видео', '#дота'])) === ['#черныйюмор', '#котики', '#видео']);
$check('подсказки: «кот» → #котики', (hashtag_suggest('кот')[0]['tag'] ?? '') === 'котики');
$check('хэштеги источника → канонические', post_source_hashtags(['text' => 'Смотрите #funny #Котята #непонятноеслово']) === ['юмор', 'котики']);

kv_update('hashtag_candidates', fn() => $saved_candidates ?? [], []);
echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
