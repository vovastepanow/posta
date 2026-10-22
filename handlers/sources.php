<?php
// Вкладка «Источники»: кандидаты по одному, решения по ним, автоподбор, источники текущей темы.

const SOURCE_PLATFORM_FILTERS = ['any' => null, 'telegram' => PLATFORM_TELEGRAM, 'vk' => PLATFORM_VK, 'dvach' => PLATFORM_DVACH];
const SOURCE_REJECT_REASONS = ['off_topic', 'dead', 'other'];
const SOURCE_PREVIEW_ATTEMPTS = 12;
const SOURCE_PREVIEW_POSTS = 6;

function source_candidate($source_id, $platform, $name, $reason, array $extra = []) {
    return ['source_id' => $source_id, 'platform' => $platform, 'name' => $name, 'reason' => $reason] + $extra;
}

/** Доски, треды которых не предлагаем: пользователи стабильно оценивают их посты плохо или отклонено ≥ 3 треда без одобренных. */
function dvach_blocked_boards(array $votes, $topic) {
    $blocked = [];
    foreach (source_feedback_stats($topic) as $key => $s) if (str_starts_with($key, 'dvach:') && $s['votes'] >= SOURCE_FEEDBACK_BAN_VOTES && $s['score'] < SOURCE_FEEDBACK_BAN_SCORE) $blocked[substr($key, 6)] = true;
    $count = [];
    foreach ($votes as $v) if (!empty($v['board'])) $count[$v['board']][$v['vote'] ?? ''] = ($count[$v['board']][$v['vote'] ?? ''] ?? 0) + 1;
    foreach ($count as $board => $c) if (($c['reject'] ?? 0) >= 3 && empty($c['approve'])) $blocked[$board] = true;
    return array_keys($blocked);
}

/** Живые треды на 4 случайных досках темы (до 30 кандидатов). */
function dvach_source_candidates(array $voted, array $blocked, $topic) {
    $boards = array_values(array_diff(dvach_boards_for_topic($topic, false), $blocked));
    shuffle($boards);
    $out = [];
    foreach (array_slice($boards, 0, 4) as $board) foreach (fetch_2ch_json($board)['threads'] ?? [] as $t) {
        $thread = (string)($t['num'] ?? '');
        if (!is_valid_dvach_id($thread) || isset($voted[$sid = "dvach:$board:$thread"]) || dvach_thread_dead_reason($t) !== null) continue;
        $hours = isset($t['lasthit']) ? (int)floor((time() - (int)$t['lasthit']) / 3600) : null;
        $summary = (int)($t['posts_count'] ?? 0) . ' постов' . ($hours === null ? '' : ', последний ' . ($hours < 1 ? 'меньше часа' : ($hours < 48 ? "$hours ч" : intdiv($hours, 24) . ' дн')) . ' назад');
        $out[] = source_candidate($sid, PLATFORM_DVACH, PLATFORM_DVACH . " /$board/ · " . dvach_thread_title($t['subject'] ?? '', $thread),
            (clean_html_text($t['subject'] ?? '') ?: 'Тред с постами и изображениями.') . " · $summary",
            ['board' => $board, 'thread_id' => $thread, 'thread_posts' => (int)($t['posts_count'] ?? 0), 'thread_files' => (int)($t['files_count'] ?? 0), 'last_hit' => (int)($t['lasthit'] ?? 0)]);
        if (count($out) >= 30) return $out;
    }
    return $out;
}

function catalog_source_candidates(array $db, $platform, array $voted, $topic) {
    $out = [];
    foreach ($db[CATALOG_KEYS[$platform]] as $e) {
        $id = ltrim(trim((string)($e['id'] ?? '')), '@');
        if (!catalog_entry_matches_topic($e, $topic) || !is_valid_source_id($platform, $id) || isset($voted[$sid = source_id_for($platform, $id)])) continue;
        $out[] = source_candidate($sid, $platform, source_display_name($platform, $e['name'] ?? $id),
            $platform === PLATFORM_TELEGRAM ? 'Публичный Telegram-канал с недавними постами.' : 'Публичное сообщество ВКонтакте с изображениями в постах.', ['id' => $id]);
    }
    return $out;
}

