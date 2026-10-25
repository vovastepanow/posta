<?php
// Фильтр украинского контента (UA_FILTER_PROMPT.md): источники из Украины, тексты на украинском и посты имиджборд с флагом UA.
// Переключатель — kv admin:ua_filter, по умолчанию включён. Работает по уже скачанным данным: ни сети, ни модели здесь нет.
// Посты российских источников, где просто упоминается Украина, не трогаем: фильтр по происхождению и языку, а не по теме.

const UA_FILTER_WORDS = ['що', 'це', 'який', 'яка', 'які', 'та', 'але', 'від', 'для', 'щоб', 'також', 'вже', 'дуже', 'ще', 'тут', 'бути', 'буде',
    'немає', 'україни', 'україна', 'київ', 'гривень'];   // частые украинские слова: нужны два и больше, если нет букв ї є ґ
const UA_SOURCE_POSTS_MAX = 3;   // украинских постов за проход, чтобы запомнить источник как украинский
const UA_ZONES = ['ua', 'укр'];
const UA_ZONE_PUNYCODE = ['укр' => 'xn--j1amh'];

/** Нормализация домена или id для списка блокировки: нижний регистр, без схемы, www., @ и пути. */
function ua_norm_entry(string $value): string {
    $value = mb_strtolower(trim($value));
    $value = preg_replace(['~^https?://~u', '~^www\.~u', '~^@~u', '~/.*$~u'], ['', '', '', ''], $value) ?? $value;
    return trim($value);
}

/** Идентификатор источника для списка: как в source_id_for() — нижний регистр, без пробелов; путь не трогаем (rss:https://…). */
function ua_norm_source(string $value): string {
    return mb_strtolower(trim($value));
}

/** Список блокировки: из kv (правки админки) или из data/ua_blocklist.php. Возвращает ['domains' => [...], 'sources' => [...]]. */
function ua_blocklist_load(): array {
    $list = kv_get('admin:ua_blocklist');
    if (!is_array($list)) $list = is_file(ROOT . '/data/ua_blocklist.php') ? require ROOT . '/data/ua_blocklist.php' : [];
    return ['domains' => array_values(array_unique(array_filter(array_map('ua_norm_entry', (array)($list['domains'] ?? [])), 'strlen'))),
        'sources' => array_values(array_unique(array_filter(array_map('ua_norm_source', (array)($list['sources'] ?? [])), 'strlen')))];
}

/** Состояние фильтра — переключатель, список блокировки, запомненные решения. Читается не чаще раза в 30 с (парсер живёт долго). */
function ua_state(bool $fresh = false): array {
    static $state = null, $read = 0;
    if ($fresh || $state === null || time() - $read >= 30) {
        $state = ['on' => (bool)kv_get('admin:ua_filter', true), 'blocklist' => ua_blocklist_load(),
            'blocked' => (array)kv_get('admin:ua_blocked_sources', []), 'allowed' => (array)kv_get('admin:ua_allowed_sources', [])];
        $read = time();
    }
    return $state;
}

function ua_filter_on(): bool { return ua_state()['on']; }

function ua_filter_set(bool $on): void {
    kv_update('admin:ua_filter', fn() => $on);
    ua_state(true);
}

/** Сохранить список блокировки из админки или tools/cli.php: домены и id чистятся, пустые выкидываются. */
function ua_blocklist_save(array $list): array {
    $clean = ['domains' => array_values(array_unique(array_filter(array_map('ua_norm_entry', (array)($list['domains'] ?? [])), 'strlen'))),
        'sources' => array_values(array_unique(array_filter(array_map('ua_norm_source', (array)($list['sources'] ?? [])), 'strlen')))];
    kv_update('admin:ua_blocklist', fn() => $clean);
    ua_state(true);
    return $clean;
}

/**
 * Текст на украинском: есть ї, є или ґ; либо і (без ў, которое есть в белорусском) и два и больше частых украинских слова.
 * Ссылки, @упоминания и #хэштеги перед проверкой убираются. Короткие тексты тоже проверяются.
 */
function text_is_ukrainian(string $text): bool {
    $clean = preg_replace(['~https?://\S+~u', '~[@#]\S+~u'], ' ', $text) ?? $text;
    if (trim($clean) === '') return false;
    if (preg_match('/[їєґЇЄҐ]/u', $clean)) return true;
    if (!preg_match('/[іІ]/u', $clean) || preg_match('/[ўЎ]/u', $clean)) return false;
    $found = 0;
    foreach (UA_FILTER_WORDS as $word) {
        if (preg_match('/(?<!\p{L})' . preg_quote($word, '/') . '(?!\p{L})/ui', $clean)) $found++;
        if ($found >= 2) return true;
    }
    return false;
}

/** Адрес ведёт на украинский домен или зону: хост или родительский домен из списка; зоны ua и укр — по окончанию хоста (и по Punycode). */
function ua_url_blocked(string $url): bool {
    if (!ua_filter_on() || $url === '') return false;
    $host = mb_strtolower((string)parse_url($url, PHP_URL_HOST));
    if ($host === '') return false;
    $host = preg_replace('~^www\.~u', '', $host) ?? $host;
    foreach (ua_state()['blocklist']['domains'] as $domain) {
        if (in_array($domain, UA_ZONES, true)) {
            if (str_ends_with($host, '.' . $domain) || str_ends_with($host, '.' . (UA_ZONE_PUNYCODE[$domain] ?? '~'))) return true;
            continue;
        }
        if ($host === $domain || str_ends_with($host, '.' . $domain)) return true;
    }
    return false;
}

