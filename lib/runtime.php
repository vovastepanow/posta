<?php
// Фоновый парсер: запуск и остановка, журнал, очередь слотов и привязка к открытой вкладке.
// Вкладка при открытии занимает сессию (tab_open): прежний парсер останавливается, новый привязывается к ней. Вкладка шлёт
// сигнал раз в 5 с; нет сигналов TAB_SESSION_TTL — парсер останавливается сам. Сессию ведёт последняя открытая вкладка.

const TAB_SESSION_TTL = 150;
const PARSER_QUEUE_MAX_SECONDS = 600;   // сколько сборщик ждёт свободный слот
const PARSER_SESSION_ENDED = 'Окно приложения закрыто или открыто заново — работа остановлена.';

/** Последние строки журнала парсера для консоли сайта. */
function content_parser_activity() {
    $lines = @file(parser_runtime_file('.log'), FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [];
    $lines = array_map(fn($l) => trim(preg_replace('/\s+/u', ' ', $l) ?? $l), array_slice($lines, -400));
    return array_values(array_filter($lines, fn($l) => $l !== '' && !str_starts_with($l, 'PHP Warning:')));
}

function content_parser_process_running($pid) {
    if ($pid < 1) return false;
    $cmd = @file_get_contents("/proc/$pid/cmdline");
    return $cmd !== false ? str_contains(str_replace("\0", ' ', $cmd), PARSER_SCRIPT) : function_exists('posix_kill') && @posix_kill($pid, 0);
}

function parser_state() { return json_decode((string)@file_get_contents(parser_runtime_file('.json')), true) ?: []; }
function content_parser_is_running() { return content_parser_process_running((int)(parser_state()['pid'] ?? 0)); }

/** Сколько секунд парсер работает, или null если он не работает. */
function parser_runtime_seconds() {
    $state = parser_state();
    if (!content_parser_is_running()) return null;
    $started = (int)($state['started_at'] ?? 0);
    return $started > 0 ? time() - $started : null;
}

/** Текущий этап парсера для шапки журнала (сбор, быстрая оценка, полный анализ, поиск источников). Пишется на каждом переходе. */
function parser_phase($text = null) {
    $file = parser_runtime_file('.phase');
    if ($text === null) return trim((string)@file_get_contents($file));
    @file_put_contents($file, $text, LOCK_EX);
    return $text;
}

/** Действие под блокировкой состояния парсера. */
function with_parser_lock(callable $action) {
    if (!function_exists('exec')) return 'unavailable';
    $lock = @fopen(parser_runtime_file('.json') . '.lock', 'c+');
    if (!$lock || !flock($lock, LOCK_EX)) return 'unavailable';
    try { return $action(); } finally { flock($lock, LOCK_UN); fclose($lock); }
}

/** started | running | cooldown | no_session | unavailable. */
function launch_content_parser_locked($force) {
    $state = parser_state();
    if (content_parser_process_running((int)($state['pid'] ?? 0))) return 'running';
    if (!$force && time() - (int)($state['started_at'] ?? 0) < PARSER_COOLDOWN_SECONDS) return 'cooldown';
    $php = PHP_BINARY !== '' && is_executable(PHP_BINARY) ? PHP_BINARY : '/usr/bin/php';
    if (!is_executable($php)) return 'unavailable';
    // Без живой вкладки не запускаем: непривязанный парсер не останавливался бы и держал слот
    if (($tab = tab_session_current_tab()) === '') return 'no_session';
    exec('nohup ' . implode(' ', array_map('escapeshellarg', [$php, PARSER_SCRIPT, $tab, app_user_id()])) . ' > ' . escapeshellarg(parser_runtime_file('.log')) . ' 2>&1 < /dev/null & echo $!', $out, $code);
    if ($code !== 0 || ($pid = (int)trim($out[0] ?? '0')) < 1) return 'unavailable';
    file_put_contents(parser_runtime_file('.json'), json_encode(['pid' => $pid, 'started_at' => time()]), LOCK_EX);
    return 'started';
}

/** SIGTERM, через 3 с — SIGKILL. Обрыв соединения останавливает и генерацию в Ollama. */
function stop_content_parser_locked() {
    $pid = (int)(parser_state()['pid'] ?? 0);
    if (!content_parser_process_running($pid)) return false;
    $signal = fn($n) => function_exists('posix_kill') ? @posix_kill($pid, $n) : @exec("kill -$n $pid 2>/dev/null");
    $signal(15);
    for ($i = 0; $i < 30 && content_parser_process_running($pid); $i++) usleep(100000);
    if (content_parser_process_running($pid)) $signal(9);
    return true;
}

function start_content_parser_if_needed($force = false) { return with_parser_lock(fn() => launch_content_parser_locked($force)); }
function stop_content_parser() { return with_parser_lock('stop_content_parser_locked') === true; }
function restart_content_parser() { return with_parser_lock(function () { stop_content_parser_locked(); return launch_content_parser_locked(true); }); }

/** Ждёт свободный слот (не больше MAX_PARALLEL_PARSERS сборщиков); слот — блокировка на всё время процесса. */
function parser_wait_for_slot() {
    users_ensure_root();
    $since = time();
    for ($notice = 0; ; sleep(2)) {
        for ($slot = 0; $slot < MAX_PARALLEL_PARSERS; $slot++) {
            if ($GLOBALS['parser_slot_handle'] = try_lock(USERS_DIR . "/.slot-$slot.lock")) {
                if (time() - $since > 3) echo "Очередь дошла, начинаю.\n";
                return;
            }
        }
        if (time() - $since > PARSER_QUEUE_MAX_SECONDS) exit("Очередь сборщиков слишком долгая — сбор не запущен.\n");   // не ждём слот бесконечно: иначе ожидающие копятся
        if (time() - $notice >= 20) echo 'Все сборщики заняты другими пользователями — жду очереди (' . (($notice = time()) - $since) . " с)...\n";
    }
}

// ---- Сессия вкладки ----

function tab_session_file() { return parser_runtime_file('.session'); }
function is_valid_tab_id($tab) { return is_string($tab) && preg_match('/^[a-f0-9-]{8,64}$/', $tab) === 1; }
function tab_session_state() { return json_decode((string)@file_get_contents(tab_session_file()), true) ?: []; }
function tab_session_is_fresh(array $s) { return !empty($s['tab']) && time() - (int)($s['seen'] ?? 0) <= TAB_SESSION_TTL; }
function tab_session_current_tab() { $s = tab_session_state(); return tab_session_is_fresh($s) ? (string)$s['tab'] : ''; }

function tab_session_open($tab) {
    update_data(tab_session_file(), [], fn() => ['tab' => $tab, 'seen' => time(), 'opened_at' => time()]);
    return start_content_parser_if_needed(true);   // уже работающий парсер не трогаем: он продолжает и отдаёт журнал новой вкладке
}

/** Сигнал вкладки: owner — ведёт ли она сессию. Уснувшая вкладка возобновляет парсер, ничья сессия забирается. */
function tab_session_heartbeat($tab) {
    $outcome = 'other';
    update_data(tab_session_file(), [], function ($s) use ($tab, &$outcome) {
        $stale = !tab_session_is_fresh($s);
        if (($s['tab'] ?? '') === $tab) $outcome = $stale ? 'resumed' : 'owner';
        elseif ($stale) $outcome = 'claimed';
        else return null;
        return ['tab' => $tab, 'seen' => time(), 'opened_at' => (int)($s['opened_at'] ?? time())];
    });
    if ($outcome === 'other') return ['owner' => false, 'parser' => null];
    return ['owner' => true, 'parser' => ['claimed' => fn() => start_content_parser_if_needed(true), 'resumed' => fn() => start_content_parser_if_needed(true), 'owner' => fn() => topic_is_hot(current_topic()) && content_pool_needs_fill(search_topic_key()) ? start_content_parser_if_needed() : null][$outcome]()];
}

function tab_session_close($tab) {
    $owned = false;
    update_data(tab_session_file(), [], function ($s) use ($tab, &$owned) {
        if (($s['tab'] ?? '') !== $tab) return null;
        $owned = true;
        return ['seen' => 0] + $s;
    });
    return $owned;   // парсер не останавливаем: он доработает сам и его журнал будет виден при следующем открытии
}

/** Парсер: вкладка из аргумента командной строки. */
function parser_bind_tab($tab = null) {
    static $bound = '';
    if ($tab !== null) $bound = is_valid_tab_id($tab) ? $tab : '';
    return $bound;
}

/** Жива ли вкладка парсера (без привязки — ручной запуск, всегда да). Файл читается не чаще раза в 2 с. */
function parser_session_alive() {
    return true;   // процессы не привязаны к вкладке: закрытие окна или открытие другой вкладки их не останавливают
}