/** Находки автоподбора по теме, лучшие первыми. */
function suggestion_source_candidates(array $db, $platform, array $voted, $topic) {
    $list = array_filter($db['source_suggestions'], fn($s) => topics_are_equivalent((string)($s['topic'] ?? ''), $topic) && ($platform === null || $s['platform'] === $platform)
        && !isset($voted[$s['source_id']]) && ($s['platform'] !== PLATFORM_VK || vk_is_connected()));
    usort($list, fn($a, $b) => ($b['score'] ?? 0) <=> ($a['score'] ?? 0));
    return array_map(fn($s) => source_candidate($s['source_id'], $s['platform'], $s['name'], $s['reason'] ?? '', ['id' => $s['id'], 'suggested' => true, 'score' => $s['score'] ?? null]), $list);
}

/** Посты и сводка для карточки: url, description, subscribers, view_rate, posts_per_week, last_post, thread_*, dead (мёртвых не показываем). */
function build_source_preview(array $c, $count) {
    $stats = array_fill_keys(['url', 'description'], '') + ['subscribers' => 0] + array_fill_keys(['view_rate', 'posts_per_week', 'last_post', 'thread_posts', 'thread_files', 'dead'], null);
    if ($c['platform'] === PLATFORM_DVACH) {
        return ['posts' => build_dvach_preview_posts($c, $count), 'stats' => ['url' => dvach_post_url($c['board'], $c['thread_id'], $c['thread_id']), 'thread_posts' => $c['thread_posts'] ?? null,
            'thread_files' => $c['thread_files'] ?? null, 'last_post' => ($c['last_hit'] ?? 0) ?: null] + $stats];
    }
    if ($c['platform'] === PLATFORM_TELEGRAM) {
        if (!($info = telegram_channel_info($c['id'] ?? '', max($count, 6)))) return ['posts' => [], 'stats' => ['dead' => 'канала нет или он закрыт'] + $stats];
        $posts = array_values(array_filter($info['posts'], fn($p) => !is_post_outdated($p['posted_at'] ?? 0)));
        $activity = source_activity($info['post_times']);
        $rate = source_view_rate($info['subscribers'], $info['post_views']);
        $stats = ['url' => 'https://t.me/' . rawurlencode($info['id']), 'description' => $info['description'], 'subscribers' => $info['subscribers'], 'view_rate' => $rate,
            'dead' => source_inactivity_reason($activity) ?? source_dead_audience_reason($info['subscribers'], $rate)] + $stats;
    } else {
        $posts = build_vk_preview_posts($c, $count);
        $activity = source_activity(array_column($posts, 'posted_at'));
        $stats = ['url' => 'https://vk.com/' . rawurlencode((string)($c['id'] ?? '')), 'dead' => $posts ? source_inactivity_reason($activity) : null] + $stats;
    }
    return ['posts' => array_slice($posts, 0, $count), 'stats' => ['posts_per_week' => $activity['per_week'] ?? null, 'last_post' => $activity['last_post'] ?? null] + $stats];
}

/** Первый живой кандидат с постами или null. */
function first_previewable(array $candidates) {
    foreach (array_slice($candidates, 0, SOURCE_PREVIEW_ATTEMPTS) as $c) {
        try { $p = build_source_preview($c, SOURCE_PREVIEW_POSTS); } catch (Throwable $e) { error_log("Source preview failed ({$c['platform']}): " . $e->getMessage()); continue; }
        if ($p['stats']['dead'] === null && $p['posts']) return $c + $p;
    }
    return null;
}

