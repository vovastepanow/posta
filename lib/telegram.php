<?php
// Публикация в Telegram-канал пользователя: бот (Bot API) — сразу, личный аккаунт (tools/tg_user.py, Telethon) — в «Отложенные»
// канала. Файлы приводятся к форматам Telegram; лимит размера видео самообучается по ответам Telegram (telegram_limits.json).

// ---- Bot API ----

function telegram_error_text(array $data, $status = 0) {
    $d = (string)($data['description'] ?? '');
    $has = fn(...$words) => (bool)array_filter($words, fn($w) => str_contains(mb_strtolower($d), $w));
    $retry = (int)($data['parameters']['retry_after'] ?? 0);
    return match (true) {
        $status === 401 || $has('unauthorized') => 'Токен бота недействителен: подключите Telegram заново в «Личном кабинете».',
        $has('chat not found') => 'Telegram не нашёл канал: проверьте, что бот добавлен в канал.',
        $has('not a member', 'was kicked', 'bot was blocked') => 'Бот не состоит в канале или удалён из него: добавьте его администратором.',
        $has('not enough rights', 'have no rights', 'need administrator rights') => 'У бота нет права публиковать в канале: сделайте его администратором с правом «Публикация сообщений».',
        $status === 413 || $has('too big') => 'Файл слишком большой для Telegram.',
        $status === 429 || $has('too many requests') => 'Telegram просит подождать' . ($retry ? " {$retry} с" : '') . ' перед следующей отправкой.',
        $has('wrong file', 'wrong type') => 'Telegram не принял файл: проверьте формат (фото JPEG/PNG, видео MP4).',
        default => 'Telegram: ' . ($d !== '' ? $d : "ответ не распознан (HTTP {$status})")
    };
}

/** POST к Bot API ($files — [поле => путь], уходят multipart). Адрес можно подменить POSTMAKER_TELEGRAM_API для проверок. */
function telegram_bot_request($token, $method, array $fields = [], array $files = [], $timeout = 60) {
    foreach ($files as $name => $path) $fields[$name] = new CURLFile($path, mime_content_type($path) ?: 'application/octet-stream', basename($path));
    $r = http_post((getenv('POSTMAKER_TELEGRAM_API') ?: 'https://api.telegram.org/bot') . "$token/$method", $fields,
        [CURLOPT_CONNECTTIMEOUT => 10, CURLOPT_TIMEOUT => $timeout, CURLOPT_USERAGENT => HTTP_UA]);
    if ($r['body'] === false) throw new RuntimeException('Telegram недоступен: ' . $r['error']);
    $data = json_decode((string)$r['body'], true);
    if (!is_array($data) || empty($data['ok'])) throw new RuntimeException(telegram_error_text(is_array($data) ? $data : [], $r['status']));
    return $data['result'];
}

/** Подключённый канал с расшифрованным токеном или null. */
function telegram_connection($uid = null) {
    $t = connections_load($uid)['telegram'] ?? null;
    if (!is_array($t) || empty($t['chat_id'])) return null;
    if (!empty($t['token_enc'])) $t['token'] = secret_decrypt($t['token_enc']);
    unset($t['token_enc']);
    return !empty($t['token']) ? $t : null;
}

function update_connections(callable $mutator, $uid = null) {
    return connections_update(fn($c) => $mutator(is_array($c) ? $c : []), $uid);
}

// ---- Лимиты на видео: документация (50 МБ бот, 2 ГБ аккаунт) уточняется ответами Telegram ----

function telegram_video_limit($route = 'bot') {
    $known = load_data(TELEGRAM_LIMITS_FILE, [])[$route]['video'] ?? [];
    return ['default' => TELEGRAM_LIMIT_DEFAULTS[$route] ?? TELEGRAM_LIMIT_DEFAULTS['bot'], 'ok_max' => (int)($known['ok_max'] ?? 0), 'fail_min' => (int)($known['fail_min'] ?? 0)];
}

/** ok | risky (больше известного — пробуем) | blocked. */
function telegram_video_verdict($bytes, $route = 'bot') {
    $l = telegram_video_limit($route);
    if ($l['fail_min'] > 0 && $bytes >= $l['fail_min']) return 'blocked';
    return $bytes <= max($l['default'], $l['ok_max']) ? 'ok' : ($bytes <= TELEGRAM_UPLOAD_MAX_BYTES ? 'risky' : 'blocked');
}

