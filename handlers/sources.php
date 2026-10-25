<?php
// Вкладка «Источники»: кандидаты по одному, решения по ним, автоподбор, источники текущей темы.

// Фильтр выпадающего списка: площадка, а для RSS — вид ленты (YouTube, Pinterest… определяются по адресу ленты)
const SOURCE_PLATFORM_FILTERS = ['any' => null, 'telegram' => PLATFORM_TELEGRAM, 'vk' => PLATFORM_VK, 'tiktok' => PLATFORM_TIKTOK, 'youtube' => PLATFORM_RSS,
    'shorts' => PLATFORM_RSS, 'ok' => PLATFORM_OK, 'pinterest' => PLATFORM_RSS, 'tumblr' => PLATFORM_RSS, 'rss' => PLATFORM_RSS, 'fourchan' => PLATFORM_4CHAN, 'dvach' => PLATFORM_DVACH];
const SOURCE_KIND_FILTERS = ['youtube' => 'YouTube', 'shorts' => 'YouTube Shorts', 'pinterest' => 'Pinterest', 'tumblr' => 'Tumblr', 'rss' => 'RSS'];
const SOURCE_REJECT_REASONS = ['off_topic', 'dead', 'other'];
const SOURCE_PREVIEW_ATTEMPTS = 12;
const SOURCE_PREVIEW_POSTS = 20;        // постов в превью кандидата: Telegram, VK, RSS, ОК (одна страница канала — ~20 постов)
const SOURCE_PREVIEW_POSTS_TIKTOK = 8;  // TikTok — yt-dlp, медленно
const SOURCE_PREVIEW_POSTS_SLOW = 6;    // Двач и 4chan — как раньше
const SOURCE_PREVIEW_TEXT_MAX = 2000;   // текст поста в превью (для просмотрщика постов), ответ остаётся до ~150 КБ

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
        if (ua_source_blocked($sid, ['title' => (string)($e['name'] ?? '')]) !== null) continue;   // украинские — не показываем (UA_FILTER_PROMPT.md, B8)
        $out[] = source_candidate($sid, $platform, source_display_name($platform, $e['name'] ?? $id),
            match ($platform) { PLATFORM_TELEGRAM => 'Публичный Telegram-канал с недавними постами.', PLATFORM_RSS => 'Лента сайта или блога с картинками в записях.', PLATFORM_OK => 'Открытая группа в Одноклассниках с картинками в постах.', PLATFORM_TIKTOK => 'Аккаунт TikTok с короткими видео.', default => 'Публичное сообщество ВКонтакте с изображениями в постах.' },
            ['id' => $id] + ($platform === PLATFORM_RSS ? ['kind' => !empty($e['news']) ? news_site_kind($id) : rss_feed_kind($id)] : []));
    }
    return $out;
}

/** Находки автоподбора по теме, лучшие первыми. */
function suggestion_source_candidates(array $db, $platform, array $voted, $topic) {
    $list = array_filter($db['source_suggestions'], fn($s) => topics_are_equivalent((string)($s['topic'] ?? ''), $topic) && ($platform === null || $s['platform'] === $platform)
        && !isset($voted[$s['source_id']]) && ($s['platform'] !== PLATFORM_VK || vk_can_read())
        && ua_source_blocked((string)$s['source_id'], ['title' => (string)($s['name'] ?? '')]) === null);   // украинские — не предлагаем (UA_FILTER_PROMPT.md, B8)
    // Ранг: балл, повторные находки и поправка по виду находки (решения пользователя прошлых прогонов)
    $bonus = [];
    $rank = function ($s) use (&$bonus, $db, $topic) {
        $kind = $s['origin']['kind'] ?? 'unknown';
        $bonus[$kind] ??= discovery_origin_bonus($db['source_votes'], $topic, $kind);
        return suggestion_rank($s, $bonus[$kind]);
    };
    usort($list, fn($a, $b) => $rank($b) <=> $rank($a));
    return array_map(fn($s) => source_candidate($s['source_id'], $s['platform'], $s['name'], discovery_reason_with_refs($s['reason'] ?? '', (array)($s['refs'] ?? [])), ['id' => $s['id'], 'suggested' => true, 'score' => $s['score'] ?? null]
        + ($s['platform'] === PLATFORM_RSS ? ['kind' => rss_feed_kind((string)$s['id'])] : [])), $list);
}

