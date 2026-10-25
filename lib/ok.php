<?php
// Одноклассники: открытые группы читаются по мобильной странице группы — без входа, ключей и API. Все загрузки — через rss_get (fetch_public на каждом переходе).

const OK_POSTS_PER_GROUP = 20;   // на странице группы помещается около 8–10 записей: вторую страницу не листаем
const OK_NEED_GROUP = 'Нужна ссылка на группу Одноклассников: ok.ru/group/номер';
const OK_MONTHS = ['янв' => 1, 'фев' => 2, 'мар' => 3, 'апр' => 4, 'май' => 5, 'мая' => 5, 'июн' => 6, 'июл' => 7, 'авг' => 8, 'сен' => 9, 'окт' => 10, 'ноя' => 11, 'дек' => 12];

function is_valid_ok_group($id) { return preg_match('/^[0-9]{10,20}$/', (string)$id) === 1; }

/** Мобильная страница группы: без входа, около 140 КБ. */
function ok_group_url($group_id) { return 'https://m.ok.ru/dk?st.cmd=altGroupMain&st.groupId=' . rawurlencode((string)$group_id); }

/** Id группы из ссылки: ok.ru/group/<цифры>, m.ok.ru/group<цифры>, st.groupId=…; ok.ru/<имя> — ищется на странице. */
function ok_group_id(string $url): string {
    $p = parse_url(trim($url));
    if (!is_array($p) || !in_array(strtolower($p['host'] ?? ''), ['ok.ru', 'm.ok.ru'], true)) throw new RuntimeException(OK_NEED_GROUP);
    $path = (string)($p['path'] ?? '');
    if (preg_match('~^/group/?([0-9]{10,20})(?:/|$)~', $path, $m) || preg_match('~[?&]st\.groupId=([0-9]{10,20})~', (string)($p['query'] ?? ''), $m)) return $m[1];
    if (!preg_match('~^/[A-Za-z0-9._-]{2,64}/?$~', $path)) throw new RuntimeException(OK_NEED_GROUP);
    $r = rss_get('https://ok.ru' . rtrim($path, '/'), 3 * 1024 * 1024, 15, BROWSER_UA);
    if (!$r['ok'] || !preg_match('~groupId,([0-9]{10,20})|st\.groupId=([0-9]{10,20})~', $r['bytes'], $g)) throw new RuntimeException(OK_NEED_GROUP);
    return ($g[1] ?? '') !== '' ? $g[1] : $g[2];
}

/** HTML страницы группы; ошибка сети — исключение с понятным текстом. ОК отдаёт ленту только браузеру (без входа). */
function ok_group_html(string $group_id): string {
    $r = rss_get(ok_group_url($group_id), 2 * 1024 * 1024, 15, BROWSER_UA);
    if (!$r['ok']) throw new RuntimeException('Страница группы Одноклассников не загрузилась' . ($r['error'] !== '' ? ': ' . $r['error'] : ''));
    return (string)$r['bytes'];
}

/** Название группы из og:title (без «| OK.RU»); без названия — сам id. */
function ok_title_from_html(string $html, string $group_id): string {
    $raw = preg_match('~property="og:title" content="([^"]+)"~', $html, $m) || preg_match('~<title>([^<]+)</title>~', $html, $m) ? $m[1] : '';
    $title = trim((string)preg_replace('~\s*\|\s*OK\.RU\s*$~iu', '', clean_html_text(html_attr($raw))));
    return mb_substr($title !== '' ? $title : $group_id, 0, 120);
}

/** Название группы по её странице. */
function ok_group_title(string $group_id): string {
    try { return ok_title_from_html(ok_group_html($group_id), $group_id); } catch (Throwable $e) { return $group_id; }
}

/** Картинки записи: только i.okcdn.ru и *.mycdn.me, https. */
function ok_card_images(string $card): array {
    preg_match_all('~<img class="[^"]*\bimage_inner_img\b[^"]*"[^>]*?\ssrc="([^"]+)"~', $card, $found);   // и фото, и постер ссылки (outlink_poster_img)
    $images = [];
    foreach ($found[1] as $src) {
        $url = preg_replace('~^http://~i', 'https://', html_attr($src));
        if (preg_match('~^https://(?:i\.okcdn\.ru/|[a-z0-9-]+\.mycdn\.me/)~i', $url) && filter_var($url, FILTER_VALIDATE_URL) !== false) $images[] = $url;
    }
    return array_values(array_unique($images));
}

