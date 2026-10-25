<?php
// Личный кабинет, VK ID, Telegram (бот и личный аккаунт), публикация постов, панель модели и остановка приложения.

const ACCOUNT_COOKIE_INFO = [
    SESSION_COOKIE => ['Ваша сессия: случайный ключ, по нему сайт узнаёт ваш профиль. ID профиля в ней нет.', USER_COOKIE_DAYS . ' дней, продлевается при каждом заходе'],
    USER_COOKIE => ['Старая кука: заменяется сессией при первом заходе и удаляется', 'для совместимости со старыми браузерами'],
    'PHPSESSID' => ['Сессия PHP. Нужна только на время входа через VK (защита от подмены ответа) и для демо-режима.', 'до закрытия браузера'],
];
// Пул, история, настройки, соцсети, баланс — в базе (data/posta.sqlite). В папке профиля — только временные файлы парсера.
const ACCOUNT_FILE_INFO = [];

function account_file_description($name) {
    return ACCOUNT_FILE_INFO[$name] ?? match (true) {
        str_ends_with($name, '.lock') => 'Служебная блокировка (защищает файл от одновременной записи)',
        str_starts_with($name, 'parser-') && str_ends_with($name, '.log') => 'Журнал вашего парсера (то, что видно в «Журнале»)',
        str_starts_with($name, 'parser-') && str_ends_with($name, '.phase') => 'Текущий этап парсера (шапка «Журнала»)',
        str_starts_with($name, 'parser-') && str_ends_with($name, '.progress') => 'Ход текущего прохода парсера (полоса прогресса)',
        str_starts_with($name, 'publish-') => 'Ход отправки поста в Telegram (удаляется после отправки)',
        default => 'Служебный файл'
    };
}

/** Какие служебные файлы отдаются по HTTP (Apache не читает .htaccess). Проверка запросом к себе, кэш 10 минут. */
function account_web_exposure() {
    $file = sys_get_temp_dir() . '/posta-exposure-' . substr(sha1(ROOT . '/handlers'), 0, 8) . '.json';
    if (time() - (int)(($cached = load_data($file, []))['at'] ?? 0) < 600) return $cached['exposed'];
    $base = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off' ? 'https' : 'http') . '://127.0.0.1:' . (int)($_SERVER['SERVER_PORT'] ?? 80) . rtrim(dirname($_SERVER['SCRIPT_NAME'] ?? '/'), '/\\');
    $exposed = array_values(array_filter(['.git/config', 'data/posta.sqlite', 'README.md'], function ($path) use ($base) {
        $ch = curl_init("$base/$path");
        curl_setopt_array($ch, [CURLOPT_NOBODY => true, CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 3, CURLOPT_CONNECTTIMEOUT => 2,
            CURLOPT_HTTPHEADER => ['Host: ' . ($_SERVER['HTTP_HOST'] ?? 'localhost')], CURLOPT_SSL_VERIFYPEER => false]);
        curl_exec($ch);
        return (int)curl_getinfo($ch, CURLINFO_HTTP_CODE) === 200;
    }));
    @file_put_contents($file, json_encode(['at' => time(), 'exposed' => $exposed]));
    return $exposed;
}

function account_telegram_public() {
    if (!($t = telegram_connection())) return null;
    return ['bot' => $t['bot_username'] ?? '', 'chat_title' => $t['chat_title'] ?? '', 'chat_username' => $t['chat_username'] ?? '', 'chat_type' => $t['chat_type'] ?? '',
        'token' => mb_strlen($t['token']) <= 10 ? $t['token'] : mb_substr($t['token'], 0, 6) . '…' . mb_substr($t['token'], -4), 'connected_at' => (int)($t['connected_at'] ?? 0)];
}

/** Каналы Telegram для интерфейса: без токенов; первый в списке — канал по умолчанию. */
function channels_public($uid = null) {
    $public = [];
    foreach (telegram_channels_load($uid) as $i => $c) {
        $public[] = ['id' => $c['id'], 'title' => $c['chat_title'], 'username' => $c['chat_username'], 'bot_username' => $c['bot_username'],
            'type' => $c['chat_type'], 'connected_at' => $c['connected_at'], 'default' => $i === 0, 'schedule' => channel_schedule($c), 'style' => channel_style_public($c), 'hashtags' => $c['hashtags']];
    }
    return $public;
}

/** Аккаунт Telegram для интерфейса, без секретов. */
function telegram_user_public() {
    $c = connections_load();
    [$e, $p] = [(array)($c['telegram_user'] ?? []), $c['telegram_user_pending'] ?? null];
    $pend = fn($k) => is_array($p) ? (string)($p[$k] ?? '') : '';
    return ['connected' => !empty($e['session_enc']), 'name' => (string)($e['name'] ?? ''), 'username' => (string)($e['username'] ?? ''),
        'connected_at' => (int)($e['connected_at'] ?? 0), 'warning' => (string)($e['warning'] ?? ''), 'app_keys' => telegram_app_keys() !== null,
        'awaiting_code' => is_array($p) && time() - (int)($p['at'] ?? 0) < TELEGRAM_USER_PENDING_TTL, 'phone' => $pend('phone'), 'qr' => is_array($p) && !empty($p['qr']),
        'code_type' => $pend('code_type'), 'next_type' => $pend('next_type')];
}

function handle_account_info() {
    $history = load_history();
    $files = array_map(fn($p) => ['name' => basename($p), 'size' => filesize($p), 'modified' => filemtime($p), 'about' => account_file_description(basename($p))],
        array_filter(glob(user_dir() . '/*') ?: [], 'is_file'));
    usort($files, fn($a, $b) => $b['size'] <=> $a['size']);
    $cookies = [];
    foreach ($_COOKIE as $name => $value) {
        $info = ACCOUNT_COOKIE_INFO[$name] ?? null;
        $cookies[] = ['name' => (string)$name, 'value' => (string)$value, 'known' => $info !== null, 'lifetime' => $info[1] ?? '—',
            'purpose' => $info[0] ?? 'Неизвестная кука: этот сайт её не ставит (её прислал браузер для этого адреса).',
            'http_only' => $name === USER_COOKIE || $name === SESSION_COOKIE || ($name === session_name() && (bool)ini_get('session.cookie_httponly'))];
    }
    if (isset($_COOKIE[session_name()])) @session_start(['read_and_close' => true]);   // только ключи: в значениях служебные секреты входа VK
    json_response(['user_id' => app_user_id(), 'topic' => current_topic(), 'last_seen' => (int)db_value('SELECT last_seen FROM users WHERE id = ?', [app_user_id()]), 'pool' => count(pool_topic_positions(pool_load())),
        'counts' => array_map('count', array_intersect_key($history, array_flip(HISTORY_COLLECTIONS))), 'files' => $files, 'cookies' => $cookies,
        'session_keys' => array_keys($_SESSION ?? []), 'retention_days' => USER_RETENTION_DAYS, 'exposed' => account_web_exposure(),
        'fix_command' => 'sudo sh ' . ROOT . '/tools/apache-setup.sh', 'telegram' => account_telegram_public(), 'channels' => channels_public(), 'telegram_user' => telegram_user_public(),
        'auth' => auth_public(app_user_id())]);
}

// ---- VK ID (OAuth 2 + PKCE), одно подключение на сайт ----

function vk_redirect($result): never {
    header('Location: index.html?vk=' . (in_array($result, ['connected', 'misconfigured', 'error'], true) ? $result : 'error') . '#account');
    exit;
}

function base64url_encode($bytes) { return rtrim(strtr(base64_encode($bytes), '+/', '-_'), '='); }

function handle_vk_oauth_status() {
    $t = load_vk_tokens();
    json_response(['configured' => vk_oauth_is_configured(), 'connected' => !empty($t['refresh_token']), 'user_id' => $t['user_id'] ?? null]);
}

