<?php
// yt-dlp (tools/bin/yt-dlp, zipapp): списки и скачивание роликов TikTok, YouTube (Shorts и обычные ролики) и X.
// Все запуски — через ytdlp_exec: proc_open с массивом аргументов, без оболочки, с таймаутом.

const YTDLP_SCRIPT = ROOT . '/tools/bin/yt-dlp';
const YTDLP_CACHE_DIR = ROOT . '/data/ytdlp-cache';     // кэш yt-dlp и блокировка скачивания
const YTDLP_LIST_TIMEOUT = 30;
const YTDLP_DOWNLOAD_TIMEOUT = 180;
const YTDLP_PROBE_TIMEOUT = 20;
const YTDLP_CONVERT_TIMEOUT = 150;
const YTDLP_NODE = ROOT . '/tools/bin/node-quiet';   // /usr/bin/node без ложного «сбоя» в режиме --jitless
const YTDLP_WATCH_MAX_HEIGHT = 720;                   // обычный ролик YouTube (watch?v=): не выше 720p
const YTDLP_SHORTS_MAX_HEIGHT = 854;                   // шортсы и превью: 480p (владелец, YOUTUBE_PREVIEW_VIDEO_PROMPT.md: файлы в 2–2,5 раза меньше)
const YTDLP_WATCH_MAX_SECONDS = 600;                  // и не длиннее 10 минут; шортсы этими ограничениями не трогаем

/** Адрес для yt-dlp: только https и без пробелов и управляющих символов. Иначе — ошибка до запуска. */
function ytdlp_check_url($url) {
    if (!is_string($url) || !preg_match('~^https://[a-z0-9.-]+/[\x21-\x7e]*$~i', $url)) throw new RuntimeException('Некорректная ссылка для загрузки видео.');
    return $url;
}

/**
 * Запуск внешней программы: массив аргументов, stdin закрыт, вывод читается без блокировок, по таймауту — завершение.
 * $on_line(string) получает каждую готовую строку stdout по мере вывода (прогресс). Возвращает ['ok', 'code', 'stdout', 'stderr'].
 */
