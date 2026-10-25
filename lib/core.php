<?php
// Ядро: пользователи, HTTP, шифрование секретов, настройки, загрузка картинок.

// ---- JSON-файлы: только временные кэши в /tmp. Данные — в SQLite (lib/db.php, lib/db_access.php) ----

function load_data($file, $default = []) {
    $data = is_file($file) ? json_decode((string)file_get_contents($file), true) : null;
    return is_array($data) ? $data : $default;
}

/** Блокировка без ожидания (очереди, подборы): ресурс или null, если занято. */
function try_lock($path) {
    $lock = @fopen($path, 'c');
    if ($lock && flock($lock, LOCK_EX | LOCK_NB)) return $lock;
    if ($lock) fclose($lock);
    return null;
}

function remove_dir($dir) {
    foreach (is_dir($dir) ? scandir($dir) : [] as $name) {
        if ($name === '.' || $name === '..') continue;
        $path = "$dir/$name";
        is_dir($path) && !is_link($path) ? remove_dir($path) : @unlink($path);
    }
    @rmdir($dir);
}

function cli_echo($line) { if (PHP_SAPI === 'cli') { echo $line . "\n"; @flush(); } }

// ---- Пользователи: id в куке, папка users/<id>/ ----

function is_valid_user_id($uid) { return is_string($uid) && preg_match('/^[a-f0-9]{32}$/', $uid) === 1; }

/** Текущий пользователь процесса ('' — не задан); с аргументом — привязать. */
function app_user_bind($uid = null) {
    static $bound = '';
    if ($uid !== null) $bound = is_valid_user_id($uid) ? $uid : '';
    return $bound;
}

function app_user_id() {
    return app_user_bind() ?: throw new RuntimeException('Пользователь не определён.');
}

/** Веб-запрос: из сессии или куки; кука сессии устанавливается, старая кука удаляется. */
function app_user_from_request() {
    $result = session_resolve($_COOKIE[SESSION_COOKIE] ?? '', $_COOKIE[USER_COOKIE] ?? '');
    $uid = $result['uid'];
    if ($result['sid']) session_cookie_set($result['sid']);
    if ($result['drop_legacy'] && !headers_sent()) {
        setcookie(USER_COOKIE, '', ['expires' => 1, 'path' => rtrim(dirname($_SERVER['SCRIPT_NAME'] ?? '/'), '/\\') . '/']);
    }
    return app_user_bind($uid);
}

function users_ensure_root() {
    if (is_dir(USERS_DIR)) return;
    @mkdir(USERS_DIR, 0775, true);
    @file_put_contents(USERS_DIR . '/.htaccess', "Require all denied\n");
    @file_put_contents(USERS_DIR . '/index.html', '');
}

function user_dir($uid = null) {
    $dir = USERS_DIR . '/' . ($uid ?? app_user_id());
    if (!is_dir($dir)) {
        users_ensure_root();
        if (!@mkdir($dir, 0775) && !is_dir($dir)) throw new RuntimeException('Не удалось создать папку пользователя.');
    }
    return $dir;
}

function parser_runtime_file($suffix) { return user_dir() . '/parser-' . PARSER_RUNTIME_VERSION . $suffix; }
function empty_history() { return array_fill_keys(HISTORY_COLLECTIONS, []); }
function load_history() { return history_load(); }

/** Отметка захода (в базе, не чаще раза в 10 минут) и раз в час — удаление брошенных и пустых профилей.
 *  Раньше отметка жила в static-переменных, которые обнуляются на каждом запросе: last_seen не писался вовсе. */
function user_touch() {
    $now = time();
    $uid = app_user_id();
    db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, $now]);
    db_exec('UPDATE users SET last_seen = ? WHERE id = ? AND last_seen < ?', [$now, $uid, $now - 600]);
    if ($now - (int)kv_get('users_gc_at', 0) < 3600) return;
    kv_update('users_gc_at', fn() => $now);
    users_gc($uid);
}

