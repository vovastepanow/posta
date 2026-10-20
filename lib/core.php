<?php
// Ядро: JSON-файлы под блокировкой, пользователи, HTTP, шифрование секретов, настройки, загрузка картинок.

// ---- JSON-хранилище ----

function load_data($file, $default = []) {
    $data = is_file($file) ? json_decode((string)file_get_contents($file), true) : null;
    return is_array($data) ? $data : $default;
}

/** Атомарная запись: временный файл + rename, читатели не видят половину файла. */
function save_data($file, $data) {
    $json = json_encode($data, JSON_UNESCAPED_UNICODE | JSON_PRETTY_PRINT | JSON_INVALID_UTF8_SUBSTITUTE);
    $tmp = $json === false ? false : tempnam(dirname($file), '.content-data-');
    if ($tmp === false) throw new RuntimeException('Не удалось подготовить запись JSON-базы.');
    $mode = is_file($file) ? fileperms($file) & 0777 : 0664;
    $ok = file_put_contents($tmp, $json, LOCK_EX) === strlen($json);
    @chmod($tmp, $mode);
    if (!$ok || !rename($tmp, $file)) { @unlink($tmp); throw new RuntimeException('Не удалось сохранить JSON-базу.'); }
}

function with_file_lock($file, callable $callback) {
    $lock = @fopen($file . '.lock', 'c+');
    if ($lock && !flock($lock, LOCK_EX)) { fclose($lock); $lock = false; }
    try { return $callback(); } finally { if ($lock) { flock($lock, LOCK_UN); fclose($lock); } }
}

/** Прочитать → изменить → сохранить под одной блокировкой. $mutator вернул null — файл не трогается. */
function update_data($file, $default, callable $mutator) {
    return with_file_lock($file, function () use ($file, $default, $mutator) {
        $data = load_data($file, $default);
        $updated = $mutator($data);
        if ($updated === null) return $data;
        save_data($file, $updated);
        return $updated;
    });
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

/** Веб-запрос: id из куки или новый; кука продлевается при каждом заходе. */
function app_user_from_request() {
    $uid = $_COOKIE[USER_COOKIE] ?? '';
    if (!is_valid_user_id($uid)) $uid = bin2hex(random_bytes(16));
    $https = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off') || strtolower($_SERVER['HTTP_X_FORWARDED_PROTO'] ?? '') === 'https';
    if (!headers_sent()) setcookie(USER_COOKIE, $uid, ['expires' => time() + USER_COOKIE_DAYS * DAY_SECONDS, 'secure' => $https,
        'path' => rtrim(dirname($_SERVER['SCRIPT_NAME'] ?? '/'), '/\\') . '/', 'httponly' => true, 'samesite' => 'Lax']);
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

function pool_file($uid = null) { return user_dir($uid) . '/pool.json'; }
function history_file($uid = null) { return user_dir($uid) . '/history.json'; }
function settings_file($uid = null) { return user_dir($uid) . '/settings.json'; }
function connections_file($uid = null) { return user_dir($uid) . '/connections.json'; }
function published_file($uid = null) { return user_dir($uid) . '/published.json'; }
function outbox_file($uid = null) { return user_dir($uid) . '/outbox.json'; }
function parser_runtime_file($suffix) { return user_dir() . '/parser-' . PARSER_RUNTIME_VERSION . $suffix; }
function empty_history() { return array_fill_keys(HISTORY_COLLECTIONS, []); }
function load_history() { return history_load(); }

/** Отметка захода (не чаще раза в 10 минут) и раз в сутки — удаление брошенных профилей. */
function user_touch() {
    static $last_touch = 0, $last_gc = 0;
    $now = time();

    $uid = app_user_id();
    if ($now - $last_touch < 600) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at, last_seen) VALUES (?, ?, ?)',
                [$uid, $now, $now]);
        db_exec('UPDATE users SET last_seen = ? WHERE id = ?', [$now, $uid]);
        return;
    }
    $last_touch = $now;

    if ($now - $last_gc < DAY_SECONDS) return;
    $last_gc = $now;

    foreach (user_ids() as $id) {
        if ($id === $uid) continue;
        $row = db_one('SELECT last_seen, created_at FROM users WHERE id = ?', [$id]);
        $seen = (int)($row['last_seen'] ?: $row['created_at'] ?: $now);   // 0 — отметки захода ещё не было, считаем от создания профиля
        if ($now - $seen >= USER_RETENTION_DAYS * DAY_SECONDS) {
            db_exec('DELETE FROM users WHERE id = ?', [$id]);
        }
    }
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

const HTTP_UA = 'Postmaker/1.0';
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

