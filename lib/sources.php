<?php
// Источники: Двач, Telegram (t.me/s/), ВКонтакте (VK ID + API), каталог, активность, аудитория и отзывы пользователей.

const PLATFORM_DVACH = 'Двач';
const PLATFORM_TELEGRAM = 'Telegram';
const PLATFORM_VK = 'ВКонтакте';
const PLATFORM_RSS = 'RSS';
const PLATFORM_OK = 'Одноклассники';
const PLATFORM_TIKTOK = 'TikTok';
const PLATFORM_4CHAN = '4chan';
const SOURCE_NAME_PREFIXES = [PLATFORM_TELEGRAM => 'Telegram | ', PLATFORM_VK => 'ВКонтакте | ', PLATFORM_RSS => 'RSS | ', PLATFORM_OK => 'Одноклассники | ', PLATFORM_TIKTOK => 'TikTok | ', PLATFORM_4CHAN => '4chan | '];
const CATALOG_KEYS = [PLATFORM_TELEGRAM => 'telegram_channels', PLATFORM_VK => 'vk_publics', PLATFORM_RSS => 'rss_feeds', PLATFORM_OK => 'ok_groups', PLATFORM_TIKTOK => 'tiktok_accounts'];
const TRUSTED_SOURCE_HOSTS = ['2ch.hk', 't.me', 'vk.com', 'ok.ru', 'www.tiktok.com', 'x.com', 'boards.4chan.org'];
const EMPTY_POST_TEXT = 'Пост без текста';
const NO_TEXT_PLACEHOLDERS = [EMPTY_POST_TEXT, 'Текст к посту отсутствует'];   // синхронно с NO_TEXT в js/core.js

/** Текст поста без заглушек («Пост без текста» и т.п.): заглушка в подпись не уходит. */
function post_caption_clean($text): string {
    $t = trim((string)$text);
    return in_array($t, NO_TEXT_PLACEHOLDERS, true) ? '' : $t;
}
const TELEGRAM_RESERVED_PATHS = ['joinchat', 'addlist', 'addstickers', 'addemoji', 'addtheme', 'setlanguage', 'share', 'proxy', 'socks', 'boost', 'contact', 'login', 'premium', 'username', 'telegram', 'durov', 'iv'];

function clean_html_text($raw) {
    return trim(preg_replace('/\s+/u', ' ', html_entity_decode(strip_tags((string)$raw), ENT_QUOTES | ENT_HTML5, 'UTF-8')) ?? '');
}
function html_attr($value) { return html_entity_decode($value, ENT_QUOTES | ENT_HTML5, 'UTF-8'); }
function is_valid_dvach_board($b) { return preg_match('/^[a-z0-9_-]{1,30}$/i', (string)$b) === 1; }
function is_valid_dvach_id($id) { return preg_match('/^[0-9]{1,20}$/', (string)$id) === 1; }
function is_valid_telegram_channel($c) { return preg_match('/^[A-Za-z][A-Za-z0-9_]{4,31}$/', (string)$c) === 1; }
function is_valid_vk_domain($d) { return preg_match('/^[A-Za-z0-9_.-]{1,64}$/', (string)$d) === 1; }
function is_valid_rss_url($url) { return strlen((string)$url) <= 500 && str_starts_with((string)$url, 'https://') && filter_var($url, FILTER_VALIDATE_URL) !== false; }
function is_valid_source_id($platform, $id) {
    return match ($platform) { PLATFORM_TELEGRAM => is_valid_telegram_channel($id), PLATFORM_VK => is_valid_vk_domain($id), PLATFORM_RSS => is_valid_rss_url($id), PLATFORM_OK => is_valid_ok_group($id), PLATFORM_TIKTOK => is_valid_tiktok_user($id), default => false };
}
function source_id_for($platform, $id) {
    return match ($platform) { PLATFORM_TELEGRAM => 'telegram:', PLATFORM_VK => 'vk:', PLATFORM_RSS => 'rss:', PLATFORM_OK => 'ok:', PLATFORM_TIKTOK => 'tiktok:', default => throw new InvalidArgumentException("Неизвестная площадка источника: $platform") } . strtolower($id);
}
function source_display_name($platform, $name) { return (SOURCE_NAME_PREFIXES[$platform] ?? '') . $name; }

function is_trusted_source_url($url) {
    $p = parse_url((string)$url);
    return is_array($p) && ($p['scheme'] ?? '') === 'https' && in_array(strtolower($p['host'] ?? ''), TRUSTED_SOURCE_HOSTS, true);
}

function dvach_post_url($board, $thread_id, $post_id) {
    return 'https://2ch.hk/' . rawurlencode($board) . '/res/' . rawurlencode($thread_id) . '.html#' . rawurlencode($post_id);
}

/** Площадка записи по префиксу поля source. */
function content_platform($item) {
    foreach (SOURCE_NAME_PREFIXES as $platform => $prefix) if (str_starts_with((string)($item['source'] ?? ''), $prefix)) return $platform;
    return PLATFORM_DVACH;
}

function format_subscribers($n) {
    return $n >= 1000000 ? round($n / 1000000, 1) . 'M' : ($n >= 1000 ? round($n / 1000, 1) . 'K' : (string)$n);
}

function median(array $values) {
    $values = array_values(array_filter($values, fn($x) => $x !== null));
    if (!$values) return null;
    sort($values);
    $n = count($values);
    return $n % 2 ? $values[intdiv($n, 2)] : ($values[$n / 2 - 1] + $values[$n / 2]) / 2;
}

// ---- Двач ----

/** JSON каталога доски или треда. */
function fetch_2ch_json($board, $thread_id = null, array $options = [CURLOPT_CONNECTTIMEOUT => 3, CURLOPT_TIMEOUT => 8, CURLOPT_MAXREDIRS => 2, CURLOPT_PROTOCOLS => CURLPROTO_HTTPS]) {
    if (!is_valid_dvach_board($board) || ($thread_id !== null && !is_valid_dvach_id($thread_id))) return [];
    return http_get_json('https://2ch.hk/' . rawurlencode($board) . ($thread_id === null ? '/catalog.json' : '/res/' . rawurlencode($thread_id) . '.json'), $options);
}

function dvach_thread_posts($data) {
    $posts = $data['threads'][0]['posts'] ?? null;
    return is_array($posts) ? $posts : [];
}

/** Медиа поста: [['image', 'video']]; у видео image — превью, без превью видео пропускается. */
function dvach_post_media($board, $post, $with_video = true) {
    $pattern = fn($dir, $ext) => '#^/' . preg_quote($board, '#') . "/$dir/[0-9]+/[^/]+\.(?:$ext)$#i";
    $media = [];
    foreach ($post['files'] ?? [] as $file) {
        [$path, $thumb] = [(string)($file['path'] ?? ''), (string)($file['thumbnail'] ?? '')];
        if (preg_match($pattern('src', 'jpe?g|png|gif|webp'), $path)) $media[] = ['image' => "https://2ch.hk$path", 'video' => ''];
        elseif ($with_video && preg_match($pattern('src', 'webm|mp4'), $path) && preg_match($pattern('thumb', 'jpe?g|png|gif|webp'), $thumb)) $media[] = ['image' => "https://2ch.hk$thumb", 'video' => "https://2ch.hk$path"];
    }
    return $media;
}

function dvach_post_text($post) {
    $text = preg_replace('/>>\d+(\s*\((?:OP|You)\))?/u', '', clean_html_text($post['comment'] ?? $post['subject'] ?? '')) ?? '';
    return trim(preg_replace('/\s+/u', ' ', $text) ?? '');
}

function dvach_thread_title($subject, $thread_id) {
    $title = str_replace(['[', ']'], '', clean_html_text($subject));
    if ($title === '') return '#' . $thread_id;
    return mb_strlen($title) > DVACH_THREAD_TITLE_MAX ? rtrim(mb_substr($title, 0, DVACH_THREAD_TITLE_MAX - 1)) . '…' : $title;
}

