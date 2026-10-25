<?php
// Пул и история оценок, кэш медиа (media_cache/), файлы галерей, видео через ffmpeg, живость тредов в пуле.

/** Запись однозначно определяется адресом своей картинки (у видео — превью). */
function content_image_key($item) {
    if (!is_array($item)) return '';
    $key = $item['image'] ?? (is_array($item['images'] ?? null) ? ($item['images'][0] ?? '') : '');
    return is_string($key) ? $key : '';
}

/** Ключ записи для оценок: адрес картинки, а у поста без картинки (статья, текст) — ключ поста или текста. */
function content_item_key($item) {
    if (($key = content_image_key($item)) !== '' || !is_array($item)) return $key;
    return candidate_post_key($item) ?: candidate_text_key($item);
}

function pool_find_item(array $pool, $image) {
    foreach ($pool as $item) if (content_item_key($item) === $image) return $item;
    return null;
}

function pool_without_image(array $pool, $image) { return array_values(array_filter($pool, fn($i) => content_item_key($i) !== $image)); }

/** Убирает из пула записи, у которых пропал кэш видео: ссылки Telegram с истёкшим токеном не играют, карточка мигала бы. */
function pool_without_lost_video(array $pool) {
    $lost = fn($i) => (bool)array_filter([$i, ...(array)($i['gallery'] ?? [])],
        fn($f) => is_array($f) && !empty($f['video']) && ($l = (string)($f['video_local'] ?? '')) !== '' && media_cache_existing($l) === '');
    $bad = array_map('content_image_key', array_filter($pool, $lost));
    // Ролик TikTok и Shorts, который так и не скачался, — из показа (в пуле остаётся, на случай повтора)
    $hidden = fn($i) => in_array($i['video_via'] ?? '', ['ytdlp'], true) && !empty($i['video_unavailable']);
    if ($bad) pool_update(fn($all) => array_values(array_filter($all, fn($i) => !in_array(content_image_key($i), $bad, true))));
    return array_values(array_filter($pool, fn($i) => !in_array(content_image_key($i), $bad, true) && !$hidden($i)));
}

/**
 * Порядок показа пула: по свежести поста (нет даты — время попадания в пул), а подряд не больше одного поста одного источника,
 * если в окне POOL_DIVERSITY_WINDOW есть другой. Темы не смешиваются: порядок тем — как в пуле, внутри темы — свой.
 * Отложенные («✋») идут после остальных, раньше отложенный — раньше: иначе свежесть вернула бы их на первое место.
 * card_score сюда не относится: он решает, что держать в пуле, а не порядок показа.
 */
function pool_order(array $pool): array {
    $groups = [];
    foreach (array_values($pool) as $i => $item) $groups[(string)($item['topic'] ?? '')][] = $item + ['_seq' => $i];
    $out = [];
    foreach ($groups as $group) {
        usort($group, fn($a, $b) => (($a['postponed_at'] ?? 0) <=> ($b['postponed_at'] ?? 0)) ?: (($b['posted_at'] ?? $b['pooled_at'] ?? 0) <=> ($a['posted_at'] ?? $a['pooled_at'] ?? 0)) ?: $a['_seq'] <=> $b['_seq']);
        $last = null;
        while ($group) {
            $pick = 0;
            if ($last !== null && content_source_key($group[0]) === $last) {
                foreach (array_slice($group, 1, POOL_DIVERSITY_WINDOW) as $k => $cand) {
                    if (content_source_key($cand) !== $last && !empty($cand['postponed_at']) === !empty($group[0]['postponed_at'])) { $pick = $k + 1; break; }
                }
            }
            $item = array_splice($group, $pick, 1)[0];
            $last = content_source_key($item);
            unset($item['_seq']);
            $out[] = $item;
        }
    }
    return $out;
}

function content_source_key($item) {
    return (string)($item['source_key'] ?? '') ?: (!empty($item['board']) ? 'dvach:' . $item['board'] : 'source:' . mb_strtolower((string)($item['source'] ?? '')));
}

/** Все файлы поста [['image', 'video', image_local?, video_local?]]; пусто, если файл один. */
function content_gallery(array $item) {
    $gallery = [];
    foreach ((array)($item['gallery'] ?? []) as $f) {
        if (!is_array($f) || !is_string($f['image'] ?? null) || $f['image'] === '') continue;
        $gallery[] = ['image' => $f['image'], 'video' => is_string($f['video'] ?? null) ? $f['video'] : '']
            + array_filter(array_intersect_key($f, ['image_local' => 1, 'video_local' => 1]), fn($v) => is_string($v) && $v !== '');
    }
    return count($gallery) > 1 ? $gallery : [];
}

function content_history_record(array $item, $complaint_reason) {
    return ['image' => content_image_key($item), 'text' => (string)($item['text'] ?? ''), 'description' => $item['description'] ?? '',
        'why_relevant' => $item['why_relevant'] ?? '', 'relevance' => $item['relevance'] ?? null, 'rating_assigned' => $item['rating'] ?? null,
        'format' => $item['format'] ?? '', 'tags' => content_tags_clean($item['tags'] ?? []), 'entity' => (string)($item['entity'] ?? ''),
        'source_hashtags' => (array)($item['source_hashtags'] ?? []), 'topic' => $item['topic'] ?? '', 'source' => $item['source'] ?? '', 'source_key' => content_source_key($item), 'board' => $item['board'] ?? '',
        'thread_id' => $item['thread_id'] ?? '', 'post_id' => $item['post_id'] ?? '', 'source_url' => source_post_url($item), 'posted_at' => $item['posted_at'] ?? 0,
        'engagement' => $item['engagement'] ?? null, 'complaint_reason' => $complaint_reason, 'video' => (string)($item['video'] ?? ''),
        'image_local' => (string)($item['image_local'] ?? ''), 'video_local' => (string)($item['video_local'] ?? ''), 'phash' => (string)($item['phash'] ?? ''), 'gallery' => content_gallery($item), 'timestamp' => time()];
}

