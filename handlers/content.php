<?php
// Вкладки «Подбор», «Предложка», «Дерево», «Тест ИИ», базы данных и демо-режим.

/** ?image=: состояние ролика записи подборки для карточки «Видео загружается…» — без проверок публикации (они ходят в сеть). */
function handle_get_item_video() {
    $image = (string)($_GET['image'] ?? '');
    $item = array_values(array_filter(pool_load(), fn($i) => is_array($i) && content_image_key($i) === $image))[0] ?? null;
    if (!$item) json_error('Записи уже нет в подборке.', 404);
    $video = (string)($item['video'] ?? '');
    $local = (string)($item['video_local'] ?? '');
    json_response(['video' => $video, 'video_local' => $local, 'display_video' => $video === '' ? '' : (media_cache_existing($local) ?: $video),
        'video_unavailable' => !empty($item['video_unavailable']), 'progress' => $video === '' ? ytdlp_progress_public(app_user_id(), $image) : null]);
}

function handle_get_current_item() {
    // Тему 18+ выбрали раньше, до запрета: показываем пусто и не собираем, пока её не сменят
    if (topic_save_denied(current_topic()) !== null) json_response(['status' => 'empty', 'message' => 'Тема «' . current_topic() . '» недоступна: выберите другую тему.']);
    if ($pool = pool_without_lost_video(pool_for_current_topic())) json_response(translate_display(content_for_display($pool[0], array_slice($pool, 1, 3))));

    // Проверяем статус парсера
    $parser_started = start_content_parser_if_needed();
    $is_running = in_array($parser_started, ['started', 'running'], true);

    if (!$is_running) {
        json_response(['status' => 'empty', 'message' => 'Нового контента пока нет. Можно повторить поиск или добавить запись вручную.']);
    }

    // Парсер работает - проверяем как долго и показываем архивные посты если долго
    $runtime = parser_runtime_seconds();
    if ($runtime !== null && $runtime >= PARSER_SLOW_THRESHOLD_SECONDS) {
        [$topic, $key, $f] = [current_topic(), search_topic_key(), content_filters()];
        $frozen = $f['active_sources_only'] ? frozen_source_labels() : [];

        // Фильтруем архивные посты по теме
        $all_archive = pool_archive_items($frozen);
        $archive = array_values(array_filter($all_archive, fn($i) => pool_item_in_topic($i, $topic, $key)));

        if ($archive) {
            $response = translate_display(content_for_display($archive[0], array_slice($archive, 1, 3)));
            $response['archive'] = true;
            $response['fire_warning'] = "🔥 Пост скоро исчезнет";
            $response['parser_runtime'] = $runtime;
            $response['parser_status'] = 'slow';
            json_response($response);
        }
    }

    // Нормальный показ "loading"
    json_response(['status' => 'loading', 'message' => 'Ищем контент...', 'parser_runtime' => $runtime ?? 0]);
}

/** Запись относится к теме поиска или к составной «первая + вторая»; записи без темы подходят всем. */
function pool_item_matches_topic($i, $topic, $key) {
    return ($i['topic'] ?? '') === '' || topic_same((string)$i['topic'], $topic) || topic_same((string)$i['topic'], $key);
}

/** Запись можно показать: подходит теме и не 18+ или «Жесть» (для пользователя без флага). */
function pool_item_in_topic($i, $topic, $key) {
    if (is_array($i) && !user_adult_allowed() && content_is_adult($i)) return false;   // страховка: 18+ и «Жесть» без флага не показываем
    return pool_item_matches_topic($i, $topic, $key);
}

/** Позиции в пуле записей выбранной темы: по ним показываются и правятся записи в «Базах данных» и в кабинете. */
function pool_topic_positions(array $pool) {
    [$topic, $key] = [current_topic(), search_topic_key()];
    return array_keys(array_filter($pool, fn($i) => pool_item_in_topic($i, $topic, $key)));
}

/** Подписи замороженных (неактивных) Telegram и VK источников, как они стоят в записях. */
function frozen_source_labels() {
    $labels = [];
    $db = load_source_db();
    foreach (CATALOG_KEYS as $platform => $key) foreach ($db[$key] as $s) {
        if (is_source_frozen($s)) $labels[mb_strtolower(source_display_name($platform, $s['name'] ?? ($s['id'] ?? '')))] = true;
    }
    return $labels;
}

/** Пул без записей другой темы и не подходящих под фильтры подборки (остаются, если тему сменили во время записи). 18+ без флага из базы не удаляются, только не отдаются. */
function pool_for_current_topic() {
    [$topic, $key, $f] = [current_topic(), search_topic_key(), content_filters()];
    $frozen = $f['active_sources_only'] ? frozen_source_labels() : [];
    $keep = fn($i) => pool_item_matches_topic($i, $topic, $key) && pool_item_is_fresh($i)
        && (!$f['video_only'] || post_item_has_video($i)) && (!$f['fresh_only'] || !is_post_outdated($i['posted_at'] ?? 0, null, CONTENT_FRESH_MAX_AGE_DAYS))
        && !isset($frozen[mb_strtolower((string)($i['source'] ?? ''))]);
    $pool = pool_load();
    $stored = count(array_filter($pool, $keep)) === count($pool) ? $pool : pool_update(fn($p) => array_values(array_filter($p, $keep)));
    $shown = array_values(array_filter($stored, fn($i) => pool_item_in_topic($i, $topic, $key) && content_passes_media_filters($i, $f)));   // фильтры медиа прячут при выдаче, в пуле записи остаются
    return pool_order($shown);   // порядок показа: свежесть и чередование источников (FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть A)
}

function handle_parser_status() {
    $running = content_parser_is_running();
    $response = ['running' => $running, 'phase' => $running ? (parser_phase() ?: 'парсер работает') : 'ничего не делается', 'activity' => content_parser_activity()];
    if ($running) $response += ['runtime' => parser_runtime_seconds(), 'progress' => parser_progress_public()];
    json_response($response);
}

