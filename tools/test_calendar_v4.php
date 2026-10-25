<?php
// Проверка календаря v4 (охват, повторы, план «Заполнить дыры»): php tools/test_calendar_v4.php. Без сети и без записи в очередь.
// Охват — на фикстуре tools/fixtures/telegram_channel.html (страница t.me/s/durov, сохранена один раз).
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$throws = function (callable $fn, string $part) { try { $fn(); return false; } catch (RuntimeException $e) { return str_contains($e->getMessage(), $part); } };
$at = fn($date) => (new DateTime($date, new DateTimeZone('UTC')))->getTimestamp();

echo "A. Охват по часам (фикстура страницы канала):\n";
$html = file_get_contents(__DIR__ . '/fixtures/telegram_channel.html');
$posts = channel_reach_posts_from_html($html);
$check('страница разбирается: посты с временем и просмотрами', count($posts) >= 10 && count(array_filter($posts, fn($p) => is_int($p['views']) && $p['at'] > 0)) === count($posts));
$reach = channel_reach_summary($posts, 'Europe/Berlin', time());
$check('охват есть при ≥ 10 постах', is_array($reach) && $reach['posts'] === count($posts));
$check('лучших часов не больше трёх и они — ключи часов 0–23', $reach && count($reach['best']) <= 3 && array_diff($reach['best'], range(0, 23)) === []);
$check('мало постов (5) — охвата нет (null)', channel_reach_summary(array_slice($posts, 0, 5), 'Europe/Berlin', time()) === null);

echo "A. Часовой пояс расписания:\n";
$synthetic = [];
for ($i = 0; $i < 12; $i++) $synthetic[] = ['at' => $at('2026-10-20 17:30:00') + $i * 3600, 'views' => 1000 + $i];   // 17:30 UTC
$berlin = channel_reach_summary($synthetic, 'Europe/Berlin', $at('2026-10-30 12:00:00'));
$utc = channel_reach_summary($synthetic, 'UTC', $at('2026-10-30 12:00:00'));
$check('пояс Берлин: 17:30 UTC 20 октября — час публикации 19 (летнее время)', $berlin && array_key_exists(19, $berlin['hours']));
$check('пояс UTC: час публикации 17', $utc && array_key_exists(17, $utc['hours']));
$check('медиана часа: одинаковые просмотры дают то же число', $utc && $utc['hours'][17] === channel_median(array_column(array_filter($synthetic, fn($p) => (int)gmdate('G', $p['at']) === 17), 'views')));

echo "A. Прогноз просмотров:\n";
$f = channel_forecast(['hours' => [19 => 3100], 'days' => [], 'overall' => 3000, 'posts' => 12], $at('2026-10-23 19:00:00'), 'UTC');
$check('прогноз — медиана часа, без поправки при < 30 постах', $f === 3100);
$check('прогноз без охвата — null', channel_forecast(null, time(), 'UTC') === null);

echo "B. Повторы:\n";
$text = 'Котик изображает хлебушек в ванной и это лучший пост недели';
$published = [['channel' => 'c1', 'text' => $text . '!!', 'at' => time() - 86400, 'url' => 'https://t.me/x/1', 'media_src' => ['https://img.test/a.jpg']]];
$dup = calendar_duplicate_of(['text' => $text, 'image' => 'https://img.test/other.jpg'], 'c1', $published);
$check('повтор по тексту (знаки и регистр не важны) — находится', is_array($dup) && $dup['url'] === 'https://t.me/x/1');
$check('тот же текст в другом канале — не повтор', calendar_duplicate_of(['text' => $text], 'c2', $published) === null);
$check('та же картинка в этом канале — повтор', is_array(calendar_duplicate_of(['image' => 'https://img.test/a.jpg', 'text' => 'другое'], 'c1', $published)));
$check('другая картинка и другой текст — не повтор', calendar_duplicate_of(['image' => 'https://img.test/z.jpg', 'text' => 'совсем другой пост про погоду в Москве'], 'c1', $published) === null);

echo "C. Свободные слоты (очередь занимает один слот):\n";
$tz = 'Europe/Berlin';
$channel = ['id' => 'c1', 'title' => 'Тест', 'username' => 'tst', 'schedule' => ['tz' => $tz, 'slots' => ['1' => ['10:00', '19:00'], '3' => ['10:00']]]];
$from = $at('2026-11-02 00:00:00');   // понедельник, по Берлину — после перехода на зимнее время
$to = $from + 7 * DAY_SECONDS;
$busy = [['channel' => 'c1', 'publish_at' => (new DateTime('2026-11-02 10:00:00', new DateTimeZone($tz)))->getTimestamp()]];
$slots = calendar_free_slots($channel, $busy, $from, $to, $from - DAY_SECONDS);
$check('слоты недели: понедельник 10:00 занят очередью — не в списке; 19:00 и среда 10:00 — есть', count($slots) === 2 && !in_array((new DateTime('2026-11-02 10:00:00', new DateTimeZone($tz)))->getTimestamp(), $slots, true));

echo "C. План «Заполнить дыры» (без записи в очередь):\n";
$cands = [
    ['image' => 'https://img.test/1.jpg', 'text' => 'первый пост для теста плана', 'relevance' => 90, 'format' => 'photo'],
    ['image' => 'https://img.test/2.jpg', 'text' => 'второй пост для теста плана', 'relevance' => 80, 'format' => 'photo'],
    ['image' => 'https://img.test/3.jpg', 'text' => 'третий пост для теста плана', 'relevance' => 70, 'format' => 'photo'],
];
$slot_list = [['at' => $at('2026-11-02 18:00:00'), 'channel' => $channel, 'best' => true], ['at' => $at('2026-11-03 09:00:00'), 'channel' => $channel, 'best' => false]];
$plan = calendar_plan_pick($slot_list, $cands, []);
$check('план: лучший слот получает лучший пост; без повторов картинок', count($plan) === 2 && $plan[0]['image'] === 'https://img.test/1.jpg' && $plan[0]['image'] !== $plan[1]['image']);
$check('план: лимит поста за раз', count(calendar_plan_pick(array_fill(0, 30, $slot_list[0]), array_fill(0, 30, $cands[0]), [])) <= FILL_MAX_POSTS);
$videos = array_map(fn($i) => ['image' => "https://img.test/v$i.jpg", 'text' => "видео номер $i для теста", 'relevance' => 90 - $i, 'format' => 'video'], range(1, 4));
$vslots = array_map(fn($i) => ['at' => $at('2026-11-02 18:00:00') + $i * 3600, 'channel' => $channel, 'best' => true], range(0, 3));
$vplan = calendar_plan_pick($vslots, $videos, []);
$check('план: не больше двух видео подряд в канале', count($vplan) === 2 && count($vplan) < 4);

echo "D. Применение: отказы до записи в очередь:\n";
$check('применение больше 21 поста — отказ', $throws(fn() => calendar_fill_apply(1, array_fill(0, 22, ['slot_at' => time() + 3600, 'channel' => 'c1', 'image' => 'x', 'text' => 'x'])), 'не больше 21'));
$past = calendar_fill_apply(1, [['slot_at' => time() - 3600, 'channel' => 'c1', 'image' => 'https://img.test/1.jpg', 'text' => 'текст']]);
$check('слот в прошлом — ошибка по строке, ничего не поставлено', $past['posted'] === [] && str_contains($past['errors'][0]['error'] ?? '', 'прошёл'));

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