/** Посты и сводка для карточки: url, description, subscribers, view_rate, posts_per_week, last_post, thread_*, dead (мёртвых не показываем). */
function build_source_preview(array $c, $count) {
    $stats = array_fill_keys(['url', 'description'], '') + ['subscribers' => 0] + array_fill_keys(['view_rate', 'posts_per_week', 'last_post', 'thread_posts', 'thread_files', 'dead'], null);
    if ($c['platform'] === PLATFORM_4CHAN) return ['posts' => array_map('source_preview_post', fourchan_preview($c, $count)), 'stats' => ['url' => fourchan_post_url($c['board'], (int)$c['thread_id'], (int)$c['thread_id']), 'thread_posts' => $c['thread_posts'] ?? null, 'last_post' => ($c['last_hit'] ?? 0) ?: null, 'dead' => null, 'description' => '', 'subscribers' => 0, 'view_rate' => null, 'posts_per_week' => null, 'thread_files' => null]];
    if ($c['platform'] === PLATFORM_DVACH) {
        return ['posts' => array_map('source_preview_post', build_dvach_preview_posts($c, $count)), 'stats' => ['url' => dvach_post_url($c['board'], $c['thread_id'], $c['thread_id']), 'thread_posts' => $c['thread_posts'] ?? null,
            'thread_files' => $c['thread_files'] ?? null, 'last_post' => ($c['last_hit'] ?? 0) ?: null] + $stats];
    }
    if ($c['platform'] === PLATFORM_TELEGRAM) {
        if (!($info = telegram_channel_info($c['id'] ?? '', 0))) return ['posts' => [], 'stats' => ['dead' => 'канала нет или он закрыт'] + $stats];   // 0 — нужна только сводка канала
        $feed = telegram_channel_media_posts($info['id']);
        // возраст постов не фильтруем: «давно не публикует» решает activity ниже; большие видео — через личный аккаунт, если он подключён
        $posts = app_user_bind() !== '' ? telegram_big_videos_via_account($feed['posts'], app_user_id()) : $feed['posts'];
        $text_posts = $feed['text_posts'];   // текстовые посты идут в ленту вперемешку с медиа
        $activity = source_activity($info['post_times']);
        $rate = source_view_rate($info['subscribers'], $info['post_views']);
        $stats = ['url' => 'https://t.me/' . rawurlencode($info['id']), 'description' => $info['description'], 'subscribers' => $info['subscribers'], 'view_rate' => $rate,
            'dead' => source_inactivity_reason($activity) ?? source_dead_audience_reason($info['subscribers'], $rate),
            'media_total' => $feed['media_total'], 'text_total' => $feed['text_total'], 'posts_total' => $feed['posts_total'], 'pages' => $feed['pages']] + $stats;
    } elseif ($c['platform'] === PLATFORM_RSS) {
        $posts = rss_preview_posts($c, $count);
        $activity = source_activity(array_column($posts, 'posted_at'));
        $stats = ['url' => (string)($c['id'] ?? ''), 'dead' => $posts ? source_inactivity_reason($activity) : 'лента не открывается или в ней нет картинок'] + $stats;
    } elseif ($c['platform'] === PLATFORM_OK) {
        $posts = ok_preview_posts($c, $count);
        $activity = source_activity(array_column($posts, 'posted_at'));
        $stats = ['url' => 'https://ok.ru/group/' . rawurlencode((string)($c['id'] ?? '')), 'dead' => $posts ? source_inactivity_reason($activity) : 'группа не открывается или в ней нет картинок'] + $stats;
    } elseif ($c['platform'] === PLATFORM_TIKTOK) {
        $posts = tiktok_preview_posts($c, $count);
        $activity = source_activity(array_column($posts, 'posted_at'));
        $stats = ['url' => 'https://www.tiktok.com/@' . rawurlencode((string)($c['id'] ?? '')), 'dead' => $posts ? source_inactivity_reason($activity) : 'аккаунт не открывается или в нём нет коротких видео'] + $stats;
    } else {
        $posts = build_vk_preview_posts($c, $count);
        $activity = source_activity(array_column($posts, 'posted_at'));
        $stats = ['url' => 'https://vk.com/' . rawurlencode((string)($c['id'] ?? '')), 'dead' => $posts ? source_inactivity_reason($activity) : null] + $stats;
    }
    $posts = array_merge($posts, $text_posts ?? []);
    usort($posts, fn($a, $b) => (int)($b['posted_at'] ?? 0) <=> (int)($a['posted_at'] ?? 0));   // новые первыми, медиа и текст вместе
    // Ролики ставятся в очередь по сырым постам (у превью нет video_page): первые — сами, у них видно «Видео загружается…»
    $posts = array_slice($posts, 0, $count);
    $posts = array_map('source_preview_post', app_user_bind() !== '' ? video_preview_prepare($posts, app_user_id()) : video_preview_apply($posts));   // без пользователя (CLI) — без очереди
    return ['posts' => $posts, 'stats' => ['posts_per_week' => $activity['per_week'] ?? null, 'last_post' => $activity['last_post'] ?? null] + $stats];
}

