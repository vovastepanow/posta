<?php
// Публикация в Telegram-канал пользователя: бот (Bot API) — сразу, личный аккаунт (tools/tg_user.py, Telethon) — в «Отложенные»
// канала. Файлы приводятся к форматам Telegram; лимит размера видео самообучается по ответам Telegram (kv telegram_limits).

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

// ---- Прогресс отправки: этап и процент в файле, браузер опрашивает его отдельным запросом ----

/** Файл прогресса отправки: id приходит от браузера (32 hex-символа). null — id нет или он некорректный. */
function telegram_progress_file($id) {
    return is_string($id) && preg_match('/^[a-f0-9]{32}$/', $id) ? user_dir() . "/publish-{$id}.json" : null;
}

/** Записывает этап и процент (0–100 или null — «неизвестно, идёт»). Не чаще раза в 300 мс, кроме смены этапа. */
function telegram_progress($stage, $percent = null) {
    static $last = ['stage' => null, 'percent' => null, 'ms' => 0];
    if (($file = $GLOBALS['telegram_progress_file'] ?? null) === null) return;
    $percent = $percent === null ? null : (int)round(max(0, min(100, $percent)));
    $ms = hrtime(true) / 1e6;
    if ($stage === $last['stage'] && $percent === $last['percent'] && $ms - $last['ms'] < 300) return;
    $last = ['stage' => $stage, 'percent' => $percent, 'ms' => $ms];
    @file_put_contents($file, json_encode(['stage' => $stage, 'percent' => $percent, 'at' => time()], JSON_UNESCAPED_UNICODE), LOCK_EX);
}

/** Колбэк заливки: пока байты уходят — проценты, после — ждём ответа Telegram. */
function telegram_upload_progress($percent) {
    $percent < 100 ? telegram_progress('Отправка в Telegram', $percent) : telegram_progress('Telegram обрабатывает видео…');
}

/** POST к Bot API ($files — [поле => путь], уходят multipart). Адрес можно подменить POSTA_TELEGRAM_API для проверок. */
function telegram_bot_request($token, $method, array $fields = [], array $files = [], $timeout = 60, ?callable $on_upload = null) {
    foreach ($files as $name => $path) $fields[$name] = new CURLFile($path, mime_content_type($path) ?: 'application/octet-stream', basename($path));
    $options = [CURLOPT_CONNECTTIMEOUT => 10, CURLOPT_TIMEOUT => $timeout, CURLOPT_USERAGENT => HTTP_UA];
    if ($on_upload) $options += [CURLOPT_NOPROGRESS => false,
        CURLOPT_XFERINFOFUNCTION => function ($ch, $down_total, $down_now, $up_total, $up_now) use ($on_upload) {
            if ($up_total > 0) $on_upload($up_now / $up_total * 100);
            return 0;   // 0 — продолжать; не 1, иначе curl оборвёт заливку
        }];
    $r = http_post((getenv('POSTA_TELEGRAM_API') ?: 'https://api.telegram.org/bot') . "$token/$method", $fields, $options);
    if ($r['body'] === false) throw new RuntimeException('Telegram недоступен: ' . $r['error']);
    $data = json_decode((string)$r['body'], true);
    if (!is_array($data) || empty($data['ok'])) throw new RuntimeException(telegram_error_text(is_array($data) ? $data : [], $r['status']));
    return $data['result'];
}

/** Подключённый канал с расшифрованным токеном или null: канал $channel_id пользователя, без него — канал по умолчанию (первый). */
function telegram_connection($uid = null, $channel_id = null) {
    $c = ($channel_id ?? '') !== '' ? channel_get($channel_id, $uid) : (telegram_channels_load($uid)[0] ?? null);
    return $c !== null && !empty($c['token']) ? $c : null;
}

/** Канал для отправки поста: любая соцсеть по id, без id — Telegram по умолчанию. Без секретов — null. */
function channel_for_post($uid, $channel_id = null) {
    $c = ($channel_id ?? '') !== '' ? channel_get($channel_id, $uid) : telegram_connection($uid);
    return $c !== null && !empty($c['secrets']) ? $c : null;
}

/** Новый для пользователя канал сверх тарифа (канал с этим chat_id уже подключён — не считается). */
function channel_over_limit($chat_id, $uid = null) {
    $channels = channels_load($uid);
    return !array_filter($channels, fn($c) => $c['chat_id'] === (int)$chat_id) && count($channels) >= billing_limits($uid)['channels'];
}

/** Текст отказа подключить канал по тарифу — только при $enforce (BILLING_ENFORCE); иначе null, канал подключается с предупреждением. */
function channel_connect_denied($chat_id, $uid = null, $enforce = BILLING_ENFORCE) {
    if (!$enforce || !channel_over_limit($chat_id, $uid)) return null;
    $l = billing_limits($uid);
    return "На тарифе «{$l['title']}» можно подключить каналов: {$l['channels']}. Больше — в «Тариф и баланс».";
}

/** Текст отказа удалить канал: у него есть отложенные посты (в «Отложенных» Telegram или в нашей очереди). Посты без поля channel относятся к каналу по умолчанию. */
function channel_delete_denied($channel_id, $uid = null) {
    $default = telegram_channels_load($uid)[0]['id'] ?? null;
    foreach (outbox_load($uid) as $e) {
        if (in_array($e['status'] ?? '', ['telegram', 'queued', 'sending', 'failed'], true) && (($e['channel'] ?? '') ?: $default) === (string)$channel_id) return 'У канала есть отложенные посты: сначала отмените их в очереди.';
    }
    return null;
}

