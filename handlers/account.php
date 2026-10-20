<?php
// Личный кабинет, VK ID, Telegram (бот и личный аккаунт), публикация постов, панель модели и остановка приложения.

const ACCOUNT_COOKIE_INFO = [
    USER_COOKIE => ['Ваш идентификатор. По нему сайт находит вашу папку с пулом, историей оценок и темой. Без неё сайт заведёт нового пользователя с пустой историей.', USER_COOKIE_DAYS . ' дней, продлевается при каждом заходе'],
    'PHPSESSID' => ['Сессия PHP. Нужна только на время входа через VK (защита от подмены ответа) и для демо-режима.', 'до закрытия браузера'],
];
const ACCOUNT_FILE_INFO = ['pool.json' => 'Пул отобранных записей, которые ждут вашей оценки', 'history.json' => 'История оценок: лайки, дизлайки, «не знаю», жалобы',
    'settings.json' => 'Настройки: тема поиска', 'connections.json' => 'Подключённые соцсети (токен бота Telegram)', 'last-seen' => 'Отметка последнего захода — по ней удаляются давно брошенные профили'];

function account_file_description($name) {
    return ACCOUNT_FILE_INFO[$name] ?? match (true) {
        str_ends_with($name, '.lock') => 'Служебная блокировка (защищает файл от одновременной записи)',
        str_starts_with($name, 'parser-') && str_ends_with($name, '.log') => 'Журнал вашего парсера (то, что видно в «Журнале»)',
        str_starts_with($name, 'parser-') && str_ends_with($name, '.session') => 'Какая вкладка сейчас ведёт сессию',
        str_starts_with($name, 'parser-') => 'Состояние парсера: когда и с каким процессом запущен',
        default => 'Служебный файл'
    };
}