/** Превью треда: $count файлов через равные промежутки по всему треду. */
function build_dvach_preview_posts($source, $count = 4) {
    [$board, $thread] = [$source['board'] ?? '', (string)($source['thread_id'] ?? '')];
    $items = [];
    foreach (dvach_thread_posts(fetch_2ch_json($board, $thread)) as $post) foreach (dvach_post_media($board, $post) as $m) $items[] = [$post, $m];
    $total = count($items);
    $picked = $total <= $count ? $items : array_map(fn($i) => $items[(int)floor(($i + 0.5) * $total / $count)], range(0, $count - 1));
    return array_map(fn($pair) => ['image' => $pair[1]['image'], 'images' => [$pair[1]['image']], 'video' => $pair[1]['video'],
        'text' => dvach_post_text($pair[0]) ?: EMPTY_POST_TEXT, 'source' => $source['name'] ?? '', 'post_id' => (string)($pair[0]['num'] ?? ''),
        'posted_at' => (int)($pair[0]['timestamp'] ?? 0), 'url' => dvach_post_url($board, $thread, (string)($pair[0]['num'] ?? ''))], $picked);
}

/** Состояние треда: alive | gone (только 404) | unknown. */
function dvach_thread_state($board, $thread_id, &$posts = null) {
    $posts = null;
    if (!is_valid_dvach_board($board) || !is_valid_dvach_id($thread_id)) return 'unknown';
    $ch = curl_init('https://2ch.hk/' . rawurlencode($board) . '/res/' . rawurlencode($thread_id) . '.json');
    curl_setopt_array($ch, [CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 20, CURLOPT_CONNECTTIMEOUT => 8, CURLOPT_FOLLOWLOCATION => true, CURLOPT_USERAGENT => HTTP_UA]);
    $body = curl_exec($ch);
    $code = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
    curl_close($ch);
    if ($code === 404) return 'gone';
    $list = $code === 200 ? dvach_thread_posts(json_decode((string)$body, true)) : [];
    if (!$list) return 'unknown';
    $posts = $list;
    return 'alive';
}

/** Мёртвый тред: закрыт, пустой или молчит дольше SOURCE_STALE_DAYS. Причина или null. */
function dvach_thread_dead_reason(array $t, $now = null) {
    if (!empty($t['closed'])) return 'тред закрыт';
    if (($posts = (int)($t['posts_count'] ?? 0)) < DVACH_THREAD_MIN_POSTS) return "в треде всего {$posts} постов";
    $days = !empty($t['lasthit']) ? (int)floor((($now ?? time()) - (int)$t['lasthit']) / DAY_SECONDS) : 0;
    return $days > SOURCE_STALE_DAYS ? "в тред не писали {$days} дн" : null;
}

function dvach_thread_is_alive(array $t, $now = null) {
    $last = (int)($t['lasthit'] ?? 0);
    return dvach_thread_dead_reason($t, $now) === null && $last > 0 && ($now ?? time()) - $last <= DVACH_THREAD_ALIVE_HOURS * 3600;
}

/** Активность постов треда: ответы (>>номер, кроме ОПа), позиция, размер и скорость треда против медиан доски. */
function dvach_thread_engagement(array $posts, array $catalog_threads) {
    if (!($count = count($posts))) return [];
    $replies = [];
    foreach ($posts as $post) {
        if (!empty($post['op']) || !preg_match_all('~data-num="(\d+)"~', (string)($post['comment'] ?? ''), $links)) continue;
        foreach (array_unique($links[1]) as $target) if ($target !== (string)($post['num'] ?? '')) $replies[$target] = ($replies[$target] ?? 0) + 1;
    }
    $now = time();
    $velocity = median(array_map(fn($t) => (int)($t['posts_count'] ?? 0) / (!empty($t['timestamp']) ? max(1.0, ($now - (int)$t['timestamp']) / 3600) : 24.0), $catalog_threads));
    [$first, $last] = [(int)($posts[0]['timestamp'] ?? 0), (int)($posts[$count - 1]['timestamp'] ?? 0)];
    $thread = ['thread_posts' => $count, 'thread_hours' => $first && $last ? round(max(1.0, ($last - $first) / 3600), 1) : null,
        'thread_files' => array_sum(array_map(fn($p) => count($p['files'] ?? []), $posts)),
        'board_median_thread_posts' => median(array_map(fn($t) => (int)($t['posts_count'] ?? 0), $catalog_threads)),
        'board_median_velocity' => $velocity === null ? null : round($velocity, 2)];
    $out = [];
    foreach ($posts as $i => $post) {
        $n = (string)($post['num'] ?? '');
        $out[$n] = ['v' => 1, 'source' => 'dvach', 'at' => $now, 'is_op' => $i === 0, 'index' => $i, 'posts_after' => $count - $i - 1,
            'replies' => $replies[$n] ?? 0, 'post_views' => isset($post['views']) ? (int)$post['views'] : null] + $thread;
    }
    return $out;
}

// ---- Картинки: один файл в нескольких размерах — один ключ, оставляем самый большой вариант ----

/** Ключ файла: адрес без параметров и размерного суффикса (_s, -m, _x …); длинное имя файла (id CDN) сравниваем без домена. */
function media_file_key($url) {
    $path = strtolower(rawurldecode((string)parse_url((string)$url, PHP_URL_PATH)));
    $name = preg_replace('~[_-](?:s|m|x|y|w|z|small|medium|large|thumb|preview)(\.[a-z0-9]+)$~', '$1', basename($path));
    if (strlen($name) >= 16) return $name;
    return strtolower((string)parse_url((string)$url, PHP_URL_HOST)) . preg_replace('~[_-](?:s|m|x|y|w|z|small|medium|large|thumb|preview)(\.[a-z0-9]+)$~', '$1', $path);
}

/** Размер по адресу: число в параметрах (w, width, size) — больше всего; буква размера в имени (s < m < x < y < z < w); без суффикса — оригинал; миниатюры — меньше всех. */
function media_size_score($url) {
    parse_str((string)parse_url((string)$url, PHP_URL_QUERY), $params);
    foreach (['w', 'width', 'size'] as $k) if (isset($params[$k]) && is_numeric($params[$k])) return 1000 + (int)$params[$k];
    $name = strtolower(basename((string)parse_url((string)$url, PHP_URL_PATH)));
    if (preg_match('~[_-](s|m|x|y|z|w)\.[a-z0-9]+$~', $name, $m)) return ['s' => 100, 'm' => 200, 'x' => 300, 'y' => 400, 'z' => 500, 'w' => 600][$m[1]];
    return preg_match('~thumb|small|preview~', $name) ? 10 : 1000;
}

/** Адреса картинок без повторов файлов: из группы одного файла остаётся самый большой вариант (при равенстве — самый длинный адрес). Порядок — первого вхождения. */
function media_dedupe_urls(array $urls): array {
    $best = [];
    foreach (array_unique(array_filter($urls, fn($u) => is_string($u) && $u !== '')) as $url) {
        $key = media_file_key($url);
        $a = media_size_score($url);
        $b = isset($best[$key]) ? media_size_score($best[$key]) : -1;
        if (!isset($best[$key]) || $a > $b || ($a === $b && strlen($url) > strlen($best[$key]))) $best[$key] = $url;
    }
    return array_values($best);
}

// ---- Telegram: публичные страницы t.me/s/<канал> ----

function fetch_telegram_channel_page($channel, $before = null) {
    if (!is_valid_telegram_channel($channel)) return false;
    host_throttle('t.me', 1.5);   // t.me отвечает через раз под частыми запросами: не чаще одного в 1,5 с
    return http_get('https://t.me/s/' . rawurlencode($channel) . ($before ? "?before=$before" : ''),
        [CURLOPT_TIMEOUT => 12, CURLOPT_MAXREDIRS => 2, CURLOPT_PROTOCOLS => CURLPROTO_HTTPS, CURLOPT_USERAGENT => 'Mozilla/5.0 (compatible; Posta/1.0)']);
}