/** Прямые признаки в названии или описании: флаг Украины, «Украина/Україна» рядом с «наш», «новости», «канал», «ЗСУ», «ВСУ». Без модели. */
function ua_direct_marks(string $text): bool {
    return (bool)preg_match('~🇺🇦|(?:украин|україн)\p{L}*[^.!?\n]{0,40}(?:наш|наша|новост|канал|зсу|всу)|(?:наш|наша|новост|канал|зсу|всу)[^.!?\n]{0,40}(?:украин|україн)\p{L}*~iu', $text);
}

/**
 * Источник из Украины: причина или null. Порядок: разрешено вручную (тогда источник не блокируем), список, запомненный отказ,
 * адрес, название и описание, страна сообщества VK (id 2 = Украина), страна канала YouTube, если yt-dlp её отдал.
 */
function ua_source_blocked(string $source_id, array $info = []): ?string {
    if (!ua_filter_on()) return null;
    $state = ua_state();
    $sid = mb_strtolower($source_id);
    if (in_array($sid, $state['allowed'], true)) return null;
    if (in_array($sid, $state['blocklist']['sources'], true)) return 'в списке блокировки';
    if (isset($state['blocked'][$sid])) return $state['blocked'][$sid]['reason'];
    if (str_starts_with($sid, 'rss:') && ua_url_blocked(substr($sid, 4))) return 'сайт или лента на украинском домене';
    if (isset($info['url']) && ua_url_blocked((string)$info['url'])) return 'адрес на украинском домене';
    $text = trim((string)($info['title'] ?? '') . "\n" . (string)($info['description'] ?? ''));
    if ($text !== '' && text_is_ukrainian($text)) return 'украинский текст в названии или описании';
    if ($text !== '' && ua_direct_marks($text)) return 'украинская символика в названии или описании';
    $country = $info['country'] ?? null;   // VK: groups.getById, country — объект с id
    if ((is_array($country) ? (int)($country['id'] ?? 0) : (int)$country) === 2 && str_starts_with($sid, 'vk:')) return 'сообщество из Украины (VK)';
    if (strtoupper((string)($info['channel_country'] ?? $info['location'] ?? '')) === 'UA') return 'страна канала — Украина';
    return null;
}

/**
 * Пост из украинского источника или на украинском: причина или null. Проверки: флаг UA имиджборды, источник, адреса
 * статьи или поста, любой текст (подпись, заголовок, текст статьи или треда) на украинском.
 */
function ua_item_blocked(array $item): ?string {
    if (!ua_filter_on()) return null;
    if (strtoupper((string)($item['country'] ?? '')) === 'UA') return 'флаг Украины на имиджборде';
    if (($reason = ua_source_blocked(content_source_key($item))) !== null) return "источник: {$reason}";
    foreach (['url', 'link', 'article_url'] as $field) if (ua_url_blocked((string)($item[$field] ?? ''))) return 'адрес поста из Украины';
    $texts = [analysis_text($item), (string)($item['text'] ?? ''), (string)($item['title'] ?? ''), ...array_map('strval', (array)($item['captions'] ?? []))];
    foreach ($texts as $text) if (text_is_ukrainian((string)$text)) return 'текст на украинском';
    return null;
}

/** Источник запомнен как украинский: id → ['name', 'reason', 'at']. Запись — kv admin:ua_blocked_sources. */
function ua_block_source(string $sid, string $name, string $reason): void {
    kv_update('admin:ua_blocked_sources', fn($list) => [...(array)$list, mb_strtolower($sid) => ['name' => $name, 'reason' => $reason, 'at' => time()]]);
    ua_state(true);
}

/** Снять блокировку вручную (ложное срабатывание): источник больше не блокируется автоматически; язык постов всё равно проверяется. */
function ua_unblock_source(string $sid): void {
    $sid = mb_strtolower($sid);
    kv_update('admin:ua_blocked_sources', function ($list) use ($sid) { $list = (array)$list; unset($list[$sid]); return $list; });
    kv_update('admin:ua_allowed_sources', fn($list) => array_values(array_unique([...(array)$list, $sid])));
    ua_state(true);
}

/** Сколько постов отсеяно за последние сутки: один вызов на проход (не на каждый пост). */
function ua_count_skipped(int $n): void {
    if ($n <= 0) return;
    kv_update('admin:ua_stats', fn($list) => array_values(array_filter([...(array)$list, [time(), $n]], fn($e) => time() - (int)$e[0] < DAY_SECONDS)));
}

function ua_skipped_last_day(): int {
    return array_sum(array_map(fn($e) => (int)$e[1], array_filter((array)kv_get('admin:ua_stats', []), fn($e) => time() - (int)$e[0] < DAY_SECONDS)));
}

/** Посты поиска (лучшие посты, зеркала): украинские убираем. Пусто, если из непустого списка ничего не осталось — вызывающий решает, что сказать. */
function ua_filter_posts(array $posts): array {
    return array_values(array_filter($posts, fn($p) => ua_item_blocked($p) === null));
}

/** Запись не показываем и не публикуем: помечена как украинская или её текст/источник украинские (при включённом фильтре). */
function ua_record_hidden(array $r): bool {
    return ua_filter_on() && (!empty($r['ua_blocked']) || ua_item_blocked($r) !== null);
}
