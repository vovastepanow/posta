<?php
// «Свой контент»: картинки и таблицы пользователя кладутся сразу в начало его подборки, без оценки моделью. В общий каталог не попадают.

const OWN_MAX_FILES = 10;                 // файлов за одну загрузку
const OWN_CSV_MAX_BYTES = 1024 * 1024;
const OWN_CSV_MAX_ROWS = 100;
const OWN_CSV_MAX_ERRORS = 10;            // сколько ошибок строк возвращаем в ответе
const OWN_TEXT_MAX = 4096;
const OWN_SERVER_FILE_LIMIT = '2 МБ';     // upload_max_filesize на сервере; настройки PHP не меняем — показываем понятную ошибку

function own_source_key() { return 'own:' . app_user_id(); }

/** Запись пула для своего поста: $media — адреса картинок в кэше медиа (одна — обычный пост, несколько — галерея). */
function own_post_item(array $media, string $text, string $label, ?int $posted_at = null): array {
    $media = array_values(array_filter($media, 'is_string'));
    if (!$media) throw new RuntimeException('В посте нет картинки.');
    $text = mb_substr(trim($text), 0, OWN_TEXT_MAX);
    $item = ['image' => $media[0], 'images' => [$media[0]], 'image_local' => $media[0], 'video' => '', 'text' => $text, 'captions' => [$text], 'description' => $text,
        'source' => 'Свой контент · ' . $label, 'source_key' => own_source_key(), 'source_url' => '', 'posted_at' => $posted_at ?: time(), 'pooled_at' => time(),
        'topic' => current_topic(), 'relevance' => 100, 'rating' => 100, 'own' => true];
    if (count($media) > 1) $item['gallery'] = array_map(fn($u) => ['image' => $u, 'video' => '', 'image_local' => $u], $media);
    return $item;
}

/** Картинка в кэш медиа: ключ — пользователь и хэш содержимого, одинаковый файл не дублируется. Возвращает адрес. */
function own_store_image(string $bytes): string {
    validate_image_bytes($bytes);
    $url = media_cache_store_image('own:' . app_user_id() . ':' . sha1($bytes), $bytes);
    return $url !== '' ? $url : throw new RuntimeException('Не удалось сохранить картинку на сервере.');
}

/** Новые посты — в начало подборки пользователя; уже известные (в пуле или оценённые) пропускаются. Возвращает сколько добавлено. */
function own_add_to_pool(array $items): int {
    $known = collect_known_images();
    $fresh = array_values(array_filter($items, fn($i) => !candidate_is_known($i, $known)));
    if ($fresh) {
        pool_update(fn($pool) => [...$fresh, ...$pool]);
        pool_items_enqueue_videos($fresh, app_user_id());   // ролики TikTok и Shorts — фоном, для всех путей: импорт, «Лучшие посты», «Зеркало»
    }
    return count($fresh);
}

function own_is_video_name(string $name, string $bytes_head = ''): bool {
    return (bool)preg_match('~\.(mp4|mov|webm|avi|mkv|m4v)$~i', $name) || str_contains(strtolower((string)(new finfo(FILEINFO_MIME_TYPE))->buffer($bytes_head)), 'video/');
}

function own_upload_error_text(int $code): string {
    return match ($code) {
        UPLOAD_ERR_INI_SIZE, UPLOAD_ERR_FORM_SIZE => 'Файл больше ' . OWN_SERVER_FILE_LIMIT . ': так настроен сервер.',
        UPLOAD_ERR_PARTIAL, UPLOAD_ERR_NO_TMP_DIR, UPLOAD_ERR_CANT_WRITE => 'Файл не дозагрузился: попробуйте ещё раз.',
        default => 'Не удалось загрузить файл.'
    };
}

/** Файлы из $_FILES['files'] списком [['name', 'tmp_name', 'error']]. */
function own_files_list(array $field): array {
    if (!is_array($field['name'] ?? null)) return isset($field['name']) ? [$field] : [];
    $files = [];
    foreach (array_keys($field['name']) as $i) $files[] = ['name' => (string)$field['name'][$i], 'tmp_name' => (string)$field['tmp_name'][$i], 'error' => (int)$field['error'][$i]];
    return $files;
}

/**
 * Загрузка картинок: каждый файл — отдельный пост или один пост-альбом ($album). $uploaded — файлы запроса.
 * Возвращает ['added' => постов, 'skipped' => файлов, 'errors' => [...]].
 */