function request_tab_id($input) { return is_valid_tab_id($tab = $input['tab'] ?? '') ? $tab : json_error('Некорректный идентификатор вкладки.'); }
function handle_tab_open($in) { ok(['parser' => tab_session_open(request_tab_id($in))]); }
function handle_tab_heartbeat($in) { json_response(tab_session_heartbeat(request_tab_id($in))); }
function handle_tab_close($in) { ok(['stopped' => tab_session_close(request_tab_id($in))]); }

function handle_refresh_content() {
    in_array(start_content_parser_if_needed(true), ['started', 'running'], true)
        ? json_response(['status' => 'loading', 'message' => 'Ищем контент...']) : json_error('Не удалось запустить поиск контента на сервере.', 503);
}

// ---- Тема и фильтры ----

function handle_settings() {
    $entry = topic_ensure($topic = current_topic());
    json_response(['topic' => $topic, 'topic2' => current_topic2(), 'query' => current_query(), 'topic_info' => $entry ? topic_public($entry) : null, 'filters' => content_filters(), 'adult_allowed' => user_adult_allowed()]);
}

/** Полная запись темы из базы, все поля как есть, для панели «Дерево». */
function handle_topic_record() {
    $entry = topic_registry()[(string)($_GET['id'] ?? '')] ?? null;
    if (!$entry) json_error('Такой темы нет.', 404);
    json_response(['topic' => $entry, 'summary' => topic_public($entry, load_source_db())]);
}

/** Темы для меню, дерева и подсказок. Без флага 18+ и ветки «Для взрослых» и «Жесть» не отдаём: фильтр только на сервере. */
function topics_public_list() {
    $db = load_source_db();
    $entries = user_adult_allowed() ? topic_registry() : array_filter(topic_registry(), fn($e) => !topic_is_restricted($e['title']));
    $topics = array_map(fn($e) => topic_public($e, $db), array_values($entries));
    usort($topics, fn($a, $b) => [$a['parent']['title'] ?? $a['title'], $a['parent'] ? 1 : 0, $a['title']] <=> [$b['parent']['title'] ?? $b['title'], $b['parent'] ? 1 : 0, $b['title']]);
    return $topics;
}

function handle_topics() {
    json_response(['current' => topic_lookup(current_topic())['id'] ?? null, 'topics' => topics_public_list()]);
}

/**
 * Новая тема: старый парсер останавливается, неоценённое по старой теме убирается.
 * Новый парсер запускается, только если в пуле новой темы не хватает свежих записей (просроченные уходят).
 */
function handle_save_topic($in) {
    if (($typed = normalize_topic($in['topic'] ?? '')) === '') json_error('Тема поиска не может быть пустой.');
    if ($denied = topic_save_denied($typed)) throw new DomainException($denied);
    $previous = current_topic();
    $previous_key = search_topic_key();
    $stopped = !topic_same($typed, $previous) && stop_content_parser();
    $resolved = topic_resolve($typed, !empty($in['force_new']));
    // Введённое слово безобидное, а тема, к которой его отнесли, — из ветки 18+: отказ до сохранения настроек
    if ($denied = topic_save_denied($resolved['topic']['title'])) {
        if ($stopped) start_content_parser_if_needed(true);   // парсер прежней темы остановили ради модели — возвращаем
        throw new DomainException($denied);
    }
    save_settings(['topic' => $topic = $resolved['topic']['title']]);
    if ($lacks = topic_lacks_sources(load_source_db(), $topic)) request_source_discovery();
    $parser = 'unchanged';
    if (!topic_same($topic, $previous)) {
        pool_update(fn($pool) => array_values(array_filter($pool, fn($i) => !is_array($i) || topic_norm($i['topic'] ?? '') !== topic_norm($previous_key))));
        media_cache_cleanup(all_users_pool_items());
    }
    if (!topic_same($topic, $previous) || $stopped) $parser = content_pool_needs_fill(search_topic_key()) ? start_content_parser_if_needed(true) : 'unchanged';
    ok(['topic' => $topic, 'parser' => $parser, 'typed' => $typed, 'matched' => $resolved['matched'], 'created' => $resolved['created'], 'ai' => $resolved['ai'],
        'discovery' => $lacks, 'topic_info' => topic_public($resolved['topic'])]);
}

/** Вторая тема (пусто — убрать): запись должна подходить под обе. Пул пересобирается. */
function handle_save_second_topic($in) {
    if ($denied = topic_save_denied($typed = normalize_topic($in['topic2'] ?? ''))) throw new DomainException($denied);
    $topic2 = $typed === '' ? '' : topic_resolve($typed)['topic']['title'];
    if ($denied = topic_save_denied($topic2)) throw new DomainException($denied);
    if ($topic2 !== '' && topic_same($topic2, current_topic())) json_error('Вторая тема совпадает с первой.');
    if (topic_norm($topic2) === topic_norm(current_topic2())) ok(['topic2' => $topic2, 'parser' => 'unchanged']);
    save_settings(['topic2' => $topic2]);
    pool_update(fn() => []);
    ok(['topic2' => $topic2, 'parser' => start_content_parser_if_needed(true)]);
}

/** Уточнение к теме: новый ключ поиска — свой пул; возврат к старому запросу — старый пул (TOPIC_QUERY_PROMPT.md). */
function handle_save_query($in) {
    $query = normalize_query($in['query'] ?? '');
    if ($denied = topic_save_denied($query)) throw new DomainException($denied);
    if (topic_norm($query) === topic_norm(current_query())) ok(['query' => $query, 'parser' => 'unchanged']);
    save_settings(['query' => $query]);
    ok(['query' => $query, 'parser' => start_content_parser_if_needed(true)]);
}

function handle_save_filters($in) {
    $before = content_filters();
    save_settings(array_map(fn($v) => filter_var($v, FILTER_VALIDATE_BOOLEAN), array_intersect_key($in, $before)));
    // Перевод — способ показа: смена только галочки «Автоперевод» парсер не перезапускает и пул не чистит
    $selection = fn($f) => array_diff_key($f, ['auto_translate' => true]);
    if ($selection(($filters = content_filters())) === $selection($before)) ok(['filters' => $filters, 'parser' => 'unchanged']);
    pool_for_current_topic();
    ok(['filters' => $filters, 'parser' => start_content_parser_if_needed(true)]);
}

