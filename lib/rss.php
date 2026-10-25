<?php
// RSS 2.0 и Atom: посты с картинками в том же виде, что у Telegram (telegram_posts_from_html). Все запросы — только через fetch_public.

const RSS_MAX_BYTES = 2 * 1024 * 1024;
const RSS_MAX_ITEMS = 100;           // записей ленты, которые разбираются до отбора самых свежих

/** Загрузка с переходами по редиректам (до 3): каждый адрес проверяется fetch_public, во внутреннюю сеть не пускает. */
function rss_get($url, $max_bytes, $timeout = 15, $user_agent = HTTP_UA) {
    for ($hop = 0; $hop <= 3; $hop++) {
        $r = fetch_public($url, $max_bytes, null, $timeout, $user_agent);
        if (in_array($r['status'], [301, 302, 303, 307, 308], true) && $r['location'] !== '') { $url = rss_absolute_url($url, $r['location']); continue; }
        return $r + ['url' => $url];
    }
    return ['ok' => false, 'bytes' => '', 'type' => '', 'error' => 'слишком много перенаправлений', 'too_big' => false, 'status' => 0, 'location' => '', 'url' => $url];
}

/** Адрес $href относительно страницы $base. */
function rss_absolute_url($base, $href) {
    $href = html_attr(trim((string)$href));
    if (preg_match('~^https?://~i', $href)) return $href;
    $p = parse_url($base) ?: [];
    $origin = ($p['scheme'] ?? 'https') . '://' . ($p['host'] ?? '');
    if (str_starts_with($href, '//')) return ($p['scheme'] ?? 'https') . ':' . $href;
    if (str_starts_with($href, '/')) return $origin . $href;
    $dir = preg_replace('~[^/]*$~', '', (string)($p['path'] ?? '/'));
    return $origin . ($dir === '' ? '/' : $dir) . $href;
}

/**
 * Разбор XML в дерево узлов ['n' имя, 'a' атрибуты, 'k' дети, 't' текст, 'i' сырая разметка внутри]; null — это не XML.
 * В PHP на сервере нет расширений XML (SimpleXML, DOM), поэтому разбор свой. Он не раскрывает сущности (кроме стандартных
 * &amp; &lt; …) и ничего не загружает по ссылкам: DOCTYPE пропускается, XXE невозможен.
 */
function rss_load($xml) {
    $xml = (string)$xml;
    if (!preg_match_all('~<!\[CDATA\[(.*?)\]\]>|<!--.*?-->|<\?.*?\?>|<!DOCTYPE(?:[^>\[]|\[.*?\])*>|<(/?)([A-Za-z_][\w:.-]*)((?:"[^"]*"|\'[^\']*\'|[^<>"\'])*?)(/?)>|([^<]+)~s', $xml, $tokens, PREG_SET_ORDER | PREG_OFFSET_CAPTURE)) return null;
    $root = ['n' => '#root', 'a' => [], 'k' => [], 't' => '', 'i' => ''];
    $stack = [&$root];
    $starts = [0];
    $got = fn($m, $g) => isset($m[$g]) && $m[$g][1] >= 0 ? $m[$g][0] : null;   // группа сработала?
    foreach ($tokens as $m) {
        $top = &$stack[count($stack) - 1];
        if (($cdata = $got($m, 1)) !== null) { $top['t'] .= $cdata; unset($top); continue; }
        if (($text = $got($m, 6)) !== null) { $top['t'] .= html_entity_decode($text, ENT_QUOTES | ENT_XML1, 'UTF-8'); unset($top); continue; }
        unset($top);
        if (($name = $got($m, 3)) === null) continue;   // комментарий, инструкция, DOCTYPE
        [$closing, $attrs, $self] = [$got($m, 2) === '/', (string)$got($m, 4), $got($m, 5) === '/'];
        if ($closing) {
            for ($i = count($stack) - 1; $i > 0; $i--) {
                if ($stack[$i]['n'] !== $name) continue;
                $stack[$i]['i'] = substr($xml, $starts[$i], $m[0][1] - $starts[$i]);
                while (count($stack) > $i) { array_pop($stack); array_pop($starts); }
                break;
            }
            continue;
        }
        $node = ['n' => $name, 'a' => [], 'k' => [], 't' => '', 'i' => ''];
        if (preg_match_all('~([\w:.-]+)\s*=\s*(?:"([^"]*)"|\'([^\']*)\')~', $attrs, $found, PREG_SET_ORDER)) {
            foreach ($found as $f) $node['a'][$f[1]] = html_entity_decode(($f[2] ?? '') !== '' ? $f[2] : ($f[3] ?? ''), ENT_QUOTES | ENT_XML1, 'UTF-8');
        }
        if (count($stack) > 60) return null;
        $parent = &$stack[count($stack) - 1];
        $parent['k'][] = &$node;
        unset($parent);
        if (!$self) { $stack[] = &$node; $starts[] = $m[0][1] + strlen($m[0][0]); }
        unset($node);
    }
    return $root['k'][0] ?? null;
}