/** Какие служебные файлы отдаются по HTTP (Apache не читает .htaccess). Проверка запросом к себе, кэш 10 минут. */
function account_web_exposure() {
    $file = sys_get_temp_dir() . '/postmaker-exposure-' . substr(sha1(ROOT . '/handlers'), 0, 8) . '.json';
    if (time() - (int)(($cached = load_data($file, []))['at'] ?? 0) < 600) return $cached['exposed'];
    $base = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off' ? 'https' : 'http') . '://127.0.0.1:' . (int)($_SERVER['SERVER_PORT'] ?? 80) . rtrim(dirname($_SERVER['SCRIPT_NAME'] ?? '/'), '/\\');
    $exposed = array_values(array_filter(['.git/config', 'sources_db.json', 'README.md'], function ($path) use ($base) {
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
            'http_only' => $name === USER_COOKIE || ($name === session_name() && (bool)ini_get('session.cookie_httponly'))];
    }
    if (isset($_COOKIE[session_name()])) @session_start(['read_and_close' => true]);   // только ключи: в значениях служебные секреты входа VK
    json_response(['user_id' => app_user_id(), 'topic' => current_topic(), 'last_seen' => (int)@filemtime(user_dir() . '/last-seen'), 'pool' => count(pool_topic_positions(pool_load())),
        'counts' => array_map('count', array_intersect_key($history, array_flip(HISTORY_COLLECTIONS))), 'files' => $files, 'cookies' => $cookies,
        'session_keys' => array_keys($_SESSION ?? []), 'retention_days' => USER_RETENTION_DAYS, 'exposed' => account_web_exposure(),
        'fix_command' => 'sudo sh ' . ROOT . '/tools/apache-setup.sh', 'telegram' => account_telegram_public(), 'telegram_user' => telegram_user_public()]);
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

/** {token, channel}: проверяет бота, канал и право публиковать, сохраняет подключение (токен — зашифрованным). */
function handle_telegram_connect($in) {
    $token = trim((string)($in['token'] ?? ''));
    $channel = preg_replace('~^(https?://)?t\.me/~i', '@', trim((string)($in['channel'] ?? '')));
    if (!preg_match('/^\d{5,12}:[A-Za-z0-9_-]{30,50}$/', $token)) json_error('Токен бота выглядит иначе: 123456789:AA… — его выдаёт @BotFather.');
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
    update_connections(fn($c) => ['telegram' => ['token_enc' => secret_encrypt($token), 'bot_id' => $bot['id'], 'bot_username' => $bot['username'] ?? '', 'chat_id' => $chat['id'],
        'chat_title' => $chat['title'] ?? '', 'chat_username' => $chat['username'] ?? '', 'chat_type' => $chat['type'] ?? '', 'connected_at' => time()]] + $c);
    ok(['telegram' => account_telegram_public()]);
}

function handle_telegram_disconnect() { update_connections(fn($c) => isset($c['telegram']) ? array_diff_key($c, ['telegram' => 1]) : null); ok(); }

// ---- Telegram: личный аккаунт для отложенных постов ----

/** Ключи из формы (запоминаются для всего сайта) или уже сохранённые. */
function telegram_user_keys_from_input(array $in) {
    [$id, $hash] = [trim((string)($in['api_id'] ?? '')), trim((string)($in['api_hash'] ?? ''))];
    if ($id === '' && $hash === '') {
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

function require_bot() { return telegram_connection() ?? json_error('Сначала подключите бота и канал: отложенный пост ставится в тот же канал.'); }

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

/** text, media (JSON [{kind, src, name, upload}]), scheduled_ts (пусто — сразу), files[]. Отложенный пост — в «Отложенные» через аккаунт. */
function handle_telegram_publish($in) {
    $bot = telegram_connection() ?? json_error('Telegram не подключён: подключите бота и канал в «Личном кабинете» → «Соцсети».', 409);
    // Запрос больше post_max_size: PHP молча отбрасывает и поля, и файлы
    if (!$_POST && !$_FILES && !$in && (int)($_SERVER['CONTENT_LENGTH'] ?? 0) > ini_parse_quantity((string)ini_get('post_max_size'))) {
        json_error('Файлы вместе весят больше лимита сервера (' . ini_get('post_max_size') . '). Уберите часть файлов или выполните tools/apache-setup.sh, чтобы поднять лимит.', 413);
    }
    $fields = $_POST ?: $in;
    $text = trim((string)($fields['text'] ?? ''));
    $items = is_string($items = $fields['media'] ?? []) ? json_decode($items, true) : $items;
    if (!is_array($items)) json_error('Некорректный список медиа.');
    $items = array_slice(array_values(array_filter($items, 'is_array')), 0, TELEGRAM_MAX_MEDIA + 1);
    $at = (int)($fields['scheduled_ts'] ?? 0);
    if ($at !== 0 && $at < time() + TELEGRAM_MIN_DELAY_SECONDS) json_error('Отложенный пост нужно назначить минимум на минуту вперёд.');
    if ($at > time() + TELEGRAM_MAX_DELAY_DAYS * DAY_SECONDS) json_error('Отложить пост можно не больше чем на ' . TELEGRAM_MAX_DELAY_DAYS . ' дней.');
    if ($at !== 0 && telegram_user_connection() === null) {
        json_error('Отложенные посты ставятся средствами Telegram: подключите свой аккаунт в «Личном кабинете» → «Соцсети» → Telegram (блок «Отложенные посты средствами Telegram»). Без отложки пост можно опубликовать сразу.', 422);
    }
    @set_time_limit(900);       // большое видео заливается и перекодируется долго
    ignore_user_abort(true);
    $work = null;
    try {
        telegram_check_text($text, count($items));
        $prepared = telegram_prepare_media($items, publish_collect_uploads(), $work = telegram_work_dir(), $at ? 'account' : 'bot');
        if ($at) {
            $ids = telegram_user_schedule(app_user_id(), $bot, $text, $prepared, $at);
            $response = ['scheduled' => true, 'id' => telegram_record_native_post(app_user_id(), $text, $prepared, $at, $ids), 'publish_at' => $at, 'native' => true];
        } else {
            $sent = telegram_publish_prepared($bot, $text, $prepared);
            telegram_log_published(app_user_id(), telegram_post_summary($text, count($prepared)) + ['at' => time(), 'scheduled_for' => 0, 'url' => $sent['url'], 'status' => 'ok', 'error' => '']);
            $response = ['scheduled' => false, 'url' => $sent['url'], 'messages' => count($sent['message_ids'])];
        }
    } catch (RuntimeException $e) {
        $failure = $e->getMessage();
    } finally {
        if ($work !== null) remove_dir($work);
    }
    isset($failure) ? json_error($failure, 422) : ok($response);
}

function handle_telegram_queue() {
    $bot = telegram_connection();
    telegram_settle_native_posts(app_user_id());
    $scheduled = array_map(fn($e) => telegram_post_summary($e['text'], count($e['media'])) + ['id' => $e['id'], 'publish_at' => (int)$e['publish_at'],
        'status' => $e['status'] ?? 'pending', 'attempts' => (int)($e['attempts'] ?? 0), 'error' => (string)($e['error'] ?? '')], outbox_load());
    usort($scheduled, fn($a, $b) => $a['publish_at'] <=> $b['publish_at']);
    json_response(['connected' => $bot !== null, 'channel' => $bot ? ['title' => $bot['chat_title'] ?? '', 'username' => $bot['chat_username'] ?? ''] : null,
        'scheduled' => $scheduled, 'published' => published_load(null, 10)]);
}

/** {id}: убрать отложенный пост из списка и из «Отложенных» канала. */
function handle_telegram_cancel($in) {
    if (!preg_match('/^[a-f0-9]{12}$/', $id = (string)($in['id'] ?? ''))) json_error('Некорректный номер поста.');
    foreach (outbox_load() as $e) {
        if (($e['id'] ?? '') !== $id || ($e['status'] ?? '') !== 'telegram' || !($bot = telegram_connection())) continue;
        try { telegram_user_cancel(app_user_id(), $bot, array_map('intval', $e['tg_ids'] ?? [])); }
        catch (RuntimeException $err) { json_error('Не удалось убрать пост из отложенных в Telegram: ' . $err->getMessage() . ' Удалите его в канале вручную («Отложенные»).', 502); }
    }
    telegram_unqueue_post(app_user_id(), $id) ? ok() : json_error('Этого поста уже нет в очереди: возможно, он отправлен.', 404);
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

function handle_app_stop($in) {
    tab_session_close(request_tab_id($in));   // вкладка больше не ведёт сессию: сборщик не перезапустится сам
    $kill = fn($sig) => array_map(fn($pid) => function_exists('posix_kill') ? @posix_kill($pid, $sig) : @exec("kill -$sig $pid"), array_keys(app_process_list()));
    $count = count($kill(15));
    for ($i = 0; $i < 20 && app_process_list(); $i++) usleep(200000);
    $kill(9);
    foreach (array_unique([...PRESCREEN_MODELS, ANALYSIS_MODEL, SMART_MODEL]) as $model) { try { ollama_unload_model($model); } catch (Throwable $e) {} }
    ok(['stopped' => $count]);
}