/** Блоки постов страницы: [[id поста, разметка]]. */
function telegram_page_blocks($html) {
    preg_match_all('/data-post="([^"]+)"(.*?)(?=data-post="[^"]+"|$)/s', (string)$html, $blocks, PREG_SET_ORDER);
    $out = [];
    foreach ($blocks as $b) if (preg_match('/^[0-9]+$/', $id = explode('/', $b[1], 2)[1] ?? '')) $out[] = [$id, $b[2] ?? ''];
    return $out;
}

/** «12.3K» / «1.2M» / «845» → число. */
function telegram_parse_counter($text) {
    if (!preg_match('/([\d.,]+)\s*([KkMm]?)/', str_replace(' ', '', (string)$text), $m)) return 0;
    return (int)round((float)str_replace(',', '.', $m[1]) * (['k' => 1000, 'm' => 1000000][strtolower($m[2])] ?? 1));
}

function telegram_post_time($content) {
    return preg_match('~<time[^>]*\sdatetime="([^"]+)"~i', $content, $m) ? (int)strtotime(html_attr($m[1])) : 0;
}

/** Просмотры и реакции всех постов страницы: [id => ['views', 'reactions']]. */
function telegram_page_engagement($html) {
    $page = [];
    foreach (telegram_page_blocks($html) as [$id, $content]) {
        $reactions = [];
        preg_match_all('~<i class="emoji"[^>]*><b>([^<]+)</b></i>([\d.,KkMm ]*)</span>~u', $content, $all, PREG_SET_ORDER);
        foreach ($all as $r) $reactions[$r[1]] = ($reactions[$r[1]] ?? 0) + telegram_parse_counter($r[2]);
        $page[$id] = ['views' => preg_match('~tgme_widget_message_views">([^<]*)~', $content, $m) ? telegram_parse_counter($m[1]) : null, 'reactions' => $reactions];
    }
    return $page;
}

/** Снимок активности поста и медианы по соседним постам канала. */
function telegram_engagement_snapshot(array $page, $post_id, $posted_at) {
    $post = $page[$post_id] ?? null;
    if ($post === null || $post['views'] === null) return null;
    $others = array_filter($page, fn($p, $id) => (string)$id !== (string)$post_id && $p['views'] !== null, ARRAY_FILTER_USE_BOTH);
    return ['v' => 1, 'source' => 'telegram', 'at' => time(), 'age_hours' => $posted_at ? round((time() - $posted_at) / 3600, 1) : null,
        'views' => $post['views'], 'reactions' => $post['reactions'], 'neighbors' => count($others),
        'neighbors_median_views' => median(array_column($others, 'views')),
        'neighbors_median_reaction_rate' => median(array_map(fn($p) => array_sum($p['reactions']) / max(1, (int)$p['views']), $others))];
}

/** Медиа сообщения по плиткам альбома: [['image' (картинка или постер), 'video']]. */
function telegram_post_media($content, $max = 10) {
    $media = [];
    foreach (array_slice(preg_split('~(?=<a class="tgme_widget_message_(?:photo_wrap|video_player|roundvideo_player)\b)~', $content), 1) as $part) {
        if (($end = strpos($part, '</a>')) !== false) $part = substr($part, 0, $end);
        $video = preg_match('~<video[^>]*?\ssrc="([^"]+)"~i', $part, $m) ? html_attr($m[1]) : '';
        $image = preg_match('~<video[^>]*?\sposter="([^"]+)"~i', $part, $m) ? html_attr($m[1])
            : (preg_match('~background-image:\s*url\([\'"]?(https://[^\'")\s]+)~i', $part, $m) ? html_attr($m[1]) : '');
        if ($image !== '') $media[] = ['image' => $image, 'video' => $video];
        if (count($media) >= $max) break;
    }
    return $media;
}

/** Превью ссылки в посте (картинка сайта, логотип): отдельно от медиа поста, в мозаику не идёт. */
function telegram_link_preview_image($content) {
    return preg_match('~tgme_widget_message_link_preview.*?background-image:\s*url\([\'"]?(https://[^\'")\s]+)~si', $content, $m) ? html_attr($m[1]) : '';
}

/** Посты с медиа из страницы канала, самые свежие первыми. Картинки — только медиа самого поста (фото, альбом, обложка видео). */
function telegram_posts_from_html($html, $source, $count, $with_text = false, $min_text = 120) {
    $channel = ltrim((string)($source['id'] ?? ''), '@');
    $page = telegram_page_engagement($html);
    $posts = [];
    foreach (array_reverse(telegram_page_blocks($html)) as [$post_id, $content]) {
        if (count($posts) >= $count) break;
        $text = preg_match('~<div class="tgme_widget_message_text[^"]*"[^>]*>(.*?)</div>~si', $content, $m) ? clean_html_text($m[1]) : '';
        $media = telegram_post_media($content);
        $images = media_dedupe_urls(array_column($media, 'image'));
        $video = $media[0]['video'] ?? '';
        // Плеер без файла: видео слишком большое для веб-версии, есть только обложка
        $unavailable = $video === '' && preg_match('~tgme_widget_message_(?:round)?video_player~', $content) === 1;
        // Текстовый пост без картинки — только если сбор текста включён и текст достаточно длинный
        $text_only = !$images && $with_text && mb_strlen($text) >= $min_text && $text !== '';
        if (!$images && !$text_only) continue;
        $time = telegram_post_time($content);
        $posts[] = ['image' => $images[0] ?? '', 'images' => array_slice($images, 0, 10), 'link_image' => telegram_link_preview_image($content), 'gallery' => count($media) > 1 ? $media : [],
            'video' => $video, 'text' => $text ?: EMPTY_POST_TEXT, 'text_post' => $text_only, 'source' => $source['name'] ?? source_display_name(PLATFORM_TELEGRAM, $channel), 'post_id' => $post_id,
            'posted_at' => $time, 'url' => 'https://t.me/' . rawurlencode($channel) . '/' . $post_id, 'video_unavailable' => $unavailable,
            'engagement' => telegram_engagement_snapshot($page, $post_id, $time)];
    }
    return $posts;
}

/** Сведения о странице канала, нужные сборщику и подбору. */
function telegram_page_facts($html) {
    return ['subscribers' => preg_match('~<span class="counter_value">([^<]+)</span>\s*<span class="counter_type">subscribers?</span>~i', (string)$html, $m) ? telegram_parse_counter($m[1]) : 0,
        'post_times' => array_values(array_filter(array_map(fn($b) => telegram_post_time($b[1]), telegram_page_blocks($html)))),
        'post_views' => array_reverse(array_values(array_column(telegram_page_engagement($html), 'views')))];
}

const TELEGRAM_PREVIEW_PAGES = 4;          // страниц канала в превью (первая + ещё три через ?before=)
const TELEGRAM_PREVIEW_BUDGET = 10;        // секунд на листание страниц канала в превью
const TELEGRAM_PAGE_CACHE_SECONDS = 600;   // страницы канала держим 10 минут: листание и повторные превью не ходят в сеть
const TELEGRAM_TEXT_POST_MIN = 80;         // текст без картинки от этой длины — карточка текста в превью
const TELEGRAM_TEXT_POSTS_MAX = 6;

/** Файл кэша страницы канала: канал + «до какого поста» однозначно задают страницу. */
function telegram_page_cache_file($channel, $before = null) {
    return sys_get_temp_dir() . '/posta-tg-' . md5(strtolower((string)$channel) . '|' . (string)$before) . '.html';
}

/** Сетевой вызов страницы канала. Тесты подменяют через $GLOBALS['telegram_page_fetch_stub'] и считают обращения. */
function telegram_channel_page_fetch($channel, $before = null) {
    if (isset($GLOBALS['telegram_page_fetch_stub'])) return ($GLOBALS['telegram_page_fetch_stub'])($channel, $before);
    return fetch_telegram_channel_page($channel, $before);
}