function rss_is_feed_xml($doc) { return is_array($doc) && in_array($doc['n'], ['rss', 'feed', 'rdf:RDF'], true); }

/** Дети узла с таким именем (с префиксом, как в документе: media:content). */
function rss_kids($node, $name) { return is_array($node) ? array_values(array_filter($node['k'], fn($k) => $k['n'] === $name)) : []; }
function rss_kid($node, $name) { return rss_kids($node, $name)[0] ?? null; }

/** HTML элемента: текст (в том числе из CDATA) или, у XHTML-контента Atom, вложенная разметка. */
function rss_element_html($node) {
    if (!is_array($node)) return '';
    return trim($node['t']) !== '' || !$node['k'] ? $node['t'] : $node['i'];
}

/** Адрес https:// из атрибута или текста; иначе ''. */
function rss_https($url, $base = '') {
    $url = $base !== '' && trim((string)$url) !== '' ? rss_absolute_url($base, $url) : html_attr(trim((string)$url));
    return str_starts_with($url, 'https://') && filter_var($url, FILTER_VALIDATE_URL) !== false && strlen($url) <= 1000 ? $url : '';
}

/** Ссылка на запись: RSS link или Atom link[rel=alternate]. */
function rss_item_link($item) {
    foreach (rss_kids($item, 'link') as $l) {
        if (trim($l['t']) !== '') return trim($l['t']);
        if (($rel = $l['a']['rel'] ?? '') === '' || $rel === 'alternate') return trim($l['a']['href'] ?? '');
    }
    return '';
}

/** Файлы-вложения записи: [['url', 'type', 'medium']] из enclosure, media:content, media:thumbnail и Atom link[rel=enclosure]. */
function rss_item_media($item) {
    $files = [];
    $add = function ($node, $default_medium = '') use (&$files) {
        $url = (string)($node['a']['url'] ?? $node['a']['href'] ?? '');
        if ($url !== '') $files[] = ['url' => $url, 'type' => strtolower((string)($node['a']['type'] ?? '')), 'medium' => strtolower((string)($node['a']['medium'] ?? '')) ?: $default_medium];
    };
    foreach (rss_kids($item, 'enclosure') as $e) $add($e);
    foreach (rss_kids($item, 'link') as $l) if (($l['a']['rel'] ?? '') === 'enclosure') $add($l);
    foreach (rss_kids($item, 'media:content') as $c) $add($c);
    foreach (rss_kids($item, 'media:group') as $g) {
        foreach (rss_kids($g, 'media:content') as $c) $add($c);
        foreach (rss_kids($g, 'media:thumbnail') as $t) $add($t, 'thumbnail');   // YouTube: превью лежит внутри media:group
    }
    foreach (rss_kids($item, 'media:thumbnail') as $t) $add($t, 'thumbnail');
    return $files;
}