/** {allowed}: галочка «Показывать чувствительный и 18+ контент». Выключили, а текущая тема из 18+ — возвращаемся к теме по умолчанию. */
function handle_adult_allow($in) {
    $allowed = filter_var($in['allowed'] ?? false, FILTER_VALIDATE_BOOLEAN);
    db_exec('UPDATE users SET adult_allowed = ? WHERE id = ?', [$allowed ? 1 : 0, app_user_id()]);
    user_adult_allowed(null, true);
    $reset = !$allowed && topic_is_restricted(current_topic());
    if ($reset) {
        stop_content_parser();
        save_settings(['topic' => DEFAULT_TOPIC]);
    }
    ok(['adult_allowed' => $allowed, 'topic_reset' => $reset]);
}

function handle_topic_reprofile($in) {
    topic_save(['profile' => []] + (topic_registry()[(string)($in['id'] ?? '')] ?? json_error('Такой темы нет.')));
    ok();
}

function handle_topic_parent($in) {
    $entry = topic_set_parent((string)($in['id'] ?? ''), (string)($in['parent'] ?? ''));
    ok(['topic_info' => topic_public($entry)]);
}

function handle_topic_rubric($in) {
    $entry = topic_set_rubric((string)($in['id'] ?? ''), (string)($in['rubric'] ?? ''));
    ok(['topic_info' => topic_public($entry)]);
}

function handle_analyze_image($in) { json_response(analyze_image_local(read_request_image($in), load_history(), current_topic())); }

// ---- Голоса ----

/** Голос: интерфейс шлёт только адрес картинки, данные для истории сервер берёт из пула. postpone — в конец очереди. */
function handle_vote($in) {
    [$vote, $image] = [(string)($in['vote'] ?? ''), (string)($in['image'] ?? '')];
    if ((!isset(CONTENT_VOTE_COLLECTIONS[$vote]) && $vote !== 'postpone') || $image === '') json_error('Некорректный голос или запись');
    $reason = $vote === 'wrong' && in_array($in['reason'] ?? '', COMPLAINT_REASONS, true) ? $in['reason'] : null;
    $before = [];
    $after = pool_update(function ($pool) use ($image, $vote, $reason, &$before) {
        $before = $pool;
        return apply_vote($pool, $image, $vote, $reason, fn($record, $c) => history_append($c, $record));
    });
    if (!in_array(CONTENT_VOTE_COLLECTIONS[$vote] ?? '', PROPOSAL_COLLECTIONS, true)) media_cache_release_removed($before, $after);   // лайк и «не знаю» держат файлы для «Предложки»
    ok();
}

/** Новый пул после голоса или null (записи уже нет). $save($record, $collection) — до удаления записи. */
function apply_vote(array $pool, $image, $vote, $reason, callable $save) {
    if (($item = pool_find_item($pool, $image)) === null) return null;
    if ($vote === 'postpone') return [...pool_without_image($pool, $image), ['postponed_at' => microtime(true)] + $item];
    $save(content_history_record($item, $reason), CONTENT_VOTE_COLLECTIONS[$vote]);
    return pool_without_image($pool, $image);
}

// ---- «Предложка»: лайки и «не знаю» из истории ----

/** Источник удалил пост, а своей копии файла нет: показывать нечего. */
function proposal_media_lost(array $r): bool {
    return !empty($r['media_gone']) && media_cache_existing($r['image_local'] ?? '') === '' && media_cache_existing($r['video_local'] ?? '') === '';
}

function proposal_items(array $history) {
    $items = [];
    $current_topic = current_topic();
    $adult_allowed = user_adult_allowed();
    foreach (PROPOSAL_COLLECTIONS as $c) foreach ($history[$c] ?? [] as $r) {
        if (is_array($r) && empty($r['proposal_hidden']) && !proposal_media_lost($r) && content_image_key($r) !== '' && ($adult_allowed || !content_is_adult($r))) {
            $item_topic = (string)($r['topic'] ?? '');
            if ($item_topic === '' || topic_same($item_topic, $current_topic)) {
                $items[] = ['vote' => $c] + content_for_display($r, [], false);
            }
        }
    }
    usort($items, fn($a, $b) => ($b['timestamp'] ?? 0) <=> ($a['timestamp'] ?? 0));
    return array_slice($items, 0, PROPOSALS_LIMIT);
}

/** Лёгкий опрос прогресса: читает только файл прогресса и отметку блокировки, без истории. */
function handle_proposals_progress() {
    $progress = proposal_progress_read();
    $lock = sys_get_temp_dir() . '/posta-backfill-' . app_user_id() . '.lock';
    json_response(['refreshing' => !empty($progress['running']) && time() - (int)@filemtime($lock) < 180, 'progress' => $progress]);
}

function handle_proposals() {
    $refreshing = proposal_backfill_async();
    json_response(['items' => proposal_items(load_history()), 'refreshing' => $refreshing, 'progress' => $refreshing ? proposal_progress_read() : null]);
}

/** Убрать из «Предложки» (в истории оценка остаётся). */
function handle_proposal_hide($in) {
    if (($image = (string)($in['image'] ?? '')) === '') json_error('Не указана запись.');
    $hidden = [];
    update_history_proposals(function ($r) use ($image, &$hidden) {
        if (content_image_key($r) !== $image || !empty($r['proposal_hidden'])) return null;
        $hidden[] = $r;
        return ['proposal_hidden' => true] + $r;
    });
    foreach ($hidden as $r) foreach (media_cache_item_urls($r) as $url) media_cache_remove($url);
    ok();
}