/** Пост превью в одном виде для всех площадок: текст, адрес, дата, медиа по порядку, просмотры/реакции/комментарии (null — нет). */
function source_preview_post(array $p): array {
    $engagement = is_array($p['engagement'] ?? null) ? $p['engagement'] : [];
    $reactions = $engagement['reactions'] ?? null;
    $text = (string)($p['text'] ?? '');
    return [
        'text' => $text === EMPTY_POST_TEXT ? '' : mb_substr(trim($text), 0, SOURCE_PREVIEW_TEXT_MAX),
        'url' => (string)($p['url'] ?? ''),
        'posted_at' => (int)($p['posted_at'] ?? 0),
        'text_post' => !empty($p['text_post']),
        'media' => source_preview_media($p),
        'views' => isset($p['views']) ? (int)$p['views'] : (isset($engagement['views']) ? (int)$engagement['views'] : null),
        'reactions' => isset($p['likes']) ? (int)$p['likes'] : (is_array($reactions) ? (int)array_sum($reactions) : null),
        'comments' => isset($p['comments']) ? (int)$p['comments'] : null,
    ];
}

/** Медиа поста по порядку: [{kind: image|video, src, poster, page, pending, unavailable}]. Видео без файла — с пометкой. */
function source_preview_media(array $p): array {
    $https = fn($url) => is_string($url) && str_starts_with($url, 'https://') ? $url : '';
    $page = (string)($p['url'] ?? '');
    $out = [];
    if (!empty($p['gallery'])) {   // альбом Telegram: у каждого элемента свой файл
        foreach ($p['gallery'] as $g) {
            $poster = $https($g['image'] ?? '');
            $video = $https($g['video'] ?? '');
            if ($video !== '') $out[] = ['kind' => 'video', 'src' => $video, 'poster' => $poster, 'page' => $page, 'pending' => false, 'unavailable' => false];
            elseif ($poster !== '') $out[] = ['kind' => 'image', 'src' => $poster, 'poster' => '', 'page' => '', 'pending' => false, 'unavailable' => false];
        }
        return $out;
    }
    $images = array_values(array_unique(array_filter(array_merge([$p['image'] ?? ''], (array)($p['images'] ?? [])), fn($u) => $https($u) !== '')));
    $poster = $images[0] ?? '';
    $video = $https($p['video'] ?? '');
    if ($video === '') $video = media_cache_existing((string)($p['video_local'] ?? ''));   // ролик, скачанный в кэш сайта
    $videoPage = (string)($p['video_page'] ?? '');
    $unavailable = !empty($p['video_unavailable']);
    if ($video !== '') $out[] = ['kind' => 'video', 'src' => $video, 'poster' => $poster, 'page' => $page, 'pending' => false, 'unavailable' => false];
    elseif ($unavailable) $out[] = ['kind' => 'video', 'src' => '', 'poster' => $poster, 'page' => $page, 'pending' => false, 'unavailable' => true];
    elseif ($videoPage !== '') $out[] = ['kind' => 'video', 'src' => '', 'poster' => $poster, 'page' => $videoPage, 'pending' => !empty($p['video_pending']), 'unavailable' => false];
    $hasVideo = $video !== '' || $unavailable || $videoPage !== '';   // обложка ролика — не отдельная картинка
    foreach (array_slice($images, $hasVideo ? 1 : 0) as $img) $out[] = ['kind' => 'image', 'src' => $img, 'poster' => '', 'page' => '', 'pending' => false, 'unavailable' => false];
    return $out;
}