/** Формат, который конвейер умеет читать (TIFF, SVG и подобное пропускаем). */
function rss_image_ok($url) { return !preg_match('~\.(tiff?|svg|heic|heif|avif|ico|psd)(\?|$)~i', (string)parse_url((string)$url, PHP_URL_PATH) . '?'); }

function rss_is_image(array $f) { return str_starts_with($f['type'], 'image/') || $f['medium'] === 'image' || (($f['type'] === '' && $f['medium'] === '') && preg_match('~\.(jpe?g|png|gif|webp)(\?|$)~i', $f['url'])); }

/** Картинка Pinterest: миниатюры (236x и меньше) заменяются на 736x; остальные адреса не меняются. */
function rss_upgrade_image_url(string $url): string {
    return preg_replace('~^https://i\.pinimg\.com/(?:136x136|236x|474x|564x)/~', 'https://i.pinimg.com/736x/', $url) ?? $url;
}

/** Первая <img src> в HTML. */
function rss_html_image($html, $base) {
    return preg_match('~<img\b[^>]*?\ssrc\s*=\s*["\']([^"\']+)["\']~i', (string)$html, $m) ? rss_https($m[1], $base) : '';
}

function rss_item_time($item) {
    foreach (['pubDate', 'published', 'updated', 'dc:date'] as $tag) {
        if (($raw = trim((string)(rss_kid($item, $tag)['t'] ?? ''))) !== '' && ($t = strtotime($raw)) !== false) return (int)$t;
    }
    return 0;
}

/** Записи ленты: RSS 2.0 (channel/item), Atom (feed/entry), RSS 1.0 (item рядом с channel). */
function rss_items($doc): array {
    if ($doc['n'] === 'feed') return rss_kids($doc, 'entry');
    return rss_kids(rss_kid($doc, 'channel'), 'item') ?: rss_kids($doc, 'item');
}