/** Загрузить медиа для одной записи заново. */
function handle_proposal_refetch($in) {
    if (($image = (string)($in['image'] ?? '')) === '') json_error('Не указана запись.');
    $uid = app_user_id();
    $refetch_limit_key = 'proposal_refetch:' . $uid . ':' . substr(sha1($image), 0, 12);   // лимит на запись: кнопки на разных карточках не мешают друг другу
    if (time() - (int)kv_get($refetch_limit_key, 0) < 5) json_error('Эта запись уже загружается, подождите несколько секунд.', 429);
    kv_update($refetch_limit_key, fn() => time());

    $record = null;
    foreach (PROPOSAL_COLLECTIONS as $c) {
        foreach (load_history()[$c] ?? [] as $r) {
            if (is_array($r) && content_image_key($r) === $image) {
                $record = $r;
                break 2;
            }
        }
    }
    if (!$record) json_error('Запись не найдена.', 404);

    // Попробовать загрузить медиа
    $m = proposal_fetch_media($record) ?? proposal_fetch_media($record);   // источник мог моргнуть: одна повторная попытка
    if ($m === null) json_error('Источник не отвечает, попробуйте позже.', 503);
    if ($m === 'gone') {
        update_history_proposals(fn($r) => content_image_key($r) === $image ? ['media_gone' => true] + $r : null);   // из «Предложки» запись уйдёт сама
        json_response(['gone' => true]);
    }

    // Сохранить загруженное медиа; без сохранённого файла запись не помечаем «загруженной»
    $m = proposal_cache_media($record, $m);
    if ($m['image_local'] === '' && $m['video_local'] === '' && media_cache_existing($record['image_local'] ?? '') === '' && media_cache_existing($record['video_local'] ?? '') === '') {
        json_error('Не удалось скачать файл: источник не отдал его.', 502);
    }
    update_history_proposals(function ($r) use ($image, $m) {
        if (content_image_key($r) !== $image) return null;
        return ['video' => $m['video'] ?: (string)($r['video'] ?? ''), 'gallery' => $m['gallery'] ?: ($r['gallery'] ?? []),
            'video_local' => $m['video_local'] ?: (string)($r['video_local'] ?? ''),
            'image_local' => $m['image_local'] ?: (string)($r['image_local'] ?? ''), 'media_checked' => true, 'media_cached' => true] + $r;
    });

    json_response(content_for_display(array_filter(['video' => $m['video'], 'gallery' => $m['gallery'], 'video_local' => $m['video_local'], 'image_local' => $m['image_local']]) + $record));
}

/** Восстановить медиа для всех записей в Предложке. */
/** «Загрузить все заново»: сначала пометить записи с пропавшими файлами (без этого загрузка их пропускает), потом скачать в фоне. */
function handle_proposal_repair($in) {
    $stats = media_repair_user(app_user_id());
    ok(['refreshing' => proposal_backfill_async(), 'repair' => $stats]);
}

/** Меняет записи «Предложки»: $change(запись) → новая запись или null (не менять). */
function update_history_proposals(callable $change, $uid = null) {
    $changed = 0;
    history_update(function ($h) use ($change, &$changed) {
        foreach (PROPOSAL_COLLECTIONS as $c) foreach ($h[$c] ?? [] as $i => $r) if (is_array($r) && ($new = $change($r)) !== null) { $h[$c][$i] = $new; $changed++; }
        return $changed ? $h : null;
    }, $uid);
    return $changed;
}

/** Записи «Предложки», не сверенные с источником, достраиваются в фоне (tools/cli.php backfill-proposals). true — идёт. */
function proposal_backfill_async() {
    $pending = proposal_pending();
    if (!$pending && time() - user_checked_at('proposals') < POOL_LIVENESS_INTERVAL) return false;
    $lock = sys_get_temp_dir() . '/posta-backfill-' . app_user_id() . '.lock';
    if (time() - (int)@filemtime($lock) < 180) return !empty(proposal_progress_read()['running']);   // недавно запущен: идёт ли он ещё
    @touch($lock);
    proposal_progress_write(['total' => count($pending), 'done' => 0, 'running' => true, 'updated_at' => time()]);
    exec('/usr/bin/php ' . escapeshellarg(ROOT . '/tools/cli.php') . ' backfill-proposals ' . escapeshellarg(app_user_id()) . ' >/dev/null 2>&1 &');
    return true;
}

/** Медиа поста из источника для старой записи (видео, галерея, свежая ссылка на картинку). null — источник недоступен, 'gone' — пост удалён. */
function proposal_fetch_media(array $r) {
    $image = content_image_key($r);
    if (preg_match('~^https://t\.me/([A-Za-z0-9_]{3,64})/(\d+)$~', (string)($r['source_url'] ?? ''), $m)) {
        if (!($html = fetch_telegram_channel_page($m[1], (int)$m[2] + 1))) return null;
        foreach (telegram_posts_from_html($html, ['id' => $m[1]], 30) as $p) {
            if ((string)$p['post_id'] !== $m[2]) continue;
            $pos = array_search($image, array_column((array)($r['gallery'] ?? []), 'image'), true);
            return ['video' => $p['video'], 'gallery' => $p['gallery'], 'image' => $pos !== false ? ($p['gallery'][$pos]['image'] ?? $p['image']) : $p['image']];
        }
        return 'gone';   // Пост не найден на странице: удалён
    } elseif (!empty($r['board']) && !empty($r['thread_id']) && !empty($r['post_id'])) {
        if (!($data = fetch_2ch_json($r['board'], (string)$r['thread_id'], []))) return null;
        foreach (dvach_thread_posts($data) as $p) {
            if ((string)($p['num'] ?? '') !== (string)$r['post_id']) continue;
            $files = dvach_post_media($r['board'], $p);
            return ['video' => array_column($files, 'video', 'image')[$image] ?? '', 'gallery' => count($files) > 1 ? $files : []];
        }
        return 'gone';   // Пост не найден в треде: удалён (или тред удалён, но мы доступ получили)
    }
    return ['video' => '', 'gallery' => []];
}