function own_upload(array $uploaded, string $text, bool $album = false): array {
    $uploaded = array_slice(array_values(array_filter($uploaded, fn($f) => ($f['error'] ?? 0) !== UPLOAD_ERR_NO_FILE)), 0, OWN_MAX_FILES + 1);
    if (!$uploaded) throw new RuntimeException('Выберите файлы с картинками.');
    if (count($uploaded) > OWN_MAX_FILES) throw new RuntimeException('За раз можно загрузить до ' . OWN_MAX_FILES . ' файлов.');
    own_quota_take(app_user_id(), count($uploaded));
    $stored = [];
    $errors = [];
    foreach ($uploaded as $f) {
        $name = mb_substr(basename((string)$f['name']), 0, 60) ?: 'файл';
        try {
            if ($f['error'] !== UPLOAD_ERR_OK) throw new RuntimeException(own_upload_error_text($f['error']));
            if (!is_uploaded_file($f['tmp_name']) && !($f['trusted'] ?? false)) throw new RuntimeException('Не удалось загрузить файл.');
            $bytes = (string)file_get_contents($f['tmp_name']);
            if (own_is_video_name($name, substr($bytes, 0, 4096))) throw new RuntimeException('Видео пока не загружается: добавьте его через редактор поста.');
            $stored[] = ['url' => own_store_image($bytes), 'name' => $name];
        } catch (RuntimeException $e) { $errors[] = "$name: " . $e->getMessage(); }
    }
    $items = !$stored ? [] : ($album
        ? [own_post_item(array_column($stored, 'url'), $text, count($stored) > 1 ? count($stored) . ' файлов' : $stored[0]['name'])]
        : array_map(fn($s) => own_post_item([$s['url']], $text, $s['name']), $stored));
    return ['added' => own_add_to_pool($items), 'skipped' => count($uploaded) - count($stored), 'errors' => $errors];
}

/** Таблица → строки с ключами-колонками (text, image, date); разделитель ; или , по первой строке, UTF-8 или Windows-1251. */
function own_csv_rows(string $csv): array {
    $csv = preg_replace('/^\xEF\xBB\xBF/', '', $csv);
    if (!mb_check_encoding($csv, 'UTF-8')) $csv = mb_convert_encoding($csv, 'UTF-8', 'Windows-1251');
    $first = strtok($csv, "\n") ?: '';
    $delimiter = substr_count($first, ';') >= substr_count($first, ',') ? ';' : ',';
    $stream = fopen('php://memory', 'w+');
    fwrite($stream, $csv);
    rewind($stream);
    $aliases = ['text' => 'text', 'текст' => 'text', 'image' => 'image', 'картинка' => 'image', 'image_url' => 'image', 'картинки' => 'image', 'date' => 'date', 'дата' => 'date'];
    $columns = null;
    $rows = [];
    for ($line = 1; ($cells = fgetcsv($stream, 0, $delimiter, '"', '')) !== false; $line++) {
        if ($cells === [null]) continue;   // пустая строка
        if ($columns === null) {
            $columns = array_map(fn($h) => $aliases[mb_strtolower(trim((string)$h))] ?? null, $cells);
            if (!in_array('image', $columns, true)) throw new RuntimeException('В таблице нет колонки с картинками: назовите её image, картинка или image_url.');
            continue;
        }
        $row = [];
        foreach ($columns as $i => $name) if ($name !== null && !isset($row[$name])) $row[$name] = trim((string)($cells[$i] ?? ''));
        $rows[] = ['line' => $line, 'text' => $row['text'] ?? '', 'image' => $row['image'] ?? '', 'date' => $row['date'] ?? ''];
    }
    fclose($stream);
    return $rows;
}

/** Импорт CSV: строка — пост, в колонке картинок одна или несколько ссылок (через пробел или |). ['added', 'skipped', 'errors']. */
function own_import_csv(string $csv): array {
    if (strlen($csv) > OWN_CSV_MAX_BYTES) throw new RuntimeException('CSV больше 1 МБ.');
    $rows = own_csv_rows($csv);
    if (!$rows) throw new RuntimeException('В таблице нет строк с данными.');
    if (count($rows) > OWN_CSV_MAX_ROWS) throw new RuntimeException('В таблице больше ' . OWN_CSV_MAX_ROWS . ' строк: загрузите её частями.');
    own_quota_take(app_user_id(), count($rows));
    $items = [];
    $errors = [];
    $failed = 0;
    foreach ($rows as $row) {
        try {
            $urls = array_values(array_filter(preg_split('~[\s|]+~', $row['image']) ?: []));
            if (!$urls) throw new RuntimeException('нет ссылки на картинку');
            $media = array_map(fn($u) => own_store_image(preg_match('~^https?://~i', $u) ? fetch_public_image($u) : throw new RuntimeException("«{$u}» — не ссылка http(s)")), array_slice($urls, 0, OWN_MAX_FILES));
            $items[] = own_post_item($media, $row['text'], 'CSV', $row['date'] !== '' && ($t = strtotime($row['date'])) !== false ? (int)$t : null);
        } catch (RuntimeException $e) {
            $failed++;
            if (count($errors) < OWN_CSV_MAX_ERRORS) $errors[] = "Строка {$row['line']}: " . $e->getMessage();
        }
    }
    $added = own_add_to_pool($items);
    return ['added' => $added, 'skipped' => $failed + count($items) - $added, 'errors' => $errors];
}