/** Канал для публикации: канал по умолчанию при пустом id, иначе выбранный. ['channel' => канал] или ['error' => текст, 'status' => код]. */
function publish_channel_resolve($channel_id, $uid = null, $enforce = BILLING_ENFORCE) {
    if ($channel_id === null || $channel_id === '') {
        return ($c = telegram_connection($uid)) ? ['channel' => $c] : ['error' => 'Telegram не подключён: подключите бота и канал в «Личном кабинете» → «Соцсети».', 'status' => 409];
    }
    $list = channels_load($uid);
    $index = array_search((string)$channel_id, array_column($list, 'id'), true);
    if ($index === false || empty($list[$index]['secrets'])) return ['error' => 'Канал не найден: обновите страницу.', 'status' => 404];
    if ($enforce && $index >= billing_limits($uid)['channels']) {
        return ['error' => 'Канал за пределами тарифа: на тарифе «' . billing_limits($uid)['title'] . '» публиковать можно в первые ' . billing_limits($uid)['channels'] . ' канала по порядку.', 'status' => 402];
    }
    return ['channel' => $list[$index]];
}

/** Поля канала для журнала и очереди: id и название (канал по умолчанию, если id не задан). */
function telegram_channel_fields($channel_id, $uid = null) {
    $c = $channel_id !== null && $channel_id !== '' ? channel_get($channel_id, $uid) : (telegram_channels_load($uid)[0] ?? null);
    return ['channel' => $c['id'] ?? '', 'channel_title' => $c['chat_title'] ?? ''];
}

function update_connections(callable $mutator, $uid = null) {
    return connections_update(fn($c) => $mutator(is_array($c) ? $c : []), $uid);
}

// ---- Лимиты на видео: документация (50 МБ бот, 2 ГБ аккаунт) уточняется ответами Telegram ----

function telegram_video_limit($route = 'bot') {
    $known = ((array)kv_get('telegram_limits', []))[$route]['video'] ?? [];
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
    kv_update('telegram_limits', function ($state) use ($route, $bytes, $accepted) {
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
    }, []);
}

// ---- Подготовка файлов ----

function telegram_work_dir() {
    $dir = sys_get_temp_dir() . '/posta-publish-' . bin2hex(random_bytes(6));
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
    // Имя файла в кэше медиа — хэш адреса, содержимое не меняется: результат ffprobe запоминаем по имени и размеру
    $file = sys_get_temp_dir() . '/posta-video-info.json';
    static $cache = null;
    $cache ??= load_data($file, []);
    $key = basename($path) . ':' . (int)@filesize($path);
    if (isset($cache[$key])) return $cache[$key];
    $info = telegram_video_probe($path);
    if ($info['video'] !== '') { $cache[$key] = $info; @file_put_contents($file, json_encode($cache), LOCK_EX); }
    return $info;
}