/** Сохраняет файлы поста в кэш, пока ссылки свежие. */
function proposal_cache_media(array $r, array $m) {
    $m += ['video_local' => '', 'image_local' => '', 'image' => ''];
    $key = content_image_key($r);
    if (($video = $m['video'] ?: (string)($r['video'] ?? '')) !== '' && empty($r['video_local']) && ($file = download_video_temp($video))) {
        $m['video_local'] = cache_downloaded_video($video, $file);
        @unlink($file);
    }
    if (media_cache_existing($r['image_local'] ?? '') === '' && ($bytes = ($m['image'] ? gallery_download_image($m['image']) : null) ?? gallery_download_image($key))) {
        $m['image_local'] = media_cache_store_image($key, gallery_prepare_image($bytes));
    }
    if (count($gallery = $m['gallery'] ?: ($r['gallery'] ?? [])) > 1) $m['gallery'] = gallery_cache_files($gallery, $key);
    return $m;
}

/** Записи «Предложки», которые ещё не сверены с источником. */
function proposal_pending(): array {
    return array_filter(array_merge(...array_map(fn($c) => (array)(load_history()[$c] ?? []), PROPOSAL_COLLECTIONS)), fn($r) => is_array($r) && empty($r['media_cached']) && empty($r['proposal_hidden']) && empty($r['media_gone']));
}

/** Прогресс достраивания для страницы: сколько записей проверено из скольких в текущем проходе. */
function proposal_progress_read(): array { return (array)kv_get('proposal_progress:' . app_user_id(), []); }
function proposal_progress_write(array $progress): void { kv_update('proposal_progress:' . app_user_id(), fn() => $progress); }
function proposal_progress_tick(): void {
    $progress = proposal_progress_read();
    $progress['done'] = (int)($progress['done'] ?? 0) + 1;
    $progress['updated_at'] = time();
    proposal_progress_write($progress);
}

function proposal_backfill_media($limit = 6) {
    $todo = [];
    foreach (PROPOSAL_COLLECTIONS as $c) foreach (load_history()[$c] ?? [] as $r) {
        if (is_array($r) && empty($r['media_cached']) && empty($r['proposal_hidden']) && empty($r['media_gone']) && count($todo) < $limit) $todo[content_image_key($r)] = $r;
    }
    if (!$todo) return 0;
    $found = [];
    $gone = [];
    foreach ($todo as $key => $r) {
        if (($m = proposal_fetch_media($r)) === 'gone') {
            $gone[$key] = true;
        } elseif ($m !== null) {
            $cached = proposal_cache_media($r, $m);
            if ($cached['image_local'] !== '' || $cached['video_local'] !== '' || media_cache_existing($r['image_local'] ?? '') !== '' || media_cache_existing($r['video_local'] ?? '') !== '' || ($r['image_local'] ?? '') === '') $found[$key] = $cached;
        }
        proposal_progress_tick();   // считаем проверенной и ту запись, источник которой недоступен
    }
    $updated = 0;
    if ($found) {
        $updated += update_history_proposals(function ($r) use ($found) {
            if (!isset($found[$k = content_image_key($r)]) || !empty($r['media_cached'])) return null;
            $m = $found[$k];   // пустой ответ не затирает то, что запись уже знала
            return ['video' => $m['video'] ?: (string)($r['video'] ?? ''), 'gallery' => $m['gallery'] ?: ($r['gallery'] ?? []), 'video_local' => $m['video_local'] ?: (string)($r['video_local'] ?? ''),
                'image_local' => $m['image_local'] ?: (string)($r['image_local'] ?? ''), 'media_checked' => true, 'media_cached' => true] + $r;
        });
    }
    if ($gone) {
        $updated += update_history_proposals(function ($r) use ($gone) {
            return isset($gone[content_image_key($r)]) ? ['media_gone' => true] + $r : null;
        });
    }
    return $updated;
}

/** Прячет посты мёртвых тредов без своей копии картинки. */
function proposal_prune_dead_threads($force = false) {
    if (!$force && time() - user_checked_at('proposals') < POOL_LIVENESS_INTERVAL) return 0;
    $threads = [];
    foreach (PROPOSAL_COLLECTIONS as $c) foreach (load_history()[$c] ?? [] as $r) {
        if (is_array($r) && empty($r['proposal_hidden']) && !empty($r['board']) && !empty($r['thread_id'])) $threads["{$r['board']}/{$r['thread_id']}"] = [$r['board'], $r['thread_id']];
    }
    $states = dvach_threads_liveness(array_slice($threads, 0, POOL_LIVENESS_MAX_THREADS, true));
    $hidden = update_history_proposals(fn($r) => empty($r['proposal_hidden']) && dvach_item_gone($r, $states) && media_cache_existing($r['image_local'] ?? '') === ''
        ? ['proposal_hidden' => true, 'proposal_dead' => true] + $r : null);
    user_proposals_checked();
    return $hidden;
}

// ---- Базы данных: просмотр и правка записей ----