/** Пустой профиль: ни оценок, ни подключений, ни постов, ни денег, ни приглашённых. Вошедший через Telegram — не пустой. */
function user_is_empty($id) {
    if (user_has_identity($id)) return false;   // вошедший — не пустой
    return !db_value('SELECT 1 FROM history_items WHERE user_id = ? LIMIT 1', [$id]) && !db_value('SELECT 1 FROM outbox WHERE user_id = ? LIMIT 1', [$id])
        && !db_value('SELECT 1 FROM published WHERE user_id = ? LIMIT 1', [$id]) && !db_value('SELECT 1 FROM ledger WHERE user_id = ? LIMIT 1', [$id])
        && !db_value('SELECT 1 FROM users WHERE referred_by = ? LIMIT 1', [$id])
        && db_one("SELECT 1 FROM users WHERE id = ? AND settings IN ('', '{}') AND connections IN ('', '{}')", [$id]) !== null;
}

/** Удаляет профили без заходов дольше USER_RETENTION_DAYS и пустые — через EMPTY_USER_RETENTION_HOURS; профили без входа (логин или Telegram) не трогает. Чистит сессии и login_requests. */
function users_gc($keep = '', $dry_run = false) {
    $now = time();
    if (!$dry_run) {
        // Удалить старые сессии
        db_exec('DELETE FROM sessions WHERE last_seen < ?', [$now - USER_COOKIE_DAYS * DAY_SECONDS]);
        // Удалить старые login_requests (старше суток)
        db_exec('DELETE FROM login_requests WHERE created_at < ?', [$now - DAY_SECONDS]);
        db_exec('DELETE FROM login_failures WHERE at < ?', [$now - DAY_SECONDS]);
    }
    $removed = 0;
    foreach (db_all('SELECT id, last_seen, created_at FROM users') as $row) {
        if ($row['id'] === $keep || user_has_identity($row['id']) || db_value('SELECT 1 FROM ledger WHERE user_id = ? LIMIT 1', [$row['id']])) continue;
        $idle = $now - (int)($row['last_seen'] ?: $row['created_at'] ?: $now);
        if ($idle < USER_RETENTION_DAYS * DAY_SECONDS && ($idle < EMPTY_USER_RETENTION_HOURS * 3600 || !user_is_empty($row['id']))) continue;
        $removed++;
        if ($dry_run) continue;
        db_exec('DELETE FROM users WHERE id = ?', [$row['id']]);
        if (is_valid_user_id($row['id'])) remove_dir(USERS_DIR . '/' . $row['id']);
    }
    return $removed;
}

/** Инициализация данных нового пользователя и нормализация каталога источников. */
function ensure_default_project_data() {
    $uid = app_user_id();
    load_settings($uid);
    history_load($uid);
    $db = load_source_db();
    $normalized = normalize_source_db($db);
    if ($db !== $normalized) update_source_db(fn() => $normalized);
}

/** История пользователя из БД (или пусто). */
function load_history_sql($uid = null): array {
    return history_load($uid);
}

// ---- HTTP ----

const HTTP_UA = 'Posta/1.0';
const BROWSER_UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36';

function http_get($url, array $options = []) {
    $ch = curl_init($url);
    curl_setopt_array($ch, $options + [CURLOPT_RETURNTRANSFER => true, CURLOPT_CONNECTTIMEOUT => 5, CURLOPT_TIMEOUT => 15,
        CURLOPT_FOLLOWLOCATION => true, CURLOPT_MAXREDIRS => 3, CURLOPT_USERAGENT => HTTP_UA]);
    $body = curl_exec($ch);
    $status = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return $body !== false && $status >= 200 && $status < 300 ? $body : false;
}

function http_get_json($url, array $options = []) {
    $data = json_decode((string)http_get($url, $options), true);
    return is_array($data) ? $data : [];
}

