<?php
// Проверка роликов по кнопке и очереди (без сети). Пишет в базу, поэтому запускается только на копии кода:
//   POSTA_TEST_ISOLATED=1 php tools/test_video_pending.php
// В копии не должно быть tools/ytdlp_worker.php: иначе постановка в очередь запустит настоящее скачивание.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
if (getenv('POSTA_TEST_ISOLATED') !== '1') { fwrite(STDERR, "Запускайте на копии кода: POSTA_TEST_ISOLATED=1 php tools/test_video_pending.php\n"); exit(2); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/sources.php';
require_once __DIR__ . '/../handlers/content.php';

$failures = 0;
$check = function (string $name, bool $ok) use (&$failures) { echo ($ok ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
// Ответы обработчиков приходят исключением: так проверяем код ответа и текст
function captured(callable $fn): array {
    try { $fn(); return ['status' => 200, 'data' => null]; }
    catch (Throwable $e) {
        $m = json_decode($e->getMessage(), true);
        return is_array($m) ? ['status' => $m['status'] ?? 200, 'data' => $m] : ['status' => 500, 'data' => $e->getMessage()];
    }
}
if (!function_exists('json_response')) { function json_response($data, $status = 200): never { throw new RuntimeException(json_encode(['data' => $data, 'status' => $status], JSON_UNESCAPED_UNICODE)); } }
if (!function_exists('json_error')) { function json_error($message, $status = 400): never { throw new RuntimeException(json_encode(['status' => $status, 'message' => $message], JSON_UNESCAPED_UNICODE)); } }

$uid = bin2hex(random_bytes(16));
db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
$tt = fn($n) => "https://www.tiktok.com/@testuser.{$n}/video/" . (7000000000 + $n);
$cleanup = function () use ($uid) {
    ytdlp_queue_update(fn($jobs) => array_values(array_filter($jobs, fn($j) => $j['uid'] !== $uid)));
    pool_update(fn($pool) => array_values(array_filter($pool, fn($i) => !is_array($i) || str_starts_with((string)($i['image'] ?? ''), 'test-'))), $uid);
};
$cleanup();

// 1. Адреса: принимаются только свои форматы
$ok_urls = ['https://t.me/denissexy/11750' => 'tgu', 'https://www.tiktok.com/@some.user/video/7123456789' => 'ytdlp', 'https://www.youtube.com/shorts/abcdefghijk' => 'ytdlp'];
foreach ($ok_urls as $u => $via) $check("адрес принят: $u", video_page_via($u) === $via);
foreach (['https://www.tiktok.com/@a;id/video/7123456789', 'https://evil.com/x', 'https://www.youtube.com/watch?v=abcdefghijk', 'https://t.me/abc/1', 'http://t.me/denissexy/1'] as $u)
    $check("адрес отклонён: $u", video_page_via($u) === null);

// 2. Способ скачивания выводится из адреса, если поле пустое (старые задачи)
$check('via из адреса: t.me → tgu', ytdlp_job_via(['via' => null, 'page' => 'https://t.me/denissexy/1']) === 'tgu');
$check('via из адреса: tiktok → ytdlp', ytdlp_job_via(['via' => '', 'page' => $tt(1)]) === 'ytdlp');

// 3. Ключ превью не трогает пул, состояние — в kv; повтор не плодит задач; лимит 3 на пользователя
$url = $tt(100);
$r1 = video_fetch_enqueue($url, 'ytdlp', $uid);
$r2 = video_fetch_enqueue($url, 'ytdlp', $uid);
$check('повторный запрос — одна задача', count(array_filter(ytdlp_queue_list(), fn($j) => $j['uid'] === $uid && $j['key'] === video_preview_key($url))) === 1);
$check('состояние в kv: queued', video_preview_status($url)['status'] === 'queued');
$check('пул не тронут превью', pool_load($uid) === [] || !array_filter(pool_load($uid), fn($i) => ($i['video_page'] ?? '') === $url));
for ($i = 101; $i <= 102; $i++) video_fetch_enqueue($tt($i), 'ytdlp', $uid);
$r4 = video_fetch_enqueue($tt(103), 'ytdlp', $uid);
$check('лимит 3 для TikTok/Shorts: четвёртый отклонён', $r4['limited'] === true && video_preview_jobs_of($uid, 'ytdlp') === 3);

// 4. Ответы обработчика: неверный адрес — 400, Telegram без аккаунта — 409
$_GET = ['url' => 'https://www.tiktok.com/@a;id/video/7123456789'];
$check('статус: чужой хост — 400', captured('handle_video_status')['status'] === 400);
$_GET = ['url' => $url];
$check('статус: адрес из очереди — queued', (captured('handle_video_status')['data']['data']['status'] ?? '') === 'queued');
$check('fetch: неверный адрес — 400', captured(fn() => handle_video_fetch(['url' => 'https://evil.com/x']))['status'] === 400);
$check('fetch: Telegram без аккаунта — 409', captured(fn() => handle_video_fetch(['url' => 'https://t.me/denissexy/11750']))['status'] === 409);

// 5. Своя запись TikTok через own_add_to_pool ставит ролик в очередь; у записи с роликом — адрес кэша
$rec = ['image' => 'test-' . substr($uid, 0, 8) . '.jpg', 'images' => [], 'video' => '', 'video_via' => 'ytdlp', 'video_page' => $tt(200), 'text' => 'test', 'source' => 'test',
        'post_id' => '200', 'url' => $tt(200), 'posted_at' => time(), 'engagement' => null, 'gallery' => [], 'video_unavailable' => false];
$before = count(ytdlp_queue_list());
pool_items_enqueue_videos([$rec], $uid);
$check('pool_items_enqueue_videos ставит задачу для записи с роликом', count(ytdlp_queue_list()) === $before + 1);
$check('запись без ролика (video_via пуст) в очередь не попадает', (function () use ($uid, $rec, $before) {
    $n = count(ytdlp_queue_list());
    pool_items_enqueue_videos([['video_via' => '', 'video_page' => 'https://t.me/x/1', 'video' => ''] + $rec], $uid);
    return count(ytdlp_queue_list()) === $n;
})());

// 5b. «Лучшие посты»: ролик, которого не хватило в лимите, ставится в очередь по странице поста
$check('лучший пост с роликом у t.me — через аккаунт', best_post_video_fields(['video' => 'https://cdn.x/v.mp4', 'url' => 'https://t.me/denissexy/11750'], '')['video_via'] === 'tgu');
$check('лучший пост с роликом у Shorts — yt-dlp', best_post_video_fields(['video' => 'https://cdn.x/v.mp4', 'url' => 'https://www.youtube.com/shorts/abcdefghijk'], '')['video_via'] === 'ytdlp');
$check('лучший пост у VK без страницы для скачивания — без ролика', best_post_video_fields(['video' => 'https://cdn.x/v.mp4', 'url' => 'https://vk.com/wall-1_2'], '')['video_via'] === '');
$check('лучший пост без ролика — без пометки', best_post_video_fields(['video' => '', 'url' => 'https://t.me/denissexy/1'], '')['video_via'] === '');
$check('лучший пост с готовым роликом — без очереди', best_post_video_fields(['video' => 'https://cdn.x/v.mp4', 'url' => 'https://t.me/denissexy/1'], '/media_cache/a.mp4')['video_via'] === '');

// 6. get_item_video: у записи с роликом — адрес кэша; без ролика — пусто
$local = MEDIA_CACHE_URL . '/' . str_repeat('a', 32) . '.mp4';
pool_update(fn($pool) => [array_replace($rec, ['video' => $local, 'video_local' => $local])] + array_values(array_filter($pool, fn($i) => ($i['image'] ?? '') !== $rec['image'])), $uid);
$_GET = ['image' => $rec['image']];
$got = captured('handle_get_item_video');
$check('get_item_video отдаёт адрес у записи с роликом', ($got['data']['data']['video'] ?? '') === $local);
$_GET = ['image' => 'нет-такой-записи'];
$check('get_item_video: нет записи — 404', captured('handle_get_item_video')['status'] === 404);

$cleanup();
ytdlp_queue_update(fn($jobs) => array_values(array_filter($jobs, fn($j) => $j['uid'] !== $uid)));
echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли\n";
exit($failures ? 1 : 0);
