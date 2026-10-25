<?php
// Проверка TikTok, YouTube Shorts и X: php tools/test_video_sources.php. Проверки с сетью помечены [сеть].
// Тестовый пользователь, тестовые записи каталога и файлы в кэше медиа удаляются при выходе.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/sources.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$throws = function (callable $fn, $fragment = '') {
    try { $fn(); return false; } catch (Throwable $e) { return $fragment === '' || str_contains($e->getMessage(), $fragment); }
};
$cached_files = [];
/** Ждёт, пока задачи пользователя в очереди роликов и фоновый воркер не закончат. */
$wait_queue = function ($uid, $timeout = 240) {
    for ($end = time() + $timeout; time() < $end; sleep(2)) {
        $pending = array_filter(ytdlp_queue_update(fn($jobs) => $jobs), fn($j) => $j['uid'] === $uid);
        if (!$pending && ($lock = try_lock(YTDLP_CACHE_DIR . '/.worker.lock'))) { flock($lock, LOCK_UN); fclose($lock); return; }
    }
};
$test_topic = 'тест-видео-источники';
$test_user = '';

register_shutdown_function(function () use (&$cached_files, &$test_user, $test_topic) {
    foreach ($cached_files as $file) @unlink($file);
    if ($test_user === '') return;
    foreach (pool_load($test_user) as $item) foreach (media_cache_item_urls($item) as $u) if (($p = media_cache_path($u)) !== null) @unlink($p);
    db_exec('DELETE FROM pool_items WHERE user_id = ?', [$test_user]);
    ytdlp_queue_update(fn($jobs) => array_values(array_filter($jobs, fn($j) => $j['uid'] !== $test_user)));
    update_source_db(function ($db) use ($test_topic) {
        foreach (['tiktok_accounts', 'telegram_channels', 'rss_feeds', 'vk_publics', 'ok_groups'] as $key) {
            $db[$key] = array_values(array_filter($db[$key], fn($e) => !in_array($test_topic, (array)($e['topics'] ?? []), true)));
        }
        return $db;
    });
    db_exec('DELETE FROM kv WHERE key LIKE ?', ['%' . $test_user . '%']);   // квоты и счётчики пользователя
    db_exec('DELETE FROM users WHERE id = ?', [$test_user]);
});

// ---- Часть A: yt-dlp и защита запуска ----
echo "A. yt-dlp\n";
$check('yt-dlp установлен, версия записана', is_file(ROOT . '/tools/bin/yt-dlp') && trim((string)@file_get_contents(ROOT . '/tools/bin/YT_DLP_VERSION')) !== '');
$check('таймаут останавливает процесс', (function () { $t = microtime(true); $r = ytdlp_exec(['sleep', '5'], 1); return !$r['ok'] && microtime(true) - $t < 3; })());
$check('ссылка с пробелом и «;» отвергнута до запуска', $throws(fn() => ytdlp_list('https://x.com/a; id', 3), 'Некорректная ссылка'));
$check('--exec в адресе отвергнут до запуска', $throws(fn() => ytdlp_list('--exec=touch /tmp/x', 3), 'Некорректная ссылка'));