/** @return array{body: string|false, status: int, error: string} */
function http_post($url, $body, array $options = []) {
    $ch = curl_init($url);
    curl_setopt_array($ch, $options + [CURLOPT_RETURNTRANSFER => true, CURLOPT_POST => true, CURLOPT_POSTFIELDS => $body,
        CURLOPT_CONNECTTIMEOUT => 5, CURLOPT_TIMEOUT => 15]);
    $result = ['body' => curl_exec($ch), 'status' => (int)curl_getinfo($ch, CURLINFO_HTTP_CODE), 'error' => curl_error($ch)];
    curl_close($ch);
    return $result;
}

function http_post_json($url, array $payload, $timeout = 15) {
    $r = http_post($url, json_encode($payload), [CURLOPT_HTTPHEADER => ['Content-Type: application/json'], CURLOPT_TIMEOUT => $timeout]);
    return $r + ['data' => json_decode((string)$r['body'], true)];
}

/** Только http(s) на 80/443 и публичные адреса. Возвращает CURLOPT_RESOLVE, закрепляющий проверенный адрес. */
function public_url_resolve($url) {
    $p = parse_url($url);
    $scheme = strtolower($p['scheme'] ?? '');
    if (!$p || !in_array($scheme, ['http', 'https'], true) || empty($p['host']) || isset($p['user']) || isset($p['pass'])) {
        throw new RuntimeException('Укажите прямую публичную ссылку на изображение.');
    }
    $host = rtrim(strtolower($p['host']), '.');
    $port = $p['port'] ?? ($scheme === 'https' ? 443 : 80);
    if ($port !== ($scheme === 'https' ? 443 : 80)) throw new RuntimeException('Ссылка должна использовать стандартный порт HTTP или HTTPS.');
    $public = fn($ip) => filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE);
    if (filter_var($host, FILTER_VALIDATE_IP)) {
        if (!$public($host) || str_contains($host, ':')) throw new RuntimeException('Ссылка должна вести на публичный IPv4-адрес или домен.');
        return [];
    }
    $addresses = gethostbynamel($host) ?: throw new RuntimeException('Не удалось найти адрес сайта из ссылки.');
    foreach ($addresses as $ip) if (!$public($ip)) throw new RuntimeException('Ссылка ведёт на непубличный адрес.');
    return ["$host:$port:{$addresses[0]}"];
}

/** Скачивает публичный файл без редиректов и не больше $max_bytes. $dest — путь (поток в файл) или null (вернуть байты). $user_agent — для сайтов, которые отдают полную страницу только браузеру. */
function fetch_public($url, $max_bytes, $dest = null, $timeout = 20, $user_agent = HTTP_UA, array $headers = []) {
    $resolve = public_url_resolve($url);
    $handle = $dest !== null ? (@fopen($dest, 'wb') ?: throw new RuntimeException('Не удалось сохранить файл во временную папку.')) : null;
    $bytes = '';
    $size = 0;
    $location = '';
    $last_modified = '';
    $ch = curl_init($url);
    curl_setopt_array($ch, [CURLOPT_FOLLOWLOCATION => false, CURLOPT_HTTPHEADER => $headers,
        CURLOPT_HEADERFUNCTION => function ($c, $line) use (&$location, &$last_modified) { if (stripos($line, 'location:') === 0) $location = trim(substr($line, 9)); if (stripos($line, 'last-modified:') === 0) $last_modified = trim(substr($line, 14)); return strlen($line); }, CURLOPT_CONNECTTIMEOUT => 8, CURLOPT_TIMEOUT => $timeout,
        CURLOPT_PROTOCOLS => CURLPROTO_HTTP | CURLPROTO_HTTPS, CURLOPT_SSL_VERIFYPEER => true, CURLOPT_SSL_VERIFYHOST => 2, CURLOPT_USERAGENT => $user_agent,
        CURLOPT_WRITEFUNCTION => function ($c, $chunk) use (&$bytes, &$size, $max_bytes, $handle) {
            if (($size += strlen($chunk)) > $max_bytes) return 0;
            if ($handle) return fwrite($handle, $chunk);
            $bytes .= $chunk;
            return strlen($chunk);
        }]);
    if ($resolve) curl_setopt($ch, CURLOPT_RESOLVE, $resolve);
    $ok = curl_exec($ch) !== false;
    $status = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $type = strtolower((string)curl_getinfo($ch, CURLINFO_CONTENT_TYPE));
    $error = curl_error($ch);
    curl_close($ch);
    if ($handle) fclose($handle);
    $ok = $ok && $status >= 200 && $status < 300 && $size > 0;
    if (!$ok && $dest !== null) @unlink($dest);
    return ['ok' => $ok, 'bytes' => $bytes, 'type' => $type, 'error' => $error, 'too_big' => $size > $max_bytes, 'status' => $status, 'location' => $location, 'last_modified' => $last_modified];
}

