<?php
// Календарь v4: охват канала по часам публикации (медиана просмотров по t.me/s/<канал>), прогноз просмотров поста
// и поиск повторов в уже опубликованном. Чистые функции — без сети и базы (их проверяет tools/test_calendar_v4.php).

const REACH_DAYS = 30;                  // глубина выборки постов канала
const REACH_MIN_POSTS = 10;             // меньше постов с просмотрами — охвата нет
const REACH_TREND_POSTS = 30;           // от стольких постов — поправка прогноза на день недели
const REACH_PAGES = 3;                  // страниц t.me/s/ за одно обновление
const REACH_CACHE_SECONDS = 43200;      // 12 часов
const REACH_JOB_SECONDS = 900;          // фоновое обновление не чаще раза в 15 минут на канал
const HISTORY_DAYS = 7;                 // прошлое календаря: дальше недели вид не листается (js/calendar.js, clampStart)
const HISTORY_PAGES = 12;                // страниц t.me/s/ за одно обновление истории
const HISTORY_CACHE_SECONDS = 900;      // история канала в кэше 15 минут
const REPEAT_DAYS = 60;                 // глубина поиска повторов в опубликованном
const REPEAT_TEXT_MIN = 20;             // короче — текст для сравнения не годится

/** Посты страницы t.me/s/: [id, at (unix), views|null]. */
function channel_reach_posts_from_html(string $html): array {
    $out = [];
    foreach (telegram_page_blocks($html) as [$id, $content]) {
        $views = preg_match('~tgme_widget_message_views">([^<]*)~', $content, $m) ? telegram_parse_counter($m[1]) : null;
        $out[] = ['id' => (int)$id, 'at' => telegram_post_time($content), 'views' => $views];
    }
    return $out;
}

/** Медиана целых чисел (0 при пустом списке). */
function channel_median(array $values): int {
    if (!$values) return 0;
    sort($values);
    $n = count($values);
    return (int)(($n % 2) ? $values[intdiv($n, 2)] : round(($values[$n / 2 - 1] + $values[$n / 2]) / 2));
}

/**
 * Охват по часам из постов [{at, views}] в поясе $tz. Null — постов с просмотрами меньше REACH_MIN_POSTS.
 * hours: час => медиана просмотров; best: три лучших часа; days: день недели (1–7) => медиана; overall: медиана всех.
 */
function channel_reach_summary(array $posts, string $tz, int $at): ?array {
    $byHour = $byDay = $all = [];
    foreach ($posts as $p) {
        if (!is_int($p['views'] ?? null)) continue;   // без просмотров не считаем
        $d = (new DateTime('@' . (int)$p['at']))->setTimezone(new DateTimeZone($tz));
        $byHour[(int)$d->format('G')][] = $p['views'];
        $byDay[(int)$d->format('N')][] = $p['views'];
        $all[] = $p['views'];
    }
    if (count($all) < REACH_MIN_POSTS) return null;
    $hours = array_map('channel_median', $byHour);
    ksort($hours);
    $best = array_keys(array_filter($hours, fn($m) => $m > 0));
    usort($best, fn($a, $b) => $hours[$b] <=> $hours[$a] ?: $a <=> $b);
    return ['hours' => $hours, 'best' => array_slice($best, 0, 3), 'days' => array_map('channel_median', $byDay),
        'overall' => channel_median($all), 'posts' => count($all), 'at' => $at];
}

/**
 * Прогноз просмотров поста: медиана его часа; при ≥ REACH_TREND_POSTS постах — поправка на день недели.
 * Null — нет охвата или час без данных.
 */
function channel_forecast(?array $reach, int $at, string $tz): ?int {
    if (!$reach) return null;
    $d = (new DateTime('@' . $at))->setTimezone(new DateTimeZone($tz));
    $hour = $reach['hours'][(int)$d->format('G')] ?? null;
    if ($hour === null) return null;
    $factor = 1.0;
    $day = $reach['days'][(int)$d->format('N')] ?? null;
    if ($reach['posts'] >= REACH_TREND_POSTS && $day !== null && $reach['overall'] > 0) $factor = $day / $reach['overall'];
    return (int)round($hour * $factor);
}

/** Посты канала за REACH_DAYS: до REACH_PAGES страниц t.me/s/, от новых к старым. */
function channel_reach_fetch(string $name): array {
    $posts = [];
    $before = null;
    $cut = time() - REACH_DAYS * DAY_SECONDS;
    for ($page = 0; $page < REACH_PAGES; $page++) {
        $html = fetch_telegram_channel_page($name, $before);
        if (!$html) break;
        $batch = channel_reach_posts_from_html($html);
        if (!$batch) break;
        array_push($posts, ...array_filter($batch, fn($p) => $p['at'] >= $cut));
        $before = min(array_column($batch, 'id'));
        if (min(array_column($batch, 'at')) < $cut) break;
    }
    return $posts;
}