/** Ссылка на исходный пост: сохранённая (только известные площадки по HTTPS) или собранная из полей Двача. */
function source_post_url($item) {
    if (is_trusted_source_url($url = (string)($item['source_url'] ?? '')) || rss_post_url_trusted($item)) return $url;
    $board = (string)($item['board'] ?? '');
    if ($board === '' && preg_match('~Двач\s+/([a-z0-9_-]+)/~iu', (string)($item['source'] ?? ''), $m)) $board = $m[1];
    [$thread, $post] = [(string)($item['thread_id'] ?? ''), (string)($item['post_id'] ?? '')];
    return is_valid_dvach_board($board) && is_valid_dvach_id($thread) && is_valid_dvach_id($post) ? dvach_post_url($board, $thread, $post) : '';
}

function post_item_has_video(array $item) {
    return trim((string)($item['video'] ?? '')) !== '' || array_filter(content_gallery($item), fn($f) => trim($f['video']) !== '');
}

/** Есть картинка или видео: главная картинка, список картинок, галерея, видео или ролик, который ещё качается (у него есть обложка). */
function post_item_has_media(array $item): bool {
    return trim((string)($item['image'] ?? '')) !== '' || array_filter((array)($item['images'] ?? []), fn($i) => is_string($i) && trim($i) !== '')
        || trim((string)($item['video'] ?? '')) !== '' || (string)($item['video_via'] ?? '') !== '' || content_gallery($item) !== [];
}

/** Есть настоящий текст: хэштеги, ссылки, @упоминания, эмодзи и знаки не считаются; меньше двух букв — текста нет. Проверяются text и подпись. */
function post_item_has_text(array $item): bool {
    foreach ([$item['text'] ?? '', $item['captions'][0] ?? ''] as $raw) {
        $text = (string)$raw;
        if ($text === '' || $text === EMPTY_POST_TEXT) continue;
        $text = preg_replace(['~(?:https?://|t\.me/)\S+~iu', '~#[\p{L}\p{N}_]+~u', '~@[\p{L}\p{N}_]+~u'], ' ', $text);
        if (preg_match_all('~\p{L}~u', $text) >= 2) return true;
    }
    return false;
}

/** Фильтры подборки «Только с медиа» и «Только медиа»: нужна картинка или видео; «Только медиа» — ещё и без текста. Только проверка, запись не меняется. */
function content_passes_media_filters(array $item, array $filters): bool {
    if (!empty($filters['media_only']) || !empty($filters['media_no_text'])) {
        if (!post_item_has_media($item)) return false;
    }
    return empty($filters['media_no_text']) || !post_item_has_text($item);
}

/** Запись для карточки: display_* — копия из кэша или исходная ссылка, preload — картинки следующих записей. */
function content_for_display(array $item, array $upcoming = [], $remote_check = true) {
    $main = content_image_key($item);
    $display = fn($f) => $f + ['display_image' => media_cache_existing($f['image_local'] ?? '') ?: $f['image'],
        'display_video' => $f['video'] === '' ? '' : (media_cache_existing($f['video_local'] ?? '') ?: $f['video'])];
    $own = ['image' => $main, 'video' => (string)($item['video'] ?? ''), 'image_local' => $item['image_local'] ?? '', 'video_local' => $item['video_local'] ?? ''];
    $gallery = array_map(fn($f) => $display($f['image'] === $main ? array_merge($f, array_intersect_key($own, ['image_local' => 1, 'video_local' => 1])) : $f), content_gallery($item));
    if ($gallery && !in_array($main, array_column($gallery, 'image'), true)) array_unshift($gallery, $display($own));
    $shown = $display($own);
    return ['item_key' => content_item_key($item), 'images' => [$main], 'captions' => [$item['captions'][0] ?? ($item['text'] ?? '')], 'source_url' => source_post_url($item), 'found_by' => (string)($item['found_by'] ?? ''), 'gallery' => $gallery,
        'post_check' => !empty($item['video']) || $gallery ? telegram_preflight_item($item, $remote_check) : ['status' => 'ok', 'problems' => [], 'warnings' => [], 'bad' => []],
        'display_image' => $shown['display_image'], 'display_video' => $shown['display_video'], 'tags_info' => content_tags_public($item['tags'] ?? []), 'hashtags' => post_hashtags($item),
        'preload' => array_values(array_filter(array_map(fn($i) => media_cache_existing($i['image_local'] ?? '') ?: content_image_key($i), $upcoming)))] + $item;
}

// ---- Кэш медиа: имя файла — хэш исходного адреса ----

/**
 * Перенос медиа из старой папки в MEDIA_CACHE_DIR и смена префикса ссылок в пуле и истории (JSON хранит «/» как «\/»).
 * Без $apply только считает, сколько файлов и строк затронет.
 */
function media_migrate($from_dir, $apply = false) {
    $names = is_dir($from_dir) ? array_values(array_filter(scandir($from_dir), fn($n) => is_file("$from_dir/$n"))) : [];
    $rows = (int)db_value('SELECT COUNT(*) FROM pool_items WHERE data LIKE ?', ['%media_cache%'])
        + (int)db_value('SELECT COUNT(*) FROM history_items WHERE data LIKE ?', ['%media_cache%']);
    $report = ['files' => count($names), 'rows' => $rows, 'from' => $from_dir, 'to' => MEDIA_CACHE_DIR, 'applied' => false, 'moved' => 0];
    if (!$apply || realpath($from_dir) === realpath(MEDIA_CACHE_DIR)) return $report;
    if (!is_dir(MEDIA_CACHE_DIR) && !@mkdir(MEDIA_CACHE_DIR, 0775, true)) throw new RuntimeException('Не удалось создать папку медиа: ' . MEDIA_CACHE_DIR);
    foreach ($names as $n) {
        $src = "$from_dir/$n";
        $dst = MEDIA_CACHE_DIR . "/$n";
        if (is_file($dst) && filesize($dst) === filesize($src)) { @unlink($src); $report['moved']++; continue; }
        if (@rename($src, $dst) || (@copy($src, $dst) && filesize($dst) === filesize($src) && @unlink($src))) $report['moved']++;
    }
    $to = MEDIA_CACHE_URL;
    db_tx(function () use ($to) {
        foreach (['pool_items', 'history_items'] as $table) {
            db_exec("UPDATE $table SET data = replace(replace(data, ?, ?), ?, ?) WHERE data LIKE ?",
                ['media_cache\\/', "$to\\/", 'media_cache/', "$to/", '%media_cache%']);
        }
    });
    return array_replace($report, ['applied' => true]);
}