function database_definitions() {
    static $defs = null;
    if ($defs !== null) return $defs;

    $defs = [
        'content_pool' => ['label' => 'Пул контента', 'collections' => ['items' => 'Записи'], 'by_topic' => true],
        'content_history' => ['label' => 'История оценок', 'collections' => ['liked' => 'Лайки', 'disliked' => 'Дизлайки', 'undecided' => 'Без решения', 'complaints' => 'Жалобы'], 'by_topic' => true],
        'sources_db' => ['label' => 'Каталог источников', 'collections' => ['2ch_boards' => 'Доски 2ch', 'vk_publics' => 'Паблики ВКонтакте', 'telegram_channels' => 'Каналы Telegram', 'rss_feeds' => 'Ленты RSS', 'ok_groups' => 'Группы Одноклассников', 'tiktok_accounts' => 'Аккаунты TikTok', 'source_votes' => 'Оценки источников']]
    ];

    // Добавляем все таблицы SQLite
    try {
        $pdo = db();
        $tables = $pdo->query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")->fetchAll(PDO::FETCH_COLUMN);
        $tableLabels = [
            'analysis_cache' => 'Кэш анализов ИИ',
            'catalog_sources' => 'Каталог источников (SQL)',
            'history_items' => 'История оценок (SQL)',
            'kv' => 'Конфиг (ключ-значение)',
            'outbox' => 'Очередь постов',
            'parser_runtime' => 'Состояние парсера',
            'pool_items' => 'Пул контента (SQL)',
            'published' => 'Опубликовано',
            'screened_cache' => 'Кэш оценок контента',
            'source_suggestions' => 'Рекомендации источников',
            'source_votes' => 'Оценки источников (SQL)',
            'topics' => 'Темы поиска',
            'users' => 'Ваш профиль',
            'ledger' => 'Баланс: операции',
            'subscriptions' => 'Тариф',
            'post_usage' => 'Учёт постов'
        ];

        foreach ($tables as $table) {
            if (in_array($table, DATABASE_SQL_COVERED, true)) continue;   // их показывают и правят базы выше — с темой и только свои записи
            if (!isset($defs[$table])) {
                $defs[$table] = [
                    'label' => $tableLabels[$table] ?? ucfirst(str_replace('_', ' ', $table)),
                    'collections' => ['all' => 'Все записи'],
                    'is_sql_table' => true
                ];
            }
        }
    } catch (Exception $e) {
        // если не смогли прочитать таблицы, используем только определённые выше
    }

    return $defs;
}

/** [определение, данные, записи коллекции]. Битый JSON — ошибка, а не пустая база: иначе сохранение затёрло бы файл. */
function database_open($database, $collection) {
    $def = database_definitions()[$database] ?? throw new InvalidArgumentException('Неизвестная база данных.');
    if (!isset($def['collections'][$collection])) throw new InvalidArgumentException('Неизвестная коллекция.');

    // Если это SQL таблица
    if ($def['is_sql_table'] ?? false) {
        // Таблицы с user_id — только записи текущего пользователя: чужие пулы выглядели как дубли, а профили других — это чужие данные
        $columns = array_column(db_all("PRAGMA table_info($database)"), 'name');
        $own = in_array('user_id', $columns, true) ? 'user_id' : ($database === 'users' ? 'id' : null);
        $rows = db_all("SELECT * FROM $database" . ($own ? " WHERE $own = ?" : '') . ' ORDER BY rowid', $own ? [app_user_id()] : []);
        // kv: секреты сайта не показываем, ключи вида «имя:id» — только свои
        if ($database === 'kv') $rows = array_filter($rows, fn($r) => !in_array($r['key'], KV_HIDDEN_KEYS, true) && (!str_contains($r['key'], ':') || str_ends_with($r['key'], ':' . app_user_id())));
        $items = [];
        foreach ($rows as $row) {
            unset($row['connections']);   // токены соцсетей (пусть и зашифрованные) в просмотрщик не отдаём
            // Парсим JSON поля
            $item = [];
            foreach ($row as $k => $v) {
                if (is_string($v) && ($v[0] ?? '') === '{') {
                    try { $item[$k] = json_decode($v, true) ?? $v; }
                    catch (Exception $e) { $item[$k] = $v; }
                } else {
                    $item[$k] = $v;
                }
            }

            // Для таблиц, где содержимое хранится в поле 'data',
            // распаковываем его на верхний уровень (для удобства отображения)
            if (isset($item['data']) && is_array($item['data'])) {
                // Сохраняем системные поля отдельно
                $system = array_intersect_key($item, array_flip(['id', 'rowid', 'user_id', 'position', 'image_key', 'collection', 'proposal_hidden']));
                // Распаковываем содержимое data на верхний уровень
                $item = array_merge($item['data'], $system);
            }

            $items[] = $item;
        }
        return [$def, null, $items];
    }

    $all = array_values(match ($database) { 'content_pool' => pool_load(), 'content_history' => history_load()[$collection] ?? [], default => load_source_db()[$collection] ?? [] });
    // Пул и история — только записи темы, выбранной во вкладках (одна настройка topic на всё приложение)
    $visible = ($def['by_topic'] ?? false) ? pool_topic_positions($all) : array_keys($all);
    return [$def, $visible, array_values(array_intersect_key($all, array_flip($visible)))];
}

const KV_HIDDEN_KEYS = ['telegram_app_keys', 'vk_tokens', 'telegram_login_bot'];

/** Таблицы SQLite, у которых есть свой просмотр выше (content_pool, content_history, sources_db). */
const DATABASE_SQL_COVERED = ['pool_items', 'history_items', 'catalog_sources', 'source_votes'];

function database_validate($database, $collection, $r) {
    // SQL таблицы: минимальная валидация или её отсутствие
    $def = database_definitions()[$database] ?? null;
    if ($def['is_sql_table'] ?? false) {
        return; // Для SQL таблиц особой валидации нет
    }

    $object = is_array($r) && !array_is_list($r);
    $error = match (true) {
        $database === 'content_pool' => !$object || trim((string)($r['image'] ?? '')) === '' ? 'Записи нужны поля-объект и непустой image.'
            : (isset($r['rating']) && (!is_numeric($r['rating']) || $r['rating'] < 0 || $r['rating'] > 100) ? 'rating должен быть числом от 0 до 100.' : null),
        in_array($collection, HISTORY_COLLECTIONS, true) => !$object || !is_string($r['text'] ?? null) || !(is_numeric($r['rating_assigned'] ?? null) || (array_key_exists('rating_assigned', $r) && $r['rating_assigned'] === null))
            ? 'Для записи истории нужны объект, строка text и число rating_assigned (или null).' : null,
        $collection === '2ch_boards' => !is_string($r) || !is_valid_dvach_board($r) ? 'Доска 2ch должна содержать только буквы, цифры, _ или - (до 30 символов).' : null,
        $collection === 'source_votes' => !$object || trim((string)($r['source_id'] ?? '')) === '' || !in_array($r['vote'] ?? '', ['approve', 'reject'], true) ? 'Оценке источника нужны source_id и vote: approve или reject.' : null,
        default => !$object || trim((string)($r['id'] ?? '')) === '' || trim((string)($r['name'] ?? '')) === '' ? 'Источнику нужны непустые поля id и name.' : null
    };
    if ($error) throw new InvalidArgumentException($error);
}