/** Посты ленты (XML-строка), самые свежие первыми: только с картинкой; у видео картинка — постер. YouTube: в конец текста — ссылка на ролик. */
function rss_parse(string $xml, int $count, string $name = '', string $feed_url = '', bool $keep_no_image = false, bool $with_text = false, int $min_text = 120): array {
    $doc = rss_load($xml);
    if (!rss_is_feed_xml($doc)) throw new RuntimeException('Это не RSS и не Atom.');
    $items = rss_items($doc);
    $source = $name !== '' ? $name : source_display_name(PLATFORM_RSS, (string)parse_url($feed_url, PHP_URL_HOST));
    $youtube = in_array(rss_feed_kind($feed_url), ['YouTube', 'YouTube Shorts'], true);
    $shorts_feed = str_contains($feed_url, 'playlist_id=UUSH');   // лента шортсов канала
    $posts = [];
    foreach (array_slice($items, 0, RSS_MAX_ITEMS) as $item) {
        $url = rss_https(rss_item_link($item));
        $base = $url !== '' ? $url : $feed_url;
        $body = array_map(fn($tag) => rss_element_html(rss_kid($item, $tag)), ['content:encoded', 'description', 'content', 'summary', 'media:description']);
        $files = rss_item_media($item);
        $video_id = $youtube ? trim((string)(rss_kid($item, 'yt:videoId')['t'] ?? '')) : '';
        if (!preg_match('~^[A-Za-z0-9_-]{11}$~', $video_id)) $video_id = '';
        $image = '';
        foreach ($files as $f) if (rss_is_image($f) && $f['medium'] !== 'thumbnail' && rss_image_ok($f['url']) && ($image = rss_https($f['url'], $base)) !== '') break;
        if ($image === '') foreach ($files as $f) if ($f['medium'] === 'thumbnail' && rss_image_ok($f['url']) && ($image = rss_https($f['url'], $base)) !== '') break;
        if ($image === '') foreach ($body as $html) if (($image = rss_html_image($html, $base)) !== '' && rss_image_ok($image)) break; else $image = '';
        if ($image === '' && $video_id !== '') $image = "https://i.ytimg.com/vi/$video_id/hqdefault.jpg";
        $title = clean_html_text(rss_element_html(rss_kid($item, 'title')));
        $description = clean_html_text(array_values(array_filter($body, fn($html) => $html !== ''))[0] ?? '');
        $text = mb_substr(trim($title . ($description !== '' && $description !== $title ? "\n" . $description : '')), 0, 1000);
        $text_only = $image === '' && $with_text && mb_strlen($text) >= $min_text;   // текстовый пост: без картинки, но с достаточным текстом (блоги)
        if ($image === '' && !$keep_no_image && !$text_only) continue;   // конвейер работает с картинками; новости дообогащаются со страницы статьи
        $video = '';
        foreach ($files as $f) if ($f['type'] === 'video/mp4' && ($video = rss_https($f['url'], $base)) !== '') break;
        // Видео YouTube (шорт или обычный ролик) скачивается отдельно (lib/ytdlp.php); у обычного ролика ссылка и так в тексте
        $short = $video_id !== '' && ($shorts_feed || str_contains($url, '/shorts/'));
        if ($video_id !== '' && !$short) $text = trim($text . "\nhttps://www.youtube.com/watch?v=$video_id");
        $extra = $video_id !== '' ? ['video_via' => 'ytdlp', 'video_page' => $short ? "https://www.youtube.com/shorts/$video_id" : "https://www.youtube.com/watch?v=$video_id"] : [];
        $image = rss_upgrade_image_url($image);
        $guid = trim((string)(rss_kid($item, 'guid')['t'] ?? '')) ?: (trim((string)(rss_kid($item, 'id')['t'] ?? '')) ?: ($url ?: $image));
        $posts[] = ['image' => $image, 'images' => $image !== '' ? [$image] : [], 'gallery' => [], 'video' => $video, 'text' => $text !== '' ? $text : EMPTY_POST_TEXT, 'source' => $source,
            'post_id' => substr(sha1($guid), 0, 12), 'posted_at' => rss_item_time($item), 'url' => $url, 'video_unavailable' => false, 'engagement' => null, 'text_post' => $text_only] + $extra;
    }
    usort($posts, fn($a, $b) => $b['posted_at'] <=> $a['posted_at']);
    return array_slice($posts, 0, max(0, $count));
}

/** Свежие посты ленты по адресу. */
function rss_fetch_posts(string $feed_url, int $count, string $name = '', bool $keep_no_image = false, bool $with_text = false): array {
    $r = rss_get($feed_url, RSS_MAX_BYTES, 15);
    if (!$r['ok'] && ($yt = youtube_feed_channel($feed_url)) !== null) {   // YouTube отдаёт 404/500 на ленту: список роликов — через yt-dlp
        cli_echo("YouTube: лента недоступна ({$r['status']}), список роликов взят через yt-dlp");
        return array_slice(youtube_channel_posts($yt[0], $yt[1], $count), 0, max(0, $count));
    }
    if (!$r['ok']) throw new RuntimeException('Лента не загрузилась' . ($r['error'] !== '' ? ': ' . $r['error'] : ''));
    return rss_parse($r['bytes'], $count, $name, $r['url'], $keep_no_image, $with_text);
}

/** Название ленты (channel/title, feed/title), иначе хост. */
function rss_feed_title(string $feed_url): string {
    $host = (string)parse_url($feed_url, PHP_URL_HOST);
    $r = rss_get($feed_url, RSS_MAX_BYTES, 15);
    $doc = $r['ok'] ? rss_load($r['bytes']) : null;
    $title = !rss_is_feed_xml($doc) ? '' : clean_html_text(rss_element_html(rss_kid($doc['n'] === 'feed' ? $doc : rss_kid($doc, 'channel'), 'title')));
    return mb_substr($title !== '' ? $title : $host, 0, 120);
}