/**
 * Живой поиск аккаунтов TikTok прямо по нажатию «Найти источник»: в каталоге TikTok обычно пусто, а полный автоподбор долгий.
 * Запросы — название темы и первый запрос её профиля (без вызова модели), до двух поисков через Chrome.
 */
function tiktok_live_candidates(array $voted, $topic): array {
    @set_time_limit(180);
    $e = topic_lookup($topic);
    $queries = array_values(array_unique(array_filter([discovery_clean_query($e['title'] ?? $topic), discovery_clean_query((string)(($e['profile']['queries'] ?? [])[0] ?? ''))])));
    $out = [];
    foreach (array_slice($queries, 0, 2) as $q) foreach (tiktok_search_users($q) as $user) {
        $sid = source_id_for(PLATFORM_TIKTOK, $user);
        if (!isset($voted[$sid]) && !isset($out[$sid])) $out[$sid] = source_candidate($sid, PLATFORM_TIKTOK, source_display_name(PLATFORM_TIKTOK, $user), "Найден поиском TikTok по запросу «{$q}».", ['id' => $user]);
    }
    return array_values($out);
}

/** Сколько постов брать в превью по площадке. */
function source_preview_count($platform) {
    return match ($platform) {
        PLATFORM_TIKTOK => SOURCE_PREVIEW_POSTS_TIKTOK,
        PLATFORM_DVACH, PLATFORM_4CHAN => SOURCE_PREVIEW_POSTS_SLOW,
        default => SOURCE_PREVIEW_POSTS
    };
}

/** Первый живой кандидат с медиа-постами или текстовыми постами (чисто текстовые каналы тоже годятся) или null. */
function first_previewable(array $candidates) {
    foreach (array_slice($candidates, 0, SOURCE_PREVIEW_ATTEMPTS) as $c) {
        try { $p = build_source_preview($c, source_preview_count($c['platform'])); } catch (Throwable $e) { error_log("Source preview failed ({$c['platform']}): " . $e->getMessage()); continue; }
        if ($p['stats']['dead'] === null && $p['posts']) {   // текстовые посты — в той же ленте, отдельно не считаем
            return $c + $p;
        }
    }
    return null;
}

/** Площадки, где автоподбор ищет сам (остальные — только ссылкой). */
function source_auto_searchable($platform, $kind = null): bool {
    if ($platform === PLATFORM_RSS) return in_array($kind, ['YouTube', 'YouTube Shorts'], true);
    return $platform === null || in_array($platform, [PLATFORM_TELEGRAM, PLATFORM_VK, PLATFORM_DVACH, PLATFORM_4CHAN, PLATFORM_OK, PLATFORM_TIKTOK], true);
}

