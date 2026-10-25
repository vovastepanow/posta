<?php
// Статьи платформы Osnova (vc.ru, dtf.ru): чистые данные из открытого API content?id=.
// Ответ приводится к общему виду статьи: без HTML, без служебных блоков; сгенерированная обложка не берётся.
// Загрузка — через fetch_public с ограничением частоты на хост и кэшем kv (ARTICLE_CACHE_SECONDS).

const OSNOVA_HOSTS = ['vc.ru' => 'api.vc.ru', 'dtf.ru' => 'api.dtf.ru'];
const OSNOVA_LEAD_MAX = 400;
const OSNOVA_FAIL_SECONDS = 3600;          // неудачный разбор помним час: сайт мог отдать ошибку временно
const OSNOVA_WORDS_PER_MINUTE = 180;
const ARTICLE_COVER_STUB = '~^https://api\.(?:vc|dtf)\.ru/[^?]*/cover/~';   // сгенерированная обложка с заголовком текстом

/** Картинка-заглушка (обложка, нарисованная сайтом с заголовком): никогда не показываем как картинку статьи. */
function article_image_is_stub(string $url): bool {
    return preg_match(ARTICLE_COVER_STUB, $url) === 1;
}

/** Адрес статьи → [api-хост, id, хост сайта] или null: не vc.ru / dtf.ru или нет числового id в пути. */
function osnova_target(string $url): ?array {
    $host = preg_replace('~^(?:www|m)\.~', '', strtolower((string)parse_url($url, PHP_URL_HOST)));
    if (!isset(OSNOVA_HOSTS[$host])) return null;
    if (!preg_match('~/(\d{5,12})(?:-|/|$)~', (string)parse_url($url, PHP_URL_PATH), $m)) return null;
    return [OSNOVA_HOSTS[$host], $m[1], $host];
}

/** Текст фрагмента HTML: теги убраны, сущности раскодированы, пробелы схлопнуты. */
function osnova_text(string $html): string {
    $text = html_entity_decode(strip_tags(preg_replace('~<br\s*/?>~i', "\n", $html) ?? ''), ENT_QUOTES | ENT_HTML5, 'UTF-8');
    return trim(preg_replace('~[ \t\r\f\v\x{00A0}]+~u', ' ', $text) ?? '');
}

/** Ссылки фрагмента: [текст, адрес]. Перенаправления vc.ru (redirect?to=) раскодируются в настоящий адрес; не https — отбрасываются. */
function osnova_links(string $html): array {
    preg_match_all('~<a\b[^>]*href="([^"]+)"[^>]*>(.*?)</a>~is', $html, $m, PREG_SET_ORDER);
    $out = [];
    foreach ($m as $a) {
        $href = html_entity_decode($a[1], ENT_QUOTES | ENT_HTML5, 'UTF-8');
        if (preg_match('~redirect\?(?:[^"]*&)?to=([^&]+)~', $href, $r)) $href = urldecode($r[1]);
        $label = osnova_text($a[2]);
        if ($label !== '' && str_starts_with($href, 'https://') && filter_var($href, FILTER_VALIDATE_URL) !== false) $out[] = [$label, $href];
    }
    return $out;
}

/** Абзацы HTML-блока текста: [['type' => 'p', 'text', 'links']]. Строки из одних хэштегов (#новости) — служебные, выбрасываем. */
function osnova_paragraphs(string $html): array {
    $out = [];
    foreach (preg_split('~</p>~i', $html) ?: [] as $chunk) {
        $text = osnova_text($chunk);
        if ($text === '' || preg_match('~^(?:#\S+\s*)+$~u', $text)) continue;
        $out[] = ['type' => 'p', 'text' => $text, 'links' => osnova_links($chunk)];
    }
    return $out;
}

/** Картинка блока media: адрес по uuid (уменьшенная копия 1300 px), размеры. null — не картинка. */
function osnova_image(array $image): ?array {
    $uuid = (string)($image['data']['uuid'] ?? '');
    if (preg_match('~^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$~i', $uuid) !== 1) return null;
    return ['src' => "https://leonardo.osnova.io/$uuid/-/resize/1300/", 'width' => (int)($image['data']['width'] ?? 0), 'height' => (int)($image['data']['height'] ?? 0)];
}

/** Обложка статьи: картинка блока media с cover, иначе первая картинка статьи. */
function osnova_cover(array $raw): ?array {
    foreach ([true, false] as $only_cover) {
        foreach ($raw as $b) {
            if (($b['type'] ?? '') !== 'media' || (bool)($b['cover'] ?? false) !== $only_cover) continue;
            foreach ($b['data']['items'] ?? [] as $item) if (($img = osnova_image($item['image'] ?? [])) !== null) return $img;
        }
    }
    return null;
}