function telegram_video_limit_text($bytes, $route = 'bot') {
    $l = telegram_video_limit($route);
    $known = $l['fail_min'] > 0 ? $l['fail_min'] - 1 : max($l['default'], $l['ok_max']);
    if ($bytes > TELEGRAM_UPLOAD_MAX_BYTES && $bytes > $known) return sprintf('весит %.0f МБ, а приложение загружает не больше %d МБ', $bytes / 1048576, TELEGRAM_UPLOAD_MAX_BYTES >> 20);
    return sprintf('весит %.0f МБ, Telegram принимает до %.0f МБ', $bytes / 1048576, $known / 1048576);
}

/** Запомнить исход отправки видео размером $bytes. */
function telegram_video_limit_learn($bytes, $accepted, $route = 'bot') {
    if ($bytes <= 0) return;
    $l = telegram_video_limit($route);
    if ($accepted && $bytes <= $l['ok_max'] && ($l['fail_min'] === 0 || $bytes < $l['fail_min'])) return;
    update_data(TELEGRAM_LIMITS_FILE, [], function ($state) use ($route, $bytes, $accepted) {
        $e = ['ok_max' => (int)($state[$route]['video']['ok_max'] ?? 0), 'fail_min' => (int)($state[$route]['video']['fail_min'] ?? 0)];
        if ($accepted) {
            $e['ok_max'] = max($e['ok_max'], $bytes);
            if ($e['fail_min'] > 0 && $bytes >= $e['fail_min']) $e['fail_min'] = 0;   // лимит, видимо, подняли
        } else {
            $e['fail_min'] = $e['fail_min'] > 0 ? min($e['fail_min'], $bytes) : $bytes;
            if ($e['ok_max'] >= $e['fail_min']) $e['ok_max'] = $e['fail_min'] - 1;
        }
        $state[$route]['video'] = $e + ['updated' => time()];
        return $state;
    });
    @chmod(TELEGRAM_LIMITS_FILE, 0664);
}

// ---- Подготовка файлов ----

function telegram_work_dir() {
    $dir = sys_get_temp_dir() . '/postmaker-publish-' . bin2hex(random_bytes(6));
    return @mkdir($dir, 0700) || is_dir($dir) ? $dir : throw new RuntimeException('Не удалось создать временную папку.');
}

function fetch_public_media($url, $dest, $max_bytes) {
    $r = fetch_public($url, $max_bytes, $dest, 180);
    if (!$r['ok'] || (!str_starts_with($r['type'], 'image/') && !str_starts_with($r['type'], 'video/'))) {
        @unlink($dest);
        throw new RuntimeException($r['too_big'] ? 'Файл по ссылке слишком большой.' : "Не удалось скачать файл по ссылке ({$url}).");
    }
}

/** Фото для Bot API: JPEG/PNG до 10 МБ, сумма сторон ≤ 10000, пропорции до 20:1; иначе пережимается. */
function telegram_prepare_photo($path, $dir, $index) {
    $info = @getimagesize($path) ?: throw new RuntimeException('Файл не похож на картинку.');
    [$w, $h] = $info;
    if (in_array($info['mime'] ?? '', ['image/jpeg', 'image/png'], true) && filesize($path) <= TELEGRAM_MAX_PHOTO_BYTES && $w + $h <= 10000 && max($w, $h) / max(1, min($w, $h)) <= 20) {
        return ['path' => $path, 'width' => $w, 'height' => $h];
    }
    if (!function_exists('imagecreatefromstring')) throw new RuntimeException('На сервере нет PHP GD: не удалось подготовить картинку.');
    $out = "$dir/photo-$index.jpg";
    $bytes = file_get_contents($path);
    for ($q = 90; $q >= 60; $q -= 10) {
        if (($jpeg = image_shrink($bytes, 2560, $q, true)) === $bytes) throw new RuntimeException('Не удалось прочитать картинку.');
        if (strlen($jpeg) <= TELEGRAM_MAX_PHOTO_BYTES) break;
    }
    if (strlen($jpeg) > TELEGRAM_MAX_PHOTO_BYTES) throw new RuntimeException('Картинка слишком большая даже после сжатия.');
    file_put_contents($out, $jpeg);
    [$w, $h] = getimagesize($out);
    return ['path' => $out, 'width' => $w, 'height' => $h];
}