function handle_database_options() {
    json_response(['databases' => array_map(fn($id, $d) => ['id' => $id, 'label' => $d['label'], 'is_sql_table' => $d['is_sql_table'] ?? false, 'by_topic' => $d['by_topic'] ?? false, 'collections' => array_map(fn($cid, $l) => ['id' => $cid, 'label' => $l], array_keys($d['collections']), $d['collections'])],
        array_keys(database_definitions()), database_definitions())]);
}

/** Столбцы таблицы — поля записей в том порядке, в каком они лежат в базе (JSON). Строки-значения (например, список досок) — один столбец value. */
function database_columns(array $items) {
    $keys = [];
    foreach ($items as $i) if (is_array($i)) foreach (array_keys($i) as $k) $keys[(string)$k] = true;
    return array_keys($keys) ?: ['value'];
}

/** Номера записей, у которых локальный файл картинки или видео указан, но на диске его нет (удалён очисткой раньше). */
function database_media_missing(array $items) {
    $missing = [];
    foreach ($items as $n => $r) {
        if (!is_array($r)) continue;
        foreach (['image_local', 'video_local'] as $field) {
            $path = media_cache_path((string)($r[$field] ?? ''));
            if ($path !== null && !is_file($path)) { $missing[] = $n; break; }
        }
    }
    return $missing;
}

function handle_database_read() {
    [$def, , $items] = database_open($db = $_GET['database'] ?? '', $c = $_GET['collection'] ?? '');
    json_response(['database_label' => $def['label'], 'collection_label' => $def['collections'][$c], 'topic' => ($def['by_topic'] ?? false) ? search_topic_key() : '',
        'columns' => database_columns($items), 'items' => $items,
        'media_missing' => database_media_missing($items)]);
}

/** Правка коллекции в памяти. $all — все записи коллекции. Номер в списке — позиция среди записей выбранной темы, поэтому переводим его в позицию в $all. */
function database_edit(array $all, $database, $collection, $in, $action): array {
    $all = array_values($all);
    $visible = in_array($database, ['content_pool', 'content_history'], true) ? pool_topic_positions($all) : array_keys($all);
    $items = $all;
    if ($action !== 'database_delete' && !array_key_exists('record', $in)) throw new InvalidArgumentException('Не переданы данные записи.');
    if ($action === 'database_add') {
        database_validate($database, $collection, $items[] = $in['record']);
        return $items;
    }
    $i = filter_var($in['index'] ?? null, FILTER_VALIDATE_INT);
    if ($i === false || $i < 0) throw new InvalidArgumentException('Некорректный номер записи.');
    if (!array_key_exists($i, $visible)) throw new DomainException('Запись уже удалена или не существует. Обновите список.');
    $pos = $visible[$i];
    if (array_key_exists('expected', $in) && $in['expected'] != $items[$pos]) throw new DomainException('Запись изменилась с момента загрузки списка. Обновите список и повторите.');
    if ($action === 'database_delete') {
        array_splice($items, $pos, 1);
        return $items;
    }
    $old = $items[$pos];
    if (gettype($old) !== gettype($in['record']) || (is_array($old) && array_is_list($old) !== array_is_list($in['record']))) throw new InvalidArgumentException('Нельзя менять тип записи.');
    database_validate($database, $collection, $items[$pos] = $in['record']);
    return $items;
}

/** Правка и удаление передают прежнее значение записи (expected): если её успел изменить парсер — 409. */
function handle_database_change($in, $action) {
    [$database, $collection] = [$in['database'] ?? '', $in['collection'] ?? ''];
    [$def] = database_open($database, $collection);

    // SQL таблицы нельзя редактировать через этот интерфейс
    if ($def['is_sql_table'] ?? false) {
        json_error('Редактирование системных таблиц SQL недопустимо из интерфейса базы данных.');
    }

    if ($database === 'content_pool') {
        $before = pool_load();
        $after = pool_update(fn($pool) => database_edit($pool, $database, $collection, $in, $action));
        media_cache_release_removed($before, $after);
        ok(['count' => count($after)]);
    }
    if ($database === 'content_history') {
        $history = history_update(function ($h) use ($in, $action, $collection) {
            $h[$collection] = database_edit(array_values($h[$collection] ?? []), 'content_history', $collection, $in, $action);
            return $h;
        });
        ok(['count' => count($history[$collection])]);
    }
    $catalog = update_source_db(function ($db) use ($in, $action, $collection) {
        $db[$collection] = database_edit(array_values($db[$collection] ?? []), 'sources_db', $collection, $in, $action);
        return $db;
    });
    ok(['count' => count($catalog[$collection])]);
}

function handle_database_reset() { ok(['removed_media' => reset_project_data(), 'parser' => start_content_parser_if_needed(true)]); }

// ---- Демо-режим (?debug=1): состояние в PHP-сессии, Ollama и файлы не трогаются ----

function demo_image($title, $bg, $accent) {
    $title = htmlspecialchars($title, ENT_QUOTES | ENT_XML1, 'UTF-8');
    return 'data:image/svg+xml;base64,' . base64_encode("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 640 480\"><rect width=\"640\" height=\"480\" fill=\"$bg\"/><circle cx=\"510\" cy=\"90\" r=\"145\" fill=\"$accent\"/><path d=\"M0 370 Q160 280 320 370 T640 340 V480 H0Z\" fill=\"#111\"/><text x=\"42\" y=\"190\" fill=\"#fff\" font-family=\"sans-serif\" font-size=\"42\" font-weight=\"700\">$title</text><text x=\"44\" y=\"245\" fill=\"#fff\" font-family=\"sans-serif\" font-size=\"22\">ТЕСТОВАЯ ЗАПИСЬ</text></svg>");
}

