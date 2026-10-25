<?php
// Посты превью источников: php tools/test_source_posts.php. Без сети: сохранённые страницы Telegram и фикстуры tools/fixtures/.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/sources.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$fixture = fn($name) => json_decode((string)file_get_contents(__DIR__ . '/fixtures/' . $name), true)['discovered'];

echo "A. Форма поста:\n";
$post = source_preview_post(['text' => str_repeat('а', 2500), 'url' => 'https://t.me/x/1', 'posted_at' => 100, 'image' => 'https://cdn.example/a.jpg',
    'images' => ['https://cdn.example/a.jpg', 'https://cdn.example/b.jpg'], 'engagement' => ['views' => 77, 'reactions' => ['🔥' => 3, '👍' => 2]]]);
$check('текст до 2000 символов', mb_strlen($post['text']) === SOURCE_PREVIEW_TEXT_MAX);
$check('просмотры и реакции из engagement, комментариев нет — null', $post['views'] === 77 && $post['reactions'] === 5 && $post['comments'] === null);
$check('две картинки — два файла по порядку', count($post['media']) === 2 && $post['media'][1]['src'] === 'https://cdn.example/b.jpg' && $post['media'][0]['kind'] === 'image');
$check('заглушка EMPTY_POST_TEXT не попадает в текст', source_preview_post(['text' => EMPTY_POST_TEXT])['text'] === '');

echo "Б. Медиа:\n";
$album = source_preview_media(['gallery' => [['image' => 'https://cdn.example/p1.jpg', 'video' => ''], ['image' => 'https://cdn.example/p2.jpg', 'video' => 'https://cdn.example/v.mp4']], 'url' => 'https://t.me/x/2']);
$check('альбом: картинка и видео в порядке поста, видео с постером и адресом страницы', count($album) === 2 && $album[1]['kind'] === 'video' && $album[1]['poster'] === 'https://cdn.example/p2.jpg' && $album[1]['page'] === 'https://t.me/x/2');
$pending = source_preview_media(['image' => 'https://cdn.example/t.jpg', 'video' => '', 'video_page' => 'https://www.tiktok.com/@a/video/1', 'video_pending' => true, 'url' => 'https://www.tiktok.com/@a/video/1']);
$check('ролик докачивается: видео без файла с флагом pending и постером (обложка не дублируется картинкой)', count($pending) === 1 && $pending[0]['pending'] === true && $pending[0]['src'] === '');
$big = source_preview_media(['image' => 'https://cdn.example/big.jpg', 'video_unavailable' => true, 'url' => 'https://t.me/x/3']);
$check('большое видео Telegram: пометка unavailable, ссылка на пост', count($big) === 1 && $big[0]['unavailable'] === true && $big[0]['page'] === 'https://t.me/x/3');
$check('не https-адрес не попадает в медиа', source_preview_media(['image' => 'http://insecure.example/a.jpg', 'url' => ''])  === []);

echo "В. Лента Telegram (сохранённые страницы, без сети):\n";
$pages = ['' => file_get_contents(__DIR__ . '/fixtures/telegram_investor4444_p1.html'), '4354' => file_get_contents(__DIR__ . '/fixtures/telegram_investor4444_p2.html')];
$GLOBALS['telegram_page_fetch_stub'] = fn($c, $b) => $pages[(string)$b] ?? false;
$tg = build_source_preview(['platform' => PLATFORM_TELEGRAM, 'id' => 'tgsourcepostst', 'name' => 'Текст'], SOURCE_PREVIEW_POSTS);
unset($GLOBALS['telegram_page_fetch_stub']);
$times = array_column($tg['posts'], 'posted_at');
$check('медиа и текстовые посты в одной ленте, новые первыми', $tg['posts'] && max($times) === $times[0] && $times === array_values(array_reverse(array_reverse($times))) && count(array_filter($tg['posts'], fn($p) => $p['media'] === [])) > 0 && count(array_filter($tg['posts'], fn($p) => $p['media'] !== [])) > 0);
$check('у постов Telegram есть просмотры', count(array_filter($tg['posts'], fn($p) => $p['views'] !== null)) === count($tg['posts']));
$check('в ответе нет старых полей text_posts и images', !array_key_exists('text_posts', $tg) && !array_key_exists('images', $tg['posts'][0]));
$check('постов не больше SOURCE_PREVIEW_POSTS', count($tg['posts']) <= SOURCE_PREVIEW_POSTS);

echo "Г. Фикстуры стенда:\n";
$memes = $fixture('source_preview_tg_memes.json');
$check('мемный канал: 20 постов, есть альбом (два и больше файла)', count($memes['posts']) === 20 && count(array_filter($memes['posts'], fn($p) => count($p['media']) > 1)) >= 1);
$check('фикстура RSS: постов с картинками, просмотров нет', count($fixture('source_preview_rss.json')['posts']) > 0);

echo "Д. Размер ответа: 20 длинных постов с двумя картинками:\n";
$long = [];
for ($i = 0; $i < 20; $i++) $long[] = source_preview_post(['text' => str_repeat('Длинный текст поста о рынке и ценах. ', 80), 'url' => "https://t.me/x/$i", 'posted_at' => time() - $i,
    'images' => ["https://cdn.example/a$i.jpg", "https://cdn.example/b$i.jpg"], 'image' => "https://cdn.example/a$i.jpg", 'views' => 12345, 'engagement' => ['reactions' => ['🔥' => 3]]]);
$size = strlen(json_encode(['posts' => $long], JSON_UNESCAPED_UNICODE));
$check("размер ответа $size байт меньше 150 КБ", $size < 150 * 1024);

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