function telegram_video_info($path) {
    $d = json_decode(ffprobe($path, '-show_entries format=format_name,duration:stream=codec_type,codec_name,width,height -of json'), true) ?: [];
    $info = ['container' => (string)($d['format']['format_name'] ?? ''), 'duration' => (int)round((float)($d['format']['duration'] ?? 0)), 'video' => '', 'audio' => '', 'width' => 0, 'height' => 0];
    foreach ($d['streams'] ?? [] as $s) {
        if (($s['codec_type'] ?? '') === 'video' && $info['video'] === '') $info = ['video' => (string)$s['codec_name'], 'width' => (int)($s['width'] ?? 0), 'height' => (int)($s['height'] ?? 0)] + $info;
        if (($s['codec_type'] ?? '') === 'audio' && $info['audio'] === '') $info['audio'] = (string)$s['codec_name'];
    }
    return $info;
}

function telegram_video_ready(array $info) { return str_contains($info['container'], 'mp4') && $info['video'] === 'h264' && in_array($info['audio'], ['', 'aac'], true); }

/** Видео для ленты Telegram: MP4 + H.264 (+ AAC); остальное перекодируется ffmpeg. */
function telegram_prepare_video($path, $dir, $index, $route = 'bot') {
    $info = telegram_video_info($path);
    if ($info['video'] === '') throw new RuntimeException('Файл не похож на видео (или на сервере нет ffprobe).');
    if (!telegram_video_ready($info)) {
        if (ffmpeg_binary() === '') throw new RuntimeException('Видео нужно перекодировать в MP4, но на сервере нет ffmpeg.');
        $out = "$dir/video-$index.mp4";
        if (!ffmpeg_run('-i ' . escapeshellarg($path) . ' -map 0:v:0 -map 0:a:0? -c:v libx264 -preset veryfast -crf 26 -pix_fmt yuv420p -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" -c:a aac -b:a 128k -movflags +faststart -f mp4', $out, 600)) {
            throw new RuntimeException('Не удалось перекодировать видео в MP4.');
        }
        $info = telegram_video_info($path = $out);
    }
    if (telegram_video_verdict(filesize($path), $route) === 'blocked') throw new RuntimeException('Видео ' . telegram_video_limit_text(filesize($path), $route) . '.');
    return ['path' => $path, 'width' => $info['width'], 'height' => $info['height'], 'duration' => $info['duration']];
}

/**
 * Медиа поста [{kind, src, name, upload}] → [['kind' => photo|video|animation, 'path', 'name', 'width', 'height', 'duration']].
 * Источник: загруженный файл ($uploads[upload]), кэш сайта или публичная ссылка. Гифка в альбоме становится видео.
 */
function telegram_prepare_media(array $items, array $uploads, $dir, $route = 'bot') {
    if (count($items) > TELEGRAM_MAX_MEDIA) throw new RuntimeException('В пост помещается не больше ' . TELEGRAM_MAX_MEDIA . ' файлов.');
    $prepared = [];
    foreach (array_values($items) as $i => $item) {
        $name = mb_substr((string)($item['name'] ?? ''), 0, 120) ?: 'файл ' . ($i + 1);
        $src = (string)($item['src'] ?? '');
        try {
            if (($item['upload'] ?? '') !== '' && isset($uploads[(int)$item['upload']])) $path = $uploads[(int)$item['upload']]['path'];
            elseif (($cached = media_cache_path($src)) !== null && is_file($cached)) $path = $cached;
            elseif (preg_match('~^https?://~i', $src)) fetch_public_media($src, $path = "$dir/source-$i", TELEGRAM_UPLOAD_MAX_BYTES);
            else throw new RuntimeException('файл недоступен (ни в кэше сайта, ни по ссылке).');
            $mime = (new finfo(FILEINFO_MIME_TYPE))->file($path) ?: '';
            $prepared[] = ['name' => $name] + match (true) {
                $mime === 'image/gif' => ['kind' => 'animation', 'path' => $path, 'width' => 0, 'height' => 0, 'duration' => 0],
                str_starts_with($mime, 'image/') => ['kind' => 'photo', 'duration' => 0] + telegram_prepare_photo($path, $dir, $i),
                str_starts_with($mime, 'video/') => ['kind' => 'video'] + telegram_prepare_video($path, $dir, $i, $route),
                default => throw new RuntimeException('поддерживаются только картинки и видео')
            };
        } catch (RuntimeException $e) {
            throw new RuntimeException("«{$name}»: " . $e->getMessage());
        }
    }
    foreach (count($prepared) > 1 ? $prepared : [] as $i => $f) {
        if ($f['kind'] !== 'animation') continue;
        if (ffmpeg_binary() === '') throw new RuntimeException("«{$f['name']}»: гифку в альбоме нельзя отправить без ffmpeg. Отправьте её отдельным постом.");
        if (!ffmpeg_run('-i ' . escapeshellarg($f['path']) . ' -movflags +faststart -pix_fmt yuv420p -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" -an -f mp4', $out = "$dir/gif-$i.mp4", 300)) {
            throw new RuntimeException("«{$f['name']}»: не удалось превратить гифку в видео.");
        }
        $prepared[$i] = ['kind' => 'video', 'path' => $out, 'name' => $f['name']] + telegram_video_info($out);
    }
    return $prepared;
}