/** Скачивает публичный файл без редиректов и не больше $max_bytes. $dest — путь (поток в файл) или null (вернуть байты). */
function fetch_public($url, $max_bytes, $dest = null, $timeout = 20) {
    $resolve = public_url_resolve($url);
    $handle = $dest !== null ? (@fopen($dest, 'wb') ?: throw new RuntimeException('Не удалось сохранить файл во временную папку.')) : null;
    $bytes = '';
    $size = 0;
    $ch = curl_init($url);
    curl_setopt_array($ch, [CURLOPT_FOLLOWLOCATION => false, CURLOPT_CONNECTTIMEOUT => 8, CURLOPT_TIMEOUT => $timeout,
        CURLOPT_PROTOCOLS => CURLPROTO_HTTP | CURLPROTO_HTTPS, CURLOPT_SSL_VERIFYPEER => true, CURLOPT_SSL_VERIFYHOST => 2, CURLOPT_USERAGENT => HTTP_UA,
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
    return ['ok' => $ok, 'bytes' => $bytes, 'type' => $type, 'error' => $error, 'too_big' => $size > $max_bytes];
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
        $code = "<?php\n// Ключ шифрования токенов Postmaker. Не публиковать и не коммитить.\nreturn '" . base64_encode(random_bytes(SODIUM_CRYPTO_SECRETBOX_KEYBYTES)) . "';\n";
        if (@file_put_contents($file, $code, LOCK_EX) === false) throw new RuntimeException('Не удалось создать ключ шифрования (secret.key.php): проверьте права на папку сайта.');
        @chmod($file, 0640);
    }
    $decoded = base64_decode((string)(include $file), true);
    if ($decoded === false || strlen($decoded) !== SODIUM_CRYPTO_SECRETBOX_KEYBYTES) throw new RuntimeException('Ключ шифрования (secret.key.php) повреждён.');
    return $key = $decoded;
}

function secret_encrypt($plain) {
    $nonce = random_bytes(SODIUM_CRYPTO_SECRETBOX_NONCEBYTES);
    return base64_encode($nonce . sodium_crypto_secretbox((string)$plain, $nonce, secret_key()));
}

function secret_decrypt($encrypted) {
    $raw = base64_decode((string)$encrypted, true);
    $n = SODIUM_CRYPTO_SECRETBOX_NONCEBYTES;
    if ($raw === false || strlen($raw) <= $n) return null;
    $plain = sodium_crypto_secretbox_open(substr($raw, $n), substr($raw, 0, $n), secret_key());
    return $plain === false ? null : $plain;
}

// ---- Настройки пользователя: тема, вторая тема, фильтры ----

const CONTENT_FILTERS = ['fresh_only', 'active_sources_only', 'video_only'];

function default_settings() {
    return ['topic' => DEFAULT_TOPIC, 'topic2' => '', 'fresh_only' => false, 'active_sources_only' => true, 'video_only' => false];
}

function normalize_topic($topic) {
    return mb_substr(trim(preg_replace('/\s+/u', ' ', (string)$topic) ?? ''), 0, TOPIC_MAX_LENGTH);
}

/** Загрузить настройки с нормализацией (обёртка над db_load_settings). */
function load_settings($uid = null) {
    $s = db_load_settings($uid);
    $s = array_merge(default_settings(), $s);
    $s['topic'] = normalize_topic($s['topic']) ?: DEFAULT_TOPIC;
    $s['topic2'] = normalize_topic($s['topic2']);
    if (topic_norm($s['topic2']) === topic_norm($s['topic'])) $s['topic2'] = '';
    foreach (CONTENT_FILTERS as $key) $s[$key] = (bool)$s[$key];
    return $s;
}

/** Сохранить настройки (обёртка над db_save_settings). */
function save_settings($changes, $uid = null) {
    $current = load_settings($uid);
    db_save_settings(array_merge(default_settings(), $current, $changes), $uid);
}

/** Тема каноническим названием из реестра («мемы и юмор» → «Мемы и юмор»). */
function current_topic() { $t = load_settings()['topic']; return topic_lookup($t)['title'] ?? $t; }
function current_topic2() { $t = load_settings()['topic2']; return $t === '' ? '' : (topic_lookup($t)['title'] ?? $t); }

/** Ключ оценок, пула и кэша: «первая + вторая» или одна тема. Источники — от первой. */
function search_topic_key() { $second = current_topic2(); return current_topic() . ($second === '' ? '' : " + $second"); }

function content_filters() { return array_intersect_key(load_settings(), array_flip(CONTENT_FILTERS)); }

function content_max_age_days($dvach = false) {
    return $dvach ? DVACH_MAX_AGE_DAYS : CONTENT_MAX_AGE_DAYS;   // сбор — полный объём; «только новое» действует при показе
}