/** Страница канала из кэша (не старше TELEGRAM_PAGE_CACHE_SECONDS) или из сети. Неудачные загрузки в кэш не пишутся. */
function telegram_channel_page_cached($channel, $before = null) {
    $file = telegram_page_cache_file($channel, $before);
    if (is_file($file) && time() - filemtime($file) < TELEGRAM_PAGE_CACHE_SECONDS) return (string)file_get_contents($file);
    if (!($html = telegram_channel_page_fetch($channel, $before))) return false;
    $tmp = $file . '.' . uniqid();   // через временный файл: параллельный запрос не прочитает обрезанную страницу
    if (@file_put_contents($tmp, $html) !== false) @rename($tmp, $file);
    return $html;
}

/**
 * Посты с медиа и текстовые посты канала со страниц t.me/s/ (от свежих к старым, через ?before=).
 * Возрастной фильтр тут не нужен: превью оценивает сам канал. Листание останавливается, когда набрано $want медиа-постов,
 * исчерпаны страницы или вышел бюджет времени. Возвращает posts, text_posts, pages, posts_total, media_total, text_total.
 */
function telegram_channel_media_posts($channel, $want = 24, $max_pages = TELEGRAM_PREVIEW_PAGES): array {
    $started = microtime(true);
    $source = ['id' => $channel, 'name' => source_display_name(PLATFORM_TELEGRAM, $channel)];
    $out = ['posts' => [], 'text_posts' => [], 'pages' => 0, 'posts_total' => 0];
    $before = null;
    while ($out['pages'] < $max_pages && count($out['posts']) < $want) {
        if ($out['pages'] > 0 && microtime(true) - $started > TELEGRAM_PREVIEW_BUDGET) break;
        if (!($html = telegram_channel_page_cached($channel, $before)) || !($blocks = telegram_page_blocks($html))) break;
        $out['pages']++;
        $out['posts_total'] += count($blocks);
        foreach (telegram_posts_from_html($html, $source, PHP_INT_MAX, true, TELEGRAM_TEXT_POST_MIN) as $p) {
            if ($p['text_post']) $out['text_posts'][] = $p; else $out['posts'][] = $p;
        }
        $oldest = min(array_map(fn($b) => (int)$b[0], $blocks));
        if ($before !== null && $oldest >= (int)$before) break;   // страница не сдвинулась: дальше не идём
        $before = $oldest;
    }
    $out['media_total'] = count($out['posts']);
    $out['text_total'] = count($out['text_posts']);
    $out['posts'] = array_slice($out['posts'], 0, $want);
    $out['text_posts'] = array_slice($out['text_posts'], 0, TELEGRAM_TEXT_POSTS_MAX);
    return $out;
}

/** Канал по странице t.me/s/: название, описание, подписчики, посты, связанные каналы. null — канала нет или он закрыт. */
function telegram_channel_info($channel, $post_count = 6) {
    $channel = ltrim(trim((string)$channel), '@');
    $html = telegram_channel_page_cached($channel);
    if (!$html || !str_contains($html, 'data-post="')) return null;
    $title = preg_match('~class="tgme_channel_info_header_title"[^>]*>(.*?)</div>~si', $html, $m) ? clean_html_text($m[1]) : '';
    $description = preg_match('~class="tgme_channel_info_description"[^>]*>(.*?)</div>~si', $html, $m) ? clean_html_text($m[1]) : '';
    return ['id' => $channel, 'title' => $title ?: $channel, 'description' => mb_substr($description, 0, 500),
        'posts' => telegram_posts_from_html($html, ['id' => $channel, 'name' => source_display_name(PLATFORM_TELEGRAM, $title ?: $channel)], $post_count),
        'linked' => telegram_linked_channels($html, $channel)] + telegram_page_facts($html);
}

/** Каналы, на которые ссылаются посты: репосты и ссылки t.me/<канал>. */
function telegram_linked_channels($html, $own = '') {
    preg_match_all('~https?://t\.me/(?:s/)?([A-Za-z][A-Za-z0-9_]{4,31})(?=[/?"#\'<\s]|$)~', (string)$html, $m);
    $found = [];
    foreach ($m[1] as $handle) {
        $key = strtolower($handle);
        if ($key !== strtolower($own) && !in_array($key, TELEGRAM_RESERVED_PATHS, true) && !str_ends_with($key, 'bot')) $found[$key] = $handle;
    }
    return array_values($found);
}

// ---- ВКонтакте: VK ID (OAuth 2 + PKCE), API ----

const VK_ID_AUTHORIZE_URL = 'https://id.vk.ru/authorize';
const VK_ID_TOKEN_URL = 'https://id.vk.ru/oauth2/auth';

function vk_oauth_config() {
    return array_map(fn($n) => trim((string)getenv($n)), ['client_id' => 'VK_CLIENT_ID', 'redirect_uri' => 'VK_REDIRECT_URI', 'service_token' => 'VK_SERVICE_TOKEN', 'scope' => 'VK_OAUTH_SCOPE']);
}
function vk_oauth_is_configured() { $c = vk_oauth_config(); return $c['client_id'] !== '' && $c['redirect_uri'] !== ''; }

/** Прежнее место токенов — файл в /tmp (у Apache он приватный и пропадает при перезагрузке). Нужен только для переноса в базу. */
function vk_legacy_token_file() {
    return trim((string)getenv('POSTA_VK_TOKEN_FILE')) ?: sys_get_temp_dir() . '/posta-vk-' . substr(hash('sha256', ROOT . '/sources'), 0, 16) . '.json';
}

/** Токены VK сайта — в kv, зашифрованные ключом сервера. Старый файл при первом чтении переносится в базу и удаляется. */
function load_vk_tokens() {
    $tokens = json_decode((string)secret_decrypt((string)kv_get('vk_tokens', '')), true);
    if (is_array($tokens)) return $tokens;
    $legacy = load_data(vk_legacy_token_file(), []);
    if ($legacy) { save_vk_tokens($legacy); @unlink(vk_legacy_token_file()); }
    return $legacy;
}

function save_vk_tokens($tokens) {
    kv_update('vk_tokens', fn() => secret_encrypt(json_encode($tokens, JSON_UNESCAPED_UNICODE)));
}

function vk_oauth_post($url, $params) {
    $r = http_post($url, http_build_query($params), [CURLOPT_HTTPHEADER => ['Content-Type: application/x-www-form-urlencoded'], CURLOPT_CONNECTTIMEOUT => 8, CURLOPT_TIMEOUT => 20]);
    $data = json_decode((string)$r['body'], true);
    if (!is_array($data) || $r['status'] < 200 || $r['status'] >= 300 || !empty($data['error'])) {
        throw new RuntimeException((is_array($data) ? ($data['error_description'] ?? $data['error'] ?? '') : '') ?: ($r['error'] ?: "Ошибка запроса к VK ID (HTTP {$r['status']})."));
    }
    return $data;
}

/** Рабочий токен VK (обновляется по refresh_token) или ''. */
function vk_access_token() {
    $t = load_vk_tokens();
    if (empty($t['access_token'])) return '';
    if ((int)($t['expires_at'] ?? 0) > time() + 60) return (string)$t['access_token'];
    $c = vk_oauth_config();
    if (empty($t['refresh_token']) || $c['client_id'] === '' || empty($t['device_id'])) return '';
    $state = bin2hex(random_bytes(16));
    $new = vk_oauth_post(VK_ID_TOKEN_URL, array_filter(['grant_type' => 'refresh_token', 'refresh_token' => $t['refresh_token'], 'client_id' => $c['client_id'],
        'device_id' => $t['device_id'], 'state' => $state, 'service_token' => $c['service_token']]));
    if (empty($new['access_token']) || empty($new['refresh_token'])) return '';
    if (!empty($new['state']) && !hash_equals($state, (string)$new['state'])) throw new RuntimeException('VK вернул неожиданный state при обновлении токена.');
    save_vk_tokens($t = array_merge($t, $new, ['expires_at' => time() + max(0, (int)($new['expires_in'] ?? 0))]));
    return (string)$t['access_token'];
}

function vk_is_connected() {
    try { return vk_access_token() !== ''; } catch (Throwable $e) { error_log('VK token refresh failed: ' . $e->getMessage()); return false; }
}