/** Честное пояснение, почему новых источников нет, и действие: подключить VK, добавить ссылку или заказать поиск. */
function discovery_empty_message($platform, $kind, $vk_connected): array {
    if ($platform === PLATFORM_VK && !$vk_connected) return ['message' => 'Подключите VK: без этого паблики не ищутся.', 'action' => 'connect_vk'];
    if (!source_auto_searchable($platform, $kind)) return ['message' => 'Сами не находятся: добавьте ссылку в «Добавить источник» выше.', 'action' => 'add_link'];
    return ['message' => 'Пока ничего нового. Автоподбор ищет и здесь — загляните позже.', 'action' => 'discover'];
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
    $kind = SOURCE_KIND_FILTERS[$filter] ?? null;   // виды RSS: YouTube, Shorts, Pinterest, Tumblr, обычные ленты
    // Фильтр «Сайты и блоги» (RSS) берёт и новостные сайты: у них метка «Сайт · …»
    $kind_ok = fn($c) => $kind === null || ($c['kind'] ?? '') === $kind || ($kind === 'RSS' && str_starts_with((string)($c['kind'] ?? ''), 'Сайт · '));
    $suggested = array_values(array_filter(suggestion_source_candidates($db, $platform, $voted, $topic), $kind_ok));
    if (!empty($in['suggested_only'])) {   // лёгкий опрос, пока идёт подбор
        json_response(($found = first_previewable($suggested)) ? ['discovered' => $found] : ['discovered' => null, 'message' => 'Подбор пока ничего не нашёл.']);
    }
    $vk = $wants(PLATFORM_VK) && vk_can_read();
    $candidates = array_merge($wants(PLATFORM_DVACH) ? dvach_source_candidates($voted, dvach_blocked_boards($votes, $topic), $topic) : [],
        $wants(PLATFORM_4CHAN) ? fourchan_source_candidates($voted, $topic) : [],
        $wants(PLATFORM_TELEGRAM) ? catalog_source_candidates($db, PLATFORM_TELEGRAM, $voted, $topic) : [],
        $wants(PLATFORM_RSS) ? catalog_source_candidates($db, PLATFORM_RSS, $voted, $topic) : [], $vk ? catalog_source_candidates($db, PLATFORM_VK, $voted, $topic) : [],
        $wants(PLATFORM_OK) ? catalog_source_candidates($db, PLATFORM_OK, $voted, $topic) : [], $wants(PLATFORM_TIKTOK) ? catalog_source_candidates($db, PLATFORM_TIKTOK, $voted, $topic) : []);
    $candidates = array_values(array_filter($candidates, $kind_ok));
    if ($platform === PLATFORM_TIKTOK && !$candidates && !$suggested) {   // каталог TikTok пуст — ищем сразу, а не отсылаем к автоподбору
        if (tiktok_search_paused()) json_response(['discovered' => null, 'message' => 'TikTok не отдаёт поиск (похоже, капча): повторим завтра. Пока добавьте аккаунт ссылкой в «Добавить источник» выше.', 'action' => 'add_link']);
        if (!($candidates = tiktok_live_candidates($voted, $topic)) && !empty($GLOBALS['tiktok_search_error'])) json_response(['discovered' => null, 'message' => 'Не удалось запустить поиск в TikTok (' . $GLOBALS['tiktok_search_error'] . '). Подробности — в журнале ошибок Apache; пока добавьте аккаунт ссылкой в «Добавить источник» выше.', 'action' => 'add_link']);
        if (!$candidates) json_response(['discovered' => null, 'message' => 'TikTok по этой теме аккаунтов не нашёл. Добавьте аккаунт ссылкой в «Добавить источник» выше.', 'action' => 'add_link']);
    }
    if (!$candidates && !$suggested) json_response(['discovered' => null] + discovery_empty_message($platform, $kind, $vk));
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
    return ['source_id' => $id, 'platform' => in_array($s['platform'] ?? '', [PLATFORM_DVACH, PLATFORM_4CHAN, PLATFORM_TELEGRAM, PLATFORM_VK, PLATFORM_RSS, PLATFORM_OK, PLATFORM_TIKTOK], true) ? $s['platform'] : 'Неизвестно',
        'name' => mb_substr(trim((string)($s['name'] ?? '')), 0, 200) ?: 'Источник', 'vote' => $vote, 'timestamp' => time()]
        + (is_valid_dvach_board($s['board'] ?? '') ? ['board' => (string)$s['board']] : []) + (is_valid_dvach_id($s['thread_id'] ?? '') ? ['thread_id' => (string)$s['thread_id']] : [])
        + (($s['platform'] ?? '') === PLATFORM_RSS && is_valid_rss_url((string)($s['id'] ?? '')) ? ['kind' => rss_feed_kind((string)$s['id'])] : []);
}