function handle_vk_oauth_start() {
    if (!vk_oauth_is_configured()) vk_redirect('misconfigured');
    $c = vk_oauth_config();
    if (session_status() === PHP_SESSION_NONE) session_start();
    $_SESSION['vk_oauth'] = ['state' => $state = bin2hex(random_bytes(24)), 'code_verifier' => $verifier = base64url_encode(random_bytes(48)), 'started_at' => time()];
    header('Location: ' . VK_ID_AUTHORIZE_URL . '?' . http_build_query(array_filter(['response_type' => 'code', 'client_id' => $c['client_id'], 'redirect_uri' => $c['redirect_uri'],
        'state' => $state, 'code_challenge' => base64url_encode(hash('sha256', $verifier, true)), 'code_challenge_method' => 'S256', 'lang_id' => '0', 'scope' => $c['scope']], 'strlen')), true, 302);
    exit;
}

function handle_vk_oauth_callback() {
    if (session_status() === PHP_SESSION_NONE) session_start();
    $cb = $_GET;
    if (is_array($payload = json_decode((string)($cb['payload'] ?? ''), true))) $cb = array_merge($cb, $payload);   // VK ID может прислать параметры JSON-строкой
    $saved = $_SESSION['vk_oauth'] ?? [];
    unset($_SESSION['vk_oauth']);
    [$state, $code, $device] = [(string)($cb['state'] ?? ''), (string)($cb['code'] ?? ''), (string)($cb['device_id'] ?? '')];
    if ((int)($saved['started_at'] ?? 0) < time() - 600 || empty($saved['state']) || !hash_equals((string)$saved['state'], $state) || $code === '' || $device === '' || !vk_oauth_is_configured()) vk_redirect('error');
    $c = vk_oauth_config();
    try {
        $t = vk_oauth_post(VK_ID_TOKEN_URL, array_filter(['grant_type' => 'authorization_code', 'code' => $code, 'code_verifier' => (string)$saved['code_verifier'], 'client_id' => $c['client_id'],
            'device_id' => $device, 'redirect_uri' => $c['redirect_uri'], 'state' => $state, 'service_token' => $c['service_token']], 'strlen'));
        if (empty($t['access_token']) || empty($t['refresh_token'])) throw new RuntimeException('VK ID не вернул access_token и refresh_token.');
        save_vk_tokens($t + ['device_id' => $device, 'expires_at' => time() + max(0, (int)($t['expires_in'] ?? 0))]);
        session_regenerate_id(true);
        vk_redirect('connected');
    } catch (Throwable $e) {
        error_log('VK OAuth callback failed: ' . $e->getMessage());
        vk_redirect('error');
    }
}

// ---- Telegram: бот и канал ----

/**
 * {token, channel, bot?}: проверяет бота, канал и право публиковать, сохраняет канал. bot — id уже подключённого канала:
 * тогда токен берётся у него (тот же бот в новом канале). Лимит тарифа: сверх него — отказ при BILLING_ENFORCE, иначе over_limit.
 */
function handle_telegram_connect($in) {
    $channel = preg_replace('~^(https?://)?t\.me/~i', '@', trim((string)($in['channel'] ?? '')));
    if (($same_bot = (string)($in['bot'] ?? '')) !== '') {
        $token = channel_get($same_bot)['token'] ?? json_error('Бот не найден: введите токен.');
    } else {
        $token = trim((string)($in['token'] ?? ''));
        if (!preg_match('/^\d{5,12}:[A-Za-z0-9_-]{30,50}$/', $token)) json_error('Токен бота выглядит иначе: 123456789:AA… — его выдаёт @BotFather.');
    }
    if (!preg_match('/^(@[A-Za-z][A-Za-z0-9_]{4,31}|-100\d{5,15})$/', $channel)) json_error('Укажите канал как @имя_канала или числовой id вида -100…');
    try {
        $bot = telegram_bot_request($token, 'getMe', [], [], 20);
        $chat = telegram_bot_request($token, 'getChat', ['chat_id' => $channel], [], 20);
        $member = telegram_bot_request($token, 'getChatMember', ['chat_id' => $chat['id'], 'user_id' => $bot['id']], [], 20);
    } catch (RuntimeException $e) { json_error('Telegram: ' . $e->getMessage()); }
    $status = $member['status'] ?? '';
    if ($status !== 'creator' && !($status === 'administrator' && (($chat['type'] ?? '') !== 'channel' || !empty($member['can_post_messages'])))) {
        json_error("Бот @{$bot['username']} не может публиковать в «" . ($chat['title'] ?? $channel) . '»: добавьте его администратором канала с правом публикации.');
    }
    if ($denied = channel_connect_denied($chat['id'])) throw new DomainException($denied);
    $over = channel_over_limit($chat['id']);
    channel_save(['token' => $token, 'bot_id' => $bot['id'], 'bot_username' => $bot['username'] ?? '', 'chat_id' => $chat['id'], 'chat_title' => $chat['title'] ?? '',
        'chat_username' => $chat['username'] ?? '', 'chat_type' => $chat['type'] ?? '', 'connected_at' => time()]);
    ok(['channels' => channels_public(), 'over_limit' => $over, 'telegram' => account_telegram_public()]);
}

/** {channel}: отключить канал. Канал с отложенными постами не отключается. */
function handle_telegram_disconnect($in) {
    if (($id = (string)($in['channel'] ?? '')) === '') json_error('Не указан канал.');
    if ($denied = channel_delete_denied($id)) throw new DomainException($denied);
    channel_delete($id) ? ok(['channels' => channels_public()]) : json_error('Такого канала нет.', 404);
}

/** {channel}: сделать канал основным (первым в списке). */
function handle_telegram_channel_default($in) {
    channel_make_default((string)($in['channel'] ?? ''));
    ok(['channels' => channels_public(), 'telegram' => account_telegram_public()]);
}

// ---- Telegram: личный аккаунт для отложенных постов ----

/** Ключи из формы (запоминаются для всего сайта) или уже сохранённые. */
function telegram_user_keys_from_input(array $in) {
    [$id, $hash] = [trim((string)($in['api_id'] ?? '')), trim((string)($in['api_hash'] ?? ''))];
    // Ключи уже сохранены, а пришёл мусор (поля скрыты, их заполнил менеджер паролей браузера) — берём сохранённые
    $valid = preg_match('/^\d{3,10}$/', $id) && preg_match('/^[a-f0-9]{32}$/i', $hash);
    if (($id === '' && $hash === '') || (!$valid && telegram_app_keys() !== null)) {
        $saved = telegram_app_keys() ?? json_error('Введите api_id и api_hash: их выдаёт my.telegram.org/apps (один раз, дальше они запомнятся для всех пользователей сайта).');
        return [$saved['api_id'], $saved['api_hash']];
    }
    if (!preg_match('/^\d{3,10}$/', $id)) json_error('api_id — число, его выдаёт my.telegram.org.');
    if (!preg_match('/^[a-f0-9]{32}$/i', $hash)) json_error('api_hash — 32 символа (цифры и буквы a–f), его выдаёт my.telegram.org.');
    if (telegram_app_keys() === null) telegram_app_keys_save($id, $hash);
    return [$id, $hash];
}

/** Телефон в международный вид (как normalizePhone в js/account.js): 8999…, 7999…, 999… → +7999…. */
function telegram_user_normalize_phone($value) {
    if (($d = preg_replace('/\D+/', '', $value)) === '') return '';
    if (str_starts_with(trim($value), '+') && $d[0] !== '7') return "+$d";
    return strlen($d) === 11 && ($d[0] === '7' || $d[0] === '8') ? '+7' . substr($d, 1) : (strlen($d) === 10 ? "+7$d" : "+$d");
}

function require_bot() { return telegram_connection() ?? json_error('Сначала подключите канал.'); }

/** Незаконченный вход: [запрос к помощнику с ключами и сессией, данные входа]. */
function telegram_user_pending() {
    $p = connections_load()['telegram_user_pending'] ?? null;
    if (!is_array($p) || time() - (int)($p['at'] ?? 0) > TELEGRAM_USER_PENDING_TTL) json_error('Код устарел: запросите новый.', 409);
    [$hash, $session] = [secret_decrypt($p['api_hash_enc']), secret_decrypt($p['session_enc'])];
    if ($hash === null || $session === null) json_error('Не удалось прочитать сохранённые данные: запросите код заново.', 409);
    return [['api_id' => $p['api_id'], 'api_hash' => $hash, 'session' => $session], $p];
}

