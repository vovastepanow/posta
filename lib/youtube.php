<?php
// YouTube без лент RSS: ролики и шортсы канала через yt-dlp. Лента videos.xml/UUSH иногда отдаёт 404 — тогда список берётся отсюда.
// Заголовки — на русском (youtube:lang=ru), даты — отдельным вызовом с approximate_date: с языком даты на вкладке /videos пропадают.

const YOUTUBE_LIST_COUNT = 15;            // роликов на канал и вкладку, которые разбираются
const YOUTUBE_LIST_CACHE_SECONDS = 1800;  // ответ по каналу и вкладке в kv на 30 минут
const YOUTUBE_SHORTS_DATES = 1;           // даты шортсов — у первого (три запроса дали 36 с на канал); у остальных — не новее предыдущего

/** Посты канала (вкладка videos или shorts) в формате rss_parse. Кэш в kv на 30 минут; ошибка yt-dlp — исключение. */
function youtube_channel_posts(string $channel_id, string $tab, int $count): array {
    if (!preg_match('~^UC[A-Za-z0-9_-]{22}$~', $channel_id) || !in_array($tab, ['videos', 'shorts'], true)) throw new RuntimeException('Нужен канал YouTube и вкладка videos или shorts.');
    $key = "youtube_posts:$channel_id:$tab";
    $cached = kv_get($key);
    if (!is_array($cached) || time() - (int)($cached['at'] ?? 0) >= YOUTUBE_LIST_CACHE_SECONDS) {
        $cached = ['at' => time(), 'posts' => youtube_list_posts($channel_id, $tab)];
        kv_update($key, fn() => $cached);
    }
    return array_slice($cached['posts'], 0, max(0, $count));
}

/** Лента YouTube → канал и вкладка для запасного пути: videos.xml?channel_id=… и playlist_id=UUSH… (шортсы). Иначе null. */
function youtube_feed_channel(string $feed_url): ?array {
    if (preg_match('~channel_id=(UC[A-Za-z0-9_-]{22})~', $feed_url, $m)) return [$m[1], 'videos'];
    if (preg_match('~playlist_id=UUSH([A-Za-z0-9_-]{22})~', $feed_url, $m)) return ['UC' . $m[1], 'shorts'];
    return null;
}

/** Список вкладки: заголовки и просмотры — одним вызовом (русский язык), даты — вторым; склеивание по id. */
function youtube_list_posts(string $channel_id, string $tab): array {
    $url = ytdlp_check_url("https://www.youtube.com/channel/$channel_id/$tab");
    $count = (string)YOUTUBE_LIST_COUNT;
    $rows = youtube_list_rows(['--flat-playlist', '--playlist-end', $count, '--extractor-args', 'youtube:lang=ru',
        '--print', '%(id)s|%(view_count)s|%(thumbnails.-1.url)s|%(title)s', '--', $url], 4);
    $dates = $tab === 'videos' ? youtube_video_dates($url, $count) : youtube_shorts_dates(array_column($rows, 0));
    $posts = [];
    $prev = 0;
    foreach ($rows as [$id, $views, $thumb, $title]) {
        if (!preg_match('~^[A-Za-z0-9_-]{11}$~', $id)) continue;
        $at = $dates[$id] ?? $prev;   // без даты — не новее предыдущего ролика (вкладка идёт от новых к старым)
        $prev = $at;
        $posts[] = youtube_post($id, $title, $thumb, $at, (int)(is_numeric($views) ? $views : 0), $tab === 'shorts');
    }
    return $posts;
}

/** Даты роликов вкладки /videos (приблизительные — до дня): [id => время]. */
function youtube_video_dates(string $url, string $count): array {
    $rows = youtube_list_rows(['--flat-playlist', '--playlist-end', $count, '--extractor-args', 'youtubetab:approximate_date',
        '--print', '%(id)s|%(timestamp)s', '--', $url], 2);
    return youtube_dates_map($rows);
}

/** Даты первых шортсов: отдельные страницы роликов одним запуском; на вкладке шортсов дат нет. */
function youtube_shorts_dates(array $ids): array {
    $ids = array_slice(array_values(array_filter($ids, fn($id) => preg_match('~^[A-Za-z0-9_-]{11}$~', (string)$id))), 0, YOUTUBE_SHORTS_DATES);
    if (!$ids) return [];
    $urls = array_map(fn($id) => ytdlp_check_url("https://www.youtube.com/shorts/$id"), $ids);
    return youtube_dates_map(youtube_list_rows(['--print', '%(id)s|%(timestamp)s', '--', ...$urls], 2));
}

/** [id => время] из строк «id|timestamp»; NA — пропуск. */
function youtube_dates_map(array $rows): array {
    $dates = [];
    foreach ($rows as [$id, $ts]) if (is_numeric($ts)) $dates[$id] = (int)$ts;
    return $dates;
}

/** Строки вывода yt-dlp, поля через «|»; последнее поле может содержать «|». Ошибка без строк — исключение. */
function youtube_list_rows(array $args, int $fields): array {
    $result = ytdlp_run($args, YTDLP_LIST_TIMEOUT);
    if (!$result['ok'] && trim($result['stdout']) === '') throw new RuntimeException('YouTube не отдал список: ' . ytdlp_error_text($result));
    $rows = [];
    foreach (preg_split('/\R/', $result['stdout']) as $line) {
        if (trim($line) !== '') $rows[] = array_pad(explode('|', trim($line), $fields), $fields, '');
    }
    return $rows;
}

/** Запись поста: как в rss_parse; у шортса и обычного ролика — ссылка на видео для скачивания через yt-dlp. */
function youtube_post(string $id, string $title, string $thumb, int $at, int $views, bool $short): array {
    $page = $short ? "https://www.youtube.com/shorts/$id" : "https://www.youtube.com/watch?v=$id";
    $image = str_starts_with($thumb, 'https://') ? $thumb : "https://i.ytimg.com/vi/$id/hqdefault.jpg";
    $text = mb_substr(trim($title . ($short ? '' : "\n$page")), 0, 1000);
    $extra = ['video_via' => 'ytdlp', 'video_page' => $page];
    return ['image' => $image, 'images' => [$image], 'gallery' => [], 'video' => '', 'text' => $text !== '' ? $text : EMPTY_POST_TEXT,
        'source' => source_display_name(PLATFORM_RSS, 'youtube.com'), 'post_id' => substr(sha1($id), 0, 12), 'posted_at' => $at, 'url' => $page,
        'video_unavailable' => false, 'engagement' => null, 'views' => $views] + $extra;
}
