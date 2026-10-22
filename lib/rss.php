<?php
// RSS 2.0 и Atom: посты с картинками в том же виде, что у Telegram (telegram_posts_from_html). Все запросы — только через fetch_public.

const RSS_MAX_BYTES = 2 * 1024 * 1024;
const RSS_MAX_ITEMS = 100;           // записей ленты, которые разбираются до отбора самых свежих

/** Загрузка с переходами по редиректам (до 3): каждый адрес проверяется fetch_public, во внутреннюю сеть не пускает. */
function rss_get($url, $max_bytes, $timeout = 15) {
    for ($hop = 0; $hop <= 3; $hop++) {
        $r = fetch_public($url, $max_bytes, null, $timeout);
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
    foreach (rss_kids($item, 'media:group') as $g) foreach (rss_kids($g, 'media:content') as $c) $add($c);
    foreach (rss_kids($item, 'media:thumbnail') as $t) $add($t, 'thumbnail');
    return $files;
}

/** Формат, который конвейер умеет читать (TIFF, SVG и подобное пропускаем). */
function rss_image_ok($url) { return !preg_match('~\.(tiff?|svg|heic|heif|avif|ico|psd)(\?|$)~i', (string)parse_url((string)$url, PHP_URL_PATH) . '?'); }

function rss_is_image(array $f) { return str_starts_with($f['type'], 'image/') || $f['medium'] === 'image' || (($f['type'] === '' && $f['medium'] === '') && preg_match('~\.(jpe?g|png|gif|webp)(\?|$)~i', $f['url'])); }

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

/** Посты ленты (XML-строка), самые свежие первыми: только с картинкой; у видео картинка — постер. */
function rss_parse(string $xml, int $count, string $name = '', string $feed_url = ''): array {
    $doc = rss_load($xml);
    if (!rss_is_feed_xml($doc)) throw new RuntimeException('Это не RSS и не Atom.');
    $items = rss_items($doc);
    $source = $name !== '' ? $name : source_display_name(PLATFORM_RSS, (string)parse_url($feed_url, PHP_URL_HOST));
    $posts = [];
    foreach (array_slice($items, 0, RSS_MAX_ITEMS) as $item) {
        $url = rss_https(rss_item_link($item));
        $base = $url !== '' ? $url : $feed_url;
        $body = array_map(fn($tag) => rss_element_html(rss_kid($item, $tag)), ['content:encoded', 'description', 'content', 'summary']);
        $files = rss_item_media($item);
        $image = '';
        foreach ($files as $f) if (rss_is_image($f) && $f['medium'] !== 'thumbnail' && rss_image_ok($f['url']) && ($image = rss_https($f['url'], $base)) !== '') break;
        if ($image === '') foreach ($files as $f) if ($f['medium'] === 'thumbnail' && rss_image_ok($f['url']) && ($image = rss_https($f['url'], $base)) !== '') break;
        if ($image === '') foreach ($body as $html) if (($image = rss_html_image($html, $base)) !== '' && rss_image_ok($image)) break; else $image = '';
        if ($image === '') continue;   // конвейер работает с картинками
        $video = '';
        foreach ($files as $f) if ($f['type'] === 'video/mp4' && ($video = rss_https($f['url'], $base)) !== '') break;
        $title = clean_html_text(rss_element_html(rss_kid($item, 'title')));
        $description = clean_html_text($body[0] !== '' ? $body[0] : ($body[1] !== '' ? $body[1] : ($body[2] !== '' ? $body[2] : $body[3])));
        $text = mb_substr(trim($title . ($description !== '' && $description !== $title ? "\n" . $description : '')), 0, 1000);
        $guid = trim((string)(rss_kid($item, 'guid')['t'] ?? '')) ?: (trim((string)(rss_kid($item, 'id')['t'] ?? '')) ?: ($url ?: $image));
        $posts[] = ['image' => $image, 'images' => [$image], 'gallery' => [], 'video' => $video, 'text' => $text !== '' ? $text : EMPTY_POST_TEXT, 'source' => $source,
            'post_id' => substr(sha1($guid), 0, 12), 'posted_at' => rss_item_time($item), 'url' => $url, 'video_unavailable' => false, 'engagement' => null];
    }
    usort($posts, fn($a, $b) => $b['posted_at'] <=> $a['posted_at']);
    return array_slice($posts, 0, max(0, $count));
}

/** Свежие посты ленты по адресу. */
function rss_fetch_posts(string $feed_url, int $count, string $name = ''): array {
    $r = rss_get($feed_url, RSS_MAX_BYTES, 15);
    if (!$r['ok']) throw new RuntimeException('Лента не загрузилась' . ($r['error'] !== '' ? ': ' . $r['error'] : ''));
    return rss_parse($r['bytes'], $count, $name, $r['url']);
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
    $r = rss_get($url, 1024 * 1024, 15);
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
