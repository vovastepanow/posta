<?php
// Поиск аккаунтов TikTok по теме для автоподбора. yt-dlp поиска не умеет, поэтому страница поиска открывается
// headless Chrome, а разбор — по ссылкам «/@имя». Одновременно — один Chrome, не больше двух поисков за прогон,
// после трёх пустых ответов подряд (обычно капча) поиск ставится на паузу на сутки.

const TIKTOK_SEARCH_TIMEOUT = 60;   // холодный старт Chrome с профилем занимает 30–37 с
const TIKTOK_SEARCH_MAX_USERS = 10;
const TIKTOK_SEARCH_EMPTY_LIMIT = 3;
const TIKTOK_SEARCH_PAUSE_SECONDS = 86400;
const TIKTOK_SEARCH_BROWSER = 'google-chrome';
const TIKTOK_SEARCH_UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36';

/** Авторы роликов из HTML страницы поиска видео: ссылки «/@имя/video/номер», без повторов (свежие популярные ролики, не ники). */
function tiktok_search_video_authors(string $html): array {
    preg_match_all('~/@([A-Za-z0-9_.]{2,24})/video/\d+~', $html, $m);
    $names = array_values(array_unique(array_filter($m[1], 'is_valid_tiktok_user')));
    return array_slice($names, 0, TIKTOK_SEARCH_MAX_USERS);
}

/** Имена аккаунтов из HTML страницы поиска: ссылки «/@имя», без повторов. */
function tiktok_search_names(string $html): array {
    preg_match_all('~href="/@([A-Za-z0-9_.]{2,24})"~', $html, $m);
    $names = array_values(array_unique(array_filter($m[1], 'is_valid_tiktok_user')));
    return array_slice($names, 0, TIKTOK_SEARCH_MAX_USERS);
}

/** Пауза после серии пустых ответов: поиск не запускается до времени из kv. */
function tiktok_search_paused(): bool {
    $until = (int)kv_get('tiktok_search_pause', 0);
    if ($until > time()) cli_echo('-> TikTok: поиск на паузе после пустых ответов (капча?) — до ' . date('d.m H:i', $until) . '.');
    return $until > time();
}

/** Учёт пустых ответов: три подряд — пауза на сутки; непустой ответ сбрасывает счётчик. */
function tiktok_search_note(bool $found): void {
    if ($found) { kv_update('tiktok_search_empty', fn() => 0); return; }
    $empty = kv_update('tiktok_search_empty', fn($n) => (int)$n + 1, 0);
    if ($empty >= TIKTOK_SEARCH_EMPTY_LIMIT) {
        kv_update('tiktok_search_pause', fn() => time() + TIKTOK_SEARCH_PAUSE_SECONDS);
        kv_update('tiktok_search_empty', fn() => 0);
        cli_echo('-> TikTok: ' . TIKTOK_SEARCH_EMPTY_LIMIT . ' пустых ответа подряд — поиск на сутки на паузе.');
    }
}

/**
 * Страница поиска TikTok через headless Chrome: HTML, если страница дошла до конца; null — Chrome не отработал или уже идёт другой поиск.
 * Один процесс за раз, отдельный профиль, только tiktok.com, таймаут. Капча и пустые ответы — в журнале.
 */
function tiktok_search_page(string $path, string $query): ?string {
    ytdlp_ensure_cache_dir();
    $profile = ROOT . '/data/chrome-tiktok';
    if (!is_dir($profile) && !@mkdir($profile, 0775, true) && !is_dir($profile)) throw new RuntimeException('Не удалось создать каталог профиля Chrome.');
    if (!($lock = try_lock(YTDLP_CACHE_DIR . '/.chrome-tiktok.lock'))) {
        cli_echo('-> TikTok: другой поиск уже идёт — пропускаю.');
        return null;
    }
    $home = $profile . '-home';   // под веб-пользователем «домашней» папки нет: Chrome пишет конфиги и базы сертификатов в HOME
    if (!is_dir($home) && !@mkdir($home, 0775, true) && !is_dir($home)) throw new RuntimeException('Не удалось создать домашний каталог Chrome.');
    try {
        // Служба Apache запрещает пространства имён и JIT: песочница Chrome не запускается, поэтому без неё и с --jitless.
        $result = ytdlp_exec(['env', 'HOME=' . $home, TIKTOK_SEARCH_BROWSER, '--headless=new', '--disable-gpu', '--no-first-run', '--no-sandbox', '--no-zygote',
            '--js-flags=--jitless', '--disable-dev-shm-usage', '--user-data-dir=' . $profile, '--virtual-time-budget=15000', '--user-agent=' . TIKTOK_SEARCH_UA,
            '--dump-dom', "https://www.tiktok.com/search/{$path}?q=" . rawurlencode($query)], TIKTOK_SEARCH_TIMEOUT);
    } finally {
        flock($lock, LOCK_UN);
        fclose($lock);
    }
    $html = $result['stdout'];
    if (!($result['ok'] && stripos($html, '</html>') !== false)) {   // Chrome не отработал — это не капча: пауза не копится, причина — в журнале
        error_log('TikTok: Chrome завершился с кодом ' . $result['code'] . ': ' . mb_substr(trim((string)($result['stderr'] ?: $result['stdout'])), 0, 400));
        cli_echo('-> ' . ($result['code'] === -1 ? 'Chrome не ответил вовремя' : 'Chrome не запустился') . ': ' . ytdlp_error_text($result));
        $GLOBALS['tiktok_search_error'] = ytdlp_error_text($result);
        return null;
    }
    return $html;
}

/** Общая проверка запроса: короткий, в пределах лимита, не пусто, не на паузе. */
function tiktok_search_allowed(string $query): bool {
    return $query !== '' && mb_strlen($query) <= 50 && !tiktok_search_paused();
}

/**
 * Авторы свежих популярных роликов по запросу/хештегу (до TIKTOK_SEARCH_MAX_USERS). Это живые аккаунты, в отличие от поиска по нику.
 * Ошибка Chrome, капча или пусто — пустой список и строка в журнале.
 */
function tiktok_search_videos(string $query): array {
    $query = trim($query);
    if (!tiktok_search_allowed($query)) return [];
    if (($html = tiktok_search_page('video', $query)) === null) return [];
    $names = tiktok_search_video_authors($html);
    if (!$names && stripos($html, 'captcha') !== false) cli_echo('-> TikTok не отдал поиск видео (капча?).');
    elseif (!$names) cli_echo("-> TikTok: по видео «{$query}» авторов не нашлось.");
    tiktok_search_note(count($names) > 0);
    return $names;
}

/**
 * Аккаунты TikTok по нику (запасной путь). $query — уже очищенный запрос; в команду он идёт отдельным аргументом.
 */
function tiktok_search_users(string $query): array {
    $query = trim($query);
    if (!tiktok_search_allowed($query)) return [];
    if (($html = tiktok_search_page('user', $query)) === null) return [];
    $names = tiktok_search_names($html);
    if (!$names && stripos($html, 'captcha') !== false) cli_echo('-> TikTok не отдал поиск (капча?).');
    elseif (!$names) cli_echo("-> TikTok по запросу «{$query}» ничего не нашёл.");
    tiktok_search_note(count($names) > 0);
    return $names;
}
