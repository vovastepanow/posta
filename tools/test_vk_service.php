<?php
// Разбор стены VK на образце ответа wall.get (без сети и без ключа): php tools/test_vk_service.php
// Живая проверка сервисного ключа — отдельно, владельцем (см. отчёт). Проверяем: пост с картинкой разбирается, текст без картинки
// и видео без фото — отбрасываются, поля счётчиков и ссылка — как у записи паблика.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function (string $name, bool $ok) use (&$failures) { echo ($ok ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

$sample = json_decode((string)file_get_contents(__DIR__ . '/fixtures/vk_wall_get.json'), true)['response']['items'] ?? [];
$check('образец содержит три записи', count($sample) === 3);

$posts = array_values(array_filter(array_map(fn($item) => vk_wall_post($item, 'Тестовый паблик', 'testpublic'), $sample)));
$check('без текста-режима остаётся один пост с картинкой', count($posts) === 1);
$post = $posts[0] ?? [];
$check('картинка — самый большой размер фото', ($post['image'] ?? '') === 'https://sun9.userapi.com/w.jpg');
$check('ссылка на запись вида vk.com/wall{owner}_{id}', ($post['url'] ?? '') === 'https://vk.com/wall-123456_4411');
$check('счётчики: лайки, репосты, комментарии, просмотры', ($post['likes'] ?? 0) === 812 && ($post['reposts'] ?? 0) === 40 && ($post['comments'] ?? 0) === 19 && ($post['views'] ?? 0) === 15230);
$check('дата — из поля date', ($post['posted_at'] ?? 0) === 1792700000);

$with_text = array_values(array_filter(array_map(fn($item) => vk_wall_post($item, 'Тестовый паблик', 'testpublic', true, 20), $sample)));
$check('с режимом «текст поста» попадает и текстовый пост', count($with_text) === 2 && ($with_text[1]['text_post'] ?? false) === true);

echo $failures ? "\nПровалов: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