function media_cache_write($name, $bytes) {
    if (!is_dir(MEDIA_CACHE_DIR) && !@mkdir(MEDIA_CACHE_DIR, 0755, true) && !is_dir(MEDIA_CACHE_DIR)) throw new RuntimeException('Не удалось создать каталог кэша медиа.');
    $tmp = tempnam(MEDIA_CACHE_DIR, '.tmp-');
    if ($tmp === false || file_put_contents($tmp, $bytes) !== strlen($bytes) || !@chmod($tmp, 0644) || !rename($tmp, MEDIA_CACHE_DIR . "/$name")) {
        if ($tmp) @unlink($tmp);
        throw new RuntimeException('Не удалось сохранить файл в кэш медиа.');
    }
    return MEDIA_CACHE_URL . "/$name";
}

function media_cache_name($url, $ext) { return substr(hash('sha256', (string)$url), 0, 32) . ".$ext"; }

function media_cache_store_image($url, $bytes) {
    $info = is_string($bytes) && $bytes !== '' ? @getimagesizefromstring($bytes) : false;
    $ext = ['image/jpeg' => 'jpg', 'image/png' => 'png', 'image/gif' => 'gif', 'image/webp' => 'webp', 'image/bmp' => 'bmp'][$info['mime'] ?? ''] ?? null;
    try { return $ext ? media_cache_write(media_cache_name($url, $ext), $bytes) : ''; } catch (Throwable $e) { error_log('Media cache: ' . $e->getMessage()); return ''; }
}

/** Видео в кэш: из уже скачанного файла или скачать заново. Перекодированное — всегда .mp4. */
function media_cache_store_video($url, $file = null, $force_mp4 = false, array $headers = []) {
    $ext = strtolower(pathinfo((string)parse_url($url, PHP_URL_PATH), PATHINFO_EXTENSION));
    if ($force_mp4 || !in_array($ext, ['mp4', 'webm'], true)) $ext = 'mp4';
    $tmp = $file === null || !is_file($file) ? download_video_temp($url, VIDEO_MAX_BYTES, $headers) : null;
    if ($tmp === null && ($file === null || !is_file($file))) return '';
    try {
        $bytes = file_get_contents($tmp ?? $file);
        return $bytes ? media_cache_write(media_cache_name($url, $ext), $bytes) : '';
    } catch (Throwable $e) { error_log('Media cache: ' . $e->getMessage()); return ''; } finally { if ($tmp) @unlink($tmp); }
}

/** Скачанный ролик → браузерный вариант → кэш. Возвращает адрес для браузера или ''. */
function cache_downloaded_video($url, $downloaded) {
    $playable = browser_playable_video($downloaded);
    $local = media_cache_store_video($url, $playable, $playable !== $downloaded);
    if ($playable !== $downloaded) @unlink($playable);
    return $local;
}

function media_cache_path($url) {
    $prefix = MEDIA_CACHE_URL . '/';
    return is_string($url) && str_starts_with($url, $prefix) && preg_match('/^[a-f0-9]{32}\.[a-z0-9]{2,5}$/', $name = substr($url, strlen($prefix))) ? MEDIA_CACHE_DIR . "/$name" : null;
}

function media_cache_existing($url) { return ($p = media_cache_path($url)) !== null && is_file($p) ? $url : ''; }

function media_cache_item_urls($item) {
    $urls = [];
    foreach ([$item, ...(array)($item['gallery'] ?? [])] as $e) foreach (['image_local', 'video_local'] as $f) if (is_array($e) && ($u = (string)($e[$f] ?? '')) !== '') $urls[] = $u;
    return $urls;
}

/** Удаляет файл, если он не показывается ни одному пользователю. */
function media_cache_remove($url) {
    foreach (all_users_pool_items() as $item) if (in_array($url, media_cache_item_urls($item), true)) return;
    if (($p = media_cache_path($url)) !== null) @unlink($p);
}

/** Удаляет файлы, на которые не ссылается ни одна запись; свежие щадит, пока не $include_fresh. */
function media_cache_cleanup(array $pool, $include_fresh = false, $force = false) {
    $keep = [];
    foreach ($pool as $item) foreach (media_cache_item_urls($item) as $u) if ($p = media_cache_path($u)) $keep[basename($p)] = true;
    $keep += video_preview_keep_names();   // большие видео, скачанные по кнопке, — сутки, даже без записи пула
    $all_files = [];
    foreach (is_dir(MEDIA_CACHE_DIR) ? scandir(MEDIA_CACHE_DIR) : [] as $name) {
        $path = MEDIA_CACHE_DIR . "/$name";
        if (!is_file($path)) continue;
        $all_files[$name] = $path;
    }
    $to_remove = [];
    foreach ($all_files as $name => $path) {
        if (isset($keep[$name]) || isset($GLOBALS['media_cache_protect'][$name]) || (!$include_fresh && time() - (int)@filemtime($path) < MEDIA_CACHE_GRACE_SECONDS)) continue;
        $to_remove[$name] = $path;
    }

    // Защита от массового удаления: пустая база или ошибка чтения
    if (!$force) {
        $total_files = count($all_files);
        $will_remove = count($to_remove);
        if (($total_files > 20 && empty($pool)) || ($total_files > 0 && $will_remove / $total_files > MEDIA_CACHE_CLEANUP_MAX_SHARE)) {
            cli_echo("Кэш медиа: очистка остановлена — удалилось бы $will_remove из $total_files файлов. Проверьте базу; удалить принудительно: tools/cli.php media-cleanup --force");
            error_log("Media cache cleanup blocked: $will_remove/$total_files files would be removed");
            return 0;
        }
    }

    $removed = 0;
    foreach ($to_remove as $path) {
        $removed += (int)@unlink($path);
    }
    return $removed;
}

