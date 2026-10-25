<?php
// X (Twitter): отдельные твиты по ссылке → «Свой контент». Ленту аккаунта без входа X не отдаёт, поэтому только твиты.

const X_LINKS_MAX = 20;          // ссылок за один импорт
const X_LINKS_PER_DAY = 100;     // в сутки на пользователя
const X_TWEET_API = 'https://cdn.syndication.twimg.com/tweet-result?id=%s&token=x';   // токен — заглушка, адрес работает без него

/** Id твита из ссылки x.com, twitter.com, mobile.twitter.com; иначе null. */
function x_status_id(string $url): ?string {
    return preg_match('~^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/[A-Za-z0-9_]{1,15}/status(?:es)?/(\d{1,20})(?:[/?#]|$)~i', trim($url), $m) ? $m[1] : null;
}

/** Самый битрейтный mp4 с video.twimg.com; '' — такого нет. */
function x_best_mp4(array $media): string {
    $best = '';
    $rate = -1;
    foreach ((array)($media['video_info']['variants'] ?? []) as $v) {
        $url = (string)($v['url'] ?? '');
        if (($v['content_type'] ?? '') !== 'video/mp4' || !str_starts_with($url, 'https://video.twimg.com/')) continue;
        if ((int)($v['bitrate'] ?? 0) > $rate) { $best = $url; $rate = (int)($v['bitrate'] ?? 0); }
    }
    return $best;
}

/**
 * Твит через syndication: текст, дата, картинки (pbs.twimg.com), видео. Видео без адреса mp4 — video_via = ytdlp (yt-dlp читает отдельные твиты).
 * Твит без картинки и видео — ошибка.
 */
function x_fetch_post(string $id): array {
    if (!preg_match('/^\d{1,20}$/', $id)) throw new RuntimeException('Не похоже на ссылку на твит.');
    $r = fetch_public(sprintf(X_TWEET_API, $id), 2 * 1024 * 1024);
    $data = $r['ok'] ? json_decode((string)$r['bytes'], true) : null;
    if (!is_array($data)) throw new RuntimeException('Твит не найден, закрыт или X временно не отвечает.');
    $images = [];
    $video = null;
    foreach ((array)($data['mediaDetails'] ?? []) as $m) {
        $type = (string)($m['type'] ?? '');
        if ($type === 'photo' && str_starts_with((string)($m['media_url_https'] ?? ''), 'https://pbs.twimg.com/')) $images[] = $m['media_url_https'];
        elseif (in_array($type, ['video', 'animated_gif'], true)) $video ??= $m;
    }
    $poster = (string)($video['media_url_https'] ?? '');
    if (!str_starts_with($poster, 'https://pbs.twimg.com/')) $poster = '';
    $has_video = $video !== null || isset($data['video']);
    if (!$images && $poster === '' && !$has_video) throw new RuntimeException('В посте нет картинки или видео.');
    return ['text' => trim((string)($data['text'] ?? '')), 'posted_at' => (int)strtotime((string)($data['created_at'] ?? '')),
        'images' => $images ?: ($poster !== '' ? [$poster] : []), 'video_url' => $video ? x_best_mp4($video) : '', 'has_video' => $has_video];
}

/** Ролик с video.twimg.com в кэш сайта. Слишком большой или недоступный — ''. */
function x_cache_video(string $url): string {
    $file = tempnam(sys_get_temp_dir(), 'x-video-');
    if ($file === false) return '';
    try {
        return fetch_public($url, VIDEO_MAX_BYTES, $file)['ok'] ? cache_downloaded_video($url, $file) : '';
    } finally {
        @unlink($file);
    }
}

/** Запись «Своего контента» из твита. Видео, которое не скачалось напрямую, ставится в очередь yt-dlp (video_via). */
function x_post_item(string $link, array $tweet): array {
    $media = array_map(fn($u) => own_store_image(fetch_public_image($u)), array_slice($tweet['images'], 0, OWN_MAX_FILES));
    $item = own_post_item($media, $tweet['text'], 'X', $tweet['posted_at'] ?: null);
    $item['source_url'] = $link;   // у own_post_item ключи пустые: присваиваем, а не «+» (слева побеждает пустое)
    $item['url'] = $link;
    if ($tweet['video_url'] !== '' && ($local = x_cache_video($tweet['video_url'])) !== '') {
        $item['video'] = $local;
        $item['video_local'] = $local;
    } elseif ($tweet['has_video']) {
        $item['video_via'] = 'ytdlp';
        $item['video_page'] = $link;
    }
    return $item;
}

/** Лимит ссылок в сутки на пользователя. */
function x_quota_take($uid, int $links): void {
    $key = 'x_links:' . $uid . ':' . date('Y-m-d');
    if ((int)kv_get($key, 0) + $links > X_LINKS_PER_DAY) throw new RuntimeException('За сутки — не больше ' . X_LINKS_PER_DAY . ' ссылок на твиты: продолжите завтра.');
    kv_update($key, fn($n) => (int)$n + $links, 0);
}

/** Импорт твитов по ссылкам: ['added', 'skipped', 'errors' => [[ссылка, текст]]]. Ошибка одной ссылки не останавливает остальные. */
function x_import_links(array $links): array {
    $uid = app_user_id();
    $links = array_values(array_filter(array_map(fn($l) => trim((string)$l), $links), fn($l) => $l !== ''));
    if (!$links) throw new RuntimeException('Вставьте ссылки на твиты, по одной в строке.');
    if (count($links) > X_LINKS_MAX) throw new RuntimeException('За раз — не больше ' . X_LINKS_MAX . ' ссылок на твиты.');
    x_quota_take($uid, count($links));
    $items = [];
    $errors = [];
    foreach ($links as $link) {
        try {
            $id = x_status_id($link) ?? throw new RuntimeException('не похоже на ссылку на твит: x.com/имя/status/номер');
            $item = x_post_item($link, x_fetch_post($id));
            if (ua_item_blocked($item) !== null) throw new RuntimeException('украинский твит — в Posta не принимаем');   // UA_FILTER_PROMPT.md, B5
            $items[] = $item;
        } catch (RuntimeException $e) {
            $errors[] = [$link, $e->getMessage()];
        }
    }
    $added = own_add_to_pool($items);   // ролики ставятся в очередь внутри own_add_to_pool
    return ['added' => $added, 'skipped' => count($links) - $added, 'errors' => $errors];
}