function handle_suggest_sources($in) {
    $filter = strtolower(trim((string)($in['platform'] ?? 'any')));
    if (!array_key_exists($filter, SOURCE_PLATFORM_FILTERS)) json_error('Неизвестная платформа источника.');
    $platform = SOURCE_PLATFORM_FILTERS[$filter];
    $wants = fn($p) => $platform === null || $platform === $p;
    $db = load_source_db();
    $topic = current_topic();
    // Решения — по теме: отказ в одной теме не прячет источник в другой; старые решения без темы — для всех
    $votes = array_values(array_filter($db['source_votes'], fn($v) => empty($v['topic']) || topic_same((string)$v['topic'], $topic)));
    $voted = array_flip(array_map('strval', array_column($votes, 'source_id')));
    foreach (array_slice((array)($in['skip'] ?? []), 0, 200) as $id) if (is_string($id) && strlen($id) <= 200) $voted[$id] = true;   // «Отложить» в этой сессии
    $suggested = suggestion_source_candidates($db, $platform, $voted, $topic);
    if (!empty($in['suggested_only'])) {   // лёгкий опрос, пока идёт подбор
        json_response(($found = first_previewable($suggested)) ? ['discovered' => $found] : ['discovered' => null, 'message' => 'Подбор пока ничего не нашёл.']);
    }
    $vk = $wants(PLATFORM_VK) && vk_is_connected();
    $candidates = array_merge($wants(PLATFORM_DVACH) ? dvach_source_candidates($voted, dvach_blocked_boards($votes, $topic), $topic) : [],
        $wants(PLATFORM_TELEGRAM) ? catalog_source_candidates($db, PLATFORM_TELEGRAM, $voted, $topic) : [], $vk ? catalog_source_candidates($db, PLATFORM_VK, $voted, $topic) : []);
    if (!$candidates && !$suggested) {
        json_response(['discovered' => null, 'message' => $platform === PLATFORM_VK && !$vk ? 'Сначала подключите VK OAuth, чтобы искать паблики.' : 'Новых неоценённых источников на этой платформе пока нет.']);
    }
    // Площадки с меньшим числом прошлых решений — первыми, внутри площадки вперемешку; находки подбора — впереди всех
    $by = [];
    foreach ($candidates as $c) $by[$c['platform']][] = $c;
    $counts = array_count_values(array_map(fn($v) => (string)($v['platform'] ?? ''), $votes));
    $platforms = array_keys($by);
    shuffle($platforms);
    usort($platforms, fn($a, $b) => ($counts[$a] ?? 0) <=> ($counts[$b] ?? 0));
    $ordered = $suggested;
    foreach ($platforms as $p) { shuffle($by[$p]); array_push($ordered, ...$by[$p]); }
    if ($found = first_previewable($ordered)) json_response(['discovered' => $found]);
    json_response(['discovered' => null, 'message' => 'Не удалось получить посты с изображениями ни из одного кандидата. Для VK подключите аккаунт, Telegram-каналы должны быть публичными.'], 502);
}

function source_vote_record(array $s, $vote) {
    if (($id = trim((string)($s['source_id'] ?? ''))) === '' || strlen($id) > 600) json_error('Некорректный источник.');
    return ['source_id' => $id, 'platform' => in_array($s['platform'] ?? '', [PLATFORM_DVACH, PLATFORM_TELEGRAM, PLATFORM_VK, PLATFORM_RSS], true) ? $s['platform'] : 'Неизвестно',
        'name' => mb_substr(trim((string)($s['name'] ?? '')), 0, 200) ?: 'Источник', 'vote' => $vote, 'timestamp' => time()]
        + (is_valid_dvach_board($s['board'] ?? '') ? ['board' => (string)$s['board']] : []) + (is_valid_dvach_id($s['thread_id'] ?? '') ? ['thread_id' => (string)$s['thread_id']] : []);
}

/** Одобренный источник — в каталог темы (доска Двача — в доски темы; уже известный канал получает ещё одну тему). */
function add_approved_source_to_catalog(array $db, array $s, $topic) {
    $id = ltrim(trim((string)($s['id'] ?? '')), '@');
    if (($s['platform'] ?? '') === PLATFORM_DVACH) {
        if (!is_valid_dvach_board($board = (string)($s['board'] ?? ''))) return $db;
        if (topic_same(DEFAULT_TOPIC, $topic)) $db['2ch_boards'] = array_values(array_unique([...$db['2ch_boards'], $board]));
        else $db['dvach_topic_boards'][$k = mb_strtolower($topic)] = array_values(array_unique([...$db['dvach_topic_boards'][$k] ?? [], $board]));
        return $db;
    }
    if (!($key = CATALOG_KEYS[$s['platform'] ?? ''] ?? null) || !is_valid_source_id($s['platform'], $id)) return $db;
    foreach ($db[$key] as $i => $e) {
        if (strcasecmp((string)($e['id'] ?? ''), $id) !== 0) continue;
        if (($topics = (array)($e['topics'] ?? [])) && !array_filter($topics, fn($t) => topic_same((string)$t, $topic))) $db[$key][$i]['topics'][] = $topic;
        return $db;
    }
    $name = preg_replace('/^' . preg_quote(SOURCE_NAME_PREFIXES[$s['platform']], '/') . '/u', '', (string)($s['name'] ?? ''));
    $db[$key][] = ['id' => $id, 'name' => mb_substr(trim($name), 0, 200) ?: $id, 'topics' => [$topic]];
    return $db;
}

/** Отказ по теме: источник перестаёт быть её источником (без тем — убирается целиком), у других тем остаётся. */
function remove_rejected_source_from_catalog(array $db, array $s, $topic) {
    $id = ltrim(trim((string)($s['id'] ?? '')), '@');
    if (!($key = CATALOG_KEYS[$s['platform'] ?? ''] ?? null) || $id === '') return $db;
    $kept = [];
    foreach ($db[$key] as $e) {
        if (strcasecmp((string)($e['id'] ?? ''), $id) === 0) {
            if (!($e['topics'] = array_values(array_filter((array)($e['topics'] ?? []), fn($t) => !topic_same((string)$t, $topic))))) continue;
        }
        $kept[] = $e;
    }
    $db[$key] = $kept;
    return $db;
}