/** Файлы записей, которые были в $before и пропали из $after. */
function media_cache_release_removed(array $before, array $after) {
    $kept = array_flip(array_merge([], ...array_map('media_cache_item_urls', array_values($after))));
    foreach ($before as $item) foreach (media_cache_item_urls($item) as $u) if (!isset($kept[$u])) media_cache_remove($u);
}

/**
 * Ремонт ссылок на кэш в одной записи (и в её gallery): старый путь media_cache/<имя> → media/<имя>, если файл есть;
 * ссылка на пропавший файл — очищается. $redownload (запись «Предложки» с источником) — снять media_cached, чтобы
 * proposal_backfill_media скачала её заново. Возвращает исправленную запись или null — менять нечего.
 * $stats: fixed — путей исправлено, cleared — пропавших ссылок очищено, marked — помечено на загрузку.
 */
function media_repair_record(array $r, $redownload, array &$stats) {
    $fix = function (array $e) use (&$stats) {
        foreach (['image_local', 'video_local'] as $f) {
            if (($url = (string)($e[$f] ?? '')) === '') continue;
            if (preg_match('~^media_cache/([a-f0-9]{32}\.[a-z0-9]{2,5})$~', $url, $m) && is_file(MEDIA_CACHE_DIR . "/$m[1]")) { $e[$f] = MEDIA_CACHE_URL . "/$m[1]"; $stats['fixed']++; }
            elseif (media_cache_existing($url) === '') { $e[$f] = ''; $stats['cleared']++; }
        }
        return $e;
    };
    $cleared_before = $stats['cleared'];
    $new = $fix($r);
    if (is_array($r['gallery'] ?? null)) $new['gallery'] = array_map(fn($g) => is_array($g) ? $fix($g) : $g, $r['gallery']);
    $has_source = preg_match('~^https://t\.me/~', (string)($r['source_url'] ?? '')) || (!empty($r['board']) && !empty($r['thread_id']) && !empty($r['post_id']));
    if ($redownload && $stats['cleared'] > $cleared_before && $has_source && empty($r['media_gone'])) {   // только если файл действительно пропал
        unset($new['media_cached'], $new['media_checked']);
        $stats['marked']++;
    }
    return $new !== $r ? $new : null;
}

/** media_repair_record для пула и всей истории пользователя: «Предложка» — с повторной загрузкой, остальное — только очистка. */
function media_repair_user($uid, $dry_run = false) {
    $stats = ['fixed' => 0, 'cleared' => 0, 'marked' => 0];
    $repair = function (array $list, $redownload, &$changed) use (&$stats) {
        foreach ($list as $i => $r) if (is_array($r) && ($new = media_repair_record($r, $redownload, $stats)) !== null) { $list[$i] = $new; $changed = true; }
        return $list;
    };
    pool_update(function ($pool) use ($repair, $dry_run) {
        $changed = false;
        $pool = $repair($pool, false, $changed);
        return $changed && !$dry_run ? $pool : null;
    }, $uid);
    history_update(function ($h) use ($repair, $dry_run) {
        $changed = false;
        foreach (HISTORY_COLLECTIONS as $c) $h[$c] = $repair($h[$c] ?? [], in_array($c, PROPOSAL_COLLECTIONS, true), $changed);
        return $changed && !$dry_run ? $h : null;
    }, $uid);
    return $stats;
}

/** Запись просрочена, если добавлена раньше POOL_MAX_AGE_SECONDS назад. Записи без метки считаются свежими (метка ставится при первой проверке). */
function pool_item_is_fresh($item, $now = null) {
    return !isset($item['pooled_at']) || ($now ?? time()) - $item['pooled_at'] <= POOL_MAX_AGE_SECONDS;
}

/** Старым записям без метки ставит её сейчас. Ничего не удаляет: просроченные уходят только при нехватке места (pool_add_evicting). */
function pool_stamp_legacy() {
    pool_update(function ($pool) {
        $now = time();
        $changed = false;
        foreach ($pool as &$i) if (is_array($i) && !isset($i['pooled_at'])) { $i['pooled_at'] = $now; $changed = true; }
        unset($i);
        return $changed ? $pool : null;
    });
}

/** Свежие записи пула по теме (вместе с составной «первая + вторая»). Просроченные не считаются. */
function pool_topic_count($key) {
    return count(array_filter(pool_load(), fn($i) => is_array($i) && pool_item_is_fresh($i) && topic_norm($i['topic'] ?? '') === topic_norm($key)));
}

/** Архивные (старые, но с кэшированным медиа) посты для показа когда парсер медленный. */
function pool_archive_items(array $frozen_labels = []) {
    $archive = [];
    foreach (pool_load() as $i) {
        if (!is_array($i)) continue;
        // Пропускаем свежие посты и посты замороженных источников
        if (pool_item_is_fresh($i) || isset($frozen_labels[mb_strtolower((string)($i['source'] ?? ''))])) continue;
        if (!empty($i['proposal_hidden'])) continue;
        // Проверяем что есть локальный кэш медиа (иначе пост не покажется)
        if (!empty($i['image_local']) || !empty($i['video_local'])) $archive[] = $i;
    }
    return $archive;
}

/** Пул для темы неполный: свежих записей меньше CONTENT_POOL_TARGET. Просроченные при этом остаются в пуле до нужды. */
function content_pool_needs_fill($key) {
    pool_stamp_legacy();
    return pool_topic_count($key) < CONTENT_POOL_TARGET;
}

/** Очистка данных пользователя: пустые пул и история; каталог общий и остаётся. */
function reset_project_data() {
    pool_update(fn() => []);
    history_update(fn() => empty_history());
    return media_cache_cleanup(all_users_pool_items());
}