/** Охват канала публичного вида: считает по сети и кладёт в кэш на 12 часов. Null — канал приватный или постов мало. */
function channel_reach_by_hour(array $channel): ?array {
    $name = ltrim((string)($channel['username'] ?? ''), '@');
    if (!is_valid_telegram_channel($name)) return null;
    $tz = channel_schedule($channel)['tz'];
    $reach = channel_reach_summary(channel_reach_fetch($name), $tz, time());
    kv_update(channel_reach_key($name, $tz), fn() => ['at' => time(), 'data' => $reach]);
    return $reach;
}

function channel_reach_key(string $name, string $tz): string { return 'reach:' . $name . ':' . $tz; }

/**
 * Охват для календаря без ожидания сети: из кэша; нет кэша — null и фоновое обновление (календарь перечитает).
 * Устаревший кэш отдаётся, пока считается новый.
 */
function channel_reach_cached(array $channel): ?array {
    $name = ltrim((string)($channel['username'] ?? ''), '@');
    if (!is_valid_telegram_channel($name)) return null;
    $key = channel_reach_key($name, channel_schedule($channel)['tz']);
    $cached = kv_get($key);
    if (is_array($cached) && time() - (int)($cached['at'] ?? 0) < REACH_CACHE_SECONDS) return $cached['data'];
    channel_reach_background($name, channel_schedule($channel)['tz']);
    return is_array($cached) ? $cached['data'] : null;
}

/** Запускает обновление охвата отдельным процессом CLI, не чаще раза в REACH_JOB_SECONDS на канал. */
function channel_reach_background(string $name, string $tz): void {
    $job = 'reach_job:' . $name . ':' . $tz;
    $started = false;
    kv_update($job, function ($at) use (&$started) {
        if (time() - (int)$at < REACH_JOB_SECONDS && (int)$at > 0) return null;
        $started = true;
        return time();
    }, 0);
    if ($started) exec('/usr/bin/php ' . escapeshellarg(ROOT . '/tools/cli.php') . ' channel-reach ' . escapeshellarg($name) . ' ' . escapeshellarg($tz) . ' >/dev/null 2>&1 &');
}

/** Текст для сравнения повторов: без ссылок, знаков и регистра; короче REPEAT_TEXT_MIN — пусто. */
function calendar_text_key(string $text): string {
    $t = mb_strtolower($text);
    $t = trim((string)preg_replace(['~https?://\S+~u', '~[^\p{L}\p{N}\s]+~u', '~\s+~u'], [' ', ' ', ' '], $t));
    return mb_strlen($t) >= REPEAT_TEXT_MIN ? 'text:' . sha1($t) : '';
}

/** Опубликованные посты пользователя за REPEAT_DAYS: [{at, url, channel, text, media_src}]. */
function calendar_published_recent($uid): array {
    $rows = db_all('SELECT at, data FROM published WHERE user_id = ? AND at >= ? ORDER BY at DESC LIMIT 500', [$uid, time() - REPEAT_DAYS * DAY_SECONDS]);
    return array_values(array_map(fn($r) => ['at' => (int)$r['at']] + (array)json_decode((string)$r['data'], true), $rows));
}

/** Ключи картинки поста для сравнения: адрес источника и (если это кэш сайта) перцептивный хэш. */
function calendar_image_hash(string $src): ?string {
    $path = media_cache_path($src);
    return $path !== null && is_file($path) ? image_phash((string)file_get_contents($path)) : null;
}

/**
 * Повтор: в этом канале за REPEAT_DAYS уже выходил пост с тем же текстом, тем же адресом картинки или близкой картинкой
 * (хэш как у дедупликации парсера). Возвращает {at, url} прошлой публикации или null.
 */
function calendar_duplicate_of(array $item, string $channel_id, array $published): ?array {
    $text = calendar_text_key((string)($item['text'] ?? $item['full_text'] ?? ''));
    $image = (string)($item['image'] ?? $item['display_image'] ?? '');
    $hash = $image !== '' ? calendar_image_hash($image) : null;
    foreach ($published as $p) {
        if ((string)($p['channel'] ?? '') !== $channel_id) continue;
        $hit = ['at' => (int)($p['at'] ?? 0), 'url' => (string)($p['url'] ?? '')];
        if ($text !== '' && $text === calendar_text_key((string)($p['text'] ?? ''))) return $hit;
        foreach ((array)($p['media_src'] ?? []) as $src) {
            if ($image !== '' && $src === $image) return $hit;
            if ($hash !== null && ($other = calendar_image_hash((string)$src)) !== null && phash_distance($hash, $other) <= PHASH_SAME_DISTANCE) return $hit;
        }
    }
    return null;
}