/** Адрес ленты по адресу сайта: сам адрес, если это лента, иначе <link rel="alternate"> страницы. */
function rss_discover(string $url): string {
    $r = rss_get($url, 3 * 1024 * 1024, 15);
    if (!$r['ok']) throw new RuntimeException('Страница не загрузилась' . ($r['error'] !== '' ? ': ' . $r['error'] : ''));
    if (rss_is_feed_xml(rss_load($r['bytes']))) return $r['url'];
    preg_match_all('~<link\b[^>]*>~i', $r['bytes'], $tags);
    foreach ($tags[0] as $tag) {
        $attr = fn($n) => preg_match('~\b' . $n . '\s*=\s*(?:"([^"]*)"|\'([^\']*)\')~i', $tag, $m) ? html_attr($m[1] !== '' ? $m[1] : ($m[2] ?? '')) : '';
        if (!preg_match('~\balternate\b~i', $attr('rel')) || !preg_match('~^application/(rss|atom)\+xml~i', trim($attr('type'))) || ($href = $attr('href')) === '') continue;
        return preg_replace('~^http://~i', 'https://', rss_absolute_url($r['url'], $href));
    }
    throw new RuntimeException('На этой странице нет RSS-ленты');
}

/** Ссылка YouTube, Pinterest или Tumblr → адрес ленты без поиска по странице; null — обычный сайт. */
function rss_known_feed(string $url): ?string {
    $p = parse_url($url);
    if (!is_array($p) || empty($p['host'])) return null;
    $host = strtolower($p['host']);
    $host = str_starts_with($host, 'www.') ? substr($host, 4) : $host;
    $parts = array_values(array_filter(explode('/', trim((string)($p['path'] ?? ''), '/')), fn($s) => $s !== ''));
    if ($host === 'tumblr.com' || str_ends_with($host, '.tumblr.com')) return rss_tumblr_feed($host, $parts);
    if (preg_match('~(^|\.)pinterest\.(com|ru)$~', $host)) return rss_pinterest_feed($parts);
    if ($host === 'youtu.be' || $host === 'youtube.com' || str_ends_with($host, '.youtube.com')) return rss_youtube_feed($url, $host === 'youtu.be' ? [] : $parts);
    return null;
}

/** Блог Tumblr: имя.tumblr.com или tumblr.com/имя. */
function rss_tumblr_feed(string $host, array $parts): string {
    $name = strtolower($host === 'tumblr.com' ? (string)($parts[0] ?? '') : substr($host, 0, -strlen('.tumblr.com')));
    if (!preg_match('~^[a-z0-9-]{1,32}$~', $name)) throw new RuntimeException('Нужна ссылка на блог Tumblr: адрес вида имя.tumblr.com');
    return "https://$name.tumblr.com/rss";
}

/** Pinterest: профиль → лента профиля, доска → лента доски. Пины и служебные страницы — ошибка. */
function rss_pinterest_feed(array $parts): string {
    $reserved = ['pin', 'search', 'ideas', 'today', 'explore', 'categories', 'login', 'settings', 'business'];
    [$user, $board] = [(string)($parts[0] ?? ''), (string)($parts[1] ?? '')];
    if (!preg_match('~^[A-Za-z0-9_.-]{1,64}$~', $user) || in_array(strtolower($user), $reserved, true)) throw new RuntimeException('Нужна ссылка на профиль или доску Pinterest');
    if ($board === '') return "https://www.pinterest.com/$user/feed.rss";
    if (!preg_match('~^[A-Za-z0-9_.-]{1,100}$~', $board)) throw new RuntimeException('Нужна ссылка на профиль или доску Pinterest');
    return "https://www.pinterest.com/$user/$board.rss";
}