// ---- Галерея поста: остальные файлы — уменьшенными картинками и видео в H.264 ----

const GALLERY_CACHE_MAX_FILES = 4;

function gallery_prepare_image($bytes) {
    $info = @getimagesizefromstring((string)$bytes);
    if (!$info || $info['mime'] === 'image/gif' || (max($info[0], $info[1]) <= 1600 && strlen($bytes) <= 600 * 1024)) return $bytes;
    $jpeg = image_shrink($bytes, 1600, 85, true);
    return strlen($jpeg) < strlen($bytes) ? $jpeg : $bytes;
}

function gallery_download_image($url) {
    $bytes = http_get($url, [CURLOPT_TIMEOUT => 20]);
    return $bytes && strlen($bytes) <= MAX_IMAGE_BYTES ? $bytes : null;
}

/** Кэширует файлы галереи, кроме главного (он кэшируется вместе с записью). */
function gallery_cache_files(array $gallery, $main_image = null) {
    $done = 0;
    foreach ($gallery as $i => $f) {
        $is_main = $main_image !== null ? ($f['image'] ?? '') === $main_image : $i === 0;
        if ($is_main || !empty($f['image_local']) || !empty($f['video_local']) || $done++ >= GALLERY_CACHE_MAX_FILES) continue;
        if ($bytes = gallery_download_image($f['image'])) $gallery[$i]['image_local'] = media_cache_store_image($f['image'], gallery_prepare_image($bytes));
        if (($f['video'] ?? '') !== '' && ($file = download_video_temp($f['video']))) {
            $gallery[$i]['video_local'] = cache_downloaded_video($f['video'], $file);
            @unlink($file);
        }
    }
    return $gallery;
}

/** Догружает кэш галерей у записей пула, где его ещё нет (по нескольку за запуск). */
function cache_pool_galleries($limit = 4) {
    $todo = [];
    foreach (pool_load() as $item) {
        $rest = array_slice((array)($item['gallery'] ?? []), 1);
        if (!$rest || count($todo) >= $limit || array_filter($rest, fn($f) => !empty($f['image_local']) || !empty($f['video_local']))) continue;
        $todo[content_image_key($item)] = gallery_cache_files($item['gallery'], content_image_key($item));
    }
    if (!$todo) return 0;
    pool_update(fn($pool) => array_map(fn($i) => isset($todo[$k = content_image_key($i)]) ? ['gallery' => $todo[$k]] + $i : $i, $pool));
    cli_echo('Кэш галерей: обработано записей: ' . count($todo) . '.');
    return count($todo);
}

// ---- Видео: загрузка, раскадровка, перекодирование для браузера ----

function ffmpeg_binary() {
    static $bin = null;
    return $bin ??= function_exists('shell_exec') && function_exists('exec') ? trim((string)@shell_exec('command -v ffmpeg 2>/dev/null')) : '';
}

function ffprobe($path, $args) { return trim((string)@shell_exec("ffprobe -v error $args " . escapeshellarg($path) . ' 2>/dev/null')); }

/** ffmpeg с аргументами; true — файл $out получился. */
function ffmpeg_run($args, $out, $timeout = 0) {
    exec(($timeout ? "timeout $timeout " : '') . escapeshellarg(ffmpeg_binary()) . " -v error -y $args " . escapeshellarg($out) . ' 2>/dev/null', $o, $code);
    return $code === 0 && is_file($out) && filesize($out) > 0;
}

/** Как ffmpeg_run, но сообщает долю готового: $on_progress(0–100) по out_time_us из -progress и длительности исходника. */
function ffmpeg_run_progress($args, $out, $timeout, $duration, callable $on_progress) {
    $command = ($timeout ? "timeout $timeout " : '') . escapeshellarg(ffmpeg_binary()) . " -v error -y -progress pipe:1 -nostats $args " . escapeshellarg($out);
    $proc = proc_open($command, [['file', '/dev/null', 'r'], ['pipe', 'w'], ['file', '/dev/null', 'w']], $pipes);
    if (!is_resource($proc)) return false;
    if ($duration <= 0) $on_progress(null);
    while (($line = fgets($pipes[1])) !== false) {
        if ($duration > 0 && str_starts_with($line, 'out_time_us=')) $on_progress(min(99, max(0, (int)substr($line, 12)) / 1e6 / $duration * 100));
    }
    fclose($pipes[1]);
    return proc_close($proc) === 0 && is_file($out) && filesize($out) > 0;
}

function download_video_temp($url, $max_bytes = VIDEO_MAX_BYTES, array $headers = []) {
    if (str_starts_with((string)$url, FOURCHAN_IMG . '/')) { host_throttle('i.4cdn.org', FOURCHAN_MEDIA_GAP); $headers = $headers ?: fourchan_media_headers($url); }   // 4chan: заголовки браузера и пауза, иначе 429
    if (($path = tempnam(sys_get_temp_dir(), 'ct-video-')) === false || !($handle = @fopen($path, 'wb'))) { if ($path) @unlink($path); return null; }
    $size = 0;
    $ch = curl_init($url);
    curl_setopt_array($ch, [CURLOPT_FOLLOWLOCATION => true, CURLOPT_MAXREDIRS => 3, CURLOPT_CONNECTTIMEOUT => 8, CURLOPT_TIMEOUT => 90, CURLOPT_HTTPHEADER => $headers,
        CURLOPT_PROTOCOLS => CURLPROTO_HTTP | CURLPROTO_HTTPS, CURLOPT_USERAGENT => HTTP_UA,
        CURLOPT_WRITEFUNCTION => function ($c, $chunk) use (&$size, $max_bytes, $handle) { return ($size += strlen($chunk)) > $max_bytes ? 0 : fwrite($handle, $chunk); }]);
    $ok = curl_exec($ch) !== false && (int)curl_getinfo($ch, CURLINFO_HTTP_CODE) === 200;
    curl_close($ch);
    fclose($handle);
    if ($ok && $size > 0) return $path;
    @unlink($path);
    return null;
}