/** Ключ для чтения открытых сообществ: OAuth-токен пользователя, если подключён, иначе сервисный ключ приложения (VK_SERVICE_TOKEN). '' — читать нечем. */
function vk_api_token_for_read() {
    try { $token = vk_access_token(); } catch (Throwable $e) { error_log('VK token refresh failed: ' . $e->getMessage()); $token = ''; }
    return $token !== '' ? $token : trim((string)getenv('VK_SERVICE_TOKEN'));
}

/** Паблики VK можно читать: есть OAuth или сервисный ключ. Поиск сообществ (groups.search) требует только OAuth — см. vk_is_connected. */
function vk_can_read() { return vk_api_token_for_read() !== ''; }

/** Запрос к VK API. $service — метод читается сервисным ключом (wall.get, groups.getById): вход пользователя не нужен. */
function vk_api_request($method, $params, bool $service = false) {
    if (($token = $service ? vk_api_token_for_read() : vk_access_token()) === '') return [];
    $r = http_post('https://api.vk.com/method/' . rawurlencode($method), http_build_query($params + ['access_token' => $token, 'v' => trim((string)getenv('VK_API_VERSION')) ?: '5.199']));
    $data = json_decode((string)$r['body'], true);
    if (!is_array($data) || $r['status'] < 200 || $r['status'] >= 300) throw new RuntimeException($r['error'] ?: 'Не удалось получить ответ VK API.');
    if (!empty($data['error'])) throw new RuntimeException((string)($data['error']['error_msg'] ?? 'VK API отклонил запрос.'));
    return is_array($data['response'] ?? null) ? $data['response'] : [];
}

/** Записи стены паблика (сырые, как отдаёт VK) со смещением $offset. */
function vk_wall_items($domain, $count, $offset = 0) {
    return vk_api_request('wall.get', ['domain' => $domain, 'count' => max(1, min(100, $count))] + ($offset > 0 ? ['offset' => $offset] : []), true)['items'] ?? [];
}

/** Запись стены → пост с картинками (счётчики likes, reposts, comments, views) или null, если картинок нет. */
function vk_wall_post($post, $source_name, $domain, bool $with_text = false, int $min_text = 120) {
    $images = [];
    foreach ($post['attachments'] ?? [] as $a) {
        if (($a['type'] ?? '') !== 'photo') continue;
        $sizes = $a['photo']['sizes'] ?? [];
        usort($sizes, fn($x, $y) => ($y['width'] ?? 0) * ($y['height'] ?? 0) <=> ($x['width'] ?? 0) * ($x['height'] ?? 0));
        if (str_starts_with($url = (string)($sizes[0]['url'] ?? ''), 'https://')) $images[] = $url;
    }
    $text = clean_html_text($post['text'] ?? '');
    $has_video = (bool)array_filter($post['attachments'] ?? [], fn($a) => ($a['type'] ?? '') === 'video');
    $text_only = !$images && !$has_video && $with_text && mb_strlen($text) >= $min_text;   // текстовый пост паблика: без картинки и без видео, с достаточным текстом
    if (!$images && !$text_only) return null;
    [$owner, $id] = [(string)($post['owner_id'] ?? ''), (string)($post['id'] ?? '')];
    return ['image' => $images[0] ?? '', 'images' => array_values(array_unique($images)), 'text' => $text ?: EMPTY_POST_TEXT, 'text_post' => $text_only,
        'source' => $source_name, 'post_id' => $id, 'posted_at' => (int)($post['date'] ?? 0),
        'likes' => (int)($post['likes']['count'] ?? 0), 'reposts' => (int)($post['reposts']['count'] ?? 0), 'comments' => (int)($post['comments']['count'] ?? 0),
        'views' => isset($post['views']['count']) ? (int)$post['views']['count'] : null,
        'url' => $owner !== '' && $id !== '' ? "https://vk.com/wall{$owner}_{$id}" : 'https://vk.com/' . rawurlencode($domain)];
}

function build_vk_preview_posts($source, $count = 4, bool $with_text = false, int $min_text = 120) {
    $domain = trim((string)($source['id'] ?? ''));
    if (!is_valid_vk_domain($domain)) return [];
    $posts = [];
    foreach (vk_wall_items($domain, $count) as $item) {
        if ($post = vk_wall_post($item, $source['name'] ?? source_display_name(PLATFORM_VK, $domain), $domain, $with_text, $min_text)) $posts[] = $post;
        if (count($posts) >= $count) break;
    }
    return $posts;
}

/** Открытые сообщества по запросу, крупные первыми: [['id', 'title', 'description', 'subscribers']]. */
function vk_search_groups($query, $count = 15) {
    $ids = [];
    foreach (vk_api_request('groups.search', ['q' => $query, 'count' => max(1, min(100, $count)), 'sort' => 0])['items'] ?? [] as $g) {
        if ((int)($g['is_closed'] ?? 1) === 0 && ($g['type'] ?? '') !== 'event') $ids[] = (int)$g['id'];
    }
    if (!$ids) return [];
    $details = vk_api_request('groups.getById', ['group_ids' => implode(',', $ids), 'fields' => 'members_count,description,country'], true);   // country — для фильтра Украины (UA_FILTER_PROMPT.md, A)
    $groups = [];
    foreach ($details['groups'] ?? $details as $g) {
        $name = is_array($g) ? (string)($g['screen_name'] ?? 'club' . (int)($g['id'] ?? 0)) : '';
        if (!is_valid_vk_domain($name)) continue;
        $groups[] = ['id' => $name, 'title' => clean_html_text($g['name'] ?? $name), 'description' => mb_substr(clean_html_text($g['description'] ?? ''), 0, 500), 'subscribers' => (int)($g['members_count'] ?? 0), 'country' => $g['country'] ?? null];
    }
    usort($groups, fn($a, $b) => $b['subscribers'] <=> $a['subscribers']);
    return $groups;
}

function build_source_preview_posts($source, $count = 4) {
    return match ($source['platform'] ?? '') {
        PLATFORM_TELEGRAM => ($html = fetch_telegram_channel_page(ltrim((string)($source['id'] ?? ''), '@'))) ? telegram_posts_from_html($html, $source, $count) : [],
        PLATFORM_VK => build_vk_preview_posts($source, $count),
        PLATFORM_RSS => rss_preview_posts($source, $count),
        PLATFORM_OK => ok_preview_posts($source, $count),
        PLATFORM_TIKTOK => tiktok_preview_posts($source, $count),
        PLATFORM_DVACH => build_dvach_preview_posts($source, $count),
        default => []
    };
}

// ---- Активность: свежесть постов, частота, заморозка ----

function is_post_outdated($posted_at, $now = null, $max_days = CONTENT_MAX_AGE_DAYS) {
    return (int)$posted_at > 0 && ($now ?? time()) - (int)$posted_at > $max_days * DAY_SECONDS;
}

/** Последний пост и постов в неделю (от самого раннего поста до сейчас). null — дат нет. */
function source_activity(array $times, $now = null) {
    $now ??= time();
    $times = array_values(array_filter(array_map('intval', $times), fn($t) => $t > 0 && $t <= $now + DAY_SECONDS));
    if (!$times) return null;
    return ['last_post' => max($times), 'per_week' => round(count($times) / max(1.0, ($now - min($times)) / DAY_SECONDS) * 7, 1), 'checked_at' => $now];
}

function source_inactivity_reason($activity, $now = null) {
    if ($activity === null) return 'нет постов с датой';
    $days = (int)floor((($now ?? time()) - $activity['last_post']) / DAY_SECONDS);
    if ($days > SOURCE_STALE_DAYS) return "последний пост {$days} дн назад";
    return $activity['per_week'] < SOURCE_MIN_POSTS_PER_WEEK ? "редко публикует: {$activity['per_week']} постов в неделю" : null;
}

function describe_source_activity($activity, $now = null) {
    if ($activity === null) return 'дат постов нет';
    $h = (int)floor((($now ?? time()) - $activity['last_post']) / 3600);
    return 'последний пост ' . ($h < 1 ? 'меньше часа назад' : ($h < 48 ? "$h ч назад" : intdiv($h, 24) . ' дн назад')) . ", ~{$activity['per_week']} в неделю";
}