function handle_source_vote($in) {
    [$vote, $source] = [$in['vote'] ?? '', $in['source'] ?? null];
    if (!in_array($vote, ['approve', 'reject', 'maybe'], true) || !is_array($source)) json_error('Некорректное решение');
    $topic = current_topic();
    $record = source_vote_record($source, $vote) + ['topic' => $topic] + ($vote === 'reject' && in_array($in['reason'] ?? '', SOURCE_REJECT_REASONS, true) ? ['reason' => $in['reason']] : []);
    update_source_db(function ($db) use ($record, $source, $vote, $topic) {
        if (!array_filter($db['source_votes'], fn($o) => $o['source_id'] === $record['source_id'] && topic_same((string)($o['topic'] ?? ''), $topic))) $db['source_votes'][] = $record;
        // У находки подбора данные берутся с сервера, а не из запроса; сама находка снимается
        $source_topic = $topic;
        foreach ($db['source_suggestions'] as $i => $s) {
            if ($s['source_id'] !== $record['source_id'] || !topic_same((string)($s['topic'] ?? $topic), $topic)) continue;
            [$source, $source_topic] = [['platform' => $s['platform'], 'id' => $s['id'], 'name' => $s['name']], (string)($s['topic'] ?? '') ?: $topic];
            unset($db['source_suggestions'][$i]);
        }
        $db['source_suggestions'] = array_values($db['source_suggestions']);
        return match ($vote) { 'maybe' => $db, 'approve' => add_approved_source_to_catalog($db, $source, $source_topic), 'reject' => remove_rejected_source_from_catalog($db, $source, $source_topic) };
    });
    ok();
}

function handle_discover_sources() {
    request_source_discovery();
    $parser = start_content_parser_if_needed(true);
    $message = ['started' => 'Автоподбор запущен. Ход поиска — в журнале.', 'running' => 'Парсер уже работает: автоподбор начнётся при следующем запуске.',
        'no_session' => 'Окно приложения неактивно: обновите страницу, и автоподбор начнётся.'][$parser] ?? 'Не удалось запустить парсер на сервере.';
    json_response(['status' => $parser === 'unavailable' ? 'error' : 'success', 'parser' => $parser, 'message' => $message], $parser === 'unavailable' ? 503 : 200);
}

function handle_discovery_status() {
    $db = load_source_db();
    $topic = current_topic();
    $entries = array_filter(array_merge($db["telegram_channels"], $db["vk_publics"], $db["rss_feeds"]), fn($e) => catalog_entry_matches_topic($e, $topic));
    $entry = topic_ensure($topic);
    json_response(['enabled' => SOURCE_DISCOVERY_ENABLED, 'topic' => $topic, 'topic_info' => $entry ? topic_public($entry, $db) : null, 'last_topic' => $db['discovery']['topic'],
        'last_run' => (int)$db['discovery']['last_run'], 'requested' => (bool)$db['discovery']['requested'],
        'running' => time() - (int)($db['discovery']['running_since'] ?? 0) < SOURCE_DISCOVERY_MAX_SECONDS + 120,
        'auto_added' => count(array_filter($entries, fn($e) => !empty($e['auto']))), 'frozen' => count(array_filter($entries, fn($e) => isset($e['frozen_until']))),
        'suggestions' => count(array_filter($db['source_suggestions'], fn($s) => topics_are_equivalent((string)($s['topic'] ?? ''), $topic))), 'vk_connected' => vk_is_connected()]);
}