/** Раскадровка: кадры в серединах равных отрезков (~раз в VIDEO_SECONDS_PER_FRAME с), уменьшенные до VIDEO_FRAME_MAX_SIDE. */
function video_storyboard($path) {
    if (ffmpeg_binary() === '' || !is_file($path)) return null;
    $duration = max(0.0, (float)ffprobe($path, '-show_entries format=duration -of csv=p=0'));
    $span = $duration > 0 ? $duration : VIDEO_FRAMES_MIN * VIDEO_SECONDS_PER_FRAME;
    $count = max(VIDEO_FRAMES_MIN, min(VIDEO_FRAMES_MAX, (int)round($span / VIDEO_SECONDS_PER_FRAME)));
    $s = VIDEO_FRAME_MAX_SIDE;
    $frames = $times = [];
    for ($i = 0; $i < $count; $i++) {
        $at = round($span * ($i + 0.5) / $count, 2);
        if (($out = tempnam(sys_get_temp_dir(), 'ct-frame-')) === false) break;
        $ok = ffmpeg_run(sprintf('-ss %.2F -i ', $at) . escapeshellarg($path) . ' -frames:v 1 -vf ' . escapeshellarg("scale='if(gt(iw,ih),$s,-2)':'if(gt(iw,ih),-2,$s)'") . ' -q:v 4 -f image2', $out);
        if ($ok) { $frames[] = file_get_contents($out); $times[] = $at; }
        @unlink($out);
    }
    return $frames ? ['frames' => $frames, 'timestamps' => $times, 'duration' => $duration] : null;
}

/** Видео для модели: frames, timestamps, duration, file (скачанный ролик удаляет вызывающий). Не вышло — по превью. */
function video_analysis_input($url, $poster) {
    $poster_only = ['frames' => [$poster], 'timestamps' => [], 'duration' => 0.0, 'file' => null];
    if (ffmpeg_binary() === '') { cli_echo('-> ffmpeg не найден на сервере, кадры видео не извлекаются; анализирую превью.'); return $poster_only; }
    if (($path = download_video_temp($url)) === null) { cli_echo('-> Видео не скачалось (размер или ошибка сети), анализирую превью.'); return $poster_only; }
    if (!($board = video_storyboard($path))) { cli_echo('-> Кадры видео не извлечены, анализирую превью.'); return ['file' => $path] + $poster_only; }
    cli_echo('-> Раскадровка: ' . count($board['frames']) . ' кадров из видео длиной ' . format_video_time($board['duration']) . '.');
    return $board + ['file' => $path];
}

/** Индекс moov в начале MP4: браузер играет, не скачав файл целиком. */
function mp4_has_fast_start($path) {
    if (!($h = @fopen($path, 'rb'))) return true;
    for ($offset = 0, $size = filesize($path); $offset + 8 <= $size;) {
        fseek($h, $offset);
        if (strlen($head = fread($h, 16)) < 8) break;
        $type = substr($head, 4, 4);
        if ($type === 'moov' || $type === 'mdat') { fclose($h); return $type === 'moov'; }
        $atom = unpack('N', $head)[1];
        if ($atom === 1 && strlen($head) >= 16) $atom = unpack('J', substr($head, 8, 8))[1];
        if ($atom < 8) break;
        $offset += $atom;
    }
    fclose($h);
    return true;
}

/**
 * Ролик для браузера: кодек, который все играют (иначе H.264), не тяжелее VIDEO_LIGHT_BYTES и не выше 720p (иначе пережать),
 * MP4 с быстрым стартом (иначе переупаковать). Возвращает исходный путь или новый временный файл.
 */
function browser_playable_video($path) {
    if (ffmpeg_binary() === '' || !is_file($path)) return $path;
    $codec = strtolower(ffprobe($path, '-select_streams v:0 -show_entries stream=codec_name -of csv=p=0'));
    $unplayable = $codec !== '' && !in_array($codec, ['h264', 'vp8', 'vp9', 'av1'], true);
    $sides = array_filter(array_map('intval', explode(',', ffprobe($path, '-select_streams v:0 -show_entries stream=width,height -of csv=p=0'))));
    $heavy = filesize($path) > VIDEO_LIGHT_BYTES || ($sides && min($sides) > VIDEO_LIGHT_HEIGHT);
    $slow = (string)@file_get_contents($path, false, null, 4, 4) === 'ftyp' && !mp4_has_fast_start($path);
    if ((!$unplayable && !$heavy && !$slow) || ($out = tempnam(sys_get_temp_dir(), 'ct-h264-')) === false) return $path;
    $in = '-i ' . escapeshellarg($path);
    if ($unplayable || $heavy) {
        cli_echo('-> Видео ' . ($unplayable ? "в кодеке {$codec}: браузер его может не воспроизвести" : 'тяжёлое (' . round(filesize($path) / 1048576, 1) . ' МБ)') . ' — перекодирую в H.264 до ' . VIDEO_LIGHT_HEIGHT . 'p...');
        $h = VIDEO_LIGHT_HEIGHT;
        $ok = ffmpeg_run("$in -map 0:v:0 -map 0:a:0? -vf \"scale='if(gt(iw,ih),-2,min($h,iw))':'if(gt(iw,ih),min($h,ih),-2)'\" -c:v libx264 -preset veryfast -crf 27 -pix_fmt yuv420p -c:a aac -b:a 96k -movflags +faststart -f mp4", $out);
    } else {
        $ok = ffmpeg_run("$in -map 0 -c copy -movflags +faststart -f mp4", $out);
    }
    if ($ok && !($heavy && !$unplayable && filesize($out) >= filesize($path))) return $out;
    @unlink($out);
    if ($unplayable) cli_echo('-> Перекодировать не удалось, сохраняю как есть.');
    return $path;
}

// ---- Живость тредов Двача у записей пула ----

