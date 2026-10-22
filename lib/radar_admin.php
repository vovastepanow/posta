<?php
// Админка радара лидов (admin/index.php): вход по паролю владельца, аккаунт радара, поиск чатов, проход и журнал, лиды.
// Код админки не создаёт и не привязывает пользователей приложения: uid берётся из kv `admin:radar` и передаётся явно.

require_once __DIR__ . '/lead_radar.php';

const ADMIN_SESSION_TTL = 43200;      // 12 часов бездействия
const ADMIN_LOGIN_FAILS = 5;          // неудачных входов за окно
const ADMIN_LOGIN_WINDOW = 900;       // окно подсчёта и блокировки, секунд
const ADMIN_GET = ['session', 'accounts', 'settings', 'scan_log', 'leads', 'stats', 'rejected'];
const ADMIN_POST = ['login', 'logout', 'account_select', 'settings_save', 'chats_search', 'scan_start', 'lead_status', 'rejected_retry'];
const ADMIN_LEAD_STATUSES = ['new', 'interested', 'replied', 'spam', 'ignored'];
const ADMIN_DEFAULT_QUERIES = ['автопостинг', 'чат админов телеграм', 'админы каналов', 'smm чат', 'продвижение телеграм канала',
    'контент для канала', 'отложенный постинг', 'владельцы каналов', 'ведение телеграм канала', 'реклама в телеграм каналах'];

// ---- Пароль и сессия ----

function admin_password_set($password) {
    if (mb_strlen((string)$password) < 10) throw new RuntimeException('Пароль короче 10 символов.');
    kv_update('admin:password_hash', fn() => password_hash($password, PASSWORD_DEFAULT));
}

function admin_password_hash(): ?string {
    $hash = kv_get('admin:password_hash');
    return is_string($hash) && $hash !== '' ? $hash : null;
}

/** Своя PHP-сессия владельца: отдельное имя, cookie только для каталога админки. */
function admin_session_start() {
    $https = ($_SERVER['HTTPS'] ?? '') !== '' && $_SERVER['HTTPS'] !== 'off';
    ini_set('session.gc_maxlifetime', (string)ADMIN_SESSION_TTL);
    session_name('posta_admin');
    session_set_cookie_params(['lifetime' => 0, 'path' => rtrim(dirname($_SERVER['SCRIPT_NAME'] ?? '/admin/index.php'), '/') . '/',
        'secure' => $https, 'httponly' => true, 'samesite' => 'Strict']);
    session_start();
    if (!empty($_SESSION['admin']) && time() - (int)($_SESSION['last'] ?? 0) > ADMIN_SESSION_TTL) $_SESSION = [];
    if (empty($_SESSION['csrf'])) $_SESSION['csrf'] = bin2hex(random_bytes(16));
    if (!empty($_SESSION['admin'])) $_SESSION['last'] = time();
}

function admin_is_logged(): bool { return !empty($_SESSION['admin']); }

function admin_csrf_ok(): bool {
    return hash_equals((string)($_SESSION['csrf'] ?? ''), (string)($_SERVER['HTTP_X_ADMIN_CSRF'] ?? ''));
}

/** Неудачные входы с одного IP за последние 15 минут. */
function admin_fail_key(): string { return 'admin:fails:' . sha1((string)($_SERVER['REMOTE_ADDR'] ?? '')); }

function admin_fails_recent(): array {
    return array_values(array_filter((array)kv_get(admin_fail_key(), []), fn($t) => $t > time() - ADMIN_LOGIN_WINDOW));
}

function admin_login($password) {
    if (count(admin_fails_recent()) >= ADMIN_LOGIN_FAILS) throw new AdminHttpError('Слишком много попыток, подождите 15 минут.', 429);
    $hash = admin_password_hash() ?? throw new RuntimeException('Пароль не задан: php tools/cli.php admin-password');
    if (!password_verify((string)$password, $hash)) {
        kv_update(admin_fail_key(), fn() => [...admin_fails_recent(), time()]);
        throw new AdminHttpError('Неверный пароль.', 401);
    }
    kv_update(admin_fail_key(), fn() => []);
    session_regenerate_id(true);
    $_SESSION = ['admin' => true, 'csrf' => bin2hex(random_bytes(16)), 'last' => time()];
    return ['csrf' => $_SESSION['csrf']];
}