function ytdlp_exec(array $command, int $timeout, ?callable $on_line = null): array {
    // NODE_OPTIONS: служба Apache запрещает JIT (MemoryDenyWriteExecute), без --jitless Node падает
    $env = ['PATH' => '/usr/local/bin:/usr/bin:/bin', 'NODE_OPTIONS' => '--jitless'];
    $proc = @proc_open($command, [0 => ['pipe', 'r'], 1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, null, $env);
    if (!is_resource($proc)) return ['ok' => false, 'code' => -1, 'stdout' => '', 'stderr' => 'Не удалось запустить программу.'];
    fclose($pipes[0]);
    stream_set_blocking($pipes[1], false);
    stream_set_blocking($pipes[2], false);
    $stdout = $stderr = '';
    $deadline = microtime(true) + $timeout;
    $timed_out = false;
    $seen = 0;   // сколько байт stdout уже отдано построчно в $on_line
    do {
        $status = proc_get_status($proc);
        $stdout .= stream_get_contents($pipes[1]);
        $stderr .= stream_get_contents($pipes[2]);
        if ($on_line && ($end = strrpos($stdout, "\n")) !== false && $end + 1 > $seen) {
            foreach (explode("\n", substr($stdout, $seen, $end - $seen)) as $line) $on_line($line);
            $seen = $end + 1;
        }
        if (!$status['running']) break;
        if (microtime(true) > $deadline) { proc_terminate($proc); $timed_out = true; break; }
        usleep(50000);
    } while (true);
    $stdout .= stream_get_contents($pipes[1]);
    $stderr .= stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $code = proc_close($proc);
    if ($timed_out) return ['ok' => false, 'code' => -1, 'stdout' => $stdout, 'stderr' => 'Превышено время ожидания.'];
    return ['ok' => $code === 0, 'code' => $code, 'stdout' => $stdout, 'stderr' => $stderr];
}

function ytdlp_ensure_cache_dir() {
    if (!is_dir(YTDLP_CACHE_DIR) && !@mkdir(YTDLP_CACHE_DIR, 0775, true) && !is_dir(YTDLP_CACHE_DIR)) throw new RuntimeException('Не удалось создать каталог кэша yt-dlp.');
}

/** Запуск yt-dlp с общими аргументами проекта. Аргументы — массивом, адрес передаётся после «--» вызывающим. */
function ytdlp_run(array $args, int $timeout, bool $js = true, ?callable $on_progress = null): array {
    ytdlp_ensure_cache_dir();
    // Прогресс — строками «PROG скачано всего оценка формат» в stdout (с --print yt-dlp иначе молчит)
    $progress = $on_progress ? ['--progress', '--newline', '--progress-template', 'download:PROG %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s %(info.format_id)s'] : ['--no-progress'];
    $command = array_merge(['python3', '-I', YTDLP_SCRIPT, '--ignore-config', '--no-warnings'], $progress,
        $js ? ['--js-runtimes', 'node:' . YTDLP_NODE] : [], ['--cache-dir', YTDLP_CACHE_DIR, '--socket-timeout', '15'], $args);
    return ytdlp_exec($command, $timeout, $on_progress ? function ($line) use ($on_progress) {
        if (preg_match('/^PROG (\d+) (\S+) (\S+) (\S+)/', $line, $m)) $on_progress((int)$m[1], (int)(is_numeric($m[2]) ? $m[2] : (is_numeric($m[3]) ? $m[3] : 0)), $m[4]);
    } : null);
}

// ---- Прогресс скачивания для интерфейса: стадия и процент задачи (kv ytdlp_progress:<uid>:<key>) ----

const YTDLP_PROGRESS_SAVE_SECONDS = 0.7;   // чаще не пишем в базу
const YTDLP_PROGRESS_STAGES = ['probe' => 'Ищу ролик', 'download' => 'Скачиваю', 'merge' => 'Склеиваю', 'convert' => 'Перекодирую', 'telegram' => 'Скачиваю из Telegram'];

function ytdlp_progress_key(string $uid, string $key): string { return 'ytdlp_progress:' . substr(hash('sha256', "$uid|$key"), 0, 20); }

/** Стадия задачи и процент (null — неизвестен). Пишется не чаще раза в YTDLP_PROGRESS_SAVE_SECONDS, смена стадии — сразу. */
function ytdlp_progress_save(array $job, string $stage, ?float $percent = null): void {
    static $last = [];
    $k = ytdlp_progress_key($job['uid'], $job['key']);
    $now = microtime(true);
    if (($last[$k]['stage'] ?? '') === $stage && $now - ($last[$k]['at'] ?? 0) < YTDLP_PROGRESS_SAVE_SECONDS) return;
    $last[$k] = ['stage' => $stage, 'at' => $now];
    kv_update($k, fn() => ['stage' => $stage, 'percent' => $percent === null ? null : round(max(0, min(100, $percent)), 1), 'at' => time()], null);
}

/**
 * Прогресс для интерфейса: {stage, label, percent, place}. В очереди — place (какой по счёту, с 1); идёт — стадия и процент.
 * null — задачи нет.
 */
function ytdlp_progress_public(string $uid, string $key): ?array {
    $place = 0;
    foreach (ytdlp_queue_list() as $job) {
        if ((int)$job['next_at'] <= time()) $place++;
        if ($job['uid'] !== $uid || $job['key'] !== $key) continue;
        if (($wait = (int)$job['next_at'] - time()) > 0) return ['stage' => 'retry', 'label' => "Повтор через {$wait} с", 'percent' => null, 'place' => 0];
        $p = kv_get(ytdlp_progress_key($uid, $key), null);
        if (is_array($p) && time() - (int)($p['at'] ?? 0) < 600) {
            return ['stage' => $p['stage'], 'label' => YTDLP_PROGRESS_STAGES[$p['stage']] ?? 'Скачиваю', 'percent' => $p['percent'], 'place' => 0];
        }
        return ['stage' => 'queued', 'label' => $place > 1 ? "В очереди: {$place}-й" : 'Сейчас начнётся', 'percent' => null, 'place' => $place];
    }
    return null;
}

/**
 * Обработчик строк прогресса yt-dlp для задачи: видео и звук качаются по очереди — первый файл даёт 0–85%, второй 85–97%.
 */
function ytdlp_progress_tracker(array $job): callable {
    $formats = [];
    return function (int $done, int $total, string $format) use ($job, &$formats) {
        if (!in_array($format, $formats, true)) $formats[] = $format;
        $part = $total > 0 ? min(1, $done / $total) : 0;
        $percent = count($formats) === 1 ? 85 * $part : 85 + 12 * $part;
        ytdlp_progress_save($job, count($formats) > 1 && $part >= 1 ? 'merge' : 'download', $percent);
    };
}

/** Список записей площадки (без скачивания): одна строка JSON на запись. */
function ytdlp_list($url, $count) {
    $result = ytdlp_run(['--flat-playlist', '--playlist-end', (string)max(1, (int)$count), '-j', '--', ytdlp_check_url($url)], YTDLP_LIST_TIMEOUT);
    if (!$result['ok'] && trim($result['stdout']) === '') throw new RuntimeException('Не удалось получить список: ' . ytdlp_error_text($result));
    $items = [];
    foreach (preg_split('/\R/', $result['stdout']) as $line) {
        $item = trim($line) !== '' ? json_decode($line, true) : null;
        if (is_array($item)) $items[] = $item;
    }
    return $items;
}

/** Текст ошибки для журнала: последняя строка stderr без пути к программе. */
function ytdlp_error_text(array $result) {
    $lines = array_values(array_filter(array_map('trim', explode("\n", $result['stderr'] ?: $result['stdout']))));
    return $lines ? mb_substr(end($lines), 0, 200) : ($result['stderr'] ?: 'неизвестная ошибка');
}

/** Ролик длиннее лимита: повторять скачивание бессмысленно. */
class VideoTooLongException extends RuntimeException {}

/** Скачивание ролика в $dest_dir; возвращает путь к файлу. Не больше одного скачивания на сервер. */
function ytdlp_download($url, $format, $dest_dir, $max_bytes, ?callable $on_progress = null) {
    ytdlp_ensure_cache_dir();
    if (!is_dir($dest_dir) && !@mkdir($dest_dir, 0775, true) && !is_dir($dest_dir)) throw new RuntimeException('Не удалось создать папку для ролика.');
    if (!($lock = try_lock(YTDLP_CACHE_DIR . '/.download.lock'))) throw new RuntimeException('Сейчас скачивается другой ролик, попробуйте позже.');
    $watch = ytdlp_is_watch_url($url);
    try {
        $args = ['-f', $format, '--merge-output-format', 'mp4', '--max-filesize', (string)max(1, (int)$max_bytes), '--no-playlist',
            ...($watch ? ['--match-filter', 'duration<=' . YTDLP_WATCH_MAX_SECONDS] : []),   // длиннее — yt-dlp не скачивает и молчит (stdout пуст)
            '--no-simulate', '--print', 'after_move:filepath', '-o', "$dest_dir/%(id)s.%(ext)s", '--', ytdlp_check_url($url)];
        // YouTube: сначала без разгадывания защиты (Node без JIT решает его ~5 с, а шортсам оно не нужно), не вышло — с ним
        $youtube = str_contains($url, 'youtube.com/');
        $result = ytdlp_run($args, YTDLP_DOWNLOAD_TIMEOUT, !$youtube, $on_progress);
        if ($youtube && !$result['ok']) $result = ytdlp_run($args, YTDLP_DOWNLOAD_TIMEOUT, true, $on_progress);
    } finally {
        flock($lock, LOCK_UN);
        fclose($lock);
    }
    $paths = array_values(array_filter(array_map('trim', explode("\n", $result['stdout'])), fn($l) => $l !== '' && !str_starts_with($l, 'PROG ')));
    $file = end($paths) ?: '';
    if ($watch && $result['ok'] && $file === '') throw new VideoTooLongException('Ролик длиннее ' . intdiv(YTDLP_WATCH_MAX_SECONDS, 60) . ' минут — откройте его на YouTube');
    if (!$result['ok'] || !is_file($file) || strncmp(realpath($file), realpath($dest_dir) . '/', strlen(realpath($dest_dir)) + 1) !== 0) {
        throw new RuntimeException('Не удалось скачать ролик: ' . ytdlp_error_text($result));
    }
    return $file;
}

/** Кодек видеопотока файла (h264, hevc, …) или '' — если ffprobe не ответил. */
function ytdlp_codec($file) {
    $result = ytdlp_exec(['ffprobe', '-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=codec_name', '-of', 'csv=p=0', $file], YTDLP_PROBE_TIMEOUT);
    return $result['ok'] ? strtolower(trim($result['stdout'])) : '';
}

/** Если видео не H.264 — перекодирует во временный файл и заменяет им исходный. Возвращает путь к H.264-файлу. */
function ytdlp_h264($file) {
    if (ytdlp_codec($file) === 'h264') return $file;
    $out = $file . '.h264.mp4';
    $result = ytdlp_exec(['ffmpeg', '-v', 'error', '-y', '-i', $file, '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '23',
        '-c:a', 'aac', '-movflags', '+faststart', '-f', 'mp4', $out], YTDLP_CONVERT_TIMEOUT);
    if (!$result['ok'] || !is_file($out) || filesize($out) === 0) {
        @unlink($out);
        throw new RuntimeException('Не удалось перекодировать ролик в H.264: ' . ytdlp_error_text($result));
    }
    if (!rename($out, $file)) { @unlink($out); throw new RuntimeException('Не удалось заменить ролик перекодированным.'); }
    return $file;
}

// ---- Очередь роликов: запись пула с video_via = ytdlp получает файл фоновым воркером (tools/ytdlp_worker.php) ----

const YTDLP_QUEUE_KEY = 'ytdlp_queue';
const YTDLP_QUEUE_MAX = 500;
const YTDLP_TRIES = 3;
const YTDLP_WORKER_SECONDS = 50;     // после этого воркер не берёт новую задачу, которая уже готова; скачивание, начатое раньше, идёт до своего таймаута
const YTDLP_WORKER_LIFE = 150;       // дольше живёт один запуск воркера: успевает дождаться повтора неудачной задачи (60 с)
const YTDLP_WAIT_SECONDS = 90;       // воркер ждёт задачу, у которой повтор наступает в ближайшие столько секунд

/** Формат yt-dlp по площадке: Shorts — H.264 до 480p (854 px по высоте у вертикального ролика), TikTok — H.264 или MP4. */
function ytdlp_format_for(string $page): string {
    if (ytdlp_is_watch_url($page)) {
        $h = YTDLP_WATCH_MAX_HEIGHT;
        return "bv*[vcodec^=avc1][height<=$h]+ba[ext=m4a]/b[ext=mp4][height<=$h]/b[ext=mp4]";
    }
    return str_contains($page, 'youtube.com/') ? 'bv*[vcodec^=avc1][height<=' . YTDLP_SHORTS_MAX_HEIGHT . ']+ba[ext=m4a]/b[ext=mp4]' : 'b[vcodec^=h264]/b[vcodec^=avc1]/b[ext=mp4]/b';
}

/** Обычный ролик YouTube (не шортс): с ограничениями по высоте и длительности. */
function ytdlp_is_watch_url($url): bool {
    return (bool)preg_match('~^https://www\.youtube\.com/watch\?v=[A-Za-z0-9_-]{11}$~', (string)$url);
}

/** Способ скачивания задачи: поле via, а если пусто (старые задачи) — по адресу: t.me — через аккаунт, остальное — yt-dlp. */
function ytdlp_job_via(array $job) {
    $via = (string)($job['via'] ?? '');
    if (in_array($via, ['ytdlp', 'tgu'], true)) return $via;
    return preg_match('~^https://t\.me/~', (string)($job['page'] ?? '')) ? 'tgu' : 'ytdlp';
}

/** Ставит запись пула в очередь на ролик (одна задача на запись). $front — первым в очередь (ролик, который пользователь открыл и ждёт). */
function ytdlp_enqueue($uid, $image_key, $page, $via = 'ytdlp', bool $front = false) {
    $via = ytdlp_job_via(['via' => $via, 'page' => $page]);
    ytdlp_queue_update(function ($jobs) use ($uid, $image_key, $page, $via, $front) {
        foreach ($jobs as $i => $job) {
            if ($job['uid'] !== $uid || $job['key'] !== $image_key) continue;
            if ($front) { unset($jobs[$i]); array_unshift($jobs, $job); }
            return array_values($jobs);
        }
        $new = ['uid' => $uid, 'key' => $image_key, 'page' => $page, 'via' => $via, 'tries' => 0, 'next_at' => 0];
        if (count($jobs) < YTDLP_QUEUE_MAX) $front ? array_unshift($jobs, $new) : $jobs[] = $new;
        return $jobs;
    });
    ytdlp_kick_worker();
}

/** Изменяет очередь атомарно; $fn получает список задач и возвращает новый. */
function ytdlp_queue_update(callable $fn) {
    return kv_update(YTDLP_QUEUE_KEY, fn($jobs) => $fn(is_array($jobs) ? array_values($jobs) : []), []);
}

/** Запускает воркер в фоне, если он не работает (как публикатор). */
function ytdlp_kick_worker() {
    ytdlp_ensure_cache_dir();
    if (ytdlp_queue_list() === []) return;   // нечего делать — не плодим процессы
    if (!($lock = try_lock(YTDLP_CACHE_DIR . '/.worker.lock'))) return;
    flock($lock, LOCK_UN);
    fclose($lock);
    $php = PHP_BINARY !== '' && is_executable(PHP_BINARY) ? PHP_BINARY : '/usr/bin/php';
    proc_open([$php, ROOT . '/tools/ytdlp_worker.php'], [['file', '/dev/null', 'r'], ['file', '/dev/null', 'w'], ['file', '/dev/null', 'w']], $pipes);
}

/** Первая задача, у которой наступило время попытки; null — очередь пуста или всё ждёт. */
function ytdlp_next_job() {
    $due = array_values(array_filter(ytdlp_queue_update(fn($jobs) => $jobs), fn($job) => (int)$job['next_at'] <= time()));
    usort($due, 'ytdlp_job_order');   // превью кандидата — раньше роликов пула (YOUTUBE_PREVIEW_VIDEO_PROMPT.md, шаг 2.2)
    return $due[0] ?? null;
}

/** Порядок очереди: готовые раньше ждущих повтора, превью раньше пула, потом по времени попытки. */
function ytdlp_job_order(array $a, array $b): int {
    return ((int)$a['next_at'] > time()) <=> ((int)$b['next_at'] > time())
        ?: (video_is_preview_key($b['key']) <=> video_is_preview_key($a['key']))
        ?: ((int)$a['next_at'] <=> (int)$b['next_at']);
}

/** Задача с самым ранним временем попытки, если оно наступит в ближайшие $seconds; иначе null. */
function ytdlp_next_job_within($seconds) {
    $near = array_values(array_filter(ytdlp_queue_list(), fn($job) => (int)$job['next_at'] <= time() + $seconds));
    usort($near, 'ytdlp_job_order');
    return $near[0] ?? null;
}

/** Задачи в очереди (для `cli ytdlp-queue` и проверок). */
function ytdlp_queue_list() { $jobs = kv_get(YTDLP_QUEUE_KEY, []); return array_values(is_array($jobs) ? $jobs : []); }

/** Скачивает ролик записи, кладёт в кэш и ставит в запись пула. Ошибка — задача переносится или снимается. */
function ytdlp_run_job(array $job) {
    if (video_is_preview_key($job['key'])) video_preview_save($job['page'], ['status' => 'running', 'error' => '']);
    $times = [];   // стадии и их длительность, с.: в журнал, чтобы видеть, где время
    $stage = function ($name, callable $fn) use (&$times) {
        $t = microtime(true);
        try { return $fn(); } finally { $times[$name] = round(microtime(true) - $t, 1); }
    };
    $tries = (int)($job['tries'] ?? 0) + 1;
    try {
        $tgu = ytdlp_job_via($job) === 'tgu';
        ytdlp_progress_save($job, $tgu ? 'telegram' : 'probe');
        $file = $stage('скачивание', fn() => $tgu ? ytdlp_tgu_download($job) : ytdlp_download($job['page'], ytdlp_format_for($job['page']), YTDLP_CACHE_DIR . '/downloads', TELEGRAM_UPLOAD_MAX_BYTES, ytdlp_progress_tracker($job)));
        try {
            $codec = $stage('проверка кодека', fn() => ytdlp_codec($file));
            if ($codec !== 'h264') ytdlp_progress_save($job, 'convert', 98);
            $local = $stage($codec !== 'h264' ? 'перекодирование' : 'подготовка', fn() => cache_downloaded_video($job['page'], ytdlp_h264($file)));
        } finally {
            @unlink($file);
        }
        if ($local === '') throw new RuntimeException('не удалось сохранить ролик в кэш');
        ytdlp_attach($job, $local);
        ytdlp_drop_job($job);
        cli_echo('-> Ролик ' . ytdlp_stage_log_label($job) . " готов с попытки {$tries}: " . ytdlp_stage_log_times($times) . ".");
    } catch (Throwable $e) {
        cli_echo('-> Ролик ' . ytdlp_stage_log_label($job) . " — попытка {$tries} не удалась: " . ytdlp_stage_log_times($times) . '.');
        ytdlp_job_failed($job, $e->getMessage(), $e instanceof VideoTooLongException);
    }
}

/** Имя задачи для журнала: превью или запись пула. */
function ytdlp_stage_log_label(array $job): string {
    return (video_is_preview_key($job['key']) ? 'превью ' : 'записи пула ') . mb_substr((string)$job['page'], 0, 80);
}

/** Длительности стадий одной строкой: «скачивание 12.3 с, подготовка 0.4 с». */
function ytdlp_stage_log_times(array $times): string {
    return $times ? implode(', ', array_map(fn($k, $v) => "$k {$v} с", array_keys($times), $times)) : 'стадий не было';
}

/** Большое видео поста Telegram (в веб-версии его нет) — через личный аккаунт владельца записи: https://t.me/канал/номер. */
function ytdlp_tgu_download(array $job) {
    if (!preg_match('~^https://t\.me/([A-Za-z0-9_]{4,32})/(\d+)$~', (string)$job['page'], $m)) throw new RuntimeException('не ссылка на пост Telegram');
    return telegram_user_download_video($job['uid'], $m[1], (int)$m[2], YTDLP_CACHE_DIR . '/downloads');
}

/** Ролик готов: запись пула получает video и video_local (адрес кэша) — показ и публикация берут его. Превью кандидата — только состояние в kv. */
function ytdlp_attach(array $job, string $local) {
    if (video_is_preview_key($job['key'])) return video_preview_save($job['page'], ['status' => 'done', 'local' => $local, 'error' => '', 'at' => time()]);
    pool_update(function ($pool) use ($job, $local) {
        foreach ($pool as $i => $item) {
            if (is_array($item) && content_image_key($item) === $job['key'] && ($item['video_via'] ?? '') === ytdlp_job_via($job)) {
                $pool[$i]['video'] = $local;
                $pool[$i]['video_local'] = $local;
            }
        }
        return $pool;
    }, $job['uid']);
}

function ytdlp_drop_job(array $job) {
    db_exec('DELETE FROM kv WHERE key = ?', [ytdlp_progress_key($job['uid'], $job['key'])]);
    ytdlp_queue_update(fn($jobs) => array_values(array_filter($jobs, fn($j) => !($j['uid'] === $job['uid'] && $j['key'] === $job['key']))));
}

/** Неудача: через минуту, потом ещё; после YTDLP_TRIES — запись помечается недоступным видео. $final — сразу итог, без повторов. */
function ytdlp_job_failed(array $job, string $reason, bool $final = false) {
    $tries = $final ? YTDLP_TRIES : (int)$job['tries'] + 1;
    cli_echo("-> Ролик не скачан ({$tries} из " . YTDLP_TRIES . "): {$reason}");
    error_log("ytdlp: {$job['page']}: {$reason}");
    $preview = video_is_preview_key($job['key']);
    if ($preview && $tries >= YTDLP_TRIES) {   // превью кандидата: пул не трогаем, итог — в kv
        video_preview_save($job['page'], ['status' => 'failed', 'error' => $reason]);
        ytdlp_drop_job($job);
        return;
    }
    if ($tries < YTDLP_TRIES) {
        if ($preview) video_preview_save($job['page'], ['status' => 'queued', 'error' => $reason]);
        ytdlp_queue_update(fn($jobs) => array_map(fn($j) => $j['uid'] === $job['uid'] && $j['key'] === $job['key'] ? array_merge($j, ['tries' => $tries, 'next_at' => time() + 60 * $tries]) : $j, $jobs));
        return;
    }
    pool_update(function ($pool) use ($job) {
        foreach ($pool as $i => $item) if (is_array($item) && content_image_key($item) === $job['key']) $pool[$i]['video_unavailable'] = true;
        return $pool;
    }, $job['uid']);
    ytdlp_drop_job($job);
}

// ---- Ролик по кнопке в окне просмотра и первые ролики превью: состояние — в kv video_preview:<хэш адреса> ----

const VIDEO_PREVIEW_LIMITS = ['tgu' => 2, 'ytdlp' => 3];   // задач в очереди на одного пользователя: через аккаунт Telegram и через yt-dlp
const VIDEO_PREVIEW_KEEP_SECONDS = DAY_SECONDS;             // скачанный по кнопке файл кэш не удаляет сутки
const VIDEO_PREVIEW_FIRST = 3;                              // первые ролики превью кандидата ставятся в очередь сами

/** Способ скачивания адреса ролика: 'tgu' (Telegram, через аккаунт), 'ytdlp' (TikTok, YouTube: Shorts и watch?v=) или null — адрес не принимается. */
/** Большие видео Telegram без файла: при подключённом личном аккаунте их докачивает очередь (video_via = tgu, video_page = адрес поста). */
function telegram_big_videos_via_account(array $posts, $uid = null): array {
    if (telegram_user_connection($uid) === null) return $posts;
    return array_map(fn($p) => is_array($p) && !empty($p['video_unavailable']) && video_page_via($p['url'] ?? '') === 'tgu'
        ? ['video_unavailable' => false, 'video_via' => 'tgu', 'video_page' => $p['url']] + $p : $p, $posts);
}

function video_page_via($url) {
    $url = (string)$url;
    if (preg_match('~^https://t\.me/[A-Za-z0-9_]{4,32}/\d+$~', $url)) return 'tgu';
    if (preg_match('~^https://www\.tiktok\.com/@([^/]+)/video/\d{5,25}$~', $url, $m) && is_valid_tiktok_user($m[1])) return 'ytdlp';
    if (preg_match('~^https://www\.youtube\.com/shorts/[A-Za-z0-9_-]{11}$~', $url) || ytdlp_is_watch_url($url)) return 'ytdlp';
    return null;
}

function video_is_preview_key($key) { return str_starts_with((string)$key, 'preview:'); }

/** Ключ задачи очереди для адреса ролика. */
function video_preview_key($url) { return 'preview:' . substr(hash('sha256', (string)$url), 0, 16); }

/** Ключ состояния в kv для адреса ролика. */
function video_preview_state_key($url) { return 'video_preview:' . substr(hash('sha256', (string)$url), 0, 16); }

/** Состояние по адресу: {status: none|queued|running|done|failed, local, error, at}. Готовый файл, которого уже нет в кэше, — none. */
function video_preview_status($url) {
    $saved = kv_get(video_preview_state_key($url), null);
    $state = ['status' => 'none', 'local' => '', 'error' => '', 'at' => 0];
    if (is_array($saved)) $state = array_replace($state, array_intersect_key($saved, $state));
    if ($state['status'] === 'done' && !is_file((string)media_cache_path($state['local']))) return ['status' => 'none', 'local' => '', 'error' => '', 'at' => 0];
    return $state;
}

/** Меняет отдельные поля состояния адреса. */
function video_preview_save($url, array $fields) {
    kv_update(video_preview_state_key($url), fn($old) => array_replace(is_array($old) ? $old : [], $fields), []);
}

/** Есть ли для пользователя задача очереди по этому адресу. */
function video_preview_job_exists($url, $uid) {
    $key = video_preview_key($url);
    return (bool)array_filter(ytdlp_queue_list(), fn($j) => $j['uid'] === $uid && $j['key'] === $key);
}

/** Сколько задач этого способа стоит в очереди у пользователя. */
function video_preview_jobs_of($uid, $via) {
    return count(array_filter(ytdlp_queue_list(), fn($j) => $j['uid'] === $uid && video_is_preview_key($j['key']) && ytdlp_job_via($j) === $via));
}

/**
 * Ставит ролик адреса в очередь: одна задача на адрес, лимит на пользователя. Возвращает состояние; $limited — лимит исчерпан.
 * $front — ролик открыт и его ждут: он идёт первым, а при исчерпанном лимите вытесняет последний ещё не начатый ролик превью.
 */
function video_fetch_enqueue($url, $via, $uid, bool $front = false) {
    $state = video_preview_status($url);
    if ($state['status'] === 'done') return ['state' => $state, 'limited' => false];
    if (in_array($state['status'], ['queued', 'running'], true) && video_preview_job_exists($url, $uid)) {
        if ($front && $state['status'] === 'queued') ytdlp_enqueue($uid, video_preview_key($url), $url, $via, true);
        return ['state' => $state, 'limited' => false];
    }
    if (video_preview_jobs_of($uid, $via) >= VIDEO_PREVIEW_LIMITS[$via] && !($front && video_preview_evict_one($uid, $via))) return ['state' => $state, 'limited' => true];
    video_preview_save($url, ['status' => 'queued', 'local' => '', 'error' => '', 'at' => time()]);
    ytdlp_enqueue($uid, video_preview_key($url), $url, $via, $front);
    return ['state' => video_preview_status($url), 'limited' => false];
}

/** Снимает последний ещё не начатый ролик превью пользователя (его состояние — «нет задачи», плитка предложит загрузить). false — снимать нечего. */
function video_preview_evict_one($uid, $via): bool {
    $evicted = null;
    ytdlp_queue_update(function ($jobs) use ($uid, $via, &$evicted) {
        for ($i = count($jobs) - 1; $i >= 0; $i--) {
            $j = $jobs[$i];
            if ($j['uid'] !== $uid || !video_is_preview_key($j['key']) || ytdlp_job_via($j) !== $via || video_preview_status($j['page'])['status'] !== 'queued') continue;
            $evicted = $j;
            array_splice($jobs, $i, 1);
            break;
        }
        return $jobs;
    });
    if ($evicted === null) return false;
    video_preview_save($evicted['page'], ['status' => 'none', 'local' => '', 'error' => '', 'at' => time()]);
    return true;
}

/** Посты с роликом, который можно скачать: скачанный — сразу с адресом кэша, ждущий — с пометкой video_pending. */
function video_preview_apply(array $posts): array {
    return array_map(function ($p) {
        if (!is_array($p) || ($p['video'] ?? '') !== '' || empty($p['video_page']) || video_page_via($p['video_page']) === null) return $p;
        $state = video_preview_status($p['video_page']);
        if ($state['status'] === 'done') return ['video' => $state['local'], 'video_local' => $state['local'], 'video_pending' => false] + $p;
        return ['video_pending' => in_array($state['status'], ['queued', 'running'], true)] + $p;
    }, $posts);
}

/** Первые VIDEO_PREVIEW_FIRST роликов превью кандидата, которые ещё не скачаны, ставятся в очередь сами; готовые место в окне не занимают. Лимит исчерпан — дальше не ставим, затем — отметки. */
function video_preview_prepare(array $posts, $uid): array {
    $left = VIDEO_PREVIEW_FIRST;
    foreach ($posts as $p) {
        if ($left <= 0) break;
        if (!is_array($p) || ($p['video'] ?? '') !== '' || empty($p['video_page']) || ($via = video_page_via($p['video_page'])) === null) continue;
        if (video_preview_status($p['video_page'])['status'] === 'done') continue;   // уже скачан: очередь не нужна, окно не тратим
        $left--;
        if (video_fetch_enqueue($p['video_page'], $via, $uid)['limited']) break;
    }
    return video_preview_apply($posts);
}

/** Имена файлов кэша, скачанных по кнопке за последние сутки: очистка кэша их не трогает. */
function video_preview_keep_names() {
    $names = [];
    foreach (db_all("SELECT value FROM kv WHERE key LIKE 'video_preview:%'") as $row) {
        $saved = db_unjson($row['value'], []);
        $saved = is_array($saved) && isset($saved['_kv_value']) ? $saved['_kv_value'] : [];   // обёртка kv, как в kv_get
        if (($saved['status'] ?? '') !== 'done' || time() - (int)($saved['at'] ?? 0) >= VIDEO_PREVIEW_KEEP_SECONDS) continue;
        if (($path = media_cache_path((string)($saved['local'] ?? ''))) !== null) $names[basename($path)] = true;
    }
    return $names;
}

/** Записи пула с роликом, который ещё не скачан, ставятся в очередь (одна задача на запись). Вызывать везде, где записи попадают в пул. Обычные ролики YouTube в пул не качаем: длинный ролик скрыл бы запись из подбора; их скачивает превью по кнопке. */
function pool_items_enqueue_videos(array $items, $uid) {
    foreach ($items as $item) {
        if (!is_array($item) || !in_array($item['video_via'] ?? '', ['ytdlp', 'tgu'], true) || ($item['video'] ?? '') !== '' || empty($item['video_page'])) continue;
        if (ytdlp_is_watch_url($item['video_page'])) continue;
        ytdlp_enqueue($uid, content_image_key($item), (string)$item['video_page'], $item['video_via']);
    }
}