function save_pending_code(array $base, array $r, array $extra = []) {
    update_connections(fn($c) => ['telegram_user_pending' => $extra + ['session_enc' => secret_encrypt($r['session'] ?? ''), 'phone_code_hash' => (string)($r['phone_code_hash'] ?? ''),
        'code_type' => (string)($r['code_type'] ?? ''), 'next_type' => (string)($r['next_type'] ?? ''), 'at' => time()] + $base] + $c);
}

/** {api_id, api_hash, phone}: Telegram присылает код входа в приложение. */
function handle_telegram_user_start($in) {
    require_bot();
    [$id, $hash] = telegram_user_keys_from_input($in);
    if (!preg_match('/^\+\d{8,15}$/', $phone = telegram_user_normalize_phone((string)($in['phone'] ?? '')))) json_error('Номер указывается с кодом страны: +79991234567.');
    $r = telegram_user_run(['action' => 'send_code', 'api_id' => $id, 'api_hash' => $hash, 'session' => '', 'phone' => $phone], 60);
    save_pending_code(['api_id' => $id, 'api_hash_enc' => secret_encrypt($hash), 'phone' => $phone], $r);
    ok(['telegram_user' => telegram_user_public()]);
}

function handle_telegram_user_resend() {
    [$req, $p] = telegram_user_pending();
    if (empty($p['next_type'])) json_error('Другого способа доставки кода Telegram не предлагает: подождите несколько минут и запросите код заново.', 409);
    save_pending_code($p, telegram_user_run($req + ['action' => 'resend_code', 'phone' => $p['phone'], 'phone_code_hash' => $p['phone_code_hash']], 60));
    ok(['telegram_user' => telegram_user_public()]);
}

/** Предупреждение, если у аккаунта нет права публиковать в канале. */
function telegram_user_channel_warning(array $check) {
    return !empty($check['can_post']) ? '' : 'Вы вошли как ' . ($check['account'] ?? 'неизвестный аккаунт') . ', а в канале «' . ($check['title'] ?? '') . '» у него роль: ' . ($check['role'] ?? 'неизвестна')
        . '. Нужен владелец или администратор с правом публикации — иначе отложенные посты будут идти через очередь приложения.';
}

/** Сохраняет вошедший аккаунт после проверки прав в канале. */
function telegram_user_finish($id, $hash, $session, array $user) {
    $check = telegram_user_run(['api_id' => $id, 'api_hash' => $hash, 'session' => $session, 'action' => 'check_channel', 'channel' => telegram_user_channel_ref(require_bot())], 60);
    update_connections(fn($c) => ['telegram_user' => ['api_id' => $id, 'api_hash_enc' => secret_encrypt($hash), 'session_enc' => secret_encrypt($check['session'] ?? $session),
        'name' => (string)($user['name'] ?? ''), 'username' => (string)($user['username'] ?? ''), 'connected_at' => time(), 'warning' => telegram_user_channel_warning($check)]]
        + array_diff_key($c, ['telegram_user_pending' => 1, 'telegram_user_qr' => 1]));
    if (telegram_app_keys() === null) telegram_app_keys_save($id, $hash);
    ok(['telegram_user' => telegram_user_public()]);
}

/** {code, password?}: завершает вход; при двухэтапной защите сначала needs_password. */
function handle_telegram_user_confirm($in) {
    require_bot();
    [$req, $p] = telegram_user_pending();
    [$code, $password] = [preg_replace('/\D+/', '', (string)($in['code'] ?? '')), (string)($in['password'] ?? '')];
    if ($code === '' && $password === '') json_error('Введите код из Telegram.');
    $r = telegram_user_run($req + (!empty($p['qr']) ? ['action' => 'password', 'password' => $password]
        : ['action' => 'sign_in', 'phone' => $p['phone'], 'code' => $code, 'phone_code_hash' => $p['phone_code_hash'], 'password' => $password]), 60);
    if (!empty($r['needs_password'])) {
        update_connections(fn($c) => array_replace_recursive($c, ['telegram_user_pending' => ['session_enc' => secret_encrypt($r['session'] ?? '')]]));
        json_response(['status' => 'needs_password']);
    }
    telegram_user_finish($p['api_id'], $req['api_hash'], $r['session'], $r['user'] ?? []);
}

function handle_telegram_user_qr_start($in) {
    require_bot();
    [$id, $hash] = telegram_user_keys_from_input($in);
    update_connections(fn($c) => ['telegram_user_qr' => ['api_id' => $id, 'api_hash_enc' => secret_encrypt($hash), 'at' => time()]] + array_diff_key($c, ['telegram_user_pending' => 1]));
    telegram_user_qr_start($id, $hash);
    json_response(['status' => 'waiting']);
}

/** Состояние входа по QR: waiting (+ svg), needs_password, success, busy, expired, error, none. */
function handle_telegram_user_qr_status() {
    $c = connections_load();
    if (telegram_user_connection() !== null) ok(['telegram_user' => telegram_user_public()]);
    $qr = $c['telegram_user_qr'] ?? null;
    $file = telegram_user_qr_state_file();
    $state = json_decode((string)@file_get_contents($file), true);
    if (telegram_connection() === null || !is_array($qr) || !is_array($state)) json_response(['status' => 'none']);
    switch ($state['status'] ?? '') {
        case 'needs_password':
            update_connections(fn($c) => ['telegram_user_pending' => ['api_id' => $qr['api_id'], 'api_hash_enc' => $qr['api_hash_enc'], 'session_enc' => secret_encrypt((string)$state['session']),
                'qr' => true, 'phone' => '', 'phone_code_hash' => '', 'code_type' => '', 'next_type' => '', 'at' => time()]] + array_diff_key($c, ['telegram_user_qr' => 1]));
            @unlink($file);
            json_response(['status' => 'needs_password']);
        case 'ok':
            file_put_contents($file, json_encode(['token' => $state['token'], 'status' => 'finishing']));   // повторные запросы ждут, пока этот закончит
            try {
                @unlink($file);
                telegram_user_finish($qr['api_id'], secret_decrypt($qr['api_hash_enc']), $state['session'], $state['user'] ?? []);
            } catch (RuntimeException $e) {
                $message = 'Вход выполнен, но канал проверить не удалось: ' . $e->getMessage();
                file_put_contents($file, json_encode(['token' => $state['token'], 'status' => 'error', 'error' => $message]));
                json_response(['status' => 'error', 'error' => $message]);
            }
        case 'finishing': json_response(['status' => 'busy']);
        case 'expired': json_response(['status' => 'expired']);
        case 'error': json_response(['status' => 'error', 'error' => (string)($state['error'] ?? 'Ошибка входа.')]);
        default: json_response(['status' => 'waiting', 'svg' => (string)($state['svg'] ?? '')]);
    }
}

function handle_telegram_user_disconnect() {
    update_connections(fn($c) => array_diff_key($c, ['telegram_user' => 1, 'telegram_user_pending' => 1]));
    ok(['telegram_user' => telegram_user_public()]);
}

// ---- Вход: Telegram и свои логин с паролем ----

function handle_auth_status() {
    json_response(auth_public(app_user_id()) + ['configured' => telegram_login_configured(), 'bot' => telegram_login_bot()['username'] ?? '']);
}

/** Вход по логину и паролю. Ранний выход в index.php: до ensure_default_project_data, чтобы не заводить данные анонимному профилю. */
function handle_password_login($in) {
    credentials_login(app_user_id(), $_COOKIE[SESSION_COOKIE] ?? '', $in['login'] ?? '', $in['password'] ?? '');
    ok();
}

function handle_credentials_save($in) {
    $login = credentials_set(app_user_id(), $_COOKIE[SESSION_COOKIE] ?? '', $in['login'] ?? '', $in['password'] ?? '', $in['current'] ?? '');
    ok(['login' => $login, 'auth' => auth_public(app_user_id())]);
}

function handle_login_start() {
    try {
        $result = login_start(app_user_id());
        ok(['url' => $result['url'], 'token' => $result['token']]);
    } catch (RuntimeException | DomainException $e) {
        json_error($e->getMessage());
    }
}

