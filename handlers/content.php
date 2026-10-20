<?php
// Вкладки «Подбор», «Предложка», «Дерево», «Тест ИИ», базы данных и демо-режим.

function handle_get_current_item() {
    if ($pool = pool_for_current_topic()) json_response(content_for_display($pool[0], array_slice($pool, 1, 3)));
    in_array(start_content_parser_if_needed(), ['started', 'running'], true)
        ? json_response(['status' => 'loading', 'message' => 'Ищем контент...'])
        : json_response(['status' => 'empty', 'message' => 'Нового контента пока нет. Можно повторить поиск или добавить запись вручную.']);
}

/** Запись относится к теме поиска или к составной «первая + вторая»; записи без темы подходят всем. */
function pool_item_in_topic($i, $topic, $key) {
    return ($i['topic'] ?? '') === '' || topic_same((string)$i['topic'], $topic) || topic_same((string)$i['topic'], $key);
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

/** Пул без записей другой темы и не подходящих под фильтры подборки (остаются, если тему сменили во время записи). */
function pool_for_current_topic() {
    [$topic, $key, $f] = [current_topic(), search_topic_key(), content_filters()];
    $frozen = $f['active_sources_only'] ? frozen_source_labels() : [];
    $keep = fn($i) => pool_item_in_topic($i, $topic, $key) && pool_item_is_fresh($i)
        && (!$f['video_only'] || post_item_has_video($i)) && (!$f['fresh_only'] || !is_post_outdated($i['posted_at'] ?? 0, null, CONTENT_FRESH_MAX_AGE_DAYS))
        && !isset($frozen[mb_strtolower((string)($i['source'] ?? ''))]);
    $pool = pool_load();
    return count(array_filter($pool, $keep)) === count($pool) ? $pool : pool_update(fn($p) => array_values(array_filter($p, $keep)));
}

function handle_parser_status() {
    $running = content_parser_is_running();
    json_response(['running' => $running, 'phase' => $running ? (parser_phase() ?: 'парсер работает') : 'ничего не делается', 'activity' => content_parser_activity()]);
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
    json_response(['topic' => $topic, 'topic2' => current_topic2(), 'topic_info' => $entry ? topic_public($entry) : null, 'filters' => content_filters()]);
}

/** Полная запись темы из базы, все поля как есть, для панели «Дерево». */
function handle_topic_record() {
    $entry = topic_registry()[(string)($_GET['id'] ?? '')] ?? null;
    if (!$entry) json_error('Такой темы нет.', 404);
    json_response(['topic' => $entry, 'summary' => topic_public($entry, load_source_db())]);
}

function handle_topics() {
    $db = load_source_db();
    $topics = array_map(fn($e) => topic_public($e, $db), array_values(topic_registry()));
    usort($topics, fn($a, $b) => [$a['parent']['title'] ?? $a['title'], $a['parent'] ? 1 : 0, $a['title']] <=> [$b['parent']['title'] ?? $b['title'], $b['parent'] ? 1 : 0, $b['title']]);
    json_response(['current' => topic_lookup(current_topic())['id'] ?? null, 'topics' => $topics]);
}

/**
 * Новая тема: старый парсер останавливается, неоценённое по старой теме убирается.
 * Новый парсер запускается, только если в пуле новой темы не хватает свежих записей (просроченные уходят).
 */
function handle_save_topic($in) {
    if (($typed = normalize_topic($in['topic'] ?? '')) === '') json_error('Тема поиска не может быть пустой.');
    $previous = current_topic();
    $previous_key = search_topic_key();
    $stopped = !topic_same($typed, $previous) && stop_content_parser();
    $resolved = topic_resolve($typed, !empty($in['force_new']));
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
    $topic2 = ($typed = normalize_topic($in['topic2'] ?? '')) === '' ? '' : topic_resolve($typed)['topic']['title'];
    if ($topic2 !== '' && topic_same($topic2, current_topic())) json_error('Вторая тема совпадает с первой.');
    if (topic_norm($topic2) === topic_norm(current_topic2())) ok(['topic2' => $topic2, 'parser' => 'unchanged']);
    save_settings(['topic2' => $topic2]);
    pool_update(fn() => []);
    ok(['topic2' => $topic2, 'parser' => start_content_parser_if_needed(true)]);
}

function handle_save_filters($in) {
    $before = content_filters();
    save_settings(array_map(fn($v) => filter_var($v, FILTER_VALIDATE_BOOLEAN), array_intersect_key($in, $before)));
    if (($filters = content_filters()) === $before) ok(['filters' => $filters, 'parser' => 'unchanged']);
    pool_for_current_topic();
    ok(['filters' => $filters, 'parser' => start_content_parser_if_needed(true)]);
}

function handle_topic_reprofile($in) {
    topic_save(['profile' => []] + (topic_registry()[(string)($in['id'] ?? '')] ?? json_error('Такой темы нет.')));
    ok();
}

function handle_topic_parent($in) {
    $entry = topic_set_parent((string)($in['id'] ?? ''), (string)($in['parent'] ?? ''));
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
    if ($vote === 'postpone') return [...pool_without_image($pool, $image), $item];
    $save(content_history_record($item, $reason), CONTENT_VOTE_COLLECTIONS[$vote]);
    return pool_without_image($pool, $image);
}

// ---- «Предложка»: лайки и «не знаю» из истории ----

function proposal_items(array $history) {
    $items = [];
    $current_topic = current_topic();
    foreach (PROPOSAL_COLLECTIONS as $c) foreach ($history[$c] ?? [] as $r) {
        if (is_array($r) && empty($r['proposal_hidden']) && content_image_key($r) !== '') {
            $item_topic = (string)($r['topic'] ?? '');
            if ($item_topic === '' || topic_same($item_topic, $current_topic)) {
                $items[] = ['vote' => $c] + content_for_display($r);
            }
        }
    }
    usort($items, fn($a, $b) => ($b['timestamp'] ?? 0) <=> ($a['timestamp'] ?? 0));
    return array_slice($items, 0, PROPOSALS_LIMIT);
}

/** Лёгкий опрос прогресса: читает только файл прогресса и отметку блокировки, без истории. */
function handle_proposals_progress() {
    $progress = proposal_progress_read();
    $lock = sys_get_temp_dir() . '/postmaker-backfill-' . app_user_id() . '.lock';
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
    if (!$pending && time() - (int)@filemtime(user_dir() . '/proposals-checked') < POOL_LIVENESS_INTERVAL) return false;
    $lock = sys_get_temp_dir() . '/postmaker-backfill-' . app_user_id() . '.lock';
    if (time() - (int)@filemtime($lock) < 180) return !empty(proposal_progress_read()['running']);   // недавно запущен: идёт ли он ещё
    @touch($lock);
    proposal_progress_write(['total' => count($pending), 'done' => 0, 'running' => true, 'updated_at' => time()]);
    exec('/usr/bin/php ' . escapeshellarg(ROOT . '/tools/cli.php') . ' backfill-proposals ' . escapeshellarg(app_user_id()) . ' >/dev/null 2>&1 &');
    return true;
}

/** Медиа поста из источника для старой записи (видео, галерея, свежая ссылка на картинку). null — источник недоступен. */
function proposal_fetch_media(array $r) {
    $image = content_image_key($r);
    if (preg_match('~^https://t\.me/([A-Za-z0-9_]{3,64})/(\d+)$~', (string)($r['source_url'] ?? ''), $m)) {
        if (!($html = fetch_telegram_channel_page($m[1], (int)$m[2] + 1))) return null;
        foreach (telegram_posts_from_html($html, ['id' => $m[1]], 30) as $p) {
            if ((string)$p['post_id'] !== $m[2]) continue;
            $pos = array_search($image, array_column((array)($r['gallery'] ?? []), 'image'), true);
            return ['video' => $p['video'], 'gallery' => $p['gallery'], 'image' => $pos !== false ? ($p['gallery'][$pos]['image'] ?? $p['image']) : $p['image']];
        }
    } elseif (!empty($r['board']) && !empty($r['thread_id']) && !empty($r['post_id'])) {
        if (!($data = fetch_2ch_json($r['board'], (string)$r['thread_id'], []))) return null;
        foreach (dvach_thread_posts($data) as $p) {
            if ((string)($p['num'] ?? '') !== (string)$r['post_id']) continue;
            $files = dvach_post_media($r['board'], $p);
            return ['video' => array_column($files, 'video', 'image')[$image] ?? '', 'gallery' => count($files) > 1 ? $files : []];
        }
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
    if (media_cache_existing($r['image_local'] ?? '') === '' && ($bytes = gallery_download_image($key) ?? ($m['image'] ? gallery_download_image($m['image']) : null))) {
        $m['image_local'] = media_cache_store_image($key, gallery_prepare_image($bytes));
    }
    if (count($gallery = $m['gallery'] ?: ($r['gallery'] ?? [])) > 1) $m['gallery'] = gallery_cache_files($gallery, $key);
    return $m;
}

/** Записи «Предложки», которые ещё не сверены с источником. */
function proposal_pending(): array {
    return array_filter(array_merge(...array_map(fn($c) => (array)(load_history()[$c] ?? []), PROPOSAL_COLLECTIONS)), fn($r) => is_array($r) && empty($r['media_cached']) && empty($r['proposal_hidden']));
}

/** Прогресс достраивания для страницы: сколько записей проверено из скольких в текущем проходе. */
function proposal_progress_file() { return user_dir() . '/proposals-backfill.json'; }
function proposal_progress_read(): array { return load_data(proposal_progress_file(), []); }
function proposal_progress_write(array $progress): void { save_data(proposal_progress_file(), $progress); }
function proposal_progress_tick(): void {
    $progress = proposal_progress_read();
    $progress['done'] = (int)($progress['done'] ?? 0) + 1;
    $progress['updated_at'] = time();
    proposal_progress_write($progress);
}

function proposal_backfill_media($limit = 6) {
    $todo = [];
    foreach (PROPOSAL_COLLECTIONS as $c) foreach (load_history()[$c] ?? [] as $r) {
        if (is_array($r) && empty($r['media_cached']) && empty($r['proposal_hidden']) && count($todo) < $limit) $todo[content_image_key($r)] = $r;
    }
    if (!$todo) return 0;
    $found = [];
    foreach ($todo as $key => $r) {
        if (($m = proposal_fetch_media($r)) !== null) $found[$key] = proposal_cache_media($r, $m);
        proposal_progress_tick();   // считаем проверенной и ту запись, источник которой недоступен
    }
    if (!$found) return 0;
    return update_history_proposals(function ($r) use ($found) {
        if (!isset($found[$k = content_image_key($r)]) || !empty($r['media_cached'])) return null;
        $m = $found[$k];   // пустой ответ не затирает то, что запись уже знала
        return ['video' => $m['video'] ?: (string)($r['video'] ?? ''), 'gallery' => $m['gallery'] ?: ($r['gallery'] ?? []), 'video_local' => $m['video_local'] ?: (string)($r['video_local'] ?? ''),
            'image_local' => $m['image_local'] ?: (string)($r['image_local'] ?? ''), 'media_checked' => true, 'media_cached' => true] + $r;
    });
}

/** Прячет посты мёртвых тредов без своей копии картинки. */
function proposal_prune_dead_threads($force = false) {
    $marker = user_dir() . '/proposals-checked';
    if (!$force && time() - (int)@filemtime($marker) < POOL_LIVENESS_INTERVAL) return 0;
    $threads = [];
    foreach (PROPOSAL_COLLECTIONS as $c) foreach (load_history()[$c] ?? [] as $r) {
        if (is_array($r) && empty($r['proposal_hidden']) && !empty($r['board']) && !empty($r['thread_id'])) $threads["{$r['board']}/{$r['thread_id']}"] = [$r['board'], $r['thread_id']];
    }
    $states = dvach_threads_liveness(array_slice($threads, 0, POOL_LIVENESS_MAX_THREADS, true));
    $hidden = update_history_proposals(fn($r) => empty($r['proposal_hidden']) && dvach_item_gone($r, $states) && media_cache_existing($r['image_local'] ?? '') === ''
        ? ['proposal_hidden' => true, 'proposal_dead' => true] + $r : null);
    @touch($marker);
    return $hidden;
}

// ---- Базы данных: просмотр и правка записей JSON-файлов ----

function database_definitions() {
    return [
        'content_pool' => ['label' => 'Пул контента', 'file' => null, 'collections' => ['items' => 'Записи']],
        'content_history' => ['label' => 'История оценок', 'file' => null, 'collections' => ['liked' => 'Лайки', 'disliked' => 'Дизлайки', 'undecided' => 'Без решения', 'complaints' => 'Жалобы']],
        'sources_db' => ['label' => 'Каталог источников', 'file' => null, 'collections' => ['2ch_boards' => 'Доски 2ch', 'vk_publics' => 'Паблики ВКонтакте', 'telegram_channels' => 'Каналы Telegram', 'source_votes' => 'Оценки источников']]
    ];
}

/** [определение, данные, записи коллекции]. Битый JSON — ошибка, а не пустая база: иначе сохранение затёрло бы файл. */
function database_open($database, $collection) {
    $def = database_definitions()[$database] ?? throw new InvalidArgumentException('Неизвестная база данных.');
    if (!isset($def['collections'][$collection])) throw new InvalidArgumentException('Неизвестная коллекция.');
    // Пул хранится в SQLite: показываем записи выбранной темы
    // Пул показывается целиком, все темы: таблица выводит все записи, а тема — отдельным столбцом
    if ($database === 'content_pool') return [$def, null, array_values(pool_load())];
    // История и каталог источников тоже в базе
    $items = $database === 'content_history' ? (history_load()[$collection] ?? []) : (load_source_db()[$collection] ?? []);
    return [$def, null, array_values($items)];
}

function database_validate($database, $collection, $r) {
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
    json_response(['databases' => array_map(fn($id, $d) => ['id' => $id, 'label' => $d['label'], 'collections' => array_map(fn($cid, $l) => ['id' => $cid, 'label' => $l], array_keys($d['collections']), $d['collections'])],
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
    json_response(['database_label' => $def['label'], 'collection_label' => $def['collections'][$c], 'columns' => database_columns($items), 'items' => $items,
        'media_missing' => database_media_missing($items)]);
}

/** Правка коллекции в памяти. $all — все записи файла или базы. Номер в списке пула — позиция среди записей выбранной темы, поэтому переводим его в позицию в $all. */
function database_edit(array $all, $database, $collection, $in, $action): array {
    $visible = array_keys($all);   // таблица показывает все записи коллекции, номер строки — позиция в коллекции
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
    if ($action === 'debug_reset' || !is_array($_SESSION['postmaker_debug'] ?? null)) $_SESSION['postmaker_debug'] = demo_initial_state();
    $s = &$_SESSION['postmaker_debug'];
    $debug = ['status' => 'success', 'debug' => true];
    if ($action === 'vote') $s['pool'] = apply_vote($s['pool'], (string)($in['image'] ?? ''), (string)($in['vote'] ?? 'postpone'), null, function ($r, $c) use (&$s) { $s['history'][$c][] = $r; }) ?? $s['pool'];
    match ($action) {
        'get_current_item' => $s['pool'] ? json_response(content_for_display($s['pool'][0], array_slice($s['pool'], 1, 3))) : json_response(['status' => 'empty', 'message' => 'Демо-пул пуст: нажмите «Сбросить демо».']),
        'settings' => json_response(['topic' => $s['topic'], 'topic2' => '', 'topic_info' => null, 'filters' => default_settings()]),
        'save_topic' => json_response($debug + ['topic' => $s['topic'] = normalize_topic($in['topic'] ?? '') ?: DEFAULT_TOPIC]),
        'topics' => json_response(['current' => null, 'topics' => []]),
        'proposals' => json_response(['items' => proposal_items($s['history']), 'refreshing' => false]),
        'parser_status' => json_response(['running' => false, 'activity' => []]),
        'discovery_status' => json_response(['enabled' => false]),
        'topic_sources' => json_response(['topic' => $s['topic'], 'sources' => []]),
        'suggest_sources' => json_response(['discovered' => null, 'message' => 'В демо-режиме источники не подбираются.']),
        'account_info' => json_response(['debug' => true]),
        'telegram_queue' => json_response(['connected' => false, 'scheduled' => [], 'published' => []]),
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