// ---- Предварительная проверка записи (ракета на карточке) ----

/** Размер файла по ссылке (HEAD), с кэшем в /tmp. null — неизвестно. */
function telegram_remote_size($url) {
    $file = sys_get_temp_dir() . '/postmaker-remote-sizes.json';
    $cache = load_data($file, []);
    $key = substr(sha1($url), 0, 16);
    if (isset($cache[$key]) && time() - (int)$cache[$key][1] < ($cache[$key][0] === null ? 600 : 6 * 3600)) return $cache[$key][0];
    $size = null;
    try {
        $ch = curl_init($url);
        curl_setopt_array($ch, [CURLOPT_NOBODY => true, CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => 5, CURLOPT_CONNECTTIMEOUT => 3,
            CURLOPT_PROTOCOLS => CURLPROTO_HTTP | CURLPROTO_HTTPS, CURLOPT_USERAGENT => HTTP_UA, CURLOPT_RESOLVE => public_url_resolve($url)]);
        curl_exec($ch);
        $length = (float)curl_getinfo($ch, CURLINFO_CONTENT_LENGTH_DOWNLOAD);
        if ((int)curl_getinfo($ch, CURLINFO_HTTP_CODE) === 200 && $length > 0) $size = (int)$length;
        curl_close($ch);
    } catch (Throwable $e) {}
    $cache = array_filter($cache, fn($e) => time() - (int)$e[1] < 6 * 3600);
    $cache[$key] = [$size, time()];
    @file_put_contents($file, json_encode($cache), LOCK_EX);
    return $size;
}

/** Можно ли запостить видео записи: status ok | partial | blocked, problems, warnings, bad (номера файлов). */
function telegram_preflight_item(array $item) {
    $gallery = (array)($item['gallery'] ?? []);
    $main = content_image_key($item);
    $files = count($gallery) > 1
        ? array_map(fn($f) => ['video' => (string)($f['video'] ?? ''), 'video_local' => (string)(($f['image'] ?? '') === $main ? ($item['video_local'] ?? '') : ($f['video_local'] ?? ''))], $gallery)
        : [['video' => (string)($item['video'] ?? ''), 'video_local' => (string)($item['video_local'] ?? '')]];
    $problems = $warnings = $bad = [];
    foreach ($files as $i => $f) {
        if ($f['video'] === '') continue;
        $label = count($files) > 1 ? 'Видео ' . ($i + 1) . ': ' : 'Видео: ';
        $problem = '';
        if (($path = media_cache_path($f['video_local'])) !== null && is_file($path)) {
            $size = filesize($path);
            $info = telegram_video_info($path);
            if ($info['video'] === '') $problem = 'файл не распознан как видео (или на сервере нет ffprobe)';
            elseif (!telegram_video_ready($info) && ffmpeg_binary() === '') $problem = 'нужно перекодировать в MP4, а ffmpeg на сервере не найден';
        } else $size = telegram_remote_size($f['video']);
        if ($problem === '' && $size !== null) {
            $verdict = telegram_video_verdict($size);
            if ($verdict === 'blocked') $problem = telegram_video_limit_text($size);
            elseif ($verdict === 'risky') $warnings[] = $label . telegram_video_limit_text($size) . ' по последним данным; Telegram мог изменить лимит, поэтому попробуем отправить.';
        }
        if ($problem !== '') { $problems[] = $label . $problem; $bad[] = $i; }
    }
    return ['status' => !$bad ? 'ok' : (count($bad) >= count($files) ? 'blocked' : 'partial'), 'problems' => $problems, 'warnings' => $warnings, 'bad' => $bad];
}