// ---- API: пост в подборку по токену (Make, n8n, свои скрипты) ----

const API_RATE_PER_HOUR = 60;

/** Новый токен пользователя: хранится только sha256, сам токен показывается один раз. Старый заменяется. */
function api_token_create($uid): string {
    $token = bin2hex(random_bytes(24));
    kv_update('api_token:' . $uid, fn() => hash('sha256', $token));
    return $token;
}

function api_token_revoke($uid): void { db_exec('DELETE FROM kv WHERE key = ?', ['api_token:' . $uid]); }
function api_token_exists($uid): bool { return kv_get('api_token:' . $uid) !== null; }

/** Пользователь по токену или null (поиск по хэшу). */
function api_token_user($token): ?string {
    if (!preg_match('/^[a-f0-9]{48}$/', (string)$token)) return null;
    $row = db_one('SELECT key FROM kv WHERE key LIKE ? AND value = ?', ['api_token:%', db_json(['_kv_value' => hash('sha256', $token)])]);
    return $row ? substr($row['key'], strlen('api_token:')) : null;
}

/** Токен из заголовка Authorization: Bearer … (при mod_php заголовок бывает только в getallheaders()). */
function api_bearer_token(): string {
    $h = (string)($_SERVER['HTTP_AUTHORIZATION'] ?? $_SERVER['REDIRECT_HTTP_AUTHORIZATION'] ?? (function_exists('getallheaders') ? (getallheaders()['Authorization'] ?? '') : ''));
    return preg_match('/^Bearer\s+(\S+)$/i', trim($h), $m) ? $m[1] : '';
}

/** Не больше API_RATE_PER_HOUR запросов в час на пользователя; true — запрос можно выполнять. */
function api_rate_take($uid): bool {
    $key = "api_rate:$uid:" . date('YmdH');
    $count = kv_update($key, fn($n) => (int)$n + 1, 0);
    db_exec('DELETE FROM kv WHERE key LIKE ? AND key != ?', ["api_rate:$uid:%", $key]);
    return $count <= API_RATE_PER_HOUR;
}

/** Тело запроса api_add_post → пост в подборку: text и image_url или image_urls (до 10). ['added' => 0|1]. */
function api_add_post(array $in): array {
    $urls = array_values(array_filter(array_map('trim', array_map('strval', array_filter((array)($in['image_urls'] ?? [$in['image_url'] ?? '']), 'is_scalar')))));
    if (!$urls) throw new RuntimeException('Нужна ссылка на картинку: image_url или image_urls.');
    if (count($urls) > OWN_MAX_FILES) throw new RuntimeException('Картинок не больше ' . OWN_MAX_FILES . '.');
    $media = array_map(fn($u) => own_store_image(fetch_public_image($u)), $urls);
    return ['added' => own_add_to_pool([own_post_item($media, is_string($in['text'] ?? '') ? (string)($in['text'] ?? '') : '', 'API')])];
}

// ---- Папка и Яндекс Диск ----

const OWN_DAY_FILES = 1000;        // файлов в сутки на пользователя (загрузка картинок, папка, Яндекс Диск)
const YADISK_PER_REQUEST = 100;    // файлов за одно нажатие «Импорт»; остальное — следующим нажатием
const YADISK_SECONDS = 50;         // лимит времени на один импорт

/** Суточный лимит файлов: проверка до записи, чтобы отказ не тратил лимит. */
function own_quota_take($uid, int $files): void {
    $key = 'own_files:' . $uid . ':' . date('Y-m-d');
    if ((int)kv_get($key, 0) + $files > OWN_DAY_FILES) throw new RuntimeException('За сутки — не больше ' . OWN_DAY_FILES . ' файлов: продолжите завтра.');
    kv_update($key, fn($n) => (int)$n + $files, 0);
}