/** Убирает записи удалённых тредов и удалённых постов. Раз в POOL_LIVENESS_INTERVAL; null — ещё рано. */
function prune_dead_pool_items($uid = null, $dry_run = false, $force = false, $log = true) {
    if (!$force && time() - user_checked_at('threads', $uid) < POOL_LIVENESS_INTERVAL) return null;
    $pool = pool_load($uid);
    $threads = [];
    foreach ($pool as $i) if (!empty($i['board']) && !empty($i['thread_id'])) $threads["{$i['board']}/{$i['thread_id']}"] = [$i['board'], $i['thread_id']];
    $states = dvach_threads_liveness(array_slice($threads, 0, POOL_LIVENESS_MAX_THREADS, true));
    $removed = [];
    $mutator = function ($pool) use ($states, &$removed) {
        $kept = [];
        foreach ($pool as $i) dvach_item_gone($i, $states) ? $removed[] = $i : $kept[] = $i;
        return $removed ? $kept : null;
    };
    if ($dry_run) $mutator($pool);
    else {
        $after = pool_update($mutator, $uid);
        if ($removed) media_cache_release_removed($pool, $after);
        user_threads_checked($uid);
    }
    $r = ['threads' => count($states), 'gone' => count(array_filter($states, fn($s) => $s === false)), 'removed' => count($removed)];
    if ($log && $removed) cli_echo("Проверка тредов: проверено {$r['threads']}, удалённых тредов {$r['gone']}, убрано записей из пула: {$r['removed']}.");
    return $r;
}

/** [ключ треда => false (удалён) | [номер поста => true] (жив) | null (неизвестно)]. */
function dvach_threads_liveness(array $threads) {
    $states = [];
    foreach ($threads as $key => [$board, $thread]) {
        $state = dvach_thread_state($board, $thread, $posts);
        $states[$key] = $state === 'gone' ? false : ($state === 'alive' ? array_fill_keys(array_map(fn($p) => (string)($p['num'] ?? ''), $posts), true) : null);
        usleep(300000);
    }
    return $states;
}

function dvach_item_gone(array $item, array $states) {
    $s = $states[($item['board'] ?? '') . '/' . ($item['thread_id'] ?? '')] ?? null;
    return $s === false || (is_array($s) && !empty($item['post_id']) && !isset($s[(string)$item['post_id']]));
}

// ---- Повторы: отпечаток картинки (dHash) ----

/** Отпечаток картинки (dHash 256 бит, 64 hex-символа): одинаков у пережатой, уменьшенной копии. null — не картинка. */
function image_phash($bytes) {
    $img = @imagecreatefromstring($bytes);
    if (!$img) return null;
    $small = imagecreatetruecolor(17, 16);
    imagecopyresampled($small, $img, 0, 0, 0, 0, 17, 16, imagesx($img), imagesy($img));
    $hash = '';
    for ($row = 0; $row < 16; $row++) {
        for ($col = 0; $col < 16; $col++) {
            $left = imagecolorat($small, $col, $row);
            $right = imagecolorat($small, $col + 1, $row);
            $left_lum = 0.299 * (($left >> 16) & 0xFF) + 0.587 * (($left >> 8) & 0xFF) + 0.114 * ($left & 0xFF);
            $right_lum = 0.299 * (($right >> 16) & 0xFF) + 0.587 * (($right >> 8) & 0xFF) + 0.114 * ($right & 0xFF);
            $hash .= $left_lum > $right_lum ? '1' : '0';
        }
    }
    imagedestroy($img);
    imagedestroy($small);
    $hex = '';
    for ($i = 0; $i < 256; $i += 8) {
        $byte = substr($hash, $i, 8);
        $hex .= str_pad(dechex(bindec($byte)), 2, '0', STR_PAD_LEFT);
    }
    return $hex;
}

/** Сколько бит из 256 различаются у двух отпечатков. */
function phash_distance($a, $b) {
    if (strlen($a) !== 64 || strlen($b) !== 64) return 256;
    try {
        $a_bytes = unpack('J4', hex2bin($a));
        $b_bytes = unpack('J4', hex2bin($b));
        $distance = 0;
        foreach ($a_bytes as $i => $a_val) {
            $b_val = $b_bytes[$i] ?? 0;
            $distance += substr_count(decbin($a_val ^ $b_val), '1');
        }
        return $distance;
    } catch (Throwable $e) {
        return 256;
    }
}

/** Первый ключ из $known ([отпечаток => что угодно]), чей отпечаток ближе PHASH_SAME_DISTANCE, или null. */
function phash_find($hash, array $known) {
    foreach ($known as $known_hash => $data) {
        if (phash_distance($hash, (string)$known_hash) <= PHASH_SAME_DISTANCE) return $known_hash;
    }
    return null;
}

// ---- 18+ и «Жесть» ----

/** Разрешён ли пользователю контент 18+ и «Жесть» (галочка в «Подборе» или tools/cli.php adult-allow). */
function user_adult_allowed($uid = null, $reset = false) {
    static $allowed = [];
    $uid ??= app_user_id();
    if ($reset) unset($allowed[$uid]);
    return $allowed[$uid] ??= (bool)db_value('SELECT adult_allowed FROM users WHERE id = ?', [$uid]);
}

/** Тема 18+ или «Жесть»: по реестру (topic_is_nsfw) или по словам (topic_is_adult). Пустая тема — нет. Результат кэшируется на процесс. */
function topic_is_restricted($topic) {
    static $cache = [];
    if (($topic = (string)$topic) === '') return false;
    return $cache[$topic] ??= topic_is_nsfw($topic) || topic_is_adult($topic);
}

/** Запись 18+ или «Жесть». Порядок проверок важен: сначала самое надёжное. */
function content_is_adult(array $r) {
    foreach ((array)($r['tags'] ?? []) as $key) if (content_tag_catalog()[$key]['nsfw'] ?? false) return true;
    if (topic_is_restricted($r['topic'] ?? '')) return true;
    return !empty($r['board']) && in_array(DVACH_ADULT_TAG, dvach_board_tags($r['board']), true);
}

