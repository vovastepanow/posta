<?php
// TikTok: короткие видео аккаунта читаются через yt-dlp (lib/ytdlp.php). Обложки кэшируются сразу: адреса протухают.

const TIKTOK_POSTS_PER_ACCOUNT = 12;
const TIKTOK_MAX_SECONDS = 180;           // длиннее — пропускаем: это не короткое видео
const TIKTOK_NEED_ACCOUNT = 'Нужна ссылка на аккаунт TikTok: tiktok.com/@имя';

function is_valid_tiktok_user($user) { return preg_match('/^[A-Za-z0-9_.]{2,24}$/', (string)$user) === 1; }

/** Имя аккаунта из ссылки или «@имя»: tiktok.com/@имя, m.tiktok.com, ссылка на видео автора, короткие vm./vt. (через редирект). */
function tiktok_user_from_link(string $input): string {
    $input = trim($input);
    if (preg_match('~^@([A-Za-z0-9_.]{2,24})$~', $input, $m)) return $m[1];
    $p = parse_url($input);
    if (!is_array($p) || ($p['scheme'] ?? '') !== 'https') throw new RuntimeException(TIKTOK_NEED_ACCOUNT);
    $host = strtolower($p['host'] ?? '');
    if (in_array($host, ['vm.tiktok.com', 'vt.tiktok.com'], true)) {
        $location = fetch_public($input, 4096)['location'] ?? '';
        if (!str_starts_with($location, 'https://')) throw new RuntimeException(TIKTOK_NEED_ACCOUNT);
        return tiktok_user_from_link($location);
    }
    if (!in_array($host, ['tiktok.com', 'www.tiktok.com', 'm.tiktok.com'], true)) throw new RuntimeException(TIKTOK_NEED_ACCOUNT);
    if (!preg_match('~^/@([A-Za-z0-9_.]{2,24})(?:/|$)~', (string)($p['path'] ?? ''), $m)) throw new RuntimeException(TIKTOK_NEED_ACCOUNT);
    return $m[1];
}

/** Самая большая обложка с https-адресом; у записей без размеров — последняя. */
function tiktok_best_thumbnail(array $thumbnails): string {
    $best = '';
    $best_size = -1;
    foreach ($thumbnails as $t) {
        $url = (string)($t['url'] ?? '');
        if (!str_starts_with($url, 'https://')) continue;
        $size = (int)($t['width'] ?? 0) * (int)($t['height'] ?? 0);
        if ($size >= $best_size) { $best = $url; $best_size = $size; }
    }
    return $best;
}

/** Обложку — в кэш медиа сразу (адрес на CDN протухает). Не скачалась — ''. Файл защищён от очистки до конца этого запуска парсера. */
function tiktok_cache_cover(string $url): string {
    try {
        $cached = media_cache_store_image($url, fetch_public_image($url));
        if ($cached !== '') $GLOBALS['media_cache_protect'][basename($cached)] = true;
        return $cached;
    } catch (Throwable $e) { return ''; }
}

/** Запись ленты yt-dlp → пост; null — пропустить (длинное видео, нет id или обложки). */
function tiktok_post_from_item(string $user, array $item, string $source): ?array {
    $id = (string)($item['id'] ?? '');
    if (!preg_match('/^[0-9]{5,25}$/', $id) || (int)($item['duration'] ?? 0) > TIKTOK_MAX_SECONDS) return null;
    $cover = tiktok_cache_cover(tiktok_best_thumbnail((array)($item['thumbnails'] ?? [])));
    if ($cover === '') return null;
    $page = "https://www.tiktok.com/@$user/video/$id";
    $text = trim((string)($item['description'] ?? '')) ?: trim((string)($item['title'] ?? ''));
    return ['image' => $cover, 'images' => [$cover], 'gallery' => [], 'video' => '', 'video_unavailable' => false, 'video_via' => 'ytdlp', 'video_page' => $page,
        'engagement' => null, 'text' => $text !== '' ? mb_substr($text, 0, 1000) : EMPTY_POST_TEXT, 'source' => $source, 'post_id' => $id,
        'posted_at' => (int)($item['timestamp'] ?? 0), 'url' => $page, 'views' => (int)($item['view_count'] ?? 0), 'likes' => (int)($item['like_count'] ?? 0),
        'comments' => (int)($item['comment_count'] ?? 0), 'reshares' => (int)($item['repost_count'] ?? 0), 'duration' => (int)($item['duration'] ?? 0)];
}

/** Последние короткие видео аккаунта (не больше $count). Ошибка yt-dlp — RuntimeException. */
function tiktok_fetch_posts(string $user, int $count, string $name = ''): array {
    if (!is_valid_tiktok_user($user)) return [];
    $source = $name !== '' ? $name : source_display_name(PLATFORM_TIKTOK, $user);
    $posts = [];
    foreach (ytdlp_list("https://www.tiktok.com/@$user", max(1, min($count, TIKTOK_POSTS_PER_ACCOUNT))) as $item) {
        if (($post = tiktok_post_from_item($user, $item, $source)) !== null) $posts[] = $post;
        if (count($posts) >= $count) break;
    }
    return $posts;
}

/** Превью для карточек: ошибка yt-dlp — пустой список. */
function tiktok_preview_posts(array $source, int $count): array {
    try { return tiktok_fetch_posts((string)($source['id'] ?? ''), $count, (string)($source['name'] ?? '')); } catch (Throwable $e) { return []; }
}