/** Ошибка админки с HTTP-кодом (401, 403, 404, 429). */
class AdminHttpError extends RuntimeException {
    public function __construct(string $message, public int $status = 400) { parent::__construct($message); }
}

// ---- Действия API: функция admin_api_<действие>($input) возвращает массив ответа ----

function admin_api_session($in) {
    return ['admin' => admin_is_logged(), 'csrf' => $_SESSION['csrf'], 'configured' => admin_password_hash() !== null];
}

function admin_api_login($in) { return admin_login($in['password'] ?? ''); }

function admin_api_logout($in) {
    $_SESSION = [];
    session_destroy();
    return [];
}

// ---- Аккаунт радара и настройки ----

/** Подключённые личные аккаунты Telegram пользователей приложения. */
function admin_connected_uids(): array {
    return array_values(array_filter(user_ids(), fn($uid) => telegram_user_connection($uid) !== null));
}

/** uid аккаунта радара: выбранный владельцем или единственный подключённый. */
function admin_account_uid(): ?string {
    $uid = kv_get('admin:radar', [])['account_uid'] ?? null;
    $connected = admin_connected_uids();
    if ($uid !== null && in_array($uid, $connected, true)) return $uid;
    if (count($connected) !== 1) return null;
    kv_update('admin:radar', fn($c) => ['account_uid' => $connected[0]] + (array)$c);
    return $connected[0];
}

function admin_account_required(): string {
    return admin_account_uid() ?? throw new RuntimeException('Нет аккаунта Telegram: подключите его в приложении → Личный кабинет или выберите аккаунт.');
}

function admin_api_accounts($in) {
    $selected = admin_account_uid();
    return ['accounts' => array_map(fn($uid) => ['uid' => $uid, 'short' => substr($uid, 0, 8),
        'created_at' => (int)db_value('SELECT created_at FROM users WHERE id = ?', [$uid]), 'selected' => $uid === $selected], admin_connected_uids())];
}

function admin_api_account_select($in) {
    $uid = (string)($in['uid'] ?? '');
    if (!in_array($uid, admin_connected_uids(), true)) throw new RuntimeException('Такого подключённого аккаунта нет.');
    kv_update('admin:radar', fn() => ['account_uid' => $uid]);
    return [];
}

function admin_queries(array $settings): array {
    return array_values($settings['queries'] ?? []) ?: ADMIN_DEFAULT_QUERIES;
}

function admin_api_settings($in) {
    $uid = admin_account_uid();
    if ($uid === null) return ['account_uid' => null, 'enabled' => false, 'chats' => [], 'queries' => ADMIN_DEFAULT_QUERIES, 'last_run' => null, 'last_seen' => 0, 'last_error' => null];
    $s = lead_radar_settings($uid);
    $info = $s['chat_info'] ?? [];
    return ['account_uid' => $uid, 'enabled' => (bool)($s['enabled'] ?? false), 'queries' => admin_queries($s),
        'chats' => array_map(fn($ref) => ['ref' => $ref, 'title' => (string)($info[$ref]['title'] ?? ''), 'members' => (int)($info[$ref]['members'] ?? 0),
            'error' => $info[$ref]['error'] ?? null], $s['chats'] ?? []),
        'last_run' => $s['last_run'] ?? null, 'last_seen' => (int)($s['last_seen'] ?? 0), 'last_error' => $s['last_error'] ?? null];
}

/** Запросы поиска: до 15 строк по 2–60 символов. */
function admin_validate_queries($queries): array {
    $clean = [];
    foreach ((array)$queries as $q) {
        $q = trim(preg_replace('/\s+/u', ' ', (string)$q));
        if (mb_strlen($q) >= 2 && mb_strlen($q) <= 60 && !in_array($q, $clean, true)) $clean[] = $q;
    }
    if (count($clean) > 15) throw new RuntimeException('Запросов не больше 15.');
    return $clean;
}