/** Посты канала за HISTORY_DAYS по страницам t.me/s/ (от новых к старым): [{id, at, text, kind, media, thumb, url}]. Только публичные каналы. */
function channel_history_fetch(string $name): array {
    $posts = [];
    $before = null;
    $cut = time() - HISTORY_DAYS * DAY_SECONDS;
    for ($page = 0; $page < HISTORY_PAGES; $page++) {
        $html = fetch_telegram_channel_page($name, $before);
        if (!$html) break;
        $blocks = telegram_page_blocks($html);
        if (!$blocks) break;
        foreach (array_reverse($blocks) as [$id, $content]) {
            $at = telegram_post_time($content);
            if ($at < $cut) continue;
            $media = telegram_post_media($content);
            $text = preg_match('~<div class="tgme_widget_message_text[^"]*"[^>]*>(.*?)</div>~si', $content, $m) ? clean_html_text($m[1]) : '';
            $posts[] = ['id' => (int)$id, 'at' => $at, 'text' => mb_substr($text, 0, 600), 'media' => count($media), 'thumb' => (string)($media[0]['image'] ?? ''),
                'kind' => array_filter(array_column($media, 'video')) ? 'video' : ($media ? 'photo' : ''), 'url' => 'https://t.me/' . rawurlencode($name) . '/' . $id];
        }
        $ids = array_map(fn($b) => (int)$b[0], $blocks);
        $times = array_filter(array_map(fn($b) => telegram_post_time($b[1]), $blocks));
        if (!$times || min($times) < $cut) break;
        $before = min($ids);
    }
    return $posts;
}

/** История одного канала для календаря: из кэша; устарела или нет — фоновое обновление (процесс CLI), старое отдаётся, пока считается новое. */
function channel_history_cached(string $name, bool &$pending): array {
    $key = 'history:' . $name;
    $cached = kv_get($key);
    if (is_array($cached) && time() - (int)($cached['at'] ?? 0) < HISTORY_CACHE_SECONDS) return $cached['posts'];
    $job = 'history_job:' . $name;
    $started = false;
    kv_update($job, function ($at) use (&$started) {
        if (time() - (int)$at < 120 && (int)$at > 0) return null;
        $started = true;
        return time();
    }, 0);
    if ($started) exec('/usr/bin/php ' . escapeshellarg(ROOT . '/tools/cli.php') . ' channel-history ' . escapeshellarg($name) . ' >/dev/null 2>&1 &');
    if (!is_array($cached)) $pending = true;
    return is_array($cached) ? $cached['posts'] : [];
}

/** Фоновая задача: загрузить историю канала и положить в кэш. */
function channel_history_refresh(string $name): int {
    $posts = channel_history_fetch($name);
    kv_update('history:' . $name, fn() => ['at' => time(), 'posts' => $posts]);
    return count($posts);
}

/**
 * Посты, которые вышли в канале не через Posta (вручную, раньше, из других программ), в виде отправленных постов календаря.
 * $sent — журнал отправленного: свои посты совпадают по ссылке или по времени (±2 минуты) в том же канале и повторно не показываются.
 */
function calendar_external_posts(array $channels, array $sent, bool &$pending): array {
    $out = [];
    foreach ($channels as $c) {
        $name = ltrim((string)($c['username'] ?? ''), '@');
        if (!is_valid_telegram_channel($name)) continue;
        $mine = array_values(array_filter($sent, fn($e) => ($e['channel'] ?? '') === $c['id']));
        $urls = array_flip(array_filter(array_column($mine, 'url')));
        $times = array_map(fn($e) => (int)(($e['scheduled_for'] ?? 0) ?: ($e['at'] ?? 0)), $mine);
        foreach (channel_history_cached($name, $pending) as $p) {
            if (isset($urls[$p['url']]) || array_filter($times, fn($t) => abs($t - $p['at']) <= 120)) continue;
            $out[] = ['id' => '', 'at' => $p['at'], 'status' => 'sent', 'text' => mb_substr($p['text'], 0, 120), 'full_text' => $p['text'], 'media' => $p['media'], 'kind' => $p['kind'], 'duration' => 0,
                'poll' => false, 'url' => $p['url'], 'channel' => $c['id'], 'channel_title' => $c['title'] ?? '', 'error' => '', 'external' => true,
                'thumb' => $p['thumb'] !== '', 'thumb_url' => $p['thumb']];
        }
    }
    return $out;
}