// ---- Часть B: TikTok — ссылки и каталог (без сети) ----
echo "B. TikTok, без сети\n";
$check('@имя → имя аккаунта', tiktok_user_from_link('@tiktok') === 'tiktok');
$check('ссылка на аккаунт → имя', tiktok_user_from_link('https://www.tiktok.com/@tiktok') === 'tiktok');
$check('ссылка на видео автора → имя автора', tiktok_user_from_link('https://www.tiktok.com/@tiktok/video/7698770783367171359') === 'tiktok');
$check('m.tiktok.com → имя', tiktok_user_from_link('https://m.tiktok.com/@tiktok') === 'tiktok');
$check('адрес с «;» — ошибка', $throws(fn() => tiktok_user_from_link('https://www.tiktok.com/@a;id'), 'Нужна ссылка на аккаунт'));
$check('тег, а не аккаунт — ошибка', $throws(fn() => tiktok_user_from_link('https://www.tiktok.com/tag/cat'), 'Нужна ссылка на аккаунт'));
$check('чужой хост — ошибка', $throws(fn() => tiktok_user_from_link('https://example.com/@tiktok'), 'Нужна ссылка на аккаунт'));
$check('ссылка TikTok → площадка TikTok', source_parse_link('https://www.tiktok.com/@tiktok') === ['platform' => PLATFORM_TIKTOK, 'id' => 'tiktok']);
$check('ключ источника TikTok: tiktok:<имя>', source_id_for(PLATFORM_TIKTOK, 'TikTok') === 'tiktok:tiktok');
$check('имя аккаунта: только латиница, цифры, _ и .', is_valid_source_id(PLATFORM_TIKTOK, 'tik_tok.1') && !is_valid_source_id(PLATFORM_TIKTOK, 'a;b'));
$check('каталог: ключ tiktok_accounts', CATALOG_KEYS[PLATFORM_TIKTOK] === 'tiktok_accounts');
$check('лучшая обложка: только https, самая большая', tiktok_best_thumbnail([['url' => 'http://x/y.jpg', 'width' => 9]]) === '' && tiktok_best_thumbnail([['url' => 'https://a/s.jpg', 'width' => 100, 'height' => 100], ['url' => 'https://a/b.jpg', 'width' => 720, 'height' => 1280]]) === 'https://a/b.jpg');

// Каталог: строка tiktok_accounts переживает запись каталога
$uid = bin2hex(random_bytes(16));
$test_user = $uid;
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
update_source_db(function ($db) use ($test_topic) { $db['tiktok_accounts'][] = ['id' => 'tiktoktest', 'name' => 'тест', 'topics' => [$test_topic]]; return $db; });
$before = count(array_filter(load_source_db()['tiktok_accounts'], fn($e) => in_array($test_topic, (array)($e['topics'] ?? []), true)));
update_source_db(fn($db) => $db);
$after = count(array_filter(load_source_db()['tiktok_accounts'], fn($e) => in_array($test_topic, (array)($e['topics'] ?? []), true)));
$check('каталог: tiktok_accounts не теряется при записи', $before === 1 && $after === 1);
update_source_db(fn($db) => [...$db, 'tiktok_accounts' => array_values(array_filter($db['tiktok_accounts'], fn($e) => ($e['id'] ?? '') !== 'tiktoktest'))]);

// ---- Часть B: TikTok — сеть ----
echo "B. TikTok, [сеть]\n";
try {
    $posts = tiktok_fetch_posts('tiktok', 3, 'тест');
    $check('[сеть] tiktok: до 3 постов с обложкой в кэше медиа', count($posts) >= 1 && count($posts) <= 3);
    $ok_cached = true;
    foreach ($posts as $p) {
        $path = media_cache_path($p['image']);
        if ($path === null || !is_file($path)) $ok_cached = false; else $cached_files[] = $path;
        if (($p['video_via'] ?? '') !== 'ytdlp' || $p['video'] !== '' || !str_starts_with((string)$p['video_page'], 'https://www.tiktok.com/@tiktok/video/')) $ok_cached = false;
    }
    $check('[сеть] tiktok: обложка лежит в кэше, видео — через yt-dlp', $posts && $ok_cached);
    foreach ($posts as $p) echo '    ', date('c', $p['posted_at']), ' | ▶', $p['views'], ' | ', mb_substr($p['text'], 0, 50), "\n";
} catch (Throwable $e) {
    $check('[сеть] tiktok: ' . $e->getMessage(), false);
}