/** chats: строки-ссылки или {ref, members} (число участников из поиска запоминается для списка). */
function admin_api_settings_save($in) {
    $uid = admin_account_required();
    $items = array_map(fn($c) => is_array($c) ? $c : ['ref' => $c], (array)($in['chats'] ?? []));
    $chats = lead_radar_validate_chats(implode("\n", array_map(fn($c) => (string)($c['ref'] ?? ''), $items)));
    if (count($items) > 50) throw new RuntimeException('Чатов не больше 50.');
    $queries = admin_validate_queries($in['queries'] ?? []);
    $enabled = (bool)($in['enabled'] ?? false);
    $members = [];
    foreach ($items as $c) if (!empty($c['members'])) $members[strtolower((string)$c['ref'])] = (int)$c['members'];
    kv_update("lead_radar:$uid", function ($s) use ($chats, $queries, $enabled, $members) {
        $s = (array)$s + lead_radar_settings_defaults();
        $info = array_intersect_key($s['chat_info'] ?? [], array_flip($chats));
        foreach ($chats as $ref) if (isset($members[strtolower($ref)])) $info[$ref]['members'] = $members[strtolower($ref)];
        return ['enabled' => $enabled, 'chats' => $chats, 'queries' => $queries, 'chat_info' => $info,
            'last_ids' => array_intersect_key($s['last_ids'] ?? [], array_flip($chats))] + $s;
    }, lead_radar_settings_defaults());
    return [];
}

// ---- Поиск чатов ----

/** Публичные группы по запросам, кроме уже добавленных и меньше 50 участников; по убыванию числа участников. */
function admin_api_chats_search($in) {
    $uid = admin_account_required();
    $queries = admin_validate_queries($in['queries'] ?? []);
    if (!$queries) throw new RuntimeException('Введите хотя бы один запрос.');
    $added = array_map('strtolower', lead_radar_settings($uid)['chats'] ?? []);
    $found = array_filter(telegram_user_find_chats($queries, $uid), fn($c) => (int)$c['members'] >= 50 && !in_array('@' . strtolower($c['username']), $added, true));
    usort($found, fn($a, $b) => $b['members'] <=> $a['members']);
    return ['chats' => array_map(fn($c) => ['ref' => '@' . $c['username'], 'title' => (string)$c['title'], 'members' => (int)$c['members'], 'query' => (string)$c['query']], $found)];
}

// ---- Проход и журнал ----

/** Запустить проход фоном (cli leads-scan пишет журнал в users/.radar-<uid>.log). auto: только если радар включён и last_run старше 10 минут. */
function admin_api_scan_start($in) {
    $uid = admin_account_required();
    if (!empty($in['auto'])) {
        $s = lead_radar_settings($uid);
        if (empty($s['enabled']) || time() - (int)($s['last_run'] ?? 0) <= 600) return ['running' => lead_radar_is_running($uid), 'started' => false];
    }
    if (lead_radar_is_running($uid)) return ['running' => true, 'started' => false];
    $cmd = '/usr/bin/php ' . escapeshellarg(ROOT . '/tools/cli.php') . ' leads-scan ' . escapeshellarg($uid) . ' ' . escapeshellarg('--log=' . lead_radar_log_path($uid));
    exec("$cmd >/dev/null 2>&1 &");
    return ['running' => true, 'started' => true];
}

function admin_api_scan_log($in) {
    $uid = admin_account_required();
    $file = lead_radar_log_path($uid);
    $lines = is_file($file) ? array_slice(file($file, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) ?: [], -200) : [];
    return ['running' => lead_radar_is_running($uid), 'lines' => $lines];
}

// ---- Лиды и «Отсеяно моделью» ----