/** Одобренный источник — в каталог темы (доска Двача — в доски темы; уже известный канал получает ещё одну тему). */
function add_approved_source_to_catalog(array $db, array $s, $topic) {
    $id = ltrim(trim((string)($s['id'] ?? '')), '@');
    if (($s['platform'] ?? '') === PLATFORM_4CHAN) {   // одобренная доска — в доски темы
        fourchan_approve_board($topic, (string)($s['board'] ?? ''));
        return $db;
    }
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
        // Происхождение — из находки подбора (до её снятия), у каталога и досок — «каталог»
        $found = null;
        foreach ($db['source_suggestions'] as $s) if ($s['source_id'] === $record['source_id'] && topic_same((string)($s['topic'] ?? $topic), $topic)) { $found = $s; break; }
        $record += source_vote_origin($found);
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
    $entries = array_filter(array_merge($db["telegram_channels"], $db["vk_publics"], $db["rss_feeds"], $db["ok_groups"], $db["tiktok_accounts"]), fn($e) => catalog_entry_matches_topic($e, $topic));
    $entry = topic_ensure($topic);
    json_response(['enabled' => SOURCE_DISCOVERY_ENABLED, 'topic' => $topic, 'topic_info' => $entry ? topic_public($entry, $db) : null, 'last_topic' => $db['discovery']['topic'],
        'last_run' => (int)$db['discovery']['last_run'], 'requested' => (bool)$db['discovery']['requested'],
        'running' => time() - (int)($db['discovery']['running_since'] ?? 0) < SOURCE_DISCOVERY_MAX_SECONDS + 120,
        'auto_added' => count(array_filter($entries, fn($e) => !empty($e['auto']))), 'frozen' => count(array_filter($entries, fn($e) => isset($e['frozen_until']))),
        'suggestions' => count(array_filter($db['source_suggestions'], fn($s) => topics_are_equivalent((string)($s['topic'] ?? ''), $topic))), 'vk_connected' => vk_is_connected()]);
}

/** Включить или выключить источник для этой темы у этого пользователя (настройки, общий каталог не трогаем). */
function handle_source_mute($in) {
    $sid = (string)($in['source_id'] ?? '');
    if (!preg_match('/^[a-z]+:\S{1,600}$/', $sid)) json_error('Некорректный источник.');
    $settings = db_load_settings();
    $topic = topic_norm(current_topic());
    $settings['source_overrides'][$topic][$sid] = !empty($in['on']) ? 'on' : 'off';
    db_save_settings($settings);
    ok(['source_id' => $sid, 'on' => !empty($in['on'])]);
}