try {
    $added = source_add_manual('https://www.tiktok.com/@tiktok', $test_topic);
    $check('[сеть] «Добавить источник»: TikTok-аккаунт в каталоге темы', $added['platform'] === PLATFORM_TIKTOK && $added['posts'] > 0);
    $collected = collect_posts_by_platform(load_source_db(), $test_topic)[PLATFORM_TIKTOK] ?? [];
    $covers = true;
    foreach ($collected as $p) { $path = media_cache_path($p['image']); if ($path === null || !is_file($path)) $covers = false; else $cached_files[] = $path; }
    $check('[сеть] сбор по теме: посты TikTok с обложками в кэше медиа', count($collected) > 0 && $covers);
} catch (Throwable $e) {
    $check('[сеть] добавление/сбор TikTok: ' . $e->getMessage(), false);
}

// ---- Часть C: ролик TikTok — фоновой задачей, после попадания в пул ----
echo "C. Ролик через очередь\n";
try {
    app_user_bind($test_user);
    $item = (tiktok_fetch_posts('tiktok', 1, 'тест'))[0] ?? null;
    if (!$item) throw new RuntimeException('у аккаунта нет подходящих роликов');
    $item += ['topic' => $test_topic, 'relevance' => 80, 'rating' => null, 'description' => 'тест', 'why_relevant' => '', 'tags' => [], 'format' => '', 'image_local' => $item['image']];
    $check('запись TikTok добавлена в пул без видео', add_item_to_pool_safely($item) === true && count(pool_load($test_user)) === 1);
    $queued = array_filter(ytdlp_queue_update(fn($jobs) => $jobs), fn($j) => $j['uid'] === $test_user);
    $check('ролик поставлен в очередь (одна задача)', count($queued) === 1);
    $wait_queue($test_user);   // постановка уже запустила фоновый воркер
    $pool = pool_load($test_user);
    $video = (string)($pool[0]['video'] ?? '');
    $path = media_cache_path($video);
    if ($path !== null && is_file($path)) $cached_files[] = $path;
    $check('[сеть] воркер: у записи video — файл в кэше', $path !== null && is_file($path) && ($pool[0]['video_local'] ?? '') === $video);
    if ($path !== null && is_file($path)) {
        $check('ролик — H.264 (ffprobe)', ytdlp_codec($path) === 'h264');
        $check('ролик не больше предела публикации в Telegram', filesize($path) <= TELEGRAM_UPLOAD_MAX_BYTES);
        echo '    ролик: ', round(filesize($path) / 1048576, 1), " МБ\n";
    }
    $again = ytdlp_exec([PHP_BINARY, ROOT . '/tools/ytdlp_worker.php'], 60);   // очередь пуста: запуск ничего не скачивает
    $check('повторный запуск воркера — ничего не делает', pool_load($test_user)[0]['video'] === $video && !array_filter(ytdlp_queue_update(fn($jobs) => $jobs), fn($j) => $j['uid'] === $test_user));
} catch (Throwable $e) {
    $check('[сеть] очередь роликов: ' . $e->getMessage(), false);
}