/** YouTube: /channel/UC… — сразу лента; @имя, /c/имя, /user/имя — канал ищется на странице; видео и прочее — ошибка. */
function rss_youtube_feed(string $url, array $parts): string {
    $head = strtolower((string)($parts[0] ?? ''));
    $shorts = strtolower((string)end($parts)) === 'shorts';   // …/shorts — только шортсы канала
    if ($head === 'channel' && preg_match('~^UC[A-Za-z0-9_-]{22}$~', (string)($parts[1] ?? ''))) return $shorts ? rss_youtube_shorts_feed($parts[1]) : 'https://www.youtube.com/feeds/videos.xml?channel_id=' . $parts[1];
    if (str_starts_with($head, '@') || (in_array($head, ['c', 'user'], true) && isset($parts[1]))) {
        $feed = rss_youtube_channel_feed($url);
        return $shorts && preg_match('~channel_id=(UC[A-Za-z0-9_-]{22})~', $feed, $m) ? rss_youtube_shorts_feed($m[1]) : $feed;
    }
    throw new RuntimeException('Нужна ссылка на канал YouTube');
}

/** Лента шортсов канала: её плейлист — UUSH и id канала без «UC». */
function rss_youtube_shorts_feed(string $channel_id): string {
    return 'https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH' . substr($channel_id, 2);
}

/** Страница канала YouTube → лента канала (channel_id из канонической ссылки или данных страницы). */
function rss_youtube_channel_feed(string $url): string {
    $r = rss_get($url, 3 * 1024 * 1024, 15);
    if (!$r['ok']) throw new RuntimeException('Страница YouTube не загрузилась' . ($r['error'] !== '' ? ': ' . $r['error'] : ''));
    if (!preg_match('~"channelId":"(UC[A-Za-z0-9_-]{22})"|youtube\.com/channel/(UC[A-Za-z0-9_-]{22})~', $r['bytes'], $m)) throw new RuntimeException('Не нашёл канал YouTube по этой ссылке');
    return 'https://www.youtube.com/feeds/videos.xml?channel_id=' . ($m[1] !== '' ? $m[1] : $m[2]);
}

/** Метка площадки для карточек по адресу ленты: YouTube, Pinterest, Tumblr или RSS. */
function rss_feed_kind(string $feed_url): string {
    $host = strtolower((string)parse_url($feed_url, PHP_URL_HOST));
    return match (true) {
        str_contains($feed_url, 'playlist_id=UUSH') => 'YouTube Shorts',
        $host === 'youtube.com' || str_ends_with($host, '.youtube.com') => 'YouTube',
        preg_match('~(^|\.)pinterest\.(com|ru)$~', $host) === 1 => 'Pinterest',
        str_ends_with($host, '.tumblr.com') => 'Tumblr',
        default => 'RSS',
    };
}

/** Превью источника для карточек: ошибка ленты — пустой список. */
function rss_preview_posts(array $source, int $count): array {
    try { return rss_fetch_posts((string)($source['id'] ?? ''), $count, (string)($source['name'] ?? '')); } catch (Throwable $e) { return []; }
}

/** Ссылка записи RSS-источника безопасна для показа: https, хост совпадает с хостом ленты или является его поддоменом (и наоборот). */
function rss_post_url_trusted($item): bool {
    [$key, $url] = [(string)($item['source_key'] ?? ''), (string)($item['source_url'] ?? '')];
    if (!str_starts_with($key, 'rss:') || !str_starts_with($url, 'https://') || filter_var($url, FILTER_VALIDATE_URL) === false) return false;
    $host = fn($u) => preg_replace('~^www\.~', '', strtolower((string)parse_url($u, PHP_URL_HOST)));
    [$feed, $post] = [$host(substr($key, 4)), $host($url)];
    return $feed !== '' && $post !== '' && ($feed === $post || str_ends_with($feed, ".$post") || str_ends_with($post, ".$feed"));
}