/** Источники текущей темы: каналы, паблики и доски Двача. */
function handle_topic_sources() {
    $db = load_source_db();
    $topic = current_topic();
    $cards = [];
    $mutes = source_user_mutes(history_load(), $topic);   // выключено по оценкам пользователя (часть F)
    $overrides = db_load_settings()['source_overrides'][topic_norm($topic)] ?? [];
    foreach (CATALOG_KEYS as $platform => $key) foreach ($db[$key] as $e) {
        if (!catalog_entry_matches_topic($e, $topic) || ua_source_blocked(source_id_for($platform, ltrim((string)($e['id'] ?? ''), '@')), ['title' => (string)($e['name'] ?? '')]) !== null) continue;
        $id = ltrim((string)($e['id'] ?? ''), '@');
        $subs = (int)($e['audience']['subscribers'] ?? 0);
        $rss = $platform === PLATFORM_RSS;
        if ($rss && !is_valid_rss_url($id)) continue;
        if ($platform === PLATFORM_OK && !is_valid_ok_group($id)) continue;
        if ($platform === PLATFORM_TIKTOK && !is_valid_tiktok_user($id)) continue;
        $cards[] = ['source_id' => source_id_for($platform, $id), 'platform' => $platform, 'id' => $id, 'name' => (string)($e['name'] ?? $id),
            'url' => match ($platform) { PLATFORM_RSS => $id, PLATFORM_OK => 'https://ok.ru/group/' . $id, PLATFORM_TIKTOK => 'https://www.tiktok.com/@' . rawurlencode($id), PLATFORM_TELEGRAM => 'https://t.me/' . rawurlencode($id), default => 'https://vk.com/' . rawurlencode($id) },
            'handle' => match ($platform) { PLATFORM_RSS => (string)parse_url($id, PHP_URL_HOST), PLATFORM_OK => 'ok.ru/group/' . $id, PLATFORM_TIKTOK => 'tiktok.com/@' . $id, PLATFORM_TELEGRAM => '@' . $id, default => 'vk.com/' . $id },
            'topics' => array_values((array)($e['topics'] ?? [])),
            'kind' => $rss ? (!empty($e['news']) ? news_site_kind($id) : rss_feed_kind($id)) : $platform,
            'own' => !empty($e['topics']) && catalog_entry_matches_topic($e, $topic, true), 'auto' => !empty($e['auto']), 'score' => isset($e['score']) ? (int)$e['score'] : null,
            'source_id' => source_id_for($platform, $id), 'muted' => false,   // источники по оценкам больше не выключаются: собираем отовсюду
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

/** Загрузка своих картинок (multipart: files[], text, album). */
function handle_own_upload() {
    // Если запрос больше post_max_size, PHP отбрасывает тело целиком: $_FILES пуст
    if (!$_FILES && (int)($_SERVER['CONTENT_LENGTH'] ?? 0) > 0) json_error('Слишком много за раз: до ' . ini_get('post_max_size') . ' в одной загрузке.');
    set_time_limit(120);
    ok(own_upload(own_files_list($_FILES['files'] ?? []), (string)($_POST['text'] ?? ''), ($_POST['album'] ?? '') === '1'));
}

/** Импорт CSV (multipart: csv). */
function handle_own_import_csv() {
    if (!$_FILES && (int)($_SERVER['CONTENT_LENGTH'] ?? 0) > 0) json_error('Файл слишком большой для этого сервера: до ' . ini_get('upload_max_filesize') . '.');
    $f = $_FILES['csv'] ?? null;
    if (!$f || $f['error'] === UPLOAD_ERR_NO_FILE) json_error('Выберите CSV-файл.');
    if ($f['error'] !== UPLOAD_ERR_OK || !is_uploaded_file($f['tmp_name'])) json_error(own_upload_error_text((int)$f['error']));
    set_time_limit(300);
    ok(own_import_csv((string)file_get_contents($f['tmp_name'])));
}

/** «Свой контент»: твиты X по ссылкам (одна ссылка в строке; body.links — массив или текст). */
function handle_own_import_x($in) {
    set_time_limit(180);
    $links = is_array($in['links'] ?? null) ? $in['links'] : preg_split('/\R/', (string)($in['links'] ?? ''));
    ok(x_import_links($links));
}

/** «Лучшие посты источника»: поиск по ссылке за период с сортировкой (кэш 15 минут). Параметры — в строке запроса. */
function handle_best_posts() {
    if (topic_save_denied($topic = current_topic()) !== null) json_error('Тема «' . $topic . '» недоступна: выберите другую тему.');
    $link = trim((string)($_GET['link'] ?? ''));
    if ($link === '' || mb_strlen($link) > SOURCE_ADD_MAX_LINK) json_error('Вставьте ссылку на канал, паблик или группу.');
    $type = (string)($_GET['type'] ?? 'any');
    if (!in_array($type, BEST_POSTS_TYPES, true)) json_error('Неизвестный тип постов.');
    ok(best_posts_public(best_posts_search($link, (int)($_GET['days'] ?? 7), (string)($_GET['sort'] ?? 'date'), $type, mb_substr(trim((string)($_GET['phrase'] ?? '')), 0, 100))));
}

/** Выбранные посты «Лучших» → подборка пользователя (key — из поиска, tokens — подписи постов). */
function handle_best_posts_take($in) {
    if (topic_save_denied(current_topic()) !== null) json_error('Тема «' . current_topic() . '» недоступна: выберите другую тему.');
    ok(['added' => best_posts_take((string)($in['key'] ?? ''), (array)($in['tokens'] ?? []))]);
}

/** «Зеркало»: список зеркал пользователя. */
function handle_mirror_list() {
    ok(['mirrors' => mirror_list(app_user_id())]);
}

/** «Зеркало»: новое зеркало; тема 18+ — ошибка, канал — только свой (в mirror_save). */
function handle_mirror_save($in) {
    if (topic_save_denied($topic = current_topic()) !== null) json_error('Тема «' . $topic . '» недоступна: выберите другую тему.');
    ok(['mirror' => mirror_save(app_user_id(), $in)]);
}

function handle_mirror_delete($in) {
    mirror_delete(app_user_id(), (string)($in['id'] ?? ''));
    ok();
}

/** «Свой контент»: картинки из публичной папки Яндекс Диска (до 100 файлов за нажатие, продолжение — повторным нажатием). */
function handle_own_import_yadisk($in) {
    set_time_limit(120);
    ok(own_import_yadisk((string)($in['url'] ?? '')));
}

// ---- Ролик по кнопке: Telegram (через личный аккаунт пользователя), TikTok и YouTube (yt-dlp); состояние — video_preview_* в lib/ytdlp.php ----

const TGU_NO_ACCOUNT_TEXT = 'Для больших видео подключите личный аккаунт Telegram: Личный кабинет → Соцсети.';

/** {url}: поставить ролик адреса в очередь скачивания; Telegram без подключённого аккаунта — 409. */
/** Перевод текста поста для просмотрщика источника: {text, lang, note} или {} (галочка «Автоперевод» выключена, текст русский, сервис не ответил). */
function handle_translate_text($in) {
    $text = trim((string)($in['text'] ?? ''));
    if ($text === '' || !load_settings()['auto_translate']) json_response([]);
    try { $translated = translate_to_russian($text, 6.0); } catch (Throwable $e) { translate_log('ошибка перевода: ' . $e->getMessage()); $translated = null; }
    json_response($translated ? $translated + ['note' => 'Переведено с ' . language_name_genitive($translated['lang'])] : []);
}

function handle_video_fetch($in) {
    $url = (string)($in['url'] ?? '');
    $via = video_page_via($url) ?? json_error('Нужен адрес поста Telegram, видео TikTok или ролика YouTube (watch или Shorts).');
    if ($via === 'tgu' && telegram_user_connection() === null) json_error(TGU_NO_ACCOUNT_TEXT, 409);
    $r = video_fetch_enqueue($url, $via, app_user_id(), true);   // открыт в окне и его ждут — первым в очередь
    if ($r['limited']) json_error('Уже скачивается максимум роликов с этого сайта: дождитесь, пока они закончатся.', 429);
    json_response($r['state']);
}

/** ?url=: состояние скачивания {status, local?, error?}. Ответ не через ok(): там поле status занято. */
function handle_video_status() {
    $url = (string)($_GET['url'] ?? '');
    video_page_via($url) ?? json_error('Нужен адрес поста Telegram, видео TikTok или ролика YouTube (watch или Shorts).');
    json_response(video_preview_status($url) + ['progress' => ytdlp_progress_public(app_user_id(), video_preview_key($url))]);
}

/** Старое имя для адресов Telegram: тот же механизм. */
function handle_telegram_video_fetch($in) {
    if (video_page_via((string)($in['url'] ?? '')) !== 'tgu') json_error('Нужен адрес поста Telegram: https://t.me/канал/номер.');
    handle_video_fetch($in);
}

function handle_telegram_video_status() {
    if (video_page_via((string)($_GET['url'] ?? '')) !== 'tgu') json_error('Нужен адрес поста Telegram: https://t.me/канал/номер.');
    handle_video_status();
}
