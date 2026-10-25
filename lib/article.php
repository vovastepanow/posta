<?php
// Страница статьи новостного сайта: картинка, описание и основной текст — для записей RSS без картинки или с коротким анонсом.
// Основной текст нужен для выжимки и показа в редакторе; целиком в пост не публикуется. Загрузка — только fetch_public, с кэшем.
// Для vc.ru и dtf.ru данные берутся из API Osnova (lib/osnova.php), остальные сайты — из страницы.

const ARTICLE_MAX_BYTES = 2 * 1024 * 1024;
const ARTICLE_TEXT_MAX = 4000;
const ARTICLE_CACHE_SECONDS = 3 * 86400;
const ARTICLE_SHORT_TEXT = 200;          // текст короче — страницу дочитываем
const ARTICLE_STRIP_TAGS = ['script', 'style', 'noscript', 'nav', 'header', 'footer', 'aside', 'form', 'figure', 'figcaption', 'iframe', 'button', 'svg'];
const ARTICLE_GAP_CHARS = 700;

/** Атрибут meta по имени свойства (og:image, article:published_time…) или ''. */
function article_meta(string $html, string $property): string {
    $q = preg_quote($property, '~');
    if (preg_match('~<meta[^>]+(?:property|name)=["\']' . $q . '["\'][^>]*content=["\']([^"\']*)["\']~i', $html, $m)
        || preg_match('~<meta[^>]+content=["\']([^"\']*)["\'][^>]*(?:property|name)=["\']' . $q . '["\']~i', $html, $m)) {
        return trim(html_entity_decode($m[1], ENT_QUOTES | ENT_HTML5, 'UTF-8'));
    }
    return '';
}

/** Картинка статьи: og:image, затем twitter:image — только https и не обложка-заглушка. */
function article_image(string $html): string {
    foreach (['og:image', 'twitter:image'] as $p) {
        $url = article_meta($html, $p);
        if (str_starts_with($url, 'https://') && filter_var($url, FILTER_VALIDATE_URL) !== false && !article_image_is_stub($url)) return $url;
    }
    return '';
}

/** Время публикации из article:published_time (ISO или 20261023T0226), 0 — не разобрано. */
function article_published(string $html): int {
    $raw = article_meta($html, 'article:published_time');
    if (preg_match('~^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})$~', $raw, $m)) $raw = "$m[1]-$m[2]-$m[3]T$m[4]:$m[5]:00";
    $t = $raw !== '' ? strtotime($raw) : false;
    return $t === false ? 0 : (int)$t;
}

/** Текст блока: без тегов, с пробелами между абзацами. */
function article_plain(string $fragment): string {
    $text = preg_replace('~<br\s*/?>~i', "\n", $fragment);
    $text = html_entity_decode(strip_tags((string)$text), ENT_QUOTES | ENT_HTML5, 'UTF-8');
    return trim(preg_replace('~[ \t\r\f\v]+~u', ' ', $text) ?? '');
}

/**
 * Основной текст статьи: листовые блоки текста (абзацы и div с классом text/content/body/article) по порядку в документе.
 * Блоки, которые идут один за другим (разрыв до ARTICLE_GAP_CHARS), образуют группу; берётся самая длинная.
 * Так находится тело статьи без меню, подписей и блоков «Читайте также».
 */
function article_main_text(string $html): string {
    foreach (ARTICLE_STRIP_TAGS as $tag) $html = preg_replace('~<' . $tag . '\b[^>]*>.*?</' . $tag . '>~is', ' ', $html) ?? $html;
    preg_match_all('~<(p|div)\b([^>]*)>((?:(?!<(?:p|div)\b).)*?)</\1>~is', $html, $m, PREG_SET_ORDER | PREG_OFFSET_CAPTURE);
    $groups = [];
    $group = [];
    $end = -1;
    foreach ($m as $b) {
        if ($b[1][0] === 'div' && !preg_match('~class=["\'][^"\']*(text|content|body|article|paragraph)~i', $b[2][0])) continue;
        $text = article_plain($b[3][0]);
        if (mb_strlen($text) < 40) continue;
        $start = $b[0][1];
        if ($group && $start - $end > ARTICLE_GAP_CHARS) { $groups[] = $group; $group = []; }
        $group[] = $text;
        $end = $start + strlen($b[0][0]);
    }
    if ($group) $groups[] = $group;
    $best = [];
    foreach ($groups as $g) if (array_sum(array_map('mb_strlen', $g)) > array_sum(array_map('mb_strlen', $best))) $best = $g;
    return mb_substr(implode("\n\n", $best), 0, ARTICLE_TEXT_MAX);
}