/** Не чаще одного запроса за $gap секунд к одному хосту (между процессами — через kv): ждёт, если нужно. */
function host_throttle(string $host, float $gap = 1.0): void {
    $key = 'throttle:' . strtolower($host);
    $wait = (float)kv_get($key, 0) + $gap - microtime(true);
    if ($wait > 0) usleep((int)($wait * 1000000));
    kv_update($key, fn() => microtime(true));
}

function fetch_public_image($url) {
    $r = fetch_public($url, MAX_IMAGE_BYTES);
    if (!$r['ok'] || !str_starts_with($r['type'], 'image/')) {
        throw new RuntimeException($r['error'] ? 'Не удалось скачать изображение: ' . $r['error'] : 'Ссылка должна вести прямо на файл изображения, без страницы или редиректа.');
    }
    return $r['bytes'];
}

function validate_image_bytes($bytes) {
    if (!is_string($bytes) || strlen($bytes) > MAX_IMAGE_BYTES) throw new RuntimeException('Изображение должно быть не больше 10 МБ.');
    if (!function_exists('getimagesizefromstring')) throw new RuntimeException('На сервере недоступна проверка изображений (PHP GD).');
    $info = @getimagesizefromstring($bytes);
    if (!$info || !in_array($info['mime'] ?? '', ['image/jpeg', 'image/png', 'image/gif', 'image/webp', 'image/bmp'], true)) {
        throw new RuntimeException('Поддерживаются только JPEG, PNG, GIF, WebP и BMP.');
    }
    if ($info[0] * $info[1] > 40000000) throw new RuntimeException('Слишком большое разрешение изображения.');
}

/** Картинка из запроса: загруженный image_file или image_url. */
function read_request_image(array $input) {
    $file = $_FILES['image_file'] ?? null;
    if ($file && $file['error'] !== UPLOAD_ERR_NO_FILE) {
        if ($file['error'] !== UPLOAD_ERR_OK || !is_uploaded_file($file['tmp_name'])) throw new RuntimeException('Не удалось загрузить файл.');
        if ($file['size'] > MAX_IMAGE_BYTES) throw new RuntimeException('Изображение должно быть не больше 10 МБ.');
        return file_get_contents($file['tmp_name']);
    }
    $url = trim((string)($_POST['image_url'] ?? $input['image_url'] ?? ''));
    return $url !== '' ? fetch_public_image($url) : throw new RuntimeException('Добавьте ссылку или выберите файл изображения.');
}