/** Дата записи: «сегодня 14:05», «вчера», «12 окт», «12 окт 2025», «12 окт 2025 в 14:05», «14:05». Не разобрал — 0. */
function ok_parse_date(string $raw, ?int $now = null): int {
    $raw = mb_strtolower(trim(clean_html_text($raw)));
    $tz = new DateTimeZone(date_default_timezone_get());
    $today = (new DateTimeImmutable('@' . ($now ?? time())))->setTimezone($tz);
    [$hour, $minute] = preg_match('~(\d{1,2}):(\d{2})~', $raw, $t) ? [(int)$t[1], (int)$t[2]] : [0, 0];
    if (str_contains($raw, 'сегодня') || preg_match('~^\d{1,2}:\d{2}$~', $raw)) $day = $today;
    elseif (str_contains($raw, 'вчера')) $day = $today->modify('-1 day');
    elseif (preg_match('~(\d{1,2})\s+([а-яё]+)\.?(?:\s+(\d{4}))?~u', $raw, $m) && isset(OK_MONTHS[mb_substr($m[2], 0, 3)])) {
        $day = $today->setDate(empty($m[3]) ? (int)$today->format('Y') : (int)$m[3], OK_MONTHS[mb_substr($m[2], 0, 3)], (int)$m[1]);
        if (empty($m[3]) && $day > $today->modify('+1 day')) $day = $day->modify('-1 year');   // «18 мар» без года — прошлый год, если «мар» ещё впереди
    } else return 0;
    return $day->setTime($hour, $minute)->getTimestamp();
}

/** Счётчики записи: класс (likes), комментарии, репосты. Нет блока счётчика — 0, нет блока вовсе — null. */
function ok_card_counts(string $card): array {
    $out = ['likes' => null, 'comments' => null, 'reshares' => null];
    foreach (explode('widget-list_i ', $card) as $segment) {
        $key = match (true) { str_starts_with($segment, '__like') => 'likes', str_starts_with($segment, '__comment') => 'comments', str_starts_with($segment, '__reshare') => 'reshares', default => null };
        if ($key !== null) $out[$key] = preg_match('~widget_action_count">\s*([^<]*)<~u', $segment, $m) ? telegram_parse_counter($m[1]) : 0;
    }
    return $out;
}

/** Посты группы из HTML мобильной страницы, самые свежие первыми; без картинки не берутся. */
function ok_parse_feed(string $html, string $group_id, int $count, string $name = '', ?int $now = null): array {
    $source = $name !== '' ? $name : source_display_name(PLATFORM_OK, $group_id);
    $posts = [];
    foreach (array_slice(explode('<li class="item it feed-card', $html), 1) as $card) {
        if (!preg_match('~topicId&quot;:&quot;(\d+)~', $card, $t)) continue;
        $images = ok_card_images($card);
        if (!$images) continue;
        $text = preg_match('~topic-text_content[^"]*"[^>]*>(.*?)(?:<a [^>]*topic-more|<div class="widget-list)~s', $card, $tx) ? clean_html_text($tx[1]) : '';
        $date = preg_match('~class="tstmp feed_descr">([^<]*)<~', $card, $d) ? ok_parse_date($d[1], $now) : 0;
        $posts[] = ['image' => $images[0], 'images' => array_slice($images, 0, 4), 'gallery' => [], 'video' => '', 'video_unavailable' => false, 'engagement' => null,
            'text' => $text !== '' ? mb_substr($text, 0, 1000) : EMPTY_POST_TEXT, 'source' => $source, 'post_id' => $t[1], 'posted_at' => $date,
            'url' => "https://ok.ru/group/$group_id/topic/{$t[1]}"] + ok_card_counts($card);
    }
    usort($posts, fn($a, $b) => $b['posted_at'] <=> $a['posted_at']);
    return array_slice($posts, 0, max(0, $count));
}

/** Превью группы для карточек: ошибка сети — пустой список. */
function ok_preview_posts(array $source, int $count): array {
    try { return ok_fetch_posts((string)($source['id'] ?? ''), $count, (string)($source['name'] ?? '')); } catch (Throwable $e) { return []; }
}

/** Посты первой страницы группы: [image, images, text, source, post_id, posted_at, url, likes, comments, reshares]. */
function ok_fetch_posts(string $group_id, int $count, string $name = ''): array {
    if (!is_valid_ok_group($group_id)) return [];
    return ok_parse_feed(ok_group_html($group_id), $group_id, $count, $name);
}