/** Данные статьи из application/ld+json: заголовок, тело (articleBody) и картинка. Пустые поля — ''. */
function article_jsonld(string $html): array {
    preg_match_all('~<script[^>]+application/ld\+json[^>]*>(.*?)</script>~is', $html, $m);
    foreach ($m[1] as $json) {
        $data = json_decode(trim($json), true);
        foreach (is_array($data) && isset($data['@graph']) ? $data['@graph'] : [$data] as $node) {
            if (!is_array($node) || !array_intersect((array)($node['@type'] ?? []), ['Article', 'NewsArticle', 'BlogPosting', 'ReportageNewsArticle'])) continue;
            $image = is_array($node['image'] ?? null) ? ($node['image'][0] ?? $node['image']['url'] ?? '') : ($node['image'] ?? '');
            if (is_array($image)) $image = $image['url'] ?? '';
            $image = is_string($image) && str_starts_with($image, 'https://') && !article_image_is_stub($image) ? $image : '';
            $text = fn($v) => is_array($v) ? implode("\n", array_filter(array_map(fn($x) => is_string($x) ? $x : '', $v))) : (is_string($v) ? $v : '');   // в schema.org поле бывает списком строк
            return ['headline' => trim($text($node['headline'] ?? '')), 'body' => trim(html_entity_decode(strip_tags($text($node['articleBody'] ?? '')), ENT_QUOTES | ENT_HTML5, 'UTF-8')), 'image' => $image];
        }
    }
    return ['headline' => '', 'body' => '', 'image' => ''];
}

/**
 * «Ключевые слова», а не предложение: SEO-список через запятую (og:description vc.ru). Нет точки/!/? в конце,
 * не меньше трёх запятых и короткие куски (в среднем меньше 6 слов).
 */
function article_is_keyword_list(string $text): bool {
    $text = trim($text);
    if ($text === '' || preg_match('~[.!?…]$~u', $text)) return false;
    $parts = array_values(array_filter(array_map('trim', explode(',', $text)), fn($p) => $p !== ''));
    if (count($parts) < 4) return false;
    $words = array_sum(array_map(fn($p) => count(preg_split('~\s+~u', $p)), $parts));
    return $words / count($parts) < 6;
}

/** Разбор страницы статьи: картинка, описание, время и основной текст (из JSON-LD, если там текст полнее). */
function article_parse(string $html): array {
    $ld = article_jsonld($html);
    $text = article_main_text($html);
    if (mb_strlen($ld['body']) > mb_strlen($text)) $text = mb_substr($ld['body'], 0, ARTICLE_TEXT_MAX);
    return [
        'image' => article_image($html) ?: $ld['image'],
        'description' => mb_substr(article_meta($html, 'og:description') ?: article_meta($html, 'description'), 0, 1000),
        'published' => article_published($html),
        'article_text' => $text,
    ];
}

/** Текст статьи для выжимки: абзацы, заголовки, пункты списков и цитаты; без картинок и видео. */
function article_body_text(array $blocks): string {
    $parts = [];
    foreach ($blocks as $b) {
        if ($b['type'] === 'list') $parts[] = implode("\n", array_column($b['items'], 'text'));
        elseif (in_array($b['type'], ['p', 'h', 'quote'], true)) $parts[] = $b['text'];
    }
    return mb_substr(implode("\n\n", $parts), 0, ARTICLE_TEXT_MAX);
}

/** Блоки для читалки у обычного сайта: абзацы текста без ссылок (разметку чужой страницы не отдаём). */
function article_plain_blocks(string $text): array {
    $out = [];
    foreach (preg_split('~\n{2,}~', $text) ?: [] as $p) if (($p = trim($p)) !== '') $out[] = ['type' => 'p', 'text' => $p, 'links' => []];
    return $out;
}