/** Источники текущей темы: каналы, паблики и доски Двача. */
function handle_topic_sources() {
    $db = load_source_db();
    $topic = current_topic();
    $cards = [];
    foreach (CATALOG_KEYS as $platform => $key) foreach ($db[$key] as $e) {
        if (!catalog_entry_matches_topic($e, $topic)) continue;
        $id = ltrim((string)($e['id'] ?? ''), '@');
        $subs = (int)($e['audience']['subscribers'] ?? 0);
        $tg = $platform === PLATFORM_TELEGRAM;
        $rss = $platform === PLATFORM_RSS;
        if ($rss && !is_valid_rss_url($id)) continue;
        $cards[] = ['source_id' => source_id_for($platform, $id), 'platform' => $platform, 'id' => $id, 'name' => (string)($e['name'] ?? $id),
            'url' => $rss ? $id : ($tg ? 'https://t.me/' : 'https://vk.com/') . rawurlencode($id), 'handle' => $rss ? (string)parse_url($id, PHP_URL_HOST) : ($tg ? '@' : 'vk.com/') . $id, 'topics' => array_values((array)($e['topics'] ?? [])),
            'own' => !empty($e['topics']) && catalog_entry_matches_topic($e, $topic, true), 'auto' => !empty($e['auto']), 'score' => isset($e['score']) ? (int)$e['score'] : null,
            'subscribers' => $subs, 'subscribers_label' => $subs ? format_subscribers($subs) : '', 'view_rate' => isset($e['audience']['view_rate']) ? (float)$e['audience']['view_rate'] : null,
            'activity' => is_array($e['activity'] ?? null) ? describe_source_activity($e['activity']) : '', 'frozen' => isset($e['frozen_until']),
            'frozen_reason' => (string)($e['frozen_reason'] ?? ''), 'avatar' => (string)($e['avatar'] ?? ''), 'preview' => array_slice((array)($e['preview'] ?? []), 0, 4)];
    }
    $names = dvach_board_names();
    foreach (dvach_boards_for_topic($topic, false) as $b) {
        $title = $names[$b]['name'] ?? '';
        $cards[] = ['source_id' => "dvach-board:$b", 'platform' => PLATFORM_DVACH, 'id' => $b, 'name' => $title !== '' ? "/$b/ — $title" : "Доска /$b/", 'category' => $names[$b]['category'] ?? '',
            'url' => 'https://2ch.hk/' . rawurlencode($b) . '/', 'handle' => "2ch.hk/$b/", 'topics' => [], 'own' => true, 'auto' => false, 'score' => null, 'subscribers' => 0,
            'subscribers_label' => '', 'view_rate' => null, 'activity' => '', 'frozen' => false, 'frozen_reason' => '', 'avatar' => '', 'preview' => []];
    }
    usort($cards, fn($a, $b) => [$b['own'], !$b['frozen'], $b['subscribers']] <=> [$a['own'], !$a['frozen'], $a['subscribers']]);
    $entry = topic_lookup($topic);
    json_response(['topic' => $topic, 'topic_info' => $entry ? topic_public($entry, $db) : null, 'sources' => $cards]);
}

/** Убрать источник из темы; решение запоминается, автоподбор его не вернёт. */
function handle_remove_topic_source($in) {
    [$platform, $id, $topic] = [(string)($in['platform'] ?? ''), ltrim(trim((string)($in['id'] ?? '')), '@'), current_topic()];
    if ($platform === PLATFORM_DVACH) {
        if (!is_valid_dvach_board($id)) json_error('Некорректная доска.');
        update_source_db(function ($db) use ($id, $topic) {
            if (topic_same(DEFAULT_TOPIC, $topic)) $db['2ch_boards'] = array_values(array_diff($db['2ch_boards'], [$id]));
            else $db['dvach_topic_boards'][$k = mb_strtolower($topic)] = array_values(array_diff($db['dvach_topic_boards'][$k] ?? [], [$id]));
            return $db;
        });
        ok();
    }
    if (!isset(CATALOG_KEYS[$platform]) || $id === '' || strlen($id) > 500) json_error('Некорректный источник.');
    $source = ['platform' => $platform, 'id' => $id, 'name' => (string)($in['name'] ?? $id), 'source_id' => source_id_for($platform, $id)];
    $record = source_vote_record($source, 'reject') + ['topic' => $topic, 'reason' => 'other'];
    update_source_db(function ($db) use ($record, $source, $topic) {
        $db['source_votes'] = [...array_values(array_filter($db['source_votes'], fn($o) => !($o['source_id'] === $record['source_id'] && topic_same((string)($o['topic'] ?? ''), $topic)))), $record];
        return remove_rejected_source_from_catalog($db, $source, $topic);
    });
    ok();
}

/** «Добавить источник»: ссылка на Telegram-канал, паблик VK или сайт/RSS — в каталог с текущей темой. */
function handle_add_topic_source($in) {
    $link = (string)($in['link'] ?? '');
    if (mb_strlen($link) > SOURCE_ADD_MAX_LINK) json_error('Ссылка слишком длинная.');
    if (topic_save_denied($topic = current_topic()) !== null) json_error('Тема «' . $topic . '» недоступна: выберите другую тему.');
    ok(source_add_manual($link, $topic));
}