function handle_login_status($in) {
    $token = (string)($_GET['token'] ?? $in['token'] ?? '');   // GET: токен в адресе
    if (!preg_match('/^[a-f0-9]{32}$/', $token)) {
        json_response(['status' => 'expired']);
    } else {
        try {
            $status = login_status(app_user_id(), $_COOKIE[SESSION_COOKIE] ?? '', $token);
            json_response(['status' => $status]);
        } catch (RuntimeException $e) {
            error_log('login_status error: ' . $e->getMessage());
            json_response(['status' => 'expired']);
        }
    }
}

/** Выход: браузер получает новый анонимный профиль. $all — закрыть и остальные сессии профиля. */
function auth_logout($all) {
    $uid = app_user_id();
    if (!user_has_identity($uid)) {
        json_error('Вы не вошли: выход сотрёт ваш анонимный профиль. Сначала задайте логин и пароль или войдите через Telegram.');
    }
    $all ? session_delete_all($uid) : session_delete($_COOKIE[SESSION_COOKIE] ?? '');
    $new_uid = bin2hex(random_bytes(16));
    session_cookie_set(session_create($new_uid));
    app_user_bind($new_uid);
    ok();
}

function handle_logout() { auth_logout(false); }

function handle_logout_all() { auth_logout(true); }

// ---- Публикация ----

/** Файлы редактора ($_FILES['files'][]): [индекс => ['path', 'name']]; на них ссылается поле upload у медиа. */
function publish_collect_uploads() {
    $files = $_FILES['files'] ?? null;
    $out = [];
    foreach (is_array($files['name'] ?? null) ? $files['name'] : [] as $i => $name) {
        $label = '«' . mb_substr((string)$name, 0, 80) . '»';
        if (in_array($files['error'][$i], [UPLOAD_ERR_INI_SIZE, UPLOAD_ERR_FORM_SIZE], true)) throw new RuntimeException("Файл {$label} слишком большой для загрузки на сервер.");
        if ($files['error'][$i] !== UPLOAD_ERR_OK || !is_uploaded_file($files['tmp_name'][$i])) throw new RuntimeException("Не удалось загрузить файл {$label}.");
        if ($files['size'][$i] > TELEGRAM_UPLOAD_MAX_BYTES) throw new RuntimeException("Файл {$label} больше " . (TELEGRAM_UPLOAD_MAX_BYTES >> 20) . ' МБ.');
        $out[(int)$i] = ['path' => $files['tmp_name'][$i], 'name' => (string)$name];
    }
    return $out;
}

/** Опрос из поля poll (JSON или массив); null — обычный пост. */
function publish_poll_input($value) {
    if ($value === null || $value === '' || $value === []) return null;
    $poll = is_string($value) ? json_decode($value, true) : $value;
    if (!is_array($poll)) json_error('Некорректный опрос.');
    return $poll;
}

/** text, media (JSON [{kind, src, name, upload}]), scheduled_ts (пусто — сразу), files[], poll (JSON {question, options}, без медиа). Отложенный пост — в «Отложенные» через аккаунт. */
function handle_publish($in) {
    // Запрос больше post_max_size: PHP молча отбрасывает и поля, и файлы
    if (!$_POST && !$_FILES && !$in && (int)($_SERVER['CONTENT_LENGTH'] ?? 0) > ini_parse_quantity((string)ini_get('post_max_size'))) {
        json_error('Файлы вместе весят больше лимита сервера (' . ini_get('post_max_size') . '). Уберите часть файлов или выполните tools/apache-setup.sh, чтобы поднять лимит.', 413);
    }
    $fields = $_POST ?: $in;
    // Канал, выбранный в редакторе; пусто — канал по умолчанию
    $target = publish_channel_resolve((string)($fields['channel'] ?? $_POST['channel'] ?? ''));
    if (isset($target['error'])) json_error($target['error'], $target['status']);
    $bot = $target['channel'];
    $text = post_caption_clean($fields['text'] ?? '');   // заглушка «Пост без текста» в подпись не уходит
    $items = is_string($items = $fields['media'] ?? []) ? json_decode($items, true) : $items;
    if (!is_array($items)) json_error('Некорректный список медиа.');
    $items = array_slice(array_values(array_filter($items, 'is_array')), 0, TELEGRAM_MAX_MEDIA + 1);
    if ($denied = publish_media_denied($items)) json_error($denied, 422);   // до подготовки медиа и списания лимитов
    $at = (int)($fields['scheduled_ts'] ?? 0);
    // Режим: now — сразу; native — «Отложенные» Telegram через аккаунт; queue — наша очередь на время; slot — наша очередь в слот расписания.
    // Без режима — как раньше: есть время → native, нет → now.
    $mode = (string)($fields['mode'] ?? '') ?: ($at !== 0 ? 'native' : 'now');
    if (!in_array($mode, ['now', 'native', 'queue', 'slot'], true)) json_error('Неизвестный режим публикации.');
    $poll = publish_poll_input($fields['poll'] ?? null);
    // Запись, из которой опубликовали: публикация = лайк (POSTED_IS_LIKED_PROMPT.md). Ключ считает сервер по полям записи
    $record_raw = $fields['record'] ?? null;
    $record_key = content_record_key(is_string($record_raw) ? json_decode($record_raw, true) : $record_raw);
    if ($poll !== null && $mode === 'native') json_error('Опросы ставятся в очередь Posta или публикуются сразу: через «Отложенные» Telegram опросы пока не ставятся.', 422);
    if ($poll !== null && $items) json_error('В опросе не бывает медиа: уберите файлы.', 422);
    // Соцсети кроме Telegram: только сейчас и в очередь Posta; отложка средствами Telegram и опросы — только Telegram
    $platform = (string)($bot['platform'] ?? 'telegram');
    if ($platform !== 'telegram' && ($mode === 'native' || $poll !== null)) json_error(network_for($platform)['title'] . ': отложка средствами соцсети и опросы пока только в Telegram.', 422);
    // До скачивания и биллинга: текст по лимиту ресурса и виды файлов; частичный пропуск — только с подтверждением
    $pre_skipped = [];
    if ($platform !== 'telegram') {
        try { $pre = network_preflight($platform, $text, $items); } catch (RuntimeException $e) { json_error($e->getMessage(), 422); }
        $pre_skipped = $pre['skipped'];
        if ($pre_skipped && empty($fields['confirm_skip'])) {
            json_response(['status' => 'error', 'confirm_skip' => true, 'skipped' => $pre_skipped, 'message' => 'Не все файлы уйдут в ' . network_for($platform)['title'] . ': ' . implode('; ', $pre_skipped) . '. Опубликовать без них?'], 409);
        }
    }
    if ($mode === 'now') $at = 0;
    if (in_array($mode, ['native', 'queue'], true) && $at === 0) json_error('Укажите время публикации.');
    $slot = null;
    if ($mode === 'slot') {
        if (!channel_schedule($bot)['slots']) json_error('У канала не настроено расписание: «Соцсети» → канал → «Расписание».', 422);
        $slot = channel_next_slot($bot, time(), outbox_load()) ?? json_error('В расписании канала нет свободного слота в ближайшие 60 дней.', 422);
        $at = $slot['publish_at'];
    }
    if ($at !== 0 && $at < time() + TELEGRAM_MIN_DELAY_SECONDS) json_error('Отложенный пост нужно назначить минимум на минуту вперёд.');
    if ($at > time() + TELEGRAM_MAX_DELAY_DAYS * DAY_SECONDS) json_error('Отложить пост можно не больше чем на ' . TELEGRAM_MAX_DELAY_DAYS . ' дней.');
    if ($mode === 'native' && telegram_user_connection() === null) {
        json_error('Отложенные посты ставятся средствами Telegram: подключите свой аккаунт в «Личном кабинете» → «Соцсети» → Telegram (блок «Отложенные посты средствами Telegram»). Без отложки пост можно опубликовать сразу. Или выберите «Через очередь Posta».', 422);
    }
    if ($denied = billing_post_denied($at !== 0)) json_error($denied, 429);
    @set_time_limit(900);       // большое видео заливается и перекодируется долго
    ignore_user_abort(true);
    if (session_status() === PHP_SESSION_ACTIVE) session_write_close();   // иначе опрос прогресса ждёт конца отправки
    $GLOBALS['telegram_progress_file'] = telegram_progress_file($fields['progress_id'] ?? null);
    $work = null;
    try {
        if ($poll !== null) {
            $poll = telegram_poll_check($poll);
            $text = $poll['question'];   // в очереди и журнале опрос виден по вопросу
        } else {
            $limit = $platform === 'telegram' ? ($items ? TELEGRAM_CAPTION_LIMIT : TELEGRAM_TEXT_LIMIT) : network_text_limit($platform, (bool)$items);
            $text = text_with_hashtags($text, channel_hashtags($bot, publish_hashtags_input($fields['hashtags'] ?? null)), $limit);
            if ($platform === 'telegram') telegram_check_text($text, count($items));
        }
        $prepared = telegram_prepare_media($items, publish_collect_uploads(), $work = telegram_work_dir(), $mode === 'native' ? 'account' : 'bot');
        if (in_array($mode, ['queue', 'slot'], true)) {
            $response = outbox_queue_post(app_user_id(), $bot, $text, $prepared, $at, $slot['slot_at'] ?? null, $work, $poll, publish_article_input($fields['article'] ?? null), null, $record_key);
            if ($platform !== 'telegram') $response['skipped'] = $pre_skipped;
            $work = null;   // файлы уже в папке поста
        } elseif ($mode === 'native') {
            $ids = telegram_user_schedule(app_user_id(), $bot, $text, $prepared, $at);
            $response = ['scheduled' => true, 'id' => telegram_record_native_post(app_user_id(), $text, $prepared, $at, $ids, $bot['id']), 'publish_at' => $at, 'native' => true];
        } else {
            if ($platform !== 'telegram') $sent = outbox_send_post(['text' => $text, 'media' => $prepared], $bot);   // адаптер соцсети: файлы, которые она не примет, отсеиваются
            else $sent = $poll !== null ? telegram_publish_poll($bot, $poll) : telegram_publish_prepared($bot, $text, $prepared);
            $shown = $sent['media_kept'] ?? $prepared;   // журнал и миниатюра — по отправленным файлам
            telegram_log_published(app_user_id(), telegram_post_summary($text, count($shown)) + ['at' => time(), 'scheduled_for' => 0, 'url' => $sent['url'], 'status' => 'ok', 'error' => '', 'media_src' => media_sources($shown),
                'channel' => $bot['id'], 'channel_title' => $bot['chat_title'], 'kind' => $shown[0]['kind'] ?? '', 'duration' => $shown[0]['duration'] ?? 0, 'thumb_id' => post_thumb_make($shown), 'skipped' => $sent['skipped'] ?? []]);
            $response = ['scheduled' => false, 'url' => $sent['url'], 'messages' => count($sent['message_ids']), 'skipped' => $sent['skipped'] ?? $pre_skipped];
        }
        billing_post_used();
    } catch (RuntimeException $e) {
        $failure = $e->getMessage();
    } finally {
        if ($work !== null) remove_dir($work);
        if ($GLOBALS['telegram_progress_file'] !== null) @unlink($GLOBALS['telegram_progress_file']);
    }
    if (isset($failure)) json_error($failure, 422);
    // Успех (сейчас, в очередь или в «Отложенные»): запись отмечается лайком. Ошибка отметки публикацию не отменяет
    if ($record_key !== '') {
        try { $response['marked'] = content_mark_posted($record_key); }
        catch (Throwable $e) { error_log('posta: отметка публикации: ' . $e->getMessage()); $response['marked'] = null; }
    }
    ok($response);
}