/** Уменьшает картинку до $max_side в JPEG (белый фон под прозрачностью). Не вышло или не нужно — исходные байты. */
function image_shrink($bytes, $max_side, $quality = 90, $force = false) {
    $info = is_string($bytes) && $bytes !== '' ? @getimagesizefromstring($bytes) : false;
    if (!$info || (!$force && max($info[0], $info[1]) <= $max_side) || !function_exists('imagecreatefromstring')) return $bytes;
    $src = @imagecreatefromstring($bytes);
    if (!$src) return $bytes;
    $scale = min(1, $max_side / max($info[0], $info[1]));
    [$w, $h] = [max(1, (int)round($info[0] * $scale)), max(1, (int)round($info[1] * $scale))];
    $dst = imagecreatetruecolor($w, $h);
    imagefill($dst, 0, 0, imagecolorallocate($dst, 255, 255, 255));
    imagecopyresampled($dst, $src, 0, 0, 0, 0, $w, $h, $info[0], $info[1]);
    ob_start();
    imagejpeg($dst, null, $quality);
    $jpeg = (string)ob_get_clean();
    return $jpeg !== '' ? $jpeg : $bytes;
}

// ---- Секреты: sodium, ключ в secret.key.php (PHP-файл ничего не отдаёт по HTTP) ----

function secret_key() {
    static $key = null;
    if ($key !== null) return $key;
    if (!function_exists('sodium_crypto_secretbox')) throw new RuntimeException('На сервере нет расширения PHP sodium: нельзя безопасно хранить токены.');
    $file = ROOT . '/secret.key.php';
    if (!is_file($file)) {
        $code = "<?php\n// Ключ шифрования токенов Posta. Не публиковать и не коммитить.\nreturn '" . base64_encode(random_bytes(SODIUM_CRYPTO_SECRETBOX_KEYBYTES)) . "';\n";
        if (@file_put_contents($file, $code, LOCK_EX) === false) throw new RuntimeException('Не удалось создать ключ шифрования (secret.key.php): проверьте права на папку сайта.');
        @chmod($file, 0640);
    }
    $decoded = base64_decode((string)(include $file), true);
    if ($decoded === false || strlen($decoded) !== SODIUM_CRYPTO_SECRETBOX_KEYBYTES) throw new RuntimeException('Ключ шифрования (secret.key.php) повреждён.');
    return $key = $decoded;
}

/**
 * Шифрование секрета. Формат 2: «2.<версия ключа>.<base64(nonce|шифртекст)>», XChaCha20-Poly1305 с привязкой (AAD):
 * секрет, перенесённый в чужую запись, не расшифруется. Без привязки ($aad === '') и старые значения (secretbox) читаются как раньше.
 */
function secret_encrypt($plain, string $aad = '') {
    if ($aad === '') {
        $nonce = random_bytes(SODIUM_CRYPTO_SECRETBOX_NONCEBYTES);
        return base64_encode($nonce . sodium_crypto_secretbox((string)$plain, $nonce, secret_key()));
    }
    $nonce = random_bytes(SODIUM_CRYPTO_AEAD_XCHACHA20POLY1305_IETF_NPUBBYTES);
    $ver = SECRET_KEY_VERSION;
    $head = "2.$ver.";
    return $head . base64_encode($nonce . sodium_crypto_aead_xchacha20poly1305_ietf_encrypt((string)$plain, $head . $aad, $nonce, secret_key()));
}

function secret_decrypt($encrypted, string $aad = '') {
    $encrypted = (string)$encrypted;
    if (str_starts_with($encrypted, '2.')) {
        $parts = explode('.', $encrypted, 3);
        $raw = count($parts) === 3 ? base64_decode($parts[2], true) : false;
        $n = SODIUM_CRYPTO_AEAD_XCHACHA20POLY1305_IETF_NPUBBYTES;
        if ($raw === false || strlen($raw) <= $n || (int)$parts[1] !== SECRET_KEY_VERSION || $aad === '') return null;
        $plain = sodium_crypto_aead_xchacha20poly1305_ietf_decrypt(substr($raw, $n), "2.$parts[1]." . $aad, substr($raw, 0, $n), secret_key());
        return $plain === false ? null : $plain;
    }
    $raw = base64_decode($encrypted, true);
    $n = SODIUM_CRYPTO_SECRETBOX_NONCEBYTES;
    if ($raw === false || strlen($raw) <= $n) return null;
    $plain = sodium_crypto_secretbox_open(substr($raw, $n), substr($raw, 0, $n), secret_key());
    return $plain === false ? null : $plain;
}