const CONTENT_LEWD_HUMOR_TAG = 'adult.lewd_humor';   // пошлый юмор при включённом 18+ допускается в любой теме

/** Не пускать в подбор: 18+ при выключенном 18+, а в теме не из 18+ и не «Жесть» — эротику, порно и жесть всегда (кроме пошлого юмора). */
function content_adult_blocked(array $r, string $topic): bool {
    if (!content_is_adult($r + ['topic' => $topic])) return false;
    if (!user_adult_allowed()) return true;
    if (topic_is_restricted($topic)) return false;
    foreach ((array)($r['tags'] ?? []) as $key) if ($key !== CONTENT_LEWD_HUMOR_TAG && (content_tag_catalog()[$key]['nsfw'] ?? false)) return true;
    return false;
}

/** Запись пользователя (пул или история), у которой этот адрес — картинка, видео или их локальная копия. null — не нашли. */
function content_record_by_media($src, $uid = null) {
    if (($src = (string)$src) === '') return null;
    foreach ([pool_load($uid), ...array_values(history_load($uid))] as $list) foreach ($list as $r) {
        if (!is_array($r)) continue;
        if (in_array($src, (array)($r['images'] ?? []), true)) return $r;   // 2-я и следующие картинки поста Telegram
        foreach ([$r, ...(array)($r['gallery'] ?? [])] as $f) {
            if (!is_array($f)) continue;
            foreach (['image', 'video', 'image_local', 'video_local'] as $field) if (($f[$field] ?? '') === $src) return $r;
        }
    }
    return null;
}

/** Отказ в теме для текущего пользователя (текст) или null. */
function topic_save_denied($topic) {
    return !user_adult_allowed() && topic_is_restricted($topic) ? 'Темы 18+ и «Жесть» недоступны.' : null;
}

/** Отказ опубликовать медиа (текст) или null. Свои файлы (upload) и ссылки не из записей пользователя не проверяем: за них отвечает сам пользователь. */
function publish_media_denied(array $items) {
    $adult_ok = user_adult_allowed();
    foreach ($items as $item) {
        if (($item['upload'] ?? '') !== '' || ($src = (string)($item['src'] ?? '')) === '') continue;
        if (!($r = content_record_by_media($src))) continue;
        if (ua_record_hidden($r)) return 'В посте есть украинский материал — публиковать нельзя.';   // UA_FILTER_PROMPT.md, B7
        if (!$adult_ok && content_is_adult($r)) return 'В посте есть материал 18+ или «Жесть» — такое через Posta публиковать нельзя.';
    }
    return null;
}

/**
 * Ключ записи по её полям (из формы публикации): тот же, что у пула и истории (content_item_key). Пусто — ключа нет.
 * $payload — поля записи: image, url, source_url, source_key, board, thread_id, post_id, text, text_post.
 */
function content_record_key($payload): string {
    return is_array($payload) ? content_item_key($payload) : '';
}

/**
 * Публикация или постановка в очередь = лайк (POSTED_IS_LIKED_PROMPT.md). Запись из пула уходит в «liked» как лайк;
 * из истории — в «liked» (из «не знаю», «не понравилось», «жалобы»), у уже лайкнутой отмечается публикация.
 * Во всех случаях запись скрыта из «Предложки» (proposal_hidden) и получает published_at. Файлы медиа не трогаем.
 * Возвращает 'liked' — отметили сейчас, 'already' — отмечена раньше, null — такой записи нет. Повтор безопасен.
 */
function content_mark_posted(string $key, $uid = null): ?string {
    if ($key === '') return null;
    return db_tx(function () use ($key, $uid) {
        $pool_item = null;
        pool_update(function ($pool) use ($key, &$pool_item) {
            $pool_item = pool_find_item($pool, $key);
            return $pool_item === null ? null : pool_without_image($pool, $key);
        }, $uid);
        if ($pool_item !== null) {
            history_append('liked', ['proposal_hidden' => true, 'published_at' => time()] + content_history_record($pool_item, null), $uid);
            return 'liked';
        }
        $history = history_load($uid);
        foreach (HISTORY_COLLECTIONS as $c) foreach ($history[$c] ?? [] as $r) {
            if (!is_array($r) || content_item_key($r) !== $key) continue;
            if ($c === 'liked') {
                if (!empty($r['published_at'])) return 'already';
                history_update(fn($h) => content_mark_in_liked($h, $key, time()), $uid);
                return 'liked';
            }
            history_append('liked', ['proposal_hidden' => true, 'published_at' => time()] + $r, $uid);
            return 'liked';
        }
        return null;
    });
}

/** Отметить публикацию у записи в «liked» (только если ещё не отмечена). null — менять нечего. */
function content_mark_in_liked(array $history, string $key, int $at): ?array {
    $changed = false;
    foreach ($history['liked'] ?? [] as $i => $r) {
        if (!is_array($r) || content_item_key($r) !== $key || !empty($r['published_at'])) continue;
        $history['liked'][$i] = ['proposal_hidden' => true, 'published_at' => $at] + $r;
        $changed = true;
    }
    return $changed ? $history : null;
}

/**
 * Пост из очереди снят до выхода (POSTED_IS_LIKED_PROMPT.md, часть 2.4): запись возвращается в «Предложку».
 * Остаётся в «liked» (оценка лайк не отзывается). Ничего не отмечено — ничего не делаем.
 */
function content_unmark_posted(string $key, $uid = null): bool {
    if ($key === '') return false;
    $done = false;
    history_update(function ($h) use ($key, &$done) {
        $changed = false;
        foreach ($h['liked'] ?? [] as $i => $r) {
            if (!is_array($r) || content_item_key($r) !== $key || empty($r['published_at'])) continue;
            unset($r['published_at']);
            $r['proposal_hidden'] = false;
            $h['liked'][$i] = $r;
            $changed = true;
        }
        $done = $changed;
        return $changed ? $h : null;
    }, $uid);
    return $done;
}