/** ?id=: этап и процент идущей отправки; stage null — файла нет (отправка ещё не началась или закончилась). */
/** {id}: повторить неудачный пост нашей очереди — снова в очередь, не раньше чем через минуту. */
function handle_telegram_retry($in) {
    if (!preg_match('/^[a-f0-9]{12}$/', $id = (string)($in['id'] ?? ''))) json_error('Некорректный номер поста.');
    $found = false;
    outbox_update(fn($q) => array_map(function ($e) use ($id, &$found) {
        if (($e['id'] ?? '') !== $id || ($e['status'] ?? '') !== 'failed') return $e;
        $found = true;
        return ['status' => 'queued', 'attempts' => 0, 'error' => '', 'next_try_at' => 0, 'publish_at' => max((int)$e['publish_at'], time() + 60)] + $e;
    }, $q), app_user_id());
    $found ? ok() : json_error('Этого поста нет среди неудачных.', 404);
}

/** {id, publish_at | slot}: перенести пост нашей очереди, который ещё ждёт публикации, — на другое время или в ближайший слот.
 *  Пост из группы (один пост в нескольких ресурсах) переносится вместе со всей группой: каждый — на свой канал. */
function handle_telegram_reschedule($in) {
    if (!preg_match('/^[a-f0-9]{12}$/', $id = (string)($in['id'] ?? ''))) json_error('Некорректный номер поста.');
    $post = array_values(array_filter(outbox_load(), fn($e) => ($e['id'] ?? '') === $id))[0] ?? json_error('Такого поста нет.', 404);
    if (($post['status'] ?? '') !== 'queued') json_error('Перенести можно только пост, который ждёт публикации.', 409);
    $group = (string)($post['group'] ?? '');
    $members = array_filter(outbox_load(), fn($e) => $group !== '' ? ($e['group'] ?? '') === $group && ($e['status'] ?? '') === 'queued' : ($e['id'] ?? '') === $id);
    if (!empty($in['slot'])) {
        $plan = [];
        foreach ($members as $m) {
            $bot = telegram_connection(null, $m['channel'] ?? null) ?? json_error('Канал не найден: обновите страницу.', 404);
            $slot = channel_next_slot($bot, time(), outbox_load()) ?? json_error('В расписании канала нет свободного слота в ближайшие 60 дней.', 422);
            $plan[$m['id']] = [$slot['publish_at'], $slot['slot_at']];
        }
    } else {
        $at = (int)($in['publish_at'] ?? 0);
        if ($at < time() + TELEGRAM_MIN_DELAY_SECONDS) json_error('Отложенный пост нужно назначить минимум на минуту вперёд.');
        if ($at > time() + TELEGRAM_MAX_DELAY_DAYS * DAY_SECONDS) json_error('Отложить пост можно не больше чем на ' . TELEGRAM_MAX_DELAY_DAYS . ' дней.');
        $plan = array_fill_keys(array_column($members, 'id'), [$at, null]);
    }
    if ($denied = billing_post_denied(true)) json_error($denied, 429);
    outbox_update(fn($q) => array_map(fn($e) => isset($plan[$e['id'] ?? '']) && ($e['status'] ?? '') === 'queued'
        ? ['publish_at' => $plan[$e['id']][0], 'slot_at' => $plan[$e['id']][1], 'slot' => $plan[$e['id']][1] !== null] + $e : $e, $q), app_user_id());
    ok(['publish_at' => $plan[$id][0], 'slot' => $plan[$id][1] !== null, 'moved' => count($plan)]);
}

/** ?channel=: ближайший свободный слот канала (или none, если расписания нет или слотов нет). */
function handle_telegram_next_slot() {
    $target = publish_channel_resolve((string)($_GET['channel'] ?? ''));
    if (isset($target['error'])) json_error($target['error'], $target['status']);
    if (!channel_schedule($target['channel'])['slots']) json_response(['none' => true]);
    json_response(channel_next_slot($target['channel'], time(), outbox_load()) ?? ['none' => true]);
}

/** {channel, schedule: {tz, jitter, slots}}: сохранить расписание канала. Ошибки формата — 400 с текстом. */
function handle_channel_schedule_save($in) {
    if (!is_array($in['schedule'] ?? null)) json_error('Некорректное расписание.');
    channel_schedule_save((string)($in['channel'] ?? ''), $in['schedule'], app_user_id());
    ok(['channels' => channels_public()]);
}