// ---- Часть D: YouTube Shorts — лента шортсов и ролик из очереди ----
echo "D. YouTube Shorts\n";
try {
    $src = source_parse_link('https://www.youtube.com/@MrBeast/shorts');
    $check('[сеть] ссылка на шорты канала → лента шортсов (RSS, метка YouTube Shorts)', $src['platform'] === PLATFORM_RSS && str_contains($src['id'], 'playlist_id=UUSH') && rss_feed_kind($src['id']) === 'YouTube Shorts');
    $shorts = rss_fetch_posts($src['id'], 3, 'тест-шорты');
    $check('[сеть] лента шортсов: записи с картинкой и video_via = ytdlp', count($shorts) >= 1 && array_reduce($shorts, fn($ok, $p) => $ok && ($p['video_via'] ?? '') === 'ytdlp' && $p['video'] === '' && str_starts_with((string)$p['video_page'], 'https://www.youtube.com/shorts/'), true));
    $check('в тексте шорта нет ссылки на ролик', array_reduce($shorts, fn($ok, $p) => $ok && !str_contains($p['text'], 'watch?v='), true));
    $check('обычный канал (без /shorts) — как раньше, лента каналов', rss_known_feed('https://www.youtube.com/channel/UCBR8-60-B28hp2BmDPdntcQ') === 'https://www.youtube.com/feeds/videos.xml?channel_id=UCBR8-60-B28hp2BmDPdntcQ');
    if ($shorts) {
        app_user_bind($test_user);
        $short = $shorts[0] + ['topic' => $test_topic, 'relevance' => 80, 'rating' => null, 'description' => 'тест', 'why_relevant' => '', 'tags' => [], 'format' => '', 'image_local' => $shorts[0]['image']];
        add_item_to_pool_safely($short);
        $wait_queue($test_user);
        $pool = pool_load($test_user);
        $video = (string)($pool[count($pool) - 1]['video'] ?? '');
        $path = media_cache_path($video);
        if ($path !== null && is_file($path)) $cached_files[] = $path;
        $check('[сеть] шорт: ролик в кэше, H.264', $path !== null && is_file($path) && ytdlp_codec($path) === 'h264');
        if ($path !== null && is_file($path)) {
            $dims = ytdlp_exec(['ffprobe', '-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=width,height', '-of', 'csv=p=0', $path], 20);
            [$w, $h] = array_map('intval', explode(',', trim($dims['stdout'])) + [0, 0]);
            $check('шорт — вертикальное видео', $h > $w && $w > 0);
            echo "    шорт: {$w}x{$h}, ", round(filesize($path) / 1048576, 1), " МБ\n";
        }
    }
} catch (Throwable $e) {
    $check('[сеть] шорты: ' . $e->getMessage(), false);
}

// ---- Часть E: X (Twitter) — твит по ссылке в «Свой контент» ----
echo "E. X (Twitter)\n";
$check('ссылка на твит (x.com) → id', x_status_id('https://x.com/NASA/status/1585341984679469056') === '1585341984679469056');
$check('twitter.com и mobile.twitter.com → id', x_status_id('https://twitter.com/a_b/status/123456') === '123456' && x_status_id('https://mobile.twitter.com/a/status/99') === '99');
$check('ссылка на профиль → null', x_status_id('https://x.com/NASA') === null);
$check('/status/abc → null', x_status_id('https://x.com/NASA/status/abc') === null);
$check('чужой хост → null', x_status_id('https://example.com/a/status/123') === null);
try {
    $tweet = x_fetch_post('1585341984679469056');
    $check('[сеть] твит: текст и медиа', $tweet['text'] !== '' && ($tweet['images'] || $tweet['has_video']));
    app_user_bind($test_user);
    $before = count(pool_load($test_user));
    $result = x_import_links(['https://x.com/NASA/status/1585341984679469056', 'https://x.com/NASA', 'https://x.com/NASA/status/abc']);
    $check('[сеть] импорт твита: в пуле появилась запись', $result['added'] === 1 && count(pool_load($test_user)) === $before + 1);
    $check('ошибки по ссылкам — с текстом, остальные — добавлены', count($result['errors']) === 2 && $result['errors'][0][0] === 'https://x.com/NASA' && $result['errors'][1][0] === 'https://x.com/NASA/status/abc');
    $added = pool_load($test_user)[0];
    $local = (string)($added['image_local'] ?? '');
    $path = media_cache_path($local);
    if ($path !== null && is_file($path)) $cached_files[] = $path;
    $check('[сеть] картинка твита — в кэше медиа', $path !== null && is_file($path) && ($added['source_url'] ?? '') === 'https://x.com/NASA/status/1585341984679469056');
    foreach ((array)($added['gallery'] ?? []) as $g) { $gp = media_cache_path((string)($g['image_local'] ?? '')); if ($gp && is_file($gp)) $cached_files[] = $gp; }
} catch (Throwable $e) {
    $check('[сеть] X: ' . $e->getMessage(), false);
}

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