/** Публичная ссылка на папку Яндекс Диска (disk.yandex.ru/d/… или yadi.sk/d/…) без параметров; иначе ошибка. */
function yadisk_public_url(string $url): string {
    $url = trim($url);
    if (strlen($url) > 300 || !preg_match('~^https://((?:disk\.yandex\.(?:ru|com)|yadi\.sk))/d/([A-Za-z0-9_-]{4,})(?:[/?#]|$)~', $url, $m)) {
        throw new RuntimeException('Нужна публичная ссылка на папку Яндекс Диска: disk.yandex.ru/d/… или yadi.sk/d/…');
    }
    return 'https://' . $m[1] . '/d/' . $m[2];
}

/** Запрос к API публичных ресурсов Яндекс Диска через fetch_public; 404 — папка не открыта по ссылке. */
function yadisk_api(string $query, array $params): array {
    $r = fetch_public('https://cloud-api.yandex.net/v1/disk/public/resources' . $query . '?' . http_build_query($params), 2 * 1024 * 1024, null, 20);
    $data = json_decode((string)$r['bytes'], true);
    if (is_array($data) && isset($data['error'])) throw new RuntimeException($r['status'] === 404 ? 'Папка не найдена: проверьте, что к ней открыт доступ по ссылке.' : 'Яндекс Диск ответил ошибкой: ' . ($data['message'] ?? $data['error']));
    if (!$r['ok'] || !is_array($data)) throw new RuntimeException('Яндекс Диск не ответил: попробуйте позже.');
    return $data;
}

/** Текст подписи из .txt рядом с картинкой (не больше OWN_TEXT_MAX знаков); ошибка — пусто. */
function yadisk_caption(string $public_url, string $path): string {
    try {
        $href = (string)(yadisk_api('/download', ['public_key' => $public_url, 'path' => $path])['href'] ?? '');
        $r = fetch_public($href, 64 * 1024, null, 15);
        return $r['ok'] ? mb_substr(trim((string)$r['bytes']), 0, OWN_TEXT_MAX) : '';
    } catch (RuntimeException $e) { return ''; }
}

/**
 * Импорт картинок из публичной папки Яндекс Диска: до YADISK_PER_REQUEST записей за нажатие, продолжение — по смещению в kv.
 * Видео здесь не качаются (их загружают через редактор поста). Подпись — из .txt с тем же именем в той же папке.
 */
function own_import_yadisk(string $public_url): array {
    $url = yadisk_public_url($public_url);
    $key = 'yadisk:' . app_user_id() . ':' . sha1($url);
    $offset = (int)kv_get($key, 0);
    $folder = yadisk_api('', ['public_key' => $url, 'limit' => YADISK_PER_REQUEST, 'offset' => $offset])['_embedded'] ?? [];
    $total = (int)($folder['total'] ?? 0);
    $items = array_values(array_filter((array)($folder['items'] ?? []), 'is_array'));
    $txt = [];
    foreach ($items as $i) if (($i['type'] ?? '') === 'file' && strtolower(pathinfo((string)($i['name'] ?? ''), PATHINFO_EXTENSION)) === 'txt') $txt[pathinfo((string)$i['name'], PATHINFO_FILENAME)] = (string)$i['path'];
    own_quota_take(app_user_id(), count(array_filter($items, fn($i) => ($i['media_type'] ?? '') === 'image')));   // лимит — до скачивания
    $started = time();
    $items_done = 0;
    $posts = [];
    $errors = [];
    $skipped = 0;
    foreach ($items as $item) {
        if (time() - $started > YADISK_SECONDS) break;   // остальное — следующим нажатием
        $items_done++;
        if (($item['type'] ?? '') !== 'file') continue;
        if (($item['media_type'] ?? '') === 'video') { $skipped++; continue; }
        if (($item['media_type'] ?? '') !== 'image') continue;
        $name = (string)($item['name'] ?? '');
        try {
            $href = (string)(yadisk_api('/download', ['public_key' => $url, 'path' => (string)$item['path']])['href'] ?? '');
            $media = own_store_image(fetch_public_image($href));
            $caption = isset($txt[pathinfo($name, PATHINFO_FILENAME)]) ? yadisk_caption($url, $txt[pathinfo($name, PATHINFO_FILENAME)]) : '';
            $posts[] = own_post_item([$media], $caption, 'Яндекс Диск');
        } catch (RuntimeException $e) {
            if (count($errors) < OWN_CSV_MAX_ERRORS) $errors[] = "{$name}: " . $e->getMessage();
        }
    }
    $added = own_add_to_pool($posts);
    $next = $offset + $items_done;
    $done = $next >= $total;
    kv_update($key, fn() => $done ? 0 : $next);
    return ['added' => $added, 'processed' => $items_done, 'total' => $total, 'done' => $done, 'skipped' => $skipped, 'errors' => $errors];
}