/** {channel, mode}: хэштеги в постах канала — off, rubric (только рубрика) или full (рубрика, имя, формат). */
function handle_channel_hashtags_save($in) {
    $mode = (string)($in['mode'] ?? '');
    if (!in_array($mode, CHANNEL_HASHTAG_MODES, true)) json_error('Неизвестный режим хэштегов.');
    channel_get((string)($in['channel'] ?? '')) ?? json_error('Такого канала нет.');
    db_exec('UPDATE channels SET hashtags = ? WHERE id = ? AND user_id = ?', [$mode, (string)$in['channel'], app_user_id()]);
    ok(['channels' => channels_public()]);
}

/** ?q=: подсказки хэштегов из словаря (синоним ведёт к каноническому тегу). */
function handle_hashtag_suggest() {
    ok(['tags' => hashtag_suggest((string)($_GET['q'] ?? ''))]);
}

/** Канал по значению из интерфейса: пусто или main — канал по умолчанию. */
function style_channel_id($value): string {
    $value = (string)$value;
    if ($value !== '' && $value !== 'main') return $value;
    return telegram_channels_load()[0]['id'] ?? throw new DomainException('Telegram-канал не подключён.');
}

/** ?channel=: состояние стиля канала (для опроса, пока стиль изучается в фоне). */
function handle_channel_style() {
    $channel = channel_get(style_channel_id($_GET['channel'] ?? '')) ?? throw new DomainException('Такого канала нет.');
    ok(['style' => channel_style_public($channel)]);
}

/** {channel}: изучить стиль по постам канала (в фоне, минуты). */
function handle_channel_style_learn($in) {
    $id = style_channel_id($in['channel'] ?? '');
    $started = channel_style_learn_async($id);
    ok(['started' => $started, 'style' => channel_style_public(channel_get($id))]);
}

/** {channel, summary}: описание стиля вручную (пусто — стереть). */
function handle_channel_style_save($in) {
    $id = style_channel_id($in['channel'] ?? '');
    channel_style_save_manual($id, (string)($in['summary'] ?? ''));
    ok(['style' => channel_style_public(channel_get($id))]);
}

/** {channel, description, original, current}: подпись от ИИ в стиле канала. */
function handle_ai_caption($in) {
    $id = style_channel_id($in['channel'] ?? '');
    ok(['text' => channel_caption_generate($id, ['description' => $in['description'] ?? '', 'original' => $in['original'] ?? '', 'current' => $in['current'] ?? ''])]);
}

/** {url}: выжимка новости по статье (кнопка «Сделать выжимку» в редакторе). */
function handle_news_excerpt($in) {
    ok(['text' => news_excerpt(trim((string)($in['url'] ?? '')))]);
}

function handle_telegram_publish_progress() {
    $file = telegram_progress_file($_GET['id'] ?? null);
    $data = $file !== null ? json_decode((string)@file_get_contents($file), true) : null;
    json_response(is_array($data) ? ['stage' => $data['stage'] ?? null, 'percent' => $data['percent'] ?? null] : ['stage' => null]);
}

function handle_telegram_queue() {
    $bot = telegram_connection();
    telegram_settle_native_posts(app_user_id());
    $scheduled = array_map(fn($e) => telegram_post_summary($e['text'], count($e['media'])) + telegram_channel_fields($e['channel'] ?? null) + ['id' => $e['id'], 'publish_at' => (int)$e['publish_at'],
        'status' => $e['status'] ?? 'pending', 'attempts' => (int)($e['attempts'] ?? 0), 'error' => (string)($e['error'] ?? '')], outbox_load());
    usort($scheduled, fn($a, $b) => $a['publish_at'] <=> $b['publish_at']);
    json_response(['connected' => $bot !== null, 'native_account' => telegram_user_connection() !== null, 'channel' => $bot ? ['title' => $bot['chat_title'] ?? '', 'username' => $bot['chat_username'] ?? ''] : null, 'channels' => channels_public(),
        'scheduled' => $scheduled, 'published' => published_load(null, 10)]);
}

/** ?from=&to= (unix): всё для календаря — каналы с расписанием, посты очереди и последние отправленные. */
function handle_calendar() {
    $bot = telegram_connection();
    telegram_settle_native_posts(app_user_id());
    $post = fn($e) => telegram_post_summary($e['text'] ?? '', count($e['media'] ?? [])) + telegram_channel_fields($e['channel'] ?? null) + ['id' => $e['id'] ?? '',
        'status' => $e['status'] ?? 'pending', 'error' => (string)($e['error'] ?? ''), 'full_text' => mb_substr(trim((string)($e['text'] ?? '')), 0, 600),
        'article' => $e['article'] ?? null, 'group' => (string)($e['group'] ?? ''), 'kind' => (string)($e['media'][0]['kind'] ?? ''), 'duration' => (int)($e['media'][0]['duration'] ?? 0),
        'poll' => !empty($e['poll'])] + calendar_thumb_fields($e);
    $published = calendar_published_recent(app_user_id());   // повторы очереди — против опубликованного за 60 дней
    $channels = array_map(fn($c) => $c + ['reach' => channel_reach_cached($c)], channels_public());   // охват по часам — из кэша, без ожидания сети
    $by_id = array_column($channels, null, 'id');
    $queue = array_map(function ($e) use ($post, $published, $by_id) {
        $ch = $by_id[$e['channel'] ?? ''] ?? null;
        $forecast = $ch ? channel_forecast($ch['reach'], (int)$e['publish_at'], channel_schedule($ch)['tz']) : null;   // прогноз просмотров по часу канала
        return $post($e) + ['at' => (int)$e['publish_at'], 'forecast' => $forecast, 'image_key' => (string)($e['media'][0]['src'] ?? ''),   // та же картинка, что у записи Предложки: пометка «в расписании»
            'duplicate' => calendar_duplicate_of(['image' => (string)($e['media'][0]['src'] ?? ''), 'text' => (string)($e['text'] ?? '')], (string)($e['channel'] ?? ''), $published)];
    }, outbox_load());
    $sent = array_map(fn($e) => telegram_post_summary($e['text'] ?? '', (int)($e['media'] ?? 0)) + ['id' => '', 'at' => (int)($e['scheduled_for'] ?: $e['at']), 'status' => ($e['status'] ?? 'ok') === 'ok' ? 'sent' : 'failed',
        'error' => (string)($e['error'] ?? ''), 'url' => (string)($e['url'] ?? ''), 'channel' => (string)($e['channel'] ?? ''), 'channel_title' => (string)($e['channel_title'] ?? ''), 'full_text' => (string)($e['text'] ?? ''),
        'kind' => (string)($e['kind'] ?? ''), 'duration' => (int)($e['duration'] ?? 0), 'poll' => false] + calendar_thumb_fields($e), published_load(null, 200));
    $history_pending = false;
    $external = calendar_external_posts($channels, published_load(null, 500), $history_pending);   // посты канала, вышедшие мимо Posta, за всё прошлое календаря
    json_response(['connected' => $bot !== null, 'now' => time(), 'channels' => $channels, 'posts' => [...$external, ...$sent, ...$queue], 'history_pending' => $history_pending, 'hints_seen' => hint_seen_times()['calendar_v3'] ?? null]);
}

/** Миниатюра карточки: маленькая миниатюра поста; у старых постов очереди без неё — первая картинка поста (полный файл). */
function calendar_thumb_fields(array $e): array {
    if (($e['thumb_id'] ?? '') !== '') return ['thumb' => true, 'thumb_url' => 'index.php?' . http_build_query(['action' => 'post_thumb', 'id' => $e['thumb_id']])];
    $queued_photo = ($e['status'] ?? '') !== 'telegram' && ($e['id'] ?? '') !== '' && is_array($e['media'] ?? null) && ($e['media'][0]['kind'] ?? '') === 'photo';
    return $queued_photo ? ['thumb' => true, 'thumb_url' => 'index.php?' . http_build_query(['action' => 'outbox_media', 'id' => $e['id']])] : ['thumb' => false, 'thumb_url' => ''];
}