// ---- Отправка ----

function telegram_message_url(array $c, $id) {
    if (!empty($c['chat_username'])) return 'https://t.me/' . rawurlencode($c['chat_username']) . '/' . (int)$id;
    return preg_match('/^-100(\d+)$/', (string)($c['chat_id'] ?? ''), $m) ? "https://t.me/c/{$m[1]}/" . (int)$id : '';
}

function telegram_check_text($text, $media_count) {
    $limit = $media_count > 0 ? TELEGRAM_CAPTION_LIMIT : TELEGRAM_TEXT_LIMIT;
    if (mb_strlen($text) > $limit) throw new RuntimeException('Текст длиннее лимита Telegram: ' . mb_strlen($text) . " из {$limit}" . ($media_count > 0 ? ' (подпись к медиа)' : '') . '.');
    if ($media_count === 0 && trim($text) === '') throw new RuntimeException('Добавьте медиа или текст.');
}

/** Отправляет пост ботом. Возвращает ['message_ids', 'url']. */
function telegram_publish_prepared(array $c, $text, array $prepared) {
    [$token, $chat] = [(string)($c['token'] ?? ''), (string)($c['chat_id'] ?? '')];
    if ($token === '' || $chat === '') throw new RuntimeException('Telegram не подключён: подключите бота и канал в «Личном кабинете».');
    telegram_check_text($text, count($prepared));
    $biggest = max([0, ...array_map(fn($f) => $f['kind'] === 'video' ? (int)@filesize($f['path']) : 0, $prepared)]);
    $caption = $text !== '' ? ['caption' => $text] : [];
    $video_meta = fn($f) => ['width' => $f['width'], 'height' => $f['height'], 'duration' => $f['duration']];
    try {
        if (!$prepared) {
            $messages = [telegram_bot_request($token, 'sendMessage', ['chat_id' => $chat, 'text' => $text, 'disable_web_page_preview' => 'true'])];
        } elseif (count($prepared) === 1) {
            $f = $prepared[0];
            $method = ['photo' => 'sendPhoto', 'animation' => 'sendAnimation', 'video' => 'sendVideo'][$f['kind']];
            $extra = $f['kind'] === 'video' ? ['supports_streaming' => 'true'] + $video_meta($f) : [];
            $messages = [telegram_bot_request($token, $method, ['chat_id' => $chat] + $caption + $extra, [$f['kind'] => $f['path']], 600)];
        } else {
            $media = $files = [];
            foreach ($prepared as $i => $f) {
                $media[] = ['type' => $f['kind'] === 'video' ? 'video' : 'photo', 'media' => "attach://file$i"]
                    + ($f['kind'] === 'video' ? ['supports_streaming' => true] + $video_meta($f) : []) + ($i === 0 ? $caption : []);
                $files["file$i"] = $f['path'];
            }
            $messages = telegram_bot_request($token, 'sendMediaGroup', ['chat_id' => $chat, 'media' => json_encode($media, JSON_UNESCAPED_UNICODE)], $files, 600);
        }
    } catch (RuntimeException $e) {
        if ($biggest > 0 && $e->getMessage() === 'Файл слишком большой для Telegram.') {
            telegram_video_limit_learn($biggest, false);
            throw new RuntimeException('Telegram не принял видео (' . telegram_video_limit_text($biggest) . '). Лимит запомнен: такие видео приложение больше не предлагает.');
        }
        throw $e;
    }
    telegram_video_limit_learn($biggest, true);
    $ids = array_map(fn($m) => (int)$m['message_id'], $messages);
    return ['message_ids' => $ids, 'url' => telegram_message_url($c, $ids[0] ?? 0)];
}

function telegram_post_summary($text, $media_count) { return ['text' => mb_substr(trim((string)$text), 0, 120), 'media' => (int)$media_count]; }

function telegram_log_published($uid, array $entry) {
    published_add($entry, $uid);
}