function is_source_frozen(array $entry, $now = null) { return (int)($entry['frozen_until'] ?? 0) > ($now ?? time()); }

/** Заморозить неактивный (или с причиной $extra_reason) / разморозить оживший. [запись, frozen|still_frozen|revived|null]. */
function apply_source_activity(array $entry, $activity, $now = null, $extra_reason = null) {
    $now ??= time();
    $was = isset($entry['frozen_until']);
    if ($activity !== null) $entry['activity'] = $activity;
    if (($reason = $extra_reason ?? source_inactivity_reason($activity, $now)) !== null) {
        return [['frozen_until' => $now + SOURCE_FROZEN_RECHECK_DAYS * DAY_SECONDS, 'frozen_reason' => $reason] + $entry, $was ? 'still_frozen' : 'frozen'];
    }
    unset($entry['frozen_until'], $entry['frozen_reason']);
    return [$entry, $was ? 'revived' : null];
}

// ---- Качество: аудитория и доля просмотров ----

/** Кривая по опорным точкам [[x, y]]: между ними линейно, за краями — крайние значения. */
function quality_curve($x, array $points) {
    if ($x <= $points[0][0]) return (float)$points[0][1];
    for ($i = 1; $i < count($points); $i++) {
        [[$x0, $y0], [$x1, $y1]] = [$points[$i - 1], $points[$i]];
        if ($x <= $x1) return $y0 + ($y1 - $y0) * ($x - $x0) / ($x1 - $x0);
    }
    return (float)end($points)[1];
}

function source_audience_score($subs) {
    return (int)$subs > 0 ? round(quality_curve(log10((int)$subs), [[2.5, 0], [3.0, 30], [4.0, 65], [5.0, 90], [6.0, 100]]), 1) : null;
}

/** Медиана просмотров свежих постов ÷ подписчики (два самых новых пропускаются: просмотры ещё набираются). */
function source_view_rate($subs, array $views) {
    $views = array_values(array_filter($views, fn($v) => $v !== null && $v >= 0));
    if ((int)$subs < SOURCE_MIN_SUBSCRIBERS_FOR_RATE) return null;
    $views = array_slice(count($views) >= 7 ? array_slice($views, 2) : $views, 0, 12);
    return count($views) < 4 ? null : round(median($views) / (int)$subs, 4);
}

function source_view_rate_score($rate, $subs = 0) {
    if ($rate === null) return null;
    $scale = (int)$subs >= 100000 ? 0.6 : 1.0;   // у крупных каналов доля естественно ниже
    return round(quality_curve($rate, array_map(fn($p) => [$p[0] * $scale, $p[1]], [[0.015, 0], [0.05, 20], [0.10, 45], [0.25, 80], [0.45, 100]])), 1);
}

function source_dead_audience_reason($subs, $rate) {
    return $rate === null || $rate >= SOURCE_DEAD_VIEW_RATE ? null : sprintf('мёртвая аудитория: посты видят %.1f%% из %s подписчиков', $rate * 100, format_subscribers((int)$subs));
}

/** Итог: по теме + аудитория + доля просмотров; не по теме — ниже порога предложений, как бы ни была велика аудитория. */
function source_quality_score($content, $subs, $rate) {
    $audience = source_audience_score($subs);
    $rate_score = source_view_rate_score($rate, $subs);
    // Неизвестная аудитория или доля просмотров — нейтральна: её вес не штрафует, а делится между тем, что известно
    // (98% по теме без данных об аудитории — 98%, а не 64%)
    $weighted = $weight = 0;
    foreach ([[$content, SOURCE_WEIGHT_CONTENT], [$audience, SOURCE_WEIGHT_AUDIENCE], [$rate_score, SOURCE_WEIGHT_VIEW_RATE]] as [$value, $w]) {
        if ($value === null) continue;
        $weighted += $value * $w;
        $weight += $w;
    }
    $score = $weighted / $weight;
    if ($content < SOURCE_MIN_CONTENT_SCORE) $score = min((float)$content, SOURCE_SUGGEST_SCORE - 1);
    return ['score' => (int)round($score), 'content' => (int)round($content), 'audience' => $audience, 'view_rate_score' => $rate_score];
}

function describe_source_audience($subs, $rate, array $q = []) {
    $parts = [];
    if ((int)$subs > 0) $parts[] = format_subscribers((int)$subs) . ' подписчиков' . (isset($q['audience']) ? ' (оценка ' . round($q['audience']) . ')' : '');
    if ($rate !== null) $parts[] = 'просмотры ' . round($rate * 100) . '% подписчиков' . (isset($q['view_rate_score']) ? ' (оценка ' . round($q['view_rate_score']) . ')' : '');
    return $parts ? implode(', ', $parts) : 'аудитория неизвестна';
}

// ---- Отзывы пользователей об источниках ----

/** Лайки и дизлайки всех пользователей по источникам темы (жалоба «ошибка подбора» — дизлайк): [ключ => likes, dislikes, votes, score]. */
function source_feedback_stats($topic) {
    $stats = [];
    foreach (user_ids() as $uid) {
        $history = history_load($uid);
        foreach (['liked' => 1, 'disliked' => 0, 'complaints' => 0] as $c => $positive) foreach ($history[$c] ?? [] as $r) {
            if (!is_array($r) || empty($r['source_key']) || str_starts_with((string)$r['source_key'], 'own:') || ($c === 'complaints' && ($r['complaint_reason'] ?? '') !== 'match_error')) continue;
            if (!topics_are_equivalent((string)($r['topic'] ?? ''), $topic)) continue;
            $s = &$stats[(string)$r['source_key']];
            $s = ['likes' => ($s['likes'] ?? 0) + $positive, 'dislikes' => ($s['dislikes'] ?? 0) + 1 - $positive];
            unset($s);
        }
    }
    return array_map(fn($s) => $s + ['votes' => $s['likes'] + $s['dislikes'], 'score' => ($s['likes'] + 1) / ($s['likes'] + $s['dislikes'] + 2)], $stats);
}

/** Доля прошедших быструю оценку, смешанная с долей лайков; стабильно плохой источник — 0 (отключён). */
/**
 * Источники, которые этот пользователь не хочет по теме: доля лайков ниже SOURCE_MUTE_LIKE_SHARE при не меньше
 * SOURCE_MUTE_MIN_RATINGS оценках. Жалоба «не по теме» — как дизлайк ×2. Возвращает source_key => ['likes', 'ratings', 'share'].
 */
function source_user_mutes(array $history, string $topic): array {
    $counts = [];
    foreach (['liked' => 'likes', 'disliked' => 'dislikes', 'complaints' => 'complaints'] as $collection => $field) {
        foreach ((array)($history[$collection] ?? []) as $r) {
            if (!is_array($r) || empty($r['source_key']) || str_starts_with((string)$r['source_key'], 'own:') || !topic_same((string)($r['topic'] ?? ''), $topic)) continue;
            if ($collection === 'complaints' && ($r['complaint_reason'] ?? '') !== 'match_error') continue;
            $counts[(string)$r['source_key']][$field] = ($counts[(string)$r['source_key']][$field] ?? 0) + 1;
        }
    }
    $out = [];
    foreach ($counts as $key => $c) {
        $likes = $c['likes'] ?? 0;
        $ratings = $likes + ($c['dislikes'] ?? 0) + ($c['complaints'] ?? 0);
        if ($ratings < SOURCE_MUTE_MIN_RATINGS) continue;
        $share = $likes / max(1, $likes + ($c['dislikes'] ?? 0) + SOURCE_MUTE_COMPLAINT_WEIGHT * ($c['complaints'] ?? 0));
        if ($share < SOURCE_MUTE_LIKE_SHARE) $out[$key] = ['likes' => $likes, 'ratings' => $ratings, 'share' => $share];
    }
    return $out;
}

/**
 * Ставки источников с учётом выключенных пользователем: выключенный — 0. Ручное «включить» (overrides = 'on') отменяет выключение.
 * Работает по истории текущего пользователя и не трогает общий каталог.
 */