/** Привязка секрета ресурса к записи: пользователь, ресурс, сеть и назначение поля. */
function channel_secret_aad($user_id, $channel_id, $platform, string $field = 'secrets'): string {
    return implode('|', ['channel', $user_id, $channel_id, $platform, $field]);
}

// ---- Настройки пользователя: тема, вторая тема, фильтры ----

const CONTENT_FILTERS = ['fresh_only', 'active_sources_only', 'video_only', 'media_only', 'media_no_text', 'auto_translate'];   // auto_translate — способ показа, не отбор (AUTOTRANSLATE_PROMPT.md)

function default_settings() {
    return ['topic' => DEFAULT_TOPIC, 'topic2' => '', 'query' => '', 'fresh_only' => false, 'active_sources_only' => true, 'video_only' => false, 'media_only' => false, 'media_no_text' => false, 'auto_translate' => true];
}

function normalize_topic($topic) {
    return mb_substr(trim(preg_replace('/\s+/u', ' ', (string)$topic) ?? ''), 0, TOPIC_MAX_LENGTH);
}

/** Уточнение к теме («Города» + «тюмень»): без кавычек и «+», пробелы схлопнуты, до TOPIC_QUERY_MAX_LEN символов. */
function normalize_query($query) {
    $clean = str_replace(['"', '«', '»', '+', '?'], ' ', (string)$query);
    return mb_substr(trim(preg_replace('/\s+/u', ' ', $clean) ?? ''), 0, TOPIC_QUERY_MAX_LEN);
}

/** Загрузить настройки с нормализацией (обёртка над db_load_settings). */
function load_settings($uid = null) {
    $s = db_load_settings($uid);
    $s = array_merge(default_settings(), $s);
    $s['topic'] = normalize_topic($s['topic']) ?: DEFAULT_TOPIC;
    $s['topic2'] = normalize_topic($s['topic2']);
    if (topic_norm($s['topic2']) === topic_norm($s['topic'])) $s['topic2'] = '';
    $s['query'] = normalize_query($s['query'] ?? '');
    foreach (CONTENT_FILTERS as $key) $s[$key] = (bool)$s[$key];
    return $s;
}

/** Сохранить настройки (обёртка над db_save_settings). */
function save_settings($changes, $uid = null) {
    $current = load_settings($uid);
    db_save_settings(array_merge(default_settings(), $current, $changes), $uid);
}

/** Тема каноническим названием из реестра («мемы» → «Мемы»). */
function current_topic() { $t = load_settings()['topic']; return topic_lookup($t)['title'] ?? $t; }
function current_topic2() { $t = load_settings()['topic2']; return $t === '' ? '' : (topic_lookup($t)['title'] ?? $t); }
function current_query() { return app_user_bind() === '' ? '' : load_settings()['query']; }   // без пользователя (тесты, очередь вне запроса) — без уточнения

/** Ключ оценок, пула и кэша: «первая + вторая», одна тема, и с уточнением — «тема ? запрос» (TOPIC_QUERY_PROMPT.md). Источники — от первой. */
function search_topic_key() {
    $second = current_topic2();
    $query = current_query();
    return current_topic() . ($second === '' ? '' : " + $second") . ($query === '' ? '' : " ? $query");
}

function content_filters() { return array_intersect_key(load_settings(), array_flip(CONTENT_FILTERS)); }

function content_max_age_days($dvach = false) {
    return $dvach ? DVACH_MAX_AGE_DAYS : CONTENT_MAX_AGE_DAYS;   // сбор — полный объём; «только новое» действует при показе
}