/** Пост в «Отложенных» канала: запись в outbox.json (status telegram), чтобы его видеть и отменять в кабинете. */
function telegram_record_native_post($uid, $text, array $prepared, $publish_at, array $ids) {
    $id = bin2hex(random_bytes(6));
    outbox_update(fn($q) => [...$q, ['id' => $id, 'created_at' => time(), 'publish_at' => (int)$publish_at, 'text' => $text,
        'media' => array_map(fn($f) => ['kind' => $f['kind'], 'name' => $f['name'] ?? ''], $prepared), 'status' => 'telegram', 'tg_ids' => $ids, 'attempts' => 0, 'error' => '']]);
    return $id;
}

/** Отложенные посты, время которых прошло, переносятся в журнал отправленных. */
function telegram_settle_native_posts($uid) {
    $settled = [];
    outbox_update(function ($q) use (&$settled) {
        $kept = [];
        foreach ($q as $e) (($e['status'] ?? '') === 'telegram' && (int)$e['publish_at'] + 120 < time()) ? $settled[] = $e : $kept[] = $e;
        return $settled ? $kept : null;
    }, $uid);
    foreach ($settled as $e) telegram_log_published($uid, telegram_post_summary($e['text'], count($e['media'])) + ['at' => (int)$e['publish_at'], 'scheduled_for' => (int)$e['publish_at'], 'url' => '', 'status' => 'ok', 'error' => '']);
}

function telegram_unqueue_post($uid, $post_id) {
    $found = false;
    outbox_update(function ($q) use ($post_id, &$found) {
        $kept = array_values(array_filter($q, fn($e) => ($e['id'] ?? '') !== $post_id));
        return ($found = count($kept) !== count($q)) ? $kept : null;
    }, $uid);
    return $found;
}

// ---- Личный аккаунт: tools/tg_user.py (JSON в stdin → JSON в stdout) ----

function telegram_user_run(array $request, $timeout = TELEGRAM_USER_TIMEOUT) {
    $proc = @proc_open([TELEGRAM_USER_PYTHON, ROOT . '/tools/tg_user.py'], [['pipe', 'r'], ['pipe', 'w'], ['pipe', 'w']], $pipes, null,
        ['PATH' => '/usr/bin:/bin', 'PYTHONDONTWRITEBYTECODE' => '1', 'LANG' => 'C.UTF-8']);
    if (!is_resource($proc)) throw new RuntimeException('Не удалось запустить помощника Telegram (нужен python3).');
    fwrite($pipes[0], json_encode($request, JSON_UNESCAPED_UNICODE));
    fclose($pipes[0]);
    stream_set_blocking($pipes[1], false);
    $out = '';
    for ($deadline = time() + $timeout; ; usleep(50000)) {
        $out .= (string)fread($pipes[1], 65536);
        if (!proc_get_status($proc)['running']) { $out .= (string)stream_get_contents($pipes[1]); break; }
        if (time() > $deadline) { proc_terminate($proc, 9); fclose($pipes[1]); fclose($pipes[2]); proc_close($proc); throw new RuntimeException('Telegram не ответил вовремя.'); }
    }
    $errors = trim((string)stream_get_contents($pipes[2]));
    fclose($pipes[1]);
    fclose($pipes[2]);
    proc_close($proc);
    $r = json_decode($out, true);
    if (!is_array($r)) throw new RuntimeException('Помощник Telegram не ответил' . ($errors !== '' ? ': ' . mb_substr($errors, -300) : '.'));
    return !empty($r['ok']) ? $r : throw new RuntimeException((string)($r['error'] ?? 'Неизвестная ошибка Telegram.'));
}

function telegram_user_connection($uid = null) {
    $e = connections_load($uid)['telegram_user'] ?? null;
    if (!is_array($e) || empty($e['session_enc']) || empty($e['api_hash_enc'])) return null;
    [$hash, $session] = [secret_decrypt($e['api_hash_enc']), secret_decrypt($e['session_enc'])];
    return $hash !== null && $session !== null ? ['api_id' => (string)$e['api_id'], 'api_hash' => $hash, 'session' => $session] + $e : null;
}

/** Канал для аккаунта: @имя или id канала, к которому подключён бот. */
function telegram_user_channel_ref(array $bot) { return ($bot['chat_username'] ?? '') !== '' ? '@' . $bot['chat_username'] : (string)$bot['chat_id']; }