/** Блоки статьи в общем виде: p (абзац), h, list, quote, image, video, embed. Служебное (разделители, хэштеги) пропускается. */
function osnova_blocks(array $raw, string $url): array {
    $out = [];
    foreach ($raw as $b) {
        $type = (string)($b['type'] ?? '');
        $data = (array)($b['data'] ?? []);
        if ($type === 'text') { array_push($out, ...osnova_paragraphs((string)($data['text'] ?? ''))); continue; }
        if ($type === 'header' && ($t = osnova_text((string)($data['text'] ?? ''))) !== '') { $out[] = ['type' => 'h', 'text' => $t]; continue; }
        if ($type === 'list') { $out[] = ['type' => 'list', 'items' => array_values(array_filter(array_map(fn($i) => ['text' => osnova_text((string)$i), 'links' => osnova_links((string)$i)], $data['items'] ?? []), fn($i) => $i['text'] !== ''))]; continue; }
        if ($type === 'quote' && ($t = osnova_text((string)($data['text'] ?? ''))) !== '') { $out[] = ['type' => 'quote', 'text' => $t]; continue; }
        if ($type === 'media') { foreach ($data['items'] ?? [] as $item) if (($img = osnova_image($item['image'] ?? [])) !== null) $out[] = ['type' => 'image'] + $img + ['caption' => osnova_text((string)($item['title'] ?? ''))]; continue; }
        if ($type === 'video') { $out[] = ['type' => 'video', 'title' => osnova_text((string)($data['title'] ?? '')), 'url' => $url]; continue; }   // видео — ссылка на статью-источник
        if ($type === 'osnovaEmbed') {
            $e = (array)($data['osnovaEmbed']['data'] ?? []);
            if (($t = osnova_text((string)($e['title'] ?? ''))) !== '') $out[] = ['type' => 'embed', 'title' => $t, 'description' => osnova_text((string)($e['description'] ?? ''))];   // адреса в ответе нет: мини-карточка без ссылки
        }
    }
    return $out;
}

/** Первый абзац: из текстового блока с cover, иначе первый абзац статьи. Не длиннее OSNOVA_LEAD_MAX, по границе слова. */
function osnova_lead(array $raw, array $blocks): string {
    $lead = '';
    foreach ($raw as $b) {
        if (($b['type'] ?? '') === 'text' && !empty($b['cover'])) { $lead = osnova_paragraphs((string)($b['data']['text'] ?? ''))[0]['text'] ?? ''; if ($lead !== '') break; }
    }
    if ($lead === '') foreach ($blocks as $b) if ($b['type'] === 'p') { $lead = $b['text']; break; }
    if (mb_strlen($lead) <= OSNOVA_LEAD_MAX) return $lead;
    $cut = mb_substr($lead, 0, OSNOVA_LEAD_MAX);
    return rtrim(mb_substr($cut, 0, (int)mb_strrpos($cut, ' ') ?: OSNOVA_LEAD_MAX)) . '…';
}

/** Слова статьи (абзацы, заголовки, пункты списков, цитаты). */
function osnova_word_count(array $blocks): int {
    $words = 0;
    foreach ($blocks as $b) {
        $text = match ($b['type']) { 'list' => implode(' ', array_column($b['items'], 'text')), 'image', 'video', 'embed' => '', default => (string)($b['text'] ?? '') };
        $words += $text === '' ? 0 : count(preg_split('~\s+~u', $text));
    }
    return $words;
}

/** Ответ API content?id= → статья общего вида; null — ответ пустой или не статья. Чистая функция: проверяется на фикстурах. */
function osnova_from_api(string $json, string $url): ?array {
    $res = json_decode($json, true)['result'] ?? null;
    if (!is_array($res) || !is_array($res['blocks'] ?? null)) return null;
    $raw = $res['blocks'];
    $blocks = osnova_blocks($raw, $url);
    $words = osnova_word_count($blocks);
    $reactions = array_sum(array_column((array)($res['reactions']['counters'] ?? []), 'count'));
    $counters = (array)($res['counters'] ?? []);
    return [
        'title' => osnova_text((string)($res['title'] ?? '')),
        'lead' => osnova_lead($raw, $blocks),
        'cover' => osnova_cover($raw),
        'rubric' => osnova_text((string)($res['subsite']['name'] ?? '')),
        'author' => osnova_text((string)($res['author']['name'] ?? '')),
        'published' => (int)($res['date'] ?? 0),
        'words' => $words,
        'minutes' => max(1, (int)round($words / OSNOVA_WORDS_PER_MINUTE)),
        'counters' => ['views' => (int)($counters['views'] ?? 0), 'comments' => (int)($counters['comments'] ?? 0), 'likes' => (int)$reactions],
        'blocks' => $blocks,
    ];
}

/** Статья vc.ru / dtf.ru по адресу: из кэша kv или из API через fetch_public. $force — мимо кэша. null — не Osnova или не вышло. */
function osnova_article(string $url, bool $force = false): ?array {
    if (!($target = osnova_target($url))) return null;
    [$api, $id] = $target;
    $key = "osnova:$id";
    $cached = $force ? null : kv_get($key);
    $ttl = is_array($cached['data'] ?? null) ? ARTICLE_CACHE_SECONDS : OSNOVA_FAIL_SECONDS;
    if (!is_array($cached) || time() - (int)($cached['at'] ?? 0) > $ttl) {
        try {
            host_throttle($api, 1);
            $r = fetch_public("https://$api/v2.10/content?id=$id", ARTICLE_MAX_BYTES, null, 10);
            $cached = ['at' => time(), 'data' => $r['ok'] ? osnova_from_api((string)$r['bytes'], $url) : null];
        } catch (Throwable $e) {
            $cached = ['at' => time(), 'data' => null];
        }
        kv_update($key, fn() => $cached);
    }
    return $cached['data'] ?? null;
}