function user_source_rates(array $rates, string $topic, array $history, array $overrides = []): array {
    foreach (source_user_mutes($history, $topic) as $key => $m) if (($overrides[$key] ?? '') !== 'on') $rates[$key] = 0.0;
    return $rates;
}

function combined_source_rates(array $pass_rates, $topic) {
    foreach (source_feedback_stats($topic) as $key => $s) {
        if ($s['votes'] >= SOURCE_FEEDBACK_BAN_VOTES && $s['score'] < SOURCE_FEEDBACK_BAN_SCORE) $pass_rates[$key] = 0.0;
        elseif ($s['votes'] >= SOURCE_FEEDBACK_MIN_VOTES) $pass_rates[$key] = isset($pass_rates[$key]) ? ($pass_rates[$key] + $s['score']) / 2 : $s['score'];
    }
    return $pass_rates;
}

function popular_topics($limit = 5) {
    $counts = [];
    foreach (user_ids() as $uid) {
        if (($t = normalize_topic(db_load_settings($uid)['topic'] ?? '')) === '') continue;
        $counts[mb_strtolower($t)] = ['topic' => $t, 'users' => ($counts[mb_strtolower($t)]['users'] ?? 0) + 1];
    }
    uasort($counts, fn($a, $b) => $b['users'] <=> $a['users']);
    return array_slice(array_column($counts, 'topic'), 0, $limit);
}

// ---- Каталог источников (таблицы catalog_sources, source_votes, source_suggestions) ----
// Каналы и паблики привязаны к темам (поле topics); без topics — источник для любой темы.

function create_default_source_catalog() {
    $tag = fn(array $list) => array_map(fn($e) => ['id' => $e[0], 'name' => $e[1], 'topics' => [DEFAULT_TOPIC]], $list);
    return [
        '2ch_boards' => ['b', 'me', 'pr', 'r', 'cc', 'a', 'mu', 'd', 's'],
        'vk_publics' => $tag([['mrzlk', 'MDK'], ['leprum', 'Лепрекон'], ['sci_one', 'Умный Юмор'], ['british_humor', 'БРИТИШ ЮМОР'], ['bessm_humor', 'ОВСЯНКА, СЭР!'], ['memes_2024', 'Мемы на каждый день']]),
        'rss_feeds' => [],
        'ok_groups' => [],
        'tiktok_accounts' => [],
        'telegram_channels' => $tag([['memesdaily', 'Мемы каждый день'], ['this_is_not_funny', 'Это не смешно'], ['humorhub', 'Humor Hub'], ['trashmemes', 'Trash memes'], ['russmemes', 'Русские мемы'], ['memechi', 'Meme Chi']]),
        'source_votes' => [],
        'source_suggestions' => [],      // находки автоподбора, ждут решения пользователя
        'dvach_topic_boards' => [],      // [тема в нижнем регистре => доски]; у темы по умолчанию — 2ch_boards
        'topics' => [],                  // реестр тем (lib/topics.php)
        'discovery' => ['topic' => '', 'last_run' => 0, 'requested' => false]
    ];
}

function normalize_source_db($db) {
    $defaults = create_default_source_catalog();
    $out = $defaults;
    foreach (['2ch_boards', 'vk_publics', 'telegram_channels', 'rss_feeds', 'ok_groups', 'tiktok_accounts'] as $key) $out[$key] = is_array($db[$key] ?? null) ? array_values($db[$key]) : $defaults[$key];
    $with_id = fn($key) => array_values(array_filter((array)($db[$key] ?? []), fn($e) => is_array($e) && !empty($e['source_id'])));
    $out['source_votes'] = $with_id('source_votes');
    $out['source_suggestions'] = $with_id('source_suggestions');
    $out['dvach_topic_boards'] = array_map(fn($b) => array_values(array_filter((array)$b, 'is_valid_dvach_board')), (array)($db['dvach_topic_boards'] ?? []));
    $out['discovery'] = (array)($db['discovery'] ?? []) + $defaults['discovery'];
    $out['topics'] = topic_registry_from_db($db + ['topics' => []]);
    return $out;
}

function load_source_db() {
    $catalog = catalog_load();
    $db = [
        '2ch_boards' => array_values(array_filter($catalog['sources']['2ch_boards'] ?? [], 'is_string')),
        'vk_publics' => array_values($catalog['sources']['vk_publics'] ?? []),
        'telegram_channels' => array_values($catalog['sources']['telegram_channels'] ?? []),
        'rss_feeds' => array_values($catalog['sources']['rss_feeds'] ?? []),
        'ok_groups' => array_values($catalog['sources']['ok_groups'] ?? []),
        'tiktok_accounts' => array_values($catalog['sources']['tiktok_accounts'] ?? []),
        'source_votes' => $catalog['votes'] ?? [],
        'source_suggestions' => $catalog['suggestions'] ?? [],
        'dvach_topic_boards' => $catalog['kv']['dvach_topic_boards'] ?? [],
        'discovery' => $catalog['kv']['discovery'] ?? [],
        'topics' => $catalog['topics'] ?? [],
    ];
    return normalize_source_db($db);
}

/** Изменить каталог: $mutator получает нормализованный каталог. */
function update_source_db(callable $mutator) {
    $current = load_source_db();
    $updated = $mutator($current);
    if ($updated === null) return $current;

    catalog_update(function () use ($updated) {
        return [
            'sources' => [
                '2ch_boards' => array_values($updated['2ch_boards'] ?? []),
                'vk_publics' => array_values($updated['vk_publics'] ?? []),
                'telegram_channels' => array_values($updated['telegram_channels'] ?? []),
                'rss_feeds' => array_values($updated['rss_feeds'] ?? []),
                'ok_groups' => array_values($updated['ok_groups'] ?? []),   // без этой строки catalog_update удалит группы ОК
                'tiktok_accounts' => array_values($updated['tiktok_accounts'] ?? []),
            ],
            'votes' => array_values($updated['source_votes'] ?? []),
            'suggestions' => array_values($updated['source_suggestions'] ?? []),
            'topics' => array_column($updated['topics'] ?? [], null, 'id'),
            'kv' => [
                'dvach_topic_boards' => $updated['dvach_topic_boards'] ?? [],
                'discovery' => $updated['discovery'] ?? [],
            ],
        ];
    });
    return $updated;
}

/** Подходит ли источник теме: без topics — всем; $strict — только сама тема и синонимы, без связанных. */
function catalog_entry_matches_topic($entry, $topic, $strict = false) {
    // Источник под уточнение (TOPIC_QUERY_PROMPT.md, часть 5) — только для того же уточнения; общие источники темы — и для пары с уточнением
    $entry_query = trim((string)($entry['query'] ?? ''));
    if ($entry_query !== '' && mb_strtolower($entry_query) !== mb_strtolower(topic_query_of($topic))) return false;
    $topic = topic_base($topic);
    $topics = (array)($entry['topics'] ?? []);
    if (!$topics) return true;
    foreach ($topics as $t) if ($strict ? topic_same($topic, (string)$t) : topics_are_equivalent((string)$t, $topic)) return true;
    return false;
}

/** Своих источников у пары «тема + уточнение»: записи с этим уточнением. */
function query_own_sources_count(array $db, $key): int {
    $query = mb_strtolower(topic_query_of($key));
    if ($query === '') return 0;
    $count = 0;
    foreach (array_merge($db['telegram_channels'], $db['vk_publics'], $db['rss_feeds'], $db['ok_groups'], $db['tiktok_accounts']) as $e) if (mb_strtolower(trim((string)($e['query'] ?? ''))) === $query) $count++;
    return $count;
}

/** Записи каталога ["telegram_channels:id" => запись] одной операцией. */
function save_catalog_entry_updates(array $updates) {
    if (!$updates) return;
    update_source_db(function ($db) use ($updates) {
        foreach (CATALOG_KEYS as $key) foreach ($db[$key] as $i => $e) $db[$key][$i] = $updates[$key . ':' . strtolower((string)($e['id'] ?? ''))] ?? $e;
        return $db;
    });
}