/** Запрос от имени подключённого аккаунта; обновлённая сессия сохраняется. */
function telegram_user_call($uid, array $request, $timeout = TELEGRAM_USER_TIMEOUT) {
    $c = telegram_user_connection($uid) ?? throw new RuntimeException('Аккаунт Telegram не подключён.');
    $r = telegram_user_run(['api_id' => $c['api_id'], 'api_hash' => $c['api_hash'], 'session' => $c['session']] + $request, $timeout);
    if (!empty($r['session'])) update_connections(fn($all) => isset($all['telegram_user']) ? array_replace_recursive($all, ['telegram_user' => ['session_enc' => secret_encrypt($r['session'])]]) : null, $uid);
    return $r;
}

function telegram_user_schedule($uid, array $bot, $text, array $prepared, $publish_at) {
    $media = array_map(fn($f) => ['kind' => $f['kind'], 'path' => $f['path'], 'width' => $f['width'] ?? 0, 'height' => $f['height'] ?? 0, 'duration' => $f['duration'] ?? 0], $prepared);
    return array_map('intval', telegram_user_call($uid, ['action' => 'schedule', 'channel' => telegram_user_channel_ref($bot), 'text' => $text, 'publish_at' => (int)$publish_at, 'media' => $media])['ids'] ?? []);
}

function telegram_user_cancel($uid, array $bot, array $ids) {
    telegram_user_call($uid, ['action' => 'cancel', 'channel' => telegram_user_channel_ref($bot), 'ids' => $ids], 60);
}

/** Поиск каналов (queries) или похожих (similar_to) через аккаунт: [['username', 'title', 'subscribers', 'origin']]. */
function telegram_user_find_channels(array $queries, array $similar_to = [], $uid = null) {
    $uid ??= app_user_id();
    if (telegram_user_connection($uid) === null || (!$queries && !$similar_to)) return [];
    $request = $queries ? ['action' => 'search_channels', 'queries' => array_values($queries), 'limit' => 30] : ['action' => 'similar_channels', 'channels' => array_values($similar_to)];
    return array_values(array_filter((array)(telegram_user_call($uid, $request, 120)['channels'] ?? []), fn($c) => is_valid_telegram_channel((string)($c['username'] ?? ''))));
}

/** Состояние входа по QR — во временной папке (сессия в нём открытая). */
function telegram_user_qr_state_file($uid = null) { return sys_get_temp_dir() . '/postmaker-tgu-' . ($uid ?? app_user_id()) . '.json'; }

function telegram_user_qr_start($api_id, $api_hash) {
    $token = bin2hex(random_bytes(8));
    $state = telegram_user_qr_state_file();
    file_put_contents($state, json_encode(['token' => $token, 'status' => 'starting']));
    @chmod($state, 0600);
    $request = tempnam(sys_get_temp_dir(), 'postmaker-tgu-req-');
    file_put_contents($request, json_encode(['api_id' => $api_id, 'api_hash' => $api_hash, 'state_file' => $state, 'token' => $token]));
    @chmod($request, 0600);
    exec('PYTHONDONTWRITEBYTECODE=1 PATH=/usr/bin:/bin /usr/bin/setsid ' . escapeshellarg(TELEGRAM_USER_PYTHON) . ' ' . escapeshellarg(ROOT . '/tools/tg_user.py') . ' qr-login ' . escapeshellarg($request) . ' >/dev/null 2>&1 &');
    return $token;
}

/** api_id и api_hash выдаются приложению: вводятся один раз и подставляются всем пользователям сайта. */
function telegram_app_keys() {
    $d = load_data(TELEGRAM_APP_KEYS_FILE, []);
    $hash = !empty($d['api_hash_enc']) ? secret_decrypt($d['api_hash_enc']) : null;
    return $hash !== null && !empty($d['api_id']) ? ['api_id' => (string)$d['api_id'], 'api_hash' => $hash] : null;
}

function telegram_app_keys_save($api_id, $api_hash) {
    save_data(TELEGRAM_APP_KEYS_FILE, ['api_id' => (string)$api_id, 'api_hash_enc' => secret_encrypt($api_hash), 'saved_at' => time()]);
    @chmod(TELEGRAM_APP_KEYS_FILE, 0660);
}