function admin_api_leads($in) {
    $uid = admin_account_required();
    $status = in_array($in['status'] ?? 'new', ADMIN_LEAD_STATUSES, true) ? ($in['status'] ?? 'new') : 'new';
    $offset = max(0, (int)($in['offset'] ?? 0));
    $rows = db_all('SELECT id, message, chat_title, sender_name, telegram_username, message_url, reason, lead_status, created_at
        FROM telegram_leads WHERE user_id = ? AND lead_status = ? ORDER BY created_at DESC, id DESC LIMIT 20 OFFSET ?', [$uid, $status, $offset]);
    // Ссылки собирает сервер: имя пользователя из Telegram — чужие данные, в страницу попадает только готовый адрес
    foreach ($rows as &$r) {
        $name = (string)$r['telegram_username'];
        $r['profile_url'] = preg_match('/^[A-Za-z0-9_]{3,32}$/', $name) ? 'https://t.me/' . $name : null;
        $r['message_url'] = preg_match('#^https://t\.me/[A-Za-z0-9_]{4,32}/\d+$#', (string)$r['message_url']) ? $r['message_url'] : null;
        unset($r['telegram_username']);
        $r['sender_username'] = $name;
    }
    $total = (int)db_value('SELECT COUNT(*) FROM telegram_leads WHERE user_id = ? AND lead_status = ?', [$uid, $status]);
    return ['leads' => $rows, 'total' => $total, 'offset' => $offset];
}

function admin_api_stats($in) {
    $uid = admin_account_required();
    $by_status = array_column(db_all('SELECT lead_status, COUNT(*) AS n FROM telegram_leads WHERE user_id = ? GROUP BY lead_status', [$uid]), 'n', 'lead_status');
    return ['statuses' => array_map(fn($s) => (int)($by_status[$s] ?? 0), array_combine(ADMIN_LEAD_STATUSES, ADMIN_LEAD_STATUSES)),
        'queued' => (int)db_value("SELECT COUNT(*) FROM telegram_lead_queue WHERE user_id = ? AND status = 'pending'", [$uid]),
        'seen_24h' => (int)db_value("SELECT COUNT(*) FROM telegram_lead_queue WHERE user_id = ? AND datetime(created_at) > datetime('now', '-1 day')", [$uid]),
        'classified_24h' => (int)db_value("SELECT COUNT(*) FROM telegram_lead_queue WHERE user_id = ? AND status = 'processed' AND datetime(processed_at) > datetime('now', '-1 day')", [$uid])];
}

function admin_api_lead_status($in) {
    $uid = admin_account_required();
    $status = (string)($in['status'] ?? '');
    if (!in_array($status, ADMIN_LEAD_STATUSES, true)) throw new RuntimeException('Неизвестный статус.');
    $changed = db_exec('UPDATE telegram_leads SET lead_status = ?, updated_at = CURRENT_TIMESTAMP, replied_at = CASE WHEN ? = \'replied\' THEN CURRENT_TIMESTAMP ELSE replied_at END
        WHERE id = ? AND user_id = ?', [$status, $status, (int)($in['id'] ?? 0), $uid]);
    if (!$changed) throw new AdminHttpError('Лид не найден.', 404);
    return [];
}

/** Последние 30 сообщений, которые модель не сочла лидами: чтобы владелец видел, не отсекает ли она лишнее. */
function admin_api_rejected($in) {
    $uid = admin_account_required();
    $rows = db_all("SELECT q.id, q.message, q.chat_title, q.sender_name, q.ollama_reason AS reason, q.processed_at
        FROM telegram_lead_queue q WHERE q.user_id = ? AND q.status = 'processed' AND NOT EXISTS (SELECT 1 FROM telegram_leads l WHERE l.queue_id = q.id)
        ORDER BY q.processed_at DESC, q.id DESC LIMIT 30", [$uid]);
    foreach ($rows as &$r) $r['message'] = mb_substr((string)$r['message'], 0, 300);
    return ['rejected' => $rows];
}

/** «Переоценить»: вернуть сообщение в очередь — модель посмотрит его в следующем проходе. */
function admin_api_rejected_retry($in) {
    $uid = admin_account_required();
    $changed = db_exec("UPDATE telegram_lead_queue SET status = 'pending', attempts = 0, ollama_result = NULL, ollama_reason = NULL, processed_at = NULL
        WHERE id = ? AND user_id = ? AND status = 'processed'", [(int)($in['id'] ?? 0), $uid]);
    if (!$changed) throw new AdminHttpError('Сообщение не найдено.', 404);
    return [];
}