function telegram_video_probe($path) {
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
        if (!ffmpeg_run_progress('-i ' . escapeshellarg($path) . ' -map 0:v:0 -map 0:a:0? -c:v libx264 -preset veryfast -crf 26 -pix_fmt yuv420p -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" -c:a aac -b:a 128k -movflags +faststart -f mp4', $out, 600,
            $info['duration'], fn($p) => telegram_progress('Перекодирую видео', $p))) {
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
            elseif (preg_match('~^https?://~i', $src)) {
                if (($item['kind'] ?? '') === 'video') telegram_progress('Скачиваю видео');
                fetch_public_media($src, $path = "$dir/source-$i", TELEGRAM_UPLOAD_MAX_BYTES);
            }
            else throw new RuntimeException('файл недоступен (ни в кэше сайта, ни по ссылке).');
            $mime = (new finfo(FILEINFO_MIME_TYPE))->file($path) ?: '';
            $prepared[] = ['name' => $name, 'src' => $src] + match (true) {
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
    $file = sys_get_temp_dir() . '/posta-remote-sizes.json';
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
function telegram_preflight_item(array $item, $remote = true) {
    if (in_array($item['video_via'] ?? '', ['ytdlp', 'tgu'], true) && ($item['video'] ?? '') === '' && empty($item['video_unavailable'])) {   // ролик ещё скачивается: картинку вместо видео не публикуем
        return ['status' => 'blocked', 'problems' => ['Видео ещё загружается, попробуйте через минуту'], 'warnings' => [], 'bad' => [0]];
    }
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
        } else $size = $remote ? telegram_remote_size($f['video']) : null;   // без сети (список «Предложки»): размер проверит редактор
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
            $messages = [telegram_bot_request($token, $method, ['chat_id' => $chat] + $caption + $extra, [$f['kind'] => $f['path']], 600, 'telegram_upload_progress')];
        } else {
            $media = $files = [];
            foreach ($prepared as $i => $f) {
                $media[] = ['type' => $f['kind'] === 'video' ? 'video' : 'photo', 'media' => "attach://file$i"]
                    + ($f['kind'] === 'video' ? ['supports_streaming' => true] + $video_meta($f) : []) + ($i === 0 ? $caption : []);
                $files["file$i"] = $f['path'];
            }
            $messages = telegram_bot_request($token, 'sendMediaGroup', ['chat_id' => $chat, 'media' => json_encode($media, JSON_UNESCAPED_UNICODE)], $files, 600, 'telegram_upload_progress');
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

/** Опрос: вопрос до 300 символов и от 2 до 10 вариантов по 100. Пустые варианты отбрасываются. Возвращает ['question', 'options']. */
function telegram_poll_check($poll): array {
    $question = trim((string)($poll['question'] ?? ''));
    $options = array_values(array_filter(array_map(fn($o) => trim((string)$o), (array)($poll['options'] ?? [])), fn($o) => $o !== ''));
    if ($question === '') throw new RuntimeException('Напишите вопрос опроса.');
    if (mb_strlen($question) > 300) throw new RuntimeException('Вопрос опроса длиннее 300 символов: ' . mb_strlen($question) . '.');
    if (count($options) < 2) throw new RuntimeException('В опросе нужно минимум два варианта ответа.');
    if (count($options) > 10) throw new RuntimeException('В опросе не больше 10 вариантов.');
    foreach ($options as $option) if (mb_strlen($option) > 100) throw new RuntimeException('Вариант ответа длиннее 100 символов.');
    return ['question' => $question, 'options' => $options];
}

/** Отправляет опрос ботом (без медиа). Возвращает ['message_ids', 'url'] как telegram_publish_prepared. */
function telegram_publish_poll(array $c, array $poll) {
    [$token, $chat] = [(string)($c['token'] ?? ''), (string)($c['chat_id'] ?? '')];
    if ($token === '' || $chat === '') throw new RuntimeException('Telegram не подключён: подключите бота и канал в «Личном кабинете».');
    $message = telegram_bot_request($token, 'sendPoll', ['chat_id' => $chat, 'question' => $poll['question'], 'options' => json_encode($poll['options'], JSON_UNESCAPED_UNICODE)]);
    return ['message_ids' => [(int)$message['message_id']], 'url' => telegram_message_url($c, (int)$message['message_id'])];
}

function telegram_post_summary($text, $media_count) { return ['text' => mb_substr(trim((string)$text), 0, 120), 'media' => (int)$media_count]; }

/** Адреса источников медиа поста (кэш сайта или ссылка) — по ним повторы находятся и по картинке. */
function media_sources(array $media): array {
    return array_values(array_filter(array_map(fn($f) => (string)($f['src'] ?? ''), $media), fn($s) => $s !== ''));
}

function telegram_log_published($uid, array $entry) {
    published_add($entry, $uid);
}

/** Пост в «Отложенных» канала: запись в таблице outbox (status telegram), чтобы его видеть и отменять в кабинете. */
function telegram_record_native_post($uid, $text, array $prepared, $publish_at, array $ids, $channel_id = null) {
    $id = bin2hex(random_bytes(6));
    outbox_update(fn($q) => [...$q, ['id' => $id, 'created_at' => time(), 'publish_at' => (int)$publish_at, 'text' => $text,
        'media' => array_map(fn($f) => ['kind' => $f['kind'], 'name' => $f['name'] ?? ''], $prepared), 'status' => 'telegram', 'tg_ids' => $ids, 'attempts' => 0, 'error' => '',
        'channel' => (string)$channel_id, 'thumb_id' => post_thumb_make($prepared)]]);   // файлы ещё во временной папке — миниатюра берётся сейчас
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
    foreach ($settled as $e) telegram_log_published($uid, telegram_post_summary($e['text'], count($e['media'])) + telegram_channel_fields($e['channel'] ?? null, $uid)
        + ['at' => (int)$e['publish_at'], 'scheduled_for' => (int)$e['publish_at'], 'url' => '', 'status' => 'ok', 'error' => '', 'media_src' => media_sources($e['media'] ?? []), 'thumb_id' => (string)($e['thumb_id'] ?? ''), 'kind' => (string)($e['media'][0]['kind'] ?? '')]);
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
    return array_map('intval', telegram_user_call($uid, ['action' => 'schedule', 'channel' => telegram_user_channel_ref($bot), 'text' => $text, 'publish_at' => (int)$publish_at, 'media' => $media,
        'progress_file' => (string)($GLOBALS['telegram_progress_file'] ?? '')])['ids'] ?? []);
}

/** Видео поста публичного канала через личный аккаунт пользователя: [канал, номер поста] → файл в $dest_dir. Возвращает путь. */
function telegram_user_download_video($uid, $channel, $post_id, $dest_dir, $max_bytes = TGU_VIDEO_MAX_BYTES) {
    if (!is_dir($dest_dir) && !@mkdir($dest_dir, 0775, true) && !is_dir($dest_dir)) throw new RuntimeException('Не удалось создать папку для видео.');
    $r = telegram_user_call($uid, ['action' => 'download_video', 'channel' => '@' . ltrim((string)$channel, '@'), 'post_id' => (int)$post_id, 'dest_dir' => $dest_dir, 'max_bytes' => (int)$max_bytes], TGU_VIDEO_TIMEOUT);
    $path = (string)($r['path'] ?? '');
    if (!is_file($path)) throw new RuntimeException('Telegram не отдал файл видео.');
    return $path;
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

/**
 * Проверка имён каналов через аккаунт пользователя (не больше 10 за вызов): [имя => true|false|null]. null — не удалось проверить.
 */
function telegram_user_resolve_usernames(array $names, $uid = null): array {
    $uid ??= app_user_id();
    $names = array_slice(array_values(array_filter(array_map('strval', $names), 'is_valid_telegram_channel')), 0, 10);
    if (!$names || telegram_user_connection($uid) === null) return [];
    $out = [];
    foreach ((array)(telegram_user_call($uid, ['action' => 'resolve_usernames', 'usernames' => $names], 60)['results'] ?? []) as $r) {
        $out[(string)($r['username'] ?? '')] = array_key_exists('exists', $r) ? $r['exists'] : null;
    }
    return $out;
}

/** Поиск публичных групп-чатов (не каналов) по запросам: [['username', 'title', 'members', 'query']]. Для радара лидов. */
function telegram_user_find_chats(array $queries, $uid = null) {
    $uid ??= app_user_id();
    if (telegram_user_connection($uid) === null || !$queries) return [];
    $chats = (array)(telegram_user_call($uid, ['action' => 'search_chats', 'queries' => array_values($queries), 'limit' => 30], 120)['chats'] ?? []);
    return array_values(array_filter($chats, fn($c) => is_valid_telegram_channel((string)($c['username'] ?? ''))));
}

/** Читать новые сообщения из чатов пользователя. */
function telegram_user_read_chats(array $chats, $uid = null) {
    $uid ??= app_user_id();
    if (telegram_user_connection($uid) === null) return ['chats' => []];
    return (array)telegram_user_call($uid, ['action' => 'read_chats', 'chats' => $chats], 180);
}

/** Состояние входа по QR — во временной папке (сессия в нём открытая). */
function telegram_user_qr_state_file($uid = null) { return sys_get_temp_dir() . '/posta-tgu-' . ($uid ?? app_user_id()) . '.json'; }

function telegram_user_qr_start($api_id, $api_hash) {
    $token = bin2hex(random_bytes(8));
    $state = telegram_user_qr_state_file();
    file_put_contents($state, json_encode(['token' => $token, 'status' => 'starting']));
    @chmod($state, 0600);
    $request = tempnam(sys_get_temp_dir(), 'posta-tgu-req-');
    file_put_contents($request, json_encode(['api_id' => $api_id, 'api_hash' => $api_hash, 'state_file' => $state, 'token' => $token]));
    @chmod($request, 0600);
    exec('PYTHONDONTWRITEBYTECODE=1 PATH=/usr/bin:/bin /usr/bin/setsid ' . escapeshellarg(TELEGRAM_USER_PYTHON) . ' ' . escapeshellarg(ROOT . '/tools/tg_user.py') . ' qr-login ' . escapeshellarg($request) . ' >/dev/null 2>&1 &');
    return $token;
}

/** api_id и api_hash выдаются приложению: вводятся один раз и подставляются всем пользователям сайта. */
function telegram_app_keys() {
    $d = (array)kv_get('telegram_app_keys', []);
    $hash = !empty($d['api_hash_enc']) ? secret_decrypt($d['api_hash_enc']) : null;
    return $hash !== null && !empty($d['api_id']) ? ['api_id' => (string)$d['api_id'], 'api_hash' => $hash] : null;
}

function telegram_app_keys_save($api_id, $api_hash) {
    kv_update('telegram_app_keys', fn() => ['api_id' => (string)$api_id, 'api_hash_enc' => secret_encrypt($api_hash), 'saved_at' => time()]);
}

// ---- Расписание канала: слоты по дням недели в часовом поясе пользователя ----

const SCHED_MAX_JITTER = 30;          // случайный сдвиг, минут
const SCHED_MAX_SLOTS_PER_DAY = 24;
const SCHED_SEARCH_DAYS = 60;         // дальше слот не ищем
const SCHED_BUSY_SECONDS = 60;        // слот занят, если пост стоит в пределах минуты от него

/** Расписание канала с проверкой и значениями по умолчанию (пустые слоты, jitter 0, tz сервера). */
function channel_schedule(array $channel): array {
    $s = is_array($channel['schedule'] ?? null) ? $channel['schedule'] : [];
    $tz = in_array($s['tz'] ?? '', DateTimeZone::listIdentifiers(), true) ? $s['tz'] : date_default_timezone_get();
    $slots = [];
    foreach ((array)($s['slots'] ?? []) as $day => $times) {
        if (ctype_digit((string)$day) && (int)$day >= 1 && (int)$day <= 7 && is_array($times) && $times) $slots[(int)$day] = array_values($times);
    }
    return ['tz' => $tz, 'jitter' => min(SCHED_MAX_JITTER, max(0, (int)($s['jitter'] ?? 0))), 'slots' => $slots];
}

/**
 * Проверить и сохранить расписание. Неизвестный часовой пояс заменяется поясом сервера (не ошибка);
 * неверный формат времени, дня, сдвига или больше 24 слотов в день — InvalidArgumentException.
 */
function channel_schedule_save($channel_id, array $schedule, $uid = null): array {
    $uid = $uid ?: app_user_id();
    channel_get($channel_id, $uid) ?? throw new DomainException('Такого канала нет.');
    $tz = in_array($schedule['tz'] ?? '', DateTimeZone::listIdentifiers(), true) ? $schedule['tz'] : date_default_timezone_get();
    $jitter = $schedule['jitter'] ?? 0;
    if (!is_numeric($jitter) || (int)$jitter != $jitter || $jitter < 0 || $jitter > SCHED_MAX_JITTER) {
        throw new InvalidArgumentException('Случайный сдвиг — целое число минут от 0 до ' . SCHED_MAX_JITTER . '.');
    }
    $slots = [];
    foreach ((array)($schedule['slots'] ?? []) as $day => $times) {
        if (!ctype_digit((string)$day) || (int)$day < 1 || (int)$day > 7) throw new InvalidArgumentException('День недели — число от 1 (пн) до 7 (вс).');
        $list = is_array($times) ? $times : preg_split('/[\s,;]+/u', (string)$times, -1, PREG_SPLIT_NO_EMPTY);
        $clean = [];
        foreach ($list as $time) {
            if (!preg_match('/^([01]\d|2[0-3]):[0-5]\d$/', $time = trim((string)$time))) throw new InvalidArgumentException("Время «{$time}» нужно писать как ЧЧ:ММ, например 10:00.");
            $clean[$time] = true;   // повторы убираем
        }
        $clean = array_keys($clean);
        sort($clean);
        if (count($clean) > SCHED_MAX_SLOTS_PER_DAY) throw new InvalidArgumentException('В день — не больше ' . SCHED_MAX_SLOTS_PER_DAY . ' слотов.');
        if ($clean) $slots[(int)$day] = $clean;
    }
    $saved = ['tz' => $tz, 'jitter' => (int)$jitter, 'slots' => $slots];
    db_exec('UPDATE channels SET schedule = ? WHERE id = ? AND user_id = ?', [db_json($saved), (string)$channel_id, $uid]);
    return channel_schedule(['schedule' => $saved]);
}

/**
 * Ближайший свободный слот канала не раньше $after и не раньше now + минимальной задержки. Слот занят, если на это
 * время (± минута, до сдвига) уже стоит пост канала в статусе queued/sending/telegram. Ищем не дальше 60 дней.
 * Возвращает ['slot_at' => время слота, 'publish_at' => время со случайным сдвигом] или null — слотов нет.
 */
function channel_next_slot(array $channel, $after, array $queue): ?array {
    $s = channel_schedule($channel);
    if (!$s['slots']) return null;
    $tz = new DateTimeZone($s['tz']);
    $floor = max((int)$after, time() + TELEGRAM_MIN_DELAY_SECONDS);
    $busy = [];
    foreach ($queue as $p) {
        if (($p['channel'] ?? '') === $channel['id'] && in_array($p['status'] ?? '', ['queued', 'sending', 'telegram'], true)) {
            $busy[] = (int)($p['slot_at'] ?? $p['publish_at'] ?? 0);
        }
    }
    $start = (new DateTimeImmutable('@' . $floor))->setTimezone($tz)->setTime(0, 0);
    for ($d = 0; $d <= SCHED_SEARCH_DAYS; $d++) {
        $day = $start->modify("+$d day")->format('Y-m-d');
        foreach ($s['slots'][(int)(new DateTimeImmutable($day, $tz))->format('N')] ?? [] as $hm) {
            $at = (new DateTimeImmutable("$day $hm", $tz))->getTimestamp();
            if ($at < $floor || array_filter($busy, fn($b) => abs($b - $at) <= SCHED_BUSY_SECONDS)) continue;
            $shift = $s['jitter'] ? random_int(-$s['jitter'], $s['jitter']) * 60 : 0;
            return ['slot_at' => $at, 'publish_at' => max($floor, $at + $shift)];
        }
    }
    return null;
}

// ---- Своя очередь публикаций: посты в outbox со статусами queued / sending / failed ----

const OUTBOX_RETRY_DELAYS = [60, 300, 900];   // пауза перед повтором после 1-й и 2-й неудачи
const OUTBOX_MAX_ATTEMPTS = 3;
const OUTBOX_SENDING_TIMEOUT = 900;           // «sending» дольше — процесс упал посреди отправки
/** Ошибки, которые повтором не исправить (ищем и по тексту Telegram, и по русскому тексту telegram_error_text). */
const OUTBOX_FATAL = ['was kicked', 'chat not found', 'forbidden', 'not enough rights', 'have no rights', 'not a member', 'bot was blocked',
    'токен бота недействителен', 'бот не состоит', 'telegram не нашёл канал', 'нет права публиковать', 'файл слишком большой',
    'telegram не принял файл', 'файлы поста не найдены'];

/** Папка поста: файлы отложенного поста до публикации. */
function outbox_media_dir($post_id) {
    if (!preg_match('/^[a-f0-9]{12}$/', (string)$post_id)) throw new InvalidArgumentException('Некорректный номер поста.');
    return ROOT . '/data/outbox/' . $post_id;
}

/** Переносит готовые файлы из временной папки в папку поста; если переименование не вышло (другой раздел диска) — копирует. Возвращает медиа с новыми путями. */
function outbox_media_move($from, $post_id, array $media) {
    // Каждый файл поста — в папку поста: и из временной папки, и из кэша сайта (кэш общий и чистится сам, его не трогаем).
    // Раньше из кэша файл не копировался, а путь переписывался — пост терял файл и не выходил (CALENDAR_MEDIA_FIX).
    $to = outbox_media_dir($post_id);
    if (!is_dir(dirname($to)) && !@mkdir(dirname($to), 0775, true) && !is_dir(dirname($to))) throw new RuntimeException('Не удалось создать папку очереди.');
    if (!is_dir($to) && !@mkdir($to, 0775)) throw new RuntimeException('Не удалось создать папку поста.');
    $out = [];
    try {
        foreach ($media as $i => $f) {
            $src = (string)($f['path'] ?? '');
            if ($src === '' || !is_file($src)) throw new RuntimeException('Не удалось сохранить файлы поста: файл пропал до постановки.');
            $name = basename($src);
            $dest = "$to/$name";
            if (is_file($dest)) $dest = "$to/" . $i . '-' . $name;   // одинаковые имена у двух файлов поста
            if (!copy($src, $dest)) throw new RuntimeException('Не удалось сохранить файлы поста.');
            $out[] = ['path' => $dest] + $f;
        }
    } catch (Throwable $e) {
        remove_dir($to);
        throw $e;
    }
    if (is_dir($from) && str_starts_with(realpath($from) ?: '', realpath(sys_get_temp_dir()) ?: '\0')) remove_dir($from);   // временная папка — убираем; кэш сайта не трогаем
    return $out;
}

/** Ставит пост в нашу очередь: файлы уже готовы во временной папке $work, переносятся в папку поста. */
/** Признак статьи для записи очереди: только короткие строки без HTML; нет заголовка — null. */
function publish_article_input($raw): ?array {
    $in = is_string($raw) ? json_decode($raw, true) : $raw;
    if (!is_array($in) || trim((string)($in['title'] ?? '')) === '') return null;
    $field = fn($key, $max) => mb_substr(trim(strip_tags((string)($in[$key] ?? ''))), 0, $max);
    return ['title' => $field('title', 300), 'lead' => $field('lead', 400), 'site' => $field('site', 100), 'rubric' => $field('rubric', 100), 'author' => $field('author', 100), 'minutes' => max(0, min(999, (int)($in['minutes'] ?? 0)))];
}

/** $group — общий id поста, отправленного сразу в несколько ресурсов; у одиночного поста его нет. */
function outbox_queue_post($uid, array $channel, $text, array $prepared, $publish_at, $slot_at, $work, ?array $poll = null, ?array $article = null, ?string $group = null, ?string $record = null) {
    $id = bin2hex(random_bytes(6));
    $media = outbox_media_move($work, $id, array_map(fn($f) => ['kind' => $f['kind'], 'name' => $f['name'] ?? '', 'src' => $f['src'] ?? '', 'path' => $f['path'],
        'width' => $f['width'] ?? 0, 'height' => $f['height'] ?? 0, 'duration' => $f['duration'] ?? 0], $prepared));
    $post = ['id' => $id, 'created_at' => time(), 'publish_at' => (int)$publish_at, 'slot_at' => $slot_at, 'slot' => $slot_at !== null,
        'channel' => $channel['id'], 'channel_title' => $channel['chat_title'] ?? '', 'text' => post_caption_clean($text), 'media' => $media,
        'status' => 'queued', 'attempts' => 0, 'error' => '', 'next_try_at' => 0, 'source_key' => ''] + ($poll !== null ? ['poll' => $poll] : []) + ($article !== null ? ['article' => $article] : [])
        + ($group !== null ? ['group' => $group] : []) + ($record !== null && $record !== '' ? ['record' => $record] : []);   // запись «Подбора/Предложки»: публикация = лайк, при отмене вернётся в «Предложку»
    try {
        // Файлы должны лежать в папке поста уже сейчас: иначе пост не выйдет с «Файлы поста не найдены» в момент публикации
        if (!outbox_media_ready($post)) throw new RuntimeException('Не удалось сохранить файлы поста.');
        $post['thumb_id'] = post_thumb_make($media);
        outbox_update(fn($q) => [...$q, $post], $uid);
    } catch (Throwable $e) {
        remove_dir(outbox_media_dir($id));
        throw $e;
    }
    return ['scheduled' => true, 'id' => $id, 'publish_at' => (int)$publish_at, 'native' => false, 'channel_title' => $channel['chat_title'] ?? ''];
}

/** Удалить пост нашей очереди (queued или failed) вместе с папкой. false — такого поста в этих статусах нет. */
function outbox_remove_post($post_id, $uid = null): bool {
    $uid = $uid ?: app_user_id();
    if (db_exec('DELETE FROM outbox WHERE id = ? AND user_id = ? AND status IN (?, ?)', [(string)$post_id, $uid, 'queued', 'failed']) === 0) return false;
    remove_dir(outbox_media_dir($post_id));
    return true;
}

/** Копия поста нашей очереди (ждёт или не вышел) на другое время: файлы копируются в папку нового поста. Возвращает новый пост или null — исходного нет. */
function outbox_duplicate_post($post_id, $publish_at, $uid = null): ?array {
    $uid = $uid ?: app_user_id();
    $src = array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $post_id && in_array($e['status'] ?? '', ['queued', 'failed'], true)))[0] ?? null;
    if ($src === null) return null;
    $id = bin2hex(random_bytes(6));
    [$from, $to] = [outbox_media_dir($post_id), outbox_media_dir($id)];
    if (!is_dir($to) && !@mkdir($to, 0775, true)) throw new RuntimeException('Не удалось создать папку поста.');
    foreach ($src['media'] ?? [] as $f) {
        if (!is_file("$from/" . basename($f['path'] ?? '')) || !copy("$from/" . basename($f['path']), "$to/" . basename($f['path']))) { remove_dir($to); throw new RuntimeException('Не удалось скопировать файлы поста.'); }
    }
    $copy = ['id' => $id, 'created_at' => time(), 'publish_at' => (int)$publish_at, 'slot_at' => null, 'slot' => false, 'status' => 'queued', 'attempts' => 0, 'error' => '', 'next_try_at' => 0, 'group' => bin2hex(random_bytes(6)),
        'media' => array_map(fn($f) => ['path' => "$to/" . basename($f['path'])] + $f, $src['media'] ?? [])] + $src;
    try { outbox_update(fn($q) => [...$q, $copy], $uid); }
    catch (Throwable $e) { remove_dir($to); throw $e; }
    return $copy;
}

/** Записывает пост в строку (статус — и в колонке, и в данных). */
function outbox_row_save($post_id, array $post, $status) {
    db_exec('UPDATE outbox SET status = ?, publish_at = ?, data = ? WHERE id = ?', [$status, (int)($post['publish_at'] ?? 0), db_json($post), $post_id]);
}

/** Посты всех пользователей со статусом queued, время которых пришло (и повтор не отложен), по publish_at. */
function outbox_due($now, $limit = 20): array {
    $due = [];
    foreach (db_all('SELECT id, user_id, data FROM outbox WHERE status = ? AND publish_at <= ? ORDER BY publish_at LIMIT ' . (int)($limit * 3), ['queued', (int)$now]) as $row) {
        $post = db_unjson($row['data'], []);
        if ((int)($post['next_try_at'] ?? 0) > $now) continue;
        $due[] = ['user_id' => $row['user_id']] + $post;
        if (count($due) >= $limit) break;
    }
    return $due;
}

/** Берёт пост на отправку: в транзакции перечитывает строку; уже не queued — null (его взял другой процесс или отменили). */
function outbox_claim($post_id, $now) {
    return db_tx(function () use ($post_id, $now) {
        $row = db_one('SELECT user_id, status, data FROM outbox WHERE id = ?', [(string)$post_id]);
        if (!$row || $row['status'] !== 'queued') return null;
        $post = db_unjson($row['data'], []);
        if ((int)($post['next_try_at'] ?? 0) > $now) return null;
        $post['status'] = 'sending';
        $post['attempts'] = (int)($post['attempts'] ?? 0) + 1;
        $post['sending_at'] = $now;
        outbox_row_save($post_id, $post, 'sending');
        return ['user_id' => $row['user_id'], 'post' => $post];
    });
}

function outbox_error_is_fatal($error) {
    $text = mb_strtolower((string)$error);
    foreach (OUTBOX_FATAL as $pattern) if (str_contains($text, $pattern)) return true;
    return false;
}

/** Ошибка отправки: повтор через паузу или статус failed (неисправимая, исчерпаны попытки, force). Возвращает true, если пост failed. */
function outbox_retry_or_fail($post_id, $uid, array $post, $error, $now, $force = false) {
    $attempts = max(1, (int)($post['attempts'] ?? 1));
    $failed = $force || outbox_error_is_fatal($error) || $attempts >= OUTBOX_MAX_ATTEMPTS;
    $post['error'] = (string)$error;
    unset($post['sending_at']);
    if ($failed) {
        $post['status'] = 'failed';
    } else {
        $post['status'] = 'queued';
        $post['next_try_at'] = $now + OUTBOX_RETRY_DELAYS[$attempts - 1];
    }
    db_tx(fn() => outbox_row_save($post_id, $post, $post['status']));
    if ($failed) outbox_notify_failed($uid, $post);
    return $failed;
}

/** Успех: пост уходит из очереди в журнал опубликованных, папка удаляется. */
function outbox_finish_sent($post_id, $uid, array $post, array $channel, $url, array $sent = []) {
    // Журнал — по отправленным файлам (для соцсетей без Telegram); пропущенные названы отдельно
    $shown = $sent['media_kept'] ?? $post['media'] ?? [];
    db_tx(function () use ($post_id, $uid, $post, $channel, $url, $sent, $shown) {
        db_exec('DELETE FROM outbox WHERE id = ?', [(string)$post_id]);
        telegram_log_published($uid, telegram_post_summary($post['text'] ?? '', count($shown)) + ['at' => time(),
            'scheduled_for' => (int)($post['publish_at'] ?? 0), 'url' => (string)$url, 'status' => 'ok', 'error' => '',
            'channel' => $channel['id'], 'channel_title' => $channel['chat_title'] ?? '', 'media_src' => media_sources($shown), 'skipped' => $sent['skipped'] ?? []]);
    });
    remove_dir(outbox_media_dir($post_id));
}

/** «sending» дольше OUTBOX_SENDING_TIMEOUT — не отправляем повторно (пост мог выйти), а переводим в failed. Возвращает число. */
function outbox_fail_stuck($now) {
    $count = 0;
    foreach (db_all('SELECT id, user_id, data FROM outbox WHERE status = ?', ['sending']) as $row) {
        $post = db_unjson($row['data'], []);
        if ((int)($post['sending_at'] ?? 0) > $now - OUTBOX_SENDING_TIMEOUT) continue;
        outbox_retry_or_fail($row['id'], $row['user_id'], $post, 'Отправка прервалась: проверьте канал — пост мог выйти. Если его нет — нажмите «Повторить».', $now, true);
        $count++;
    }
    return $count;
}

/** Все файлы поста на месте? Иначе отправлять нечего: повтор не поможет. */
function outbox_media_ready(array $post) {
    foreach ($post['media'] ?? [] as $f) if (!is_file((string)($f['path'] ?? ''))) return false;
    return true;
}

// ---- Миниатюры карточек календаря: маленькие JPEG отдельно от файлов поста, живут и после публикации ----

const THUMB_SIDE = 160;
const THUMB_KEEP_DAYS = 90;

function thumb_dir() { return ROOT . '/data/thumbs'; }

function thumb_file($id) { return thumb_dir() . '/' . $id . '.jpg'; }

/** Миниатюра из первого файла поста: картинка — уменьшенная копия, видео — кадр. Возвращает id или '' (сделать нельзя — карточка без миниатюры). */
function post_thumb_make(array $media): string {
    $f = $media[0] ?? [];
    $src = (string)($f['path'] ?? '');
    if ($src === '' || !is_file($src)) return '';
    try {
        if (!is_dir(thumb_dir()) && !@mkdir(thumb_dir(), 0775, true) && !is_dir(thumb_dir())) return '';
        $id = bin2hex(random_bytes(6));
        $out = thumb_file($id);
        $vf = escapeshellarg('scale=' . THUMB_SIDE . ':' . THUMB_SIDE . ':force_original_aspect_ratio=decrease');
        if (($f['kind'] ?? '') === 'video') {
            // кадр на секунде; у коротких роликов первой секунды может не быть — тогда с начала
            $ok = ffmpeg_run('-ss 1 -i ' . escapeshellarg($src) . " -frames:v 1 -vf $vf -q:v 4 -f image2", $out, 60)
                || ffmpeg_run('-i ' . escapeshellarg($src) . " -frames:v 1 -vf $vf -q:v 4 -f image2", $out, 60);
            return $ok ? $id : '';
        }
        if (!function_exists('imagecreatefromstring')) return '';
        $jpeg = image_shrink((string)file_get_contents($src), THUMB_SIDE, 80, true);
        return $jpeg !== '' && file_put_contents($out, $jpeg) !== false ? $id : '';
    } catch (Throwable $e) {
        error_log('posta: миниатюра поста: ' . $e->getMessage());
        return '';
    }
}

/** Миниатюра записана в очереди или журнале этого пользователя (чужую не отдаём). */
function thumb_owned($id, $uid) {
    if (!preg_match('/^[a-f0-9]{12}$/', (string)$id)) return false;
    $like = '%"thumb_id":"' . $id . '"%';
    return (bool)db_value('SELECT 1 FROM outbox WHERE user_id = ? AND data LIKE ?', [$uid, $like]) || (bool)db_value('SELECT 1 FROM published WHERE user_id = ? AND data LIKE ?', [$uid, $like]);
}

/** Миниатюры старше THUMB_KEEP_DAYS, на которые уже никто не ссылается (очередь и журнал всех пользователей). Возвращает число удалённых. */
function thumb_gc() {
    $removed = 0;
    foreach (is_dir(thumb_dir()) ? glob(thumb_dir() . '/*.jpg') : [] as $file) {
        if (filemtime($file) > time() - THUMB_KEEP_DAYS * DAY_SECONDS) continue;
        $like = '%"thumb_id":"' . basename($file, '.jpg') . '"%';
        if (db_value('SELECT 1 FROM outbox WHERE data LIKE ?', [$like]) || db_value('SELECT 1 FROM published WHERE data LIKE ?', [$like])) continue;
        @unlink($file);
        $removed++;
    }
    return $removed;
}

/** Ремонт очереди (CLI outbox-repair). Файл поста, которого нет в папке поста, берётся из кэша сайта или скачивается по ссылке;
 *  заглушка в подписи очищается; «Файлы поста не найдены» с ещё не прошедшим временем — снова в очередь.
 *  Без $apply ничего не меняет: только показывает, что будет сделано. Возвращает строки отчёта. */
function outbox_repair($apply = false, $only_uid = null): array {
    $report = [];
    $users = $only_uid !== null ? [$only_uid] : array_column(db_all('SELECT DISTINCT user_id FROM outbox'), 'user_id');
    foreach ($users as $uid) {
        $changed = [];
        foreach (outbox_load($uid) as $post) {
            $st = (string)($post['status'] ?? '');
            if (!in_array($st, ['queued', 'failed'], true)) continue;
            $id = (string)($post['id'] ?? '');
            $when = date('H:i d.m', (int)($post['publish_at'] ?? 0));
            $dir = outbox_media_dir($id);
            $lines = [];
            $ok = true;
            foreach ($post['media'] ?? [] as $k => $f) {
                $name = basename((string)($f['path'] ?? ''));
                if ($name === '' || is_file("$dir/$name")) continue;
                $src = (string)($f['src'] ?? '');
                $cached = media_cache_path($src);
                if ($cached !== null && is_file($cached)) {
                    $lines[] = "файл " . ($k + 1) . ': найден в кэше' . ($apply ? '' : ' (будет скопирован)');
                    if ($apply) { @mkdir($dir, 0775, true); if (!copy($cached, "$dir/$name")) { $ok = false; $lines[] = "файл " . ($k + 1) . ': не удалось скопировать'; } }
                } elseif (preg_match('~^https?://~i', $src)) {
                    $lines[] = "файл " . ($k + 1) . ': нет в кэше, ' . ($apply ? 'скачиваю по ссылке' : 'будет скачан по ссылке');
                    if ($apply) {
                        try { @mkdir($dir, 0775, true); fetch_public_media($src, "$dir/$name", TELEGRAM_UPLOAD_MAX_BYTES); }
                        catch (RuntimeException $e) { $ok = false; $lines[] = "файл " . ($k + 1) . ': ' . $e->getMessage(); }
                    }
                } else {
                    $ok = false;
                    $lines[] = "файл " . ($k + 1) . ': не найден ни в кэше, ни по ссылке';
                }
            }
            $new = $post;
            $clean = post_caption_clean($post['text'] ?? '');
            if ($clean !== (string)($post['text'] ?? '')) { $new['text'] = $clean; $lines[] = 'заглушка в подписи очищена'; }
            if ($st === 'failed' && $ok && str_contains(mb_strtolower((string)($post['error'] ?? '')), 'файлы поста не найдены')) {
                if ((int)($post['publish_at'] ?? 0) > time()) {
                    $new = array_merge($new, ['status' => 'queued', 'error' => '', 'attempts' => 0, 'next_try_at' => 0]);
                    $lines[] = 'снова в очередь' . ($apply ? '' : ' (будет)');
                } else {
                    $lines[] = 'время уже прошло — остаётся «не вышел»';
                }
            } elseif ($st === 'failed' && !$ok) {
                $lines[] = 'файл не восстановить — останется «не вышел»';
            } elseif ($st === 'queued' && !$ok) {
                $lines[] = 'файл не восстановить — пост не выйдет';
            }
            if (!$lines) continue;
            $report[] = substr($uid, 0, 8) . " · {$id} · {$when} · {$st}" . ($apply ? '' : ' · (dry-run)');
            foreach ($lines as $line) $report[] = "    $line";
            $changed[$id] = $new;
        }
        if ($apply && $changed) outbox_update(fn($q) => array_map(fn($e) => $changed[$e['id'] ?? ''] ?? $e, $q), $uid);
    }
    return $report ?: ['Проблем с очередью не найдено.'];
}

/** Настоящая отправка поста своим каналом (подменяется в тестах). */
function outbox_send_post(array $post, array $channel): array {
    $platform = (string)($channel['platform'] ?? 'telegram');
    $network = network_for($platform);
    if ($platform === 'telegram') return $network['publish']($channel, $post);
    $channel = network_refresh_channel($channel);   // токен, который скоро истечёт, обновляем до отправки
    // Файлы, которые соцсеть не примет (вид, размер, количество), пропускаем; пост без файлов и текста — ошибка
    [$post['media'], $skipped] = network_fit_media($network, $post['media'] ?? []);
    if (!$post['media'] && trim((string)($post['text'] ?? '')) === '' && empty($post['poll'])) throw new NetworkRejectedError($network['title'] . ': в посте нет подходящих файлов и текста.');
    // media_kept — файлы, которые реально ушли в соцсеть: по ним журнал и миниатюра, а не по исходному списку
    return $network['publish']($channel, $post) + ['skipped' => $skipped, 'media_kept' => $post['media']];
}

/**
 * Отправляет созревшие посты. $send(array $post, array $channel): array — ['url' => …] или исключение (подменяется в тестах).
 * $deadline — не брать новые посты после этого времени. Возвращает ['sent' => N, 'failed' => M].
 */
function outbox_process_due($now, callable $send, $deadline = null): array {
    $result = ['sent' => 0, 'failed' => outbox_fail_stuck($now)];
    foreach (outbox_due($now) as $due) {
        if ($deadline !== null && time() > $deadline) break;
        $claim = outbox_claim($due['id'], $now);
        if ($claim === null) continue;
        [$uid, $post] = [$claim['user_id'], $claim['post']];
        $channel = channel_for_post($uid, $post['channel'] ?? null);
        if ($channel === null) {
            $result['failed'] += outbox_retry_or_fail($post['id'], $uid, $post, 'Канал отключён', $now, true) ? 1 : 0;
            continue;
        }
        try {
            if (!outbox_media_ready($post)) throw new RuntimeException('Файлы поста не найдены.');
            $sent = $send($post, $channel);
            outbox_finish_sent($post['id'], $uid, $post, $channel, $sent['url'] ?? '', $sent);
            $result['sent']++;
        } catch (Throwable $e) {
            // Отказ соцсети (NetworkRejectedError) повтор не исправит — сразу неудача
            $result['failed'] += outbox_retry_or_fail($post['id'], $uid, $post, $e->getMessage(), $now, $e instanceof NetworkRejectedError) ? 1 : 0;
        }
    }
    return $result;
}

/** Сообщение в личку бота входа, если пользователь входил через Telegram. Ошибка уведомления на пост не влияет. */
function outbox_notify_failed($uid, array $post) {
    try {
        $telegram_id = (int)db_value('SELECT telegram_id FROM users WHERE id = ?', [$uid]);
        $bot = telegram_login_bot();
        if ($telegram_id <= 0 || $bot === null) return;
        $channel = channel_get($post['channel'] ?? '', $uid);
        $tz = $channel ? channel_schedule($channel)['tz'] : date_default_timezone_get();
        $when = (new DateTimeImmutable('@' . (int)($post['publish_at'] ?? 0)))->setTimezone(new DateTimeZone($tz))->format('d.m.Y H:i');
        telegram_bot_request($bot['token'], 'sendMessage', ['chat_id' => $telegram_id, 'disable_web_page_preview' => 'true',
            'text' => "Пост в «{$post['channel_title']}» на {$when} не вышел: {$post['error']}. Повторить или удалить — в «Личном кабинете» → «Соцсети» → «Очередь»."]);
    } catch (Throwable $e) {
        error_log('Posta: уведомление о неудачной публикации не отправлено: ' . $e->getMessage());
    }
}