/** До 4 картинок самых свежих постов (карточка на вкладке «Источники»). */
function source_preview_images(array $posts) {
    usort($posts, fn($a, $b) => (int)($b['posted_at'] ?? 0) <=> (int)($a['posted_at'] ?? 0));
    $images = array_values(array_unique(array_filter(array_column($posts, 'image'), fn($i) => str_starts_with((string)$i, 'https://'))));
    return array_slice($images, 0, 4);
}

// ---- Тематические теги досок Двача (только живые доски на 2026-10-19; «18+» — только для взрослых тем) ----

const DVACH_ADULT_TAG = '18+';
const DVACH_BOARD_TAGS = [
    'b' => ['юмор', 'мемы', 'приколы', 'бред', 'треш'], 'a' => ['аниме', 'японская культура', 'манга'], 'bi' => ['велосипеды', 'спорт', 'транспорт'],
    'biz' => ['бизнес', 'деньги', 'финансы', 'предпринимательство'], 'bo' => ['книги', 'литература', 'чтение'], 'c' => ['комиксы', 'мультфильмы', 'мультики', 'анимация'],
    'dev' => ['девушки', 'общение', 'отношения'], 'em' => ['путешествия', 'эмиграция', 'другие страны'], 'ew' => ['апокалипсис', 'конец света', 'выживание'],
    'fa' => ['мода', 'стиль', 'одежда', 'красота'], 'fiz' => ['спорт', 'фитнес', 'тренировки', 'бодибилдинг', 'здоровье'], 'fl' => ['языки', 'иностранные языки', 'образование'],
    'ftb' => ['футбол', 'спорт'], 'hh' => ['хип-хоп', 'рэп', 'музыка'], 'hi' => ['история', 'наука'], 'hw' => ['железо', 'компьютеры', 'техника', 'гаджеты'],
    'me' => ['медицина', 'здоровье'], 'mg' => ['магия', 'эзотерика', 'мистика'], 'mlp' => ['my little pony', 'пони', 'мультфильмы', 'фэндом'],
    'mo' => ['мотоциклы', 'байки', 'транспорт'], 'ne' => ['животные', 'природа', 'кошки', 'собаки', 'питомцы', 'котики'], 'ph' => ['философия'],
    'po' => ['политика', 'новости'], 'pr' => ['программирование', 'it', 'айти', 'разработка'], 'psy' => ['психология', 'психиатрия', 'психическое здоровье'],
    'ra' => ['радиотехника', 'электроника', 'техника'], 're' => ['религия'], 's' => ['программы', 'софт', 'компьютеры', 'it'],
    'sf' => ['фантастика', 'научная фантастика', 'книги', 'кино'], 'sci' => ['наука', 'научпоп'], 'sn' => ['паранормальное', 'мистика', 'крипота', 'ужасы'],
    'sp' => ['спорт'], 'spc' => ['космос', 'астрономия', 'наука'], 'tr' => ['транспорт', 'авиация', 'самолёты', 'поезда'], 'tv' => ['сериалы', 'кино', 'тв'],
    'un' => ['образование', 'учёба', 'университет'], 'w' => ['оружие'], 'wh' => ['warhammer', 'настольные игры', 'миниатюры'],
    'wm' => ['военная техника', 'армия', 'оружие', 'война'], 'di' => ['еда', 'кулинария', 'готовка'], 'de' => ['дизайн', 'графика'],
    'diy' => ['хобби', 'рукоделие', 'своими руками', 'мастерская'], 'dom' => ['дом', 'ремонт', 'строительство', 'интерьер'], 'izd' => ['творчество', 'литература', 'графомания'],
    'mus' => ['музыка', 'музыканты', 'музыкальные инструменты'], 'pa' => ['живопись', 'рисование', 'искусство', 'арт'], 'p' => ['фотография', 'фото'],
    'wp' => ['обои', 'красивые картинки', 'пейзажи', 'арт'], 'td' => ['3d', 'трёхмерная графика', 'графика'], 'bg' => ['настольные игры', 'игры'],
    'cg' => ['консоли', 'видеоигры', 'игры'], 'mc' => ['minecraft', 'майнкрафт', 'видеоигры', 'игры'], 'mmo' => ['mmo', 'онлайн-игры', 'видеоигры', 'игры'],
    'tes' => ['the elder scrolls', 'skyrim', 'видеоигры', 'игры'], 'vg' => ['видеоигры', 'игры', 'гейминг'], 'wr' => ['ролевые игры', 'текстовые игры', 'игры'],
    'aa' => ['аниме', 'арт', 'аниме-арт'], 'fd' => ['фэндом', 'аниме'], 'ja' => ['японская культура', 'япония', 'аниме'], 'ma' => ['манга', 'аниме', 'комиксы'],
    'to' => ['touhou', 'аниме', 'видеоигры'], 'd' => ['обсуждение двача'], 'fag' => ['18+', 'лгбт'], 'soc' => ['общение', 'знакомства'], 'r' => ['просьбы'],
    'gg' => ['18+', 'девушки', 'эротика', 'фап'], 'sex' => ['18+', 'секс', 'отношения', 'фап', 'эротика'], 'web' => ['веб-разработка', 'сайты', 'it'],
    'gd' => ['геймдев', 'разработка игр', 'видеоигры'], 'mov' => ['кино', 'фильмы'], 'rf' => ['общение'], 'br' => ['барахолка', 'покупки'], 'wrk' => ['работа', 'карьера'],
    'mu' => ['музыка'], 'trv' => ['путешествия', 'туризм', 'отдых'], 'au' => ['автомобили', 'машины', 'авто', 'транспорт'], 'asylum' => ['общение'],
    'fs' => ['фэнтези', 'книги'], 'hc' => ['18+', 'порно'], 'mobi' => ['смартфоны', 'мобильные приложения', 'гаджеты'], 'out' => ['активный отдых', 'туризм', 'походы', 'природа'],
    'cc' => ['криптовалюта', 'биткоин', 'финансы'], 'media' => ['анимация', 'видео', 'медиа'], 'zog' => ['теории заговора', 'конспирология'],
    'jsf' => ['японская мода', 'мода', 'стиль'], 'sw' => ['звёздные войны', 'star wars', 'кино', 'фантастика'], 'law' => ['право', 'закон', 'юриспруденция'],
    'm' => ['меха', 'роботы', 'аниме'], 'wow' => ['world of warcraft', 'видеоигры', 'игры'], 'cute' => ['милое', 'милота', 'котики', 'животные'],
    'se' => ['самообразование', 'образование', 'саморазвитие'], 'alco' => ['18+', 'алкоголь', 'напитки'], 'brg' => ['браузерные игры', 'игры'], 'asmr' => ['asmr', 'релакс'],
    'dr' => ['дневники', 'личное'], 'socionics' => ['соционика', 'психология'], 'fi' => ['рыбалка', 'хобби', 'природа'], 'vape' => ['18+', 'вейп', 'курение'],
    'gsg' => ['стратегии', 'grand strategy', 'видеоигры'], 'kpop' => ['k-pop', 'кпоп', 'музыка', 'корея'], 'news' => ['новости', 'политика'], 'v' => ['видеоигры', 'игры', 'гейминг'],
    'o' => ['рисование', 'арт', 'творчество'], '2d' => ['аниме', 'общение'], 'ch' => ['чаты', 'общение'], 'int' => ['международное общение', 'иностранцы'],
    'math' => ['математика', 'наука'], 'es' => ['бесконечное лето', 'визуальные новеллы', 'аниме'], 'hry' => ['ютуб', 'блогеры', 'интернет-культура'],
    'whn' => ['warhammer', 'настольные игры'], 'ai' => ['искусственный интеллект', 'нейросети', 'it', 'технологии'], 'gacha' => ['гача-игры', 'мобильные игры', 'видеоигры', 'аниме'],
    'man' => ['мужское', 'саморазвитие', 'отношения'],
];

function dvach_board_tags($board) { return DVACH_BOARD_TAGS[$board] ?? []; }