function demo_initial_state() {
    $item = fn($title, $bg, $accent, $text, $desc, $why, $rel, $rating, $source) => ['image' => demo_image($title, $bg, $accent), 'text' => $text, 'description' => $desc,
        'why_relevant' => $why, 'relevance' => $rel, 'rating' => $rating, 'topic' => DEFAULT_TOPIC, 'source' => $source];
    return ['topic' => DEFAULT_TOPIC, 'history' => empty_history(), 'pool' => [
        $item('ПОНЕДЕЛЬНИК', '#2b2b2f', '#5a5a62', 'План на утро: стать продуктивным. Факт: уже обед.', 'Человек строит амбициозные планы, пока время незаметно уходит.', 'Узнаваемая шутка про планы и реальность.', 88, 91, 'Демо-сообщество'),
        $item('ОЖИДАНИЕ / РЕАЛЬНОСТЬ', '#3a3a40', '#77777f', 'Открыл одну вкладку. Через час их уже двадцать семь.', 'Шутка о привычке отвлекаться в интернете.', 'Юмор про бытовую ситуацию.', 72, 76, 'Локальный тест')]];
}

function demo_route($action, array $in): never {
    if (session_status() === PHP_SESSION_NONE) session_start();
    if ($action === 'debug_reset' || !is_array($_SESSION['posta_debug'] ?? null)) $_SESSION['posta_debug'] = demo_initial_state();
    $s = &$_SESSION['posta_debug'];
    $debug = ['status' => 'success', 'debug' => true];
    if ($action === 'vote') $s['pool'] = apply_vote($s['pool'], (string)($in['image'] ?? ''), (string)($in['vote'] ?? 'postpone'), null, function ($r, $c) use (&$s) { $s['history'][$c][] = $r; }) ?? $s['pool'];
    match ($action) {
        'get_current_item' => $s['pool'] ? json_response(content_for_display($s['pool'][0], array_slice($s['pool'], 1, 3))) : json_response(['status' => 'empty', 'message' => 'Демо-пул пуст: нажмите «Сбросить демо».']),
        'settings' => json_response(['topic' => $s['topic'], 'topic2' => '', 'query' => '', 'topic_info' => null, 'filters' => default_settings()]),
        'save_topic' => json_response($debug + ['topic' => $s['topic'] = normalize_topic($in['topic'] ?? '') ?: DEFAULT_TOPIC]),
        'topics' => json_response(['current' => null, 'topics' => []]),
        'proposals' => json_response(['items' => proposal_items($s['history']), 'refreshing' => false]),
        'parser_status' => json_response(['running' => false, 'activity' => []]),
        'discovery_status' => json_response(['enabled' => false]),
        'topic_sources' => json_response(['topic' => $s['topic'], 'sources' => []]),
        'suggest_sources' => json_response(['discovered' => null, 'message' => 'В демо-режиме источники не подбираются.']),
        'account_info' => json_response(['debug' => true]),
        'telegram_queue' => json_response(['connected' => false, 'scheduled' => [], 'published' => []]),
        'calendar' => json_response(['connected' => false, 'now' => time(), 'channels' => [], 'posts' => []]),
        'vk_oauth_status' => json_response(['configured' => false, 'connected' => false, 'user_id' => null]),
        'model_status' => handle_model_status(),
        'analyze_image' => json_response(analyze_image_local(read_request_image($in), $s['history'], $s['topic'])),
        'tab_heartbeat' => json_response(['owner' => true, 'debug' => true]),
        'vote', 'tab_open', 'tab_close', 'debug_reset', 'source_vote', 'proposal_hide' => null,
        default => json_error('В демо-режиме это действие недоступно.', 403)
    };
    json_response($debug);
}

function handle_debug_reset() { json_error('Сброс доступен только в демо-режиме.', 403); }

/** Формат текущей темы для строки «Ищем: …»: формат, правка пользователя, родитель и частные темы (чипы «Уточнить»). */
function handle_topic_info() {
    $topic = current_topic();
    $entry = topic_lookup($topic);
    $own = topic_format_override($topic);
    $children = $entry ? array_slice(array_map(fn($c) => ['title' => $c['title'], 'key' => $c['key']], topic_children($entry)), 0, 12) : [];
    $parent = $entry ? topic_parent($entry) : null;
    json_response(['topic' => $topic, 'format' => topic_format($topic), 'overridden' => $own !== null,
        'children' => $children, 'children_total' => $entry ? count(topic_children($entry)) : 0,
        'parent' => $parent ? ['title' => $parent['title']] : null]);
}

/** Правка формата темы этим пользователем: media, text, judge (пусто — вернуть формат темы по умолчанию). */
function handle_topic_format_save($in) {
    $topic = topic_norm(current_topic());
    $settings = db_load_settings();
    $format = topic_format_clean(['media' => $in['media'] ?? '', 'text' => $in['text'] ?? '', 'judge' => $in['judge'] ?? '']);
    if (!empty($in['reset']) || $format === null) unset($settings['topic_formats'][$topic]);
    else $settings['topic_formats'][$topic] = $format;
    db_save_settings($settings);
    ok(['format' => topic_format(current_topic())]);
}

/** Адрес — из записей пользователя (пул или история), не произвольный: читалка не открывает чужие страницы. */
function article_url_is_own(string $url): bool {
    if (!str_starts_with($url, 'https://')) return false;
    foreach (array_merge(pool_load(), history_records()) as $item) if (is_array($item) && ($item['url'] ?? '') === $url) return true;
    return false;
}

/** Читалка: блоки статьи по адресу записи пользователя. Только чтение; загрузка страницы — не чаще раза в секунду на хост. */
function handle_article_read() {
    $url = trim((string)($_GET['url'] ?? ''));
    if (!article_url_is_own($url)) json_error('Статья не из ваших записей.', 403);
    if (($article = article_blocks_for($url)) === null) json_error('Статья не открылась или на странице нет текста.', 404);
    json_response(['url' => $url, 'article' => $article]);
}