/** {channels, from, to, include_rest}: план «Заполнить дыры» — пост из Предложки на каждый свободный слот. */
function handle_calendar_fill_plan($in) {
    $from = (int)($in['from'] ?? time());
    $to = (int)($in['to'] ?? time() + 7 * DAY_SECONDS);
    if ($to <= $from || $to - $from > 31 * DAY_SECONDS) json_error('Период плана — не больше 31 дня.');
    $wanted = array_map('strval', (array)($in['channels'] ?? []));
    $channels = array_values(array_filter(channels_public(), fn($c) => !$wanted || in_array((string)$c['id'], $wanted, true)));
    ok(['plan' => calendar_fill_plan(app_user_id(), $channels, $from, $to, !empty($in['include_rest']), proposal_items(load_history()), outbox_load(), time())]);
}

/** {items: [{slot_at, channel, image, text}]}: поставить план в очередь той же подготовкой, что обычная публикация. */
function handle_calendar_fill_apply($in) {
    ok(calendar_fill_apply(app_user_id(), array_values(array_filter((array)($in['items'] ?? []), 'is_array'))));
}

/** {image, text, channel}: повтор для черновика Предложки — прошлая публикация в этом канале или null. */
function handle_calendar_check($in) {
    $dup = calendar_duplicate_of(['image' => (string)($in['image'] ?? ''), 'text' => (string)($in['text'] ?? '')], (string)($in['channel'] ?? ''),
        calendar_history(app_user_id(), outbox_load()));
    ok(['duplicate' => $dup]);
}

const HINT_NAMES = ['calendar_v3'];   // подсказки, которые показываются один раз: имя → время показа в настройках аккаунта

/** Когда показаны подсказки: {имя: unix-время} из настроек аккаунта. */
function hint_seen_times() {
    $hints = load_settings()['hints'] ?? [];
    return is_array($hints) ? $hints : [];
}

/** {name}: отметить подсказку показанной — запоминается в аккаунте, а не в браузере. */
function handle_hint_seen($in) {
    $name = (string)($in['name'] ?? '');
    if (!in_array($name, HINT_NAMES, true)) json_error('Неизвестная подсказка.');
    $hints = hint_seen_times();
    $hints[$name] = time();
    db_save_settings([...load_settings(), 'hints' => $hints]);
    ok(['hints_seen' => $hints]);
}

/** {id, publish_at}: копия поста очереди на другое время (перетаскивание с зажатой клавишей). */
function handle_telegram_duplicate($in) {
    if (!preg_match('/^[a-f0-9]{12}$/', $id = (string)($in['id'] ?? ''))) json_error('Некорректный номер поста.');
    $at = (int)($in['publish_at'] ?? 0);
    if ($at < time() + TELEGRAM_MIN_DELAY_SECONDS) json_error('Отложенный пост нужно назначить минимум на минуту вперёд.');
    if ($at > time() + TELEGRAM_MAX_DELAY_DAYS * DAY_SECONDS) json_error('Отложить пост можно не больше чем на ' . TELEGRAM_MAX_DELAY_DAYS . ' дней.');
    if ($denied = billing_post_denied(true)) json_error($denied, 429);
    $copy = outbox_duplicate_post($id, $at) ?? json_error('Копировать можно только пост нашей очереди, который ждёт или не вышел.', 409);
    ok(['id' => $copy['id'], 'publish_at' => $at]);
}

/** ?id=: миниатюра карточки (160 px), только своя — записана в очереди или журнале этого пользователя. */
function handle_post_thumb() {
    $id = (string)($_GET['id'] ?? '');
    $file = thumb_file($id);
    if (!thumb_owned($id, app_user_id()) || !is_file($file)) { http_response_code(404); exit; }
    header('Content-Type: image/jpeg');
    header('Cache-Control: private, max-age=86400');
    header('X-Content-Type-Options: nosniff');
    readfile($file);
    exit;
}

/** ?id=&n=: первая картинка поста нашей очереди, полного размера (для просмотрщика). */
function handle_outbox_media() {
    $id = (string)($_GET['id'] ?? '');
    $post = array_values(array_filter(outbox_load(), fn($e) => ($e['id'] ?? '') === $id && ($e['status'] ?? '') !== 'telegram'))[0] ?? null;
    $file = $post && preg_match('/^[a-f0-9]{12}$/', $id) ? outbox_media_dir($id) . '/' . basename($post['media'][0]['path'] ?? '') : '';
    if (!$post || ($post['media'][0]['kind'] ?? '') !== 'photo' || !is_file($file)) { http_response_code(404); exit; }
    header('Content-Type: ' . (mime_content_type($file) ?: 'application/octet-stream'));
    header('Cache-Control: private, max-age=300');
    header('X-Content-Type-Options: nosniff');
    readfile($file);
    exit;
}

/** Снять одну копию с расписания: из очереди (queued, failed) или из «Отложенных» Telegram. Исключение — с кодом HTTP. */
function telegram_cancel_one($id) {
    $queued = array_values(array_filter(outbox_load(), fn($e) => ($e['id'] ?? '') === $id))[0] ?? null;
    if (($queued['status'] ?? '') === 'sending') throw new RuntimeException('Пост уже отправляется — отменить нельзя.', 409);
    if (in_array($queued['status'] ?? '', ['queued', 'failed'], true)) {
        if (!outbox_remove_post($id)) throw new RuntimeException('Этого поста уже нет в очереди.', 404);
        return;
    }
    foreach (outbox_load() as $e) {
        if (($e['id'] ?? '') !== $id || ($e['status'] ?? '') !== 'telegram' || !($bot = telegram_connection(null, $e['channel'] ?? null))) continue;
        try { telegram_user_cancel(app_user_id(), $bot, array_map('intval', $e['tg_ids'] ?? [])); }
        catch (RuntimeException $err) { throw new RuntimeException('Не удалось убрать пост из отложенных в Telegram: ' . $err->getMessage() . ' Удалите его в канале вручную («Отложенные»).', 502); }
    }
    if (!telegram_unqueue_post(app_user_id(), $id)) throw new RuntimeException('Этого поста уже нет в очереди: возможно, он отправлен.', 404);
}

/** {id, only?}: убрать пост из расписания. Пост из группы — вся группа, если не only («только в этом канале»).
 *  Частичный успех группы — не ошибка: в ответе errors по копиям, которые не сняли. */
function handle_telegram_cancel($in) {
    if (!preg_match('/^[a-f0-9]{12}$/', $id = (string)($in['id'] ?? ''))) json_error('Некорректный номер поста.');
    $group = (string)((array_values(array_filter(outbox_load(), fn($e) => ($e['id'] ?? '') === $id))[0]['group'] ?? ''));
    $ids = empty($in['only']) && $group !== '' ? array_column(array_filter(outbox_load(), fn($e) => ($e['group'] ?? '') === $group), 'id') : [$id];
    $cancelled = 0;
    $errors = [];
    // Записи, из которых были эти посты: если в очереди их не осталось — запись возвращается в «Предложку» (часть 2.4)
    $records = array_values(array_unique(array_filter(array_map(fn($e) => (string)($e['record'] ?? ''), array_filter(outbox_load(), fn($e) => in_array($e['id'] ?? '', $ids, true))))));
    foreach ($ids as $one) {
        try { telegram_cancel_one($one); $cancelled++; }
        catch (RuntimeException $err) {
            if ($one === $id) json_error($err->getMessage(), $err->getCode() ?: 400);
            $errors[] = ['id' => $one, 'error' => $err->getMessage()];
        }
    }
    foreach ($records as $key) {
        if (array_filter(outbox_load(), fn($e) => ($e['record'] ?? '') === $key)) continue;   // другой пост этой записи ещё в очереди
        try { content_unmark_posted($key); }
        catch (Throwable $e) { error_log('posta: снятие отметки публикации: ' . $e->getMessage()); }
    }
    ok(['cancelled' => $cancelled, 'errors' => $errors]);
}

// ---- Панель модели ----

/** Модели по ролям без пробного запроса: панель ничего не грузит, пока её просто открыли. */
function model_panel_roles() {
    $prescreen = array_values(array_filter(PRESCREEN_MODELS, 'model_installed'))[0] ?? ANALYSIS_MODEL;
    return ['prescreen' => $prescreen, 'analysis' => ANALYSIS_MODEL];
}