/** Статья vc.ru / dtf.ru в записи: короткий объект, текст «заголовок + анонс» без SEO-списка, настоящая обложка. */
function article_apply_osnova(array $post, array $a, string $url): array {
    $post['article'] = ['title' => $a['title'], 'lead' => $a['lead'], 'cover' => $a['cover'], 'rubric' => $a['rubric'], 'author' => $a['author'],
        'minutes' => $a['minutes'], 'words' => $a['words'], 'site' => (string)parse_url($url, PHP_URL_HOST)];
    $post['text'] = trim($a['title'] . ($a['lead'] !== '' ? "\n" . $a['lead'] : ''));
    $post['article_text'] = article_body_text($a['blocks']);
    $post['engagement'] = ['source' => 'osnova'] + $a['counters'];
    $cover = (string)($a['cover']['src'] ?? '');
    $image = (string)($post['image'] ?? '');
    if ($cover !== '' && ($image === '' || article_image_is_stub($image))) {   // нет картинки или стоит заглушка — берём настоящую обложку
        $post['image'] = $cover;
        $post['images'] = [$cover];
    } elseif (article_image_is_stub($image)) {                                 // заглушка без настоящей обложки — убираем
        $post['image'] = '';
        $post['images'] = [];
    }
    if (($post['posted_at'] ?? 0) === 0 && $a['published'] > 0) $post['posted_at'] = $a['published'];
    $post['enriched'] = true;
    return $post;
}

/**
 * Дообогащение записи RSS: картинка и текст со страницы статьи. Не вышло — запись как была.
 * Кэш результата по адресу на ARTICLE_CACHE_SECONDS. Загрузка идёт через fetch_public с ограничением частоты на хост.
 * $force — мимо кэша (articles-refresh).
 */
function article_enrich(array $post, bool $force = false): array {
    $url = (string)($post['url'] ?? '');
    if (!str_starts_with($url, 'https://') || filter_var($url, FILTER_VALIDATE_URL) === false) return $post;
    if ($osnova = osnova_article($url, $force)) return article_apply_osnova($post, $osnova, $url);
    $key = 'article:' . sha1($url);
    $cached = $force ? null : kv_get($key);
    // Удачный разбор хранится ARTICLE_CACHE_SECONDS, неудачный — час (сайт мог отдать ошибку временно)
    $ttl = is_array($cached['data'] ?? null) ? ARTICLE_CACHE_SECONDS : 3600;
    if (!is_array($cached) || time() - (int)($cached['at'] ?? 0) > $ttl) {
        try {
            host_throttle((string)parse_url($url, PHP_URL_HOST), 1);
            $r = fetch_public($url, ARTICLE_MAX_BYTES, null, 10);
            $cached = ['at' => time(), 'data' => $r['ok'] ? article_parse((string)$r['bytes']) : null];
        } catch (Throwable $e) {
            $cached = ['at' => time(), 'data' => null];
        }
        kv_update($key, fn() => $cached);
    }
    $data = $cached['data'] ?? null;
    if (!is_array($data)) return $post;
    if (($post['image'] ?? '') === '' && $data['image'] !== '') {
        $post['image'] = $data['image'];
        $post['images'] = [$data['image']];
    }
    if (($data['article_text'] ?? '') !== '') $post['article_text'] = $data['article_text'];
    $description = article_is_keyword_list($data['description']) ? '' : $data['description'];   // SEO-список не берём в текст
    if (($post['text'] ?? '') === '' || mb_strlen((string)$post['text']) < ARTICLE_SHORT_TEXT) {
        $post['text'] = mb_substr(trim(((string)($post['text'] ?? '') !== '' ? $post['text'] . "\n" : '') . $description), 0, 1000) ?: EMPTY_POST_TEXT;
    }
    if (($post['posted_at'] ?? 0) === 0 && $data['published'] > 0) $post['posted_at'] = $data['published'];
    $post['enriched'] = true;
    return $post;
}

/** Статья по адресу для читалки: у vc.ru/dtf.ru — блоки API, у остальных — абзацы текста из кэша. null — не вышло. */
function article_blocks_for(string $url): ?array {
    if ($osnova = osnova_article($url)) return $osnova;
    $article = article_enrich(['url' => $url]);
    $text = trim((string)($article['article_text'] ?? ''));
    return $text === '' ? null : ['title' => '', 'lead' => '', 'cover' => null, 'rubric' => '', 'author' => '', 'published' => 0, 'words' => 0, 'minutes' => 1,
        'counters' => [], 'blocks' => article_plain_blocks($text)];
}