function model_panel_loaded() {
    return array_map(fn($m) => ['name' => (string)($m['name'] ?? ''), 'size_mb' => (int)round(($m['size'] ?? 0) / 1048576), 'vram_mb' => (int)round(($m['size_vram'] ?? 0) / 1048576),
        'gpu_percent' => ($m['size'] ?? 0) > 0 ? (int)round(100 * ($m['size_vram'] ?? 0) / $m['size']) : 0, 'context' => (int)($m['context_length'] ?? 0),
        'expires_at' => (string)($m['expires_at'] ?? '')], ollama_loaded_models());
}

function handle_model_status() {
    $version = http_get_json(OLLAMA_URL . '/api/version', [CURLOPT_TIMEOUT => 3])['version'] ?? null;
    // nvidia-smi из-под Apache обычно недоступен (PrivateDevices): тогда интерфейс считает видеопамять по моделям
    $gpu = preg_match('/^(\d+),\s*(\d+),\s*(\d+)/', trim((string)@shell_exec('nvidia-smi --query-gpu=memory.used,memory.total,utilization.gpu --format=csv,noheader,nounits 2>/dev/null')), $m)
        ? ['used_mb' => (int)$m[1], 'total_mb' => (int)$m[2], 'util' => (int)$m[3]] : null;
    json_response(['ollama' => $version !== null, 'version' => $version, 'loaded' => $version !== null ? model_panel_loaded() : [], 'roles' => $version !== null ? model_panel_roles() : ['prescreen' => ANALYSIS_MODEL, 'analysis' => ANALYSIS_MODEL],
        'gpu' => $gpu, 'parser_running' => app_user_bind() !== '' && content_parser_is_running()]);
}

/** Загрузка с параметрами парсера (иначе первый же его запрос перезагрузит модель); остальные модели выгружаются. */
function model_panel_load($model) {
    foreach (array_column(ollama_loaded_models(), 'name') as $name) if ($name !== $model) ollama_unload_model($name);
    $r = http_post_json(OLLAMA_GENERATE_URL, array_diff_key(ollama_common_payload(['model' => $model]), ['think' => 1]) + ['stream' => false], 300);
    if ($r['status'] !== 200) throw new RuntimeException("Ollama не загрузила {$model}: " . ($r['data']['error'] ?? ($r['error'] ?: "HTTP {$r['status']}")));
}

/** {op: load|unload|unload_all|restart, role?, model?}. */
function handle_model_control($in) {
    $started = microtime(true);
    $roles = model_panel_roles();
    $loaded = array_column(ollama_loaded_models(), 'name');
    $op = (string)($in['op'] ?? '');
    if ($op === 'load') {
        $model = $roles[(string)($in['role'] ?? '')] ?? json_error('Неизвестная роль модели.');
        model_panel_load($model);
        $message = "Загружена $model";
    } elseif ($op === 'unload') {
        if (!in_array($model = (string)($in['model'] ?? ''), $loaded, true)) json_error('Эта модель сейчас не загружена.');
        ollama_unload_model($model);
        $message = "Выгружена $model";
    } elseif ($op === 'unload_all' || $op === 'restart') {
        array_map('ollama_unload_model', $loaded);
        if ($op === 'restart') model_panel_load($target = $loaded[0] ?? $roles['prescreen']);
        $message = $op === 'restart' ? "Перезапущена $target" : ($loaded ? 'Выгружены: ' . implode(', ', $loaded) : 'Загруженных моделей не было');
    } else json_error('Неизвестное действие.');
    ok(['message' => $message . sprintf(' за %.1f с.', microtime(true) - $started)]);
}

// ---- Остановка приложения: сборщики всех пользователей, помощник Telegram, модели ----

function app_process_list() {
    $found = [];
    foreach (glob('/proc/[0-9]*/cmdline') ?: [] as $file) {
        $cmd = str_replace("\0", ' ', (string)@file_get_contents($file));
        if (($pid = (int)substr($file, 6, -8)) !== getmypid() && (str_contains($cmd, PARSER_SCRIPT) || str_contains($cmd, 'tg_user.py'))) $found[$pid] = trim($cmd);
    }
    return $found;
}

/** Только владелец: останавливает процессы всех пользователей. */
function handle_app_stop($in) {
    if (!radar_is_owner()) json_error('Неизвестное действие API.', 404);
    tab_session_close(request_tab_id($in));   // вкладка больше не ведёт сессию: сборщик не перезапустится сам
    $kill = fn($sig) => array_map(fn($pid) => function_exists('posix_kill') ? @posix_kill($pid, $sig) : @exec("kill -$sig $pid"), array_keys(app_process_list()));
    $count = count($kill(15));
    for ($i = 0; $i < 20 && app_process_list(); $i++) usleep(200000);
    $kill(9);
    foreach (array_unique([...PRESCREEN_MODELS, ANALYSIS_MODEL, SMART_MODEL]) as $model) { try { ollama_unload_model($model); } catch (Throwable $e) {} }
    ok(['stopped' => $count]);
}

// ---- Тариф, баланс и приглашения ----

function billing_ledger_public($uid) {
    return array_map(fn($r) => ['at' => (int)$r['at'], 'amount' => kop_to_rub((int)$r['amount']), 'kind' => LEDGER_KINDS[$r['kind']] ?? $r['kind'], 'note' => $r['note']],
        db_all('SELECT at, amount, kind, note FROM ledger WHERE user_id = ? ORDER BY id DESC LIMIT 30', [$uid]));
}

function handle_billing_info() {
    $uid = app_user_id();
    $limits = billing_limits($uid);
    json_response(['balance' => kop_to_rub(balance_kop($uid)), 'plan' => $limits, 'enforce' => BILLING_ENFORCE, 'period_days' => BILLING_PERIOD_DAYS,
        'usage' => ['hour' => billing_posts_since(time() - 3600, $uid), 'day' => billing_posts_since(time() - DAY_SECONDS, $uid)],
        'plans' => array_map(fn($id, $p) => ['id' => $id, 'posts_per_hour' => billing_hour_limit($p['posts_per_day']), 'days' => billing_plan_days($p),
            'available' => empty($p['once']) || billing_once_available($uid)] + $p, array_keys(BILLING_PLANS), BILLING_PLANS),
        'custom' => BILLING_CUSTOM + ['hour_share' => BILLING_HOUR_SHARE],
        'referral' => ['code' => referral_code($uid), 'percent' => REFERRAL_PERCENT, 'bound' => (string)db_value('SELECT referred_by FROM users WHERE id = ?', [$uid]) !== ''] + referral_stats($uid),
        'ledger' => billing_ledger_public($uid)]);
}

/** plan: id готового тарифа или custom (+ channels, posts_per_day). */
function handle_billing_buy($in) {
    $plan = billing_buy(app_user_id(), (string)($in['plan'] ?? ''), (int)($in['channels'] ?? 0), (int)($in['posts_per_day'] ?? 0));
    ok(['plan' => $plan, 'balance' => kop_to_rub(balance_kop())]);
}

function handle_referral_bind($in) {
    $reason = referral_bind(app_user_id(), (string)($in['code'] ?? ''));
    ok(['bound' => $reason === '', 'message' => $reason]);
}

// ---- Токен API: пост в подборку из внешних сервисов ----

function handle_api_token_status() { json_response(['exists' => api_token_exists(app_user_id())]); }

function handle_api_token_create() { ok(['token' => api_token_create(app_user_id())]); }

function handle_api_token_revoke() { api_token_revoke(app_user_id()); ok(); }

/** POST api_add_post: Authorization: Bearer <токен>, JSON {text, image_url | image_urls}. Без куки: сайт-пользователь не создаётся. */
function handle_api_add_post(?string $body = null) {
    $uid = api_token_user(api_bearer_token()) ?? json_error('Неверный токен.', 401);
    app_user_bind($uid);
    if (!api_rate_take($uid)) json_error('Слишком много запросов: не больше ' . API_RATE_PER_HOUR . ' в час.', 429);
    $in = json_decode($body ?? (string)file_get_contents('php://input'), true);
    if (!is_array($in)) json_error('Тело запроса должно быть JSON-объектом.');
    set_time_limit(60);
    ok(api_add_post($in));
}
