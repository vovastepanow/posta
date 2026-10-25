<?php
// Поиск кандидатов по площадкам для автоподбора: discovery_find_<площадка>(…) → [['platform', 'id', 'origin', 'info']].
// Общее: распознавание ссылок на площадки в тексте и HTML, порядок проверок (balance), сведения о кандидате (probe).
// Все сетевые запросы — через fetch_public / rss_get / yt-dlp / Chrome с лимитами; модель здесь не вызывается.

/** Кандидата нет на площадке (t.me отдал страницу без канала): отказ, без повторной попытки. */
class DiscoveryMissing extends RuntimeException {}

const DISCOVERY_MIN_CHECKS_PER_PLATFORM = 4;     // каждой площадке с кандидатами — столько проверок за прогон, если хватает
const DISCOVERY_YOUTUBE_MAX = 12;                // кандидатов YouTube за прогон (каждый — лишний список роликов через yt-dlp)
const DISCOVERY_YOUTUBE_SECONDS = 90;            // общее время поиска YouTube за прогон
const DISCOVERY_OK_MAX = 15;                     // групп ОК за прогон
const DISCOVERY_LINKS_MAX = 30;                  // ссылок на площадки из одного текста
const OK_DISCOVERY_FEEDS = ['hobby', 'collection'];   // общие ленты разделов ОК; в «collection» — записи людей, групп в них нет
const DISCOVERY_PINTEREST_RESERVED = ['pin', 'search', 'ideas', 'today', 'explore', 'categories', 'login', 'settings', 'business'];

/** Текст для журнала: площадка и имя. */
function discovery_label(array $c): string {
    return match ($c['platform']) {
        PLATFORM_TELEGRAM => '@' . $c['id'],
        PLATFORM_VK => 'vk.com/' . $c['id'],
        PLATFORM_TIKTOK => 'tiktok @' . $c['id'],
        PLATFORM_OK => 'ok:' . $c['id'],
        default => mb_strtolower(rss_feed_kind((string)$c['id'])) . ' ' . (string)parse_url((string)$c['id'], PHP_URL_HOST),
    };
}

/** Ссылки на площадки в тексте или HTML: [['platform', 'id']] без повторов. Только распознавание формата — сети нет. */
function source_linked_sources(string $text): array {
    $found = [];
    $add = function ($platform, $id) use (&$found) {
        if (count($found) < DISCOVERY_LINKS_MAX) $found[source_id_for($platform, $id)] = ['platform' => $platform, 'id' => $id];
    };
    preg_match_all('~(?<![\w.])(?:https?://)?(?:t|telegram)\.me/(?:s/)?([A-Za-z][A-Za-z0-9_]{4,31})(?=[/?"\'<\s#]|$)~i', $text, $m);
    foreach ($m[1] as $h) if (is_valid_telegram_channel($h) && !in_array(strtolower($h), TELEGRAM_RESERVED_PATHS, true) && !str_ends_with(strtolower($h), 'bot')) $add(PLATFORM_TELEGRAM, $h);
    preg_match_all('~(?:https?://)?(?:www\.|m\.)?vk\.com/([A-Za-z0-9_.-]{2,64})(?=[/?"\'<\s#]|$)~i', $text, $m);
    foreach ($m[1] as $v) if (is_valid_vk_domain($v) && !preg_match('~^(id\d+|wall|video|photo|feed|im|away|app|docs|share|login)$~i', $v)) $add(PLATFORM_VK, $v);
    preg_match_all('~tiktok\.com/@([A-Za-z0-9_.]{2,24})~i', $text, $m);
    foreach ($m[1] as $u) if (is_valid_tiktok_user($u)) $add(PLATFORM_TIKTOK, $u);
    preg_match_all('~ok\.ru/group/(\d{10,20})~', $text, $m);
    foreach ($m[1] as $g) $add(PLATFORM_OK, $g);
    preg_match_all('~youtube\.com/@([A-Za-z0-9_.-]{3,30})~i', $text, $m);
    foreach ($m[1] as $h) $add(PLATFORM_RSS, 'https://www.youtube.com/@' . $h);
    preg_match_all('~youtube\.com/channel/(UC[A-Za-z0-9_-]{22})~', $text, $m);
    foreach ($m[1] as $c) $add(PLATFORM_RSS, 'https://www.youtube.com/channel/' . $c);
    preg_match_all('~pinterest\.(?:com|ru)/([A-Za-z0-9_.-]{2,64})/~i', $text, $m);
    foreach ($m[1] as $u) if (!in_array(strtolower($u), DISCOVERY_PINTEREST_RESERVED, true)) $add(PLATFORM_RSS, "https://www.pinterest.com/$u/");
    preg_match_all('~(?<![\w-])([a-z0-9-]{1,32})\.tumblr\.com~i', $text, $m);
    foreach ($m[1] as $t) if (strtolower($t) !== 'www') $add(PLATFORM_RSS, 'https://' . strtolower($t) . '.tumblr.com/');
    return array_values($found);
}

/** Картинка для оценки: из кэша медиа сайта (относительный адрес) или по https через fetch_public. Не получилось — ''. */
function discovery_image_bytes(string $url): string {
    if (($path = media_cache_path($url)) !== null) return is_file($path) ? (string)file_get_contents($path) : '';
    try { return fetch_public_image($url); } catch (Throwable $e) { return ''; }
}

/**
 * Порядок проверок: каждой площадке (группе) — первые $min кандидатов, потом по кругу, пока не набран $max.
 * Группы идут в том порядке, в каком были добавлены.
 */
function discovery_balance(array $groups, int $max, int $min): array {
    $out = [];
    $rest = array_values($groups);
    foreach ($rest as $i => $list) {
        $take = array_slice($list, 0, $min);
        $out = array_merge($out, $take);
        $rest[$i] = array_slice($list, count($take));
    }
    $out = array_slice($out, 0, $max);
    while (count($out) < $max && array_filter($rest)) {
        foreach ($rest as $i => $list) {
            if (!$list || count($out) >= $max) continue;
            $out[] = array_shift($list);
            $rest[$i] = $list;
        }
    }
    return $out;
}

const DISCOVERY_GROUP_ORDER = ['telegram', 'youtube', 'tiktok', 'ok', 'vk', 'links'];   // порядок круга очереди
const DISCOVERY_GROUP_NAMES = ['telegram' => 'Telegram', 'youtube' => 'YouTube', 'tiktok' => 'TikTok', 'ok' => 'Одноклассники', 'vk' => 'ВКонтакте', 'links' => 'Ссылки'];

/** Группа очереди кандидата: по площадке; ссылки на YouTube — в группу YouTube; прочие RSS-ссылки — «links». */
function discovery_group_of(string $platform, string $id): string {
    return match (true) {
        $platform === PLATFORM_TELEGRAM => 'telegram',
        $platform === PLATFORM_TIKTOK => 'tiktok',
        $platform === PLATFORM_OK => 'ok',
        $platform === PLATFORM_VK => 'vk',
        str_contains($id, 'youtube.com/') => 'youtube',
        default => 'links',
    };
}

/** Очередь: группы площадок (по кругу) и указатель круга. Новый кандидат — в конец своей группы. */
function discovery_queue_new(): array { return ['groups' => [], 'turn' => 0]; }

function discovery_queue_add(array &$queue, string $group, array $candidate, bool $front = false): void {
    $queue['groups'][$group] ??= [];
    if ($front) array_unshift($queue['groups'][$group], $candidate);
    else $queue['groups'][$group][] = $candidate;
}

/** Следующий кандидат по кругу площадок: в группе сначала отложенные, потом глубина 0, 1, 2. null — очередь пуста. */
function discovery_queue_next(array &$queue): ?array {
    $n = count(DISCOVERY_GROUP_ORDER);
    for ($i = 0; $i < $n; $i++) {
        $group = DISCOVERY_GROUP_ORDER[($queue['turn'] + $i) % $n];
        if (empty($queue['groups'][$group])) continue;
        $queue['turn'] = ($queue['turn'] + $i + 1) % $n;
        return discovery_pop_first($queue['groups'][$group]);
    }
    return null;
}

/** Из группы — кандидат с наименьшим приоритетом: отложенный, затем меньшая глубина, затем раньше добавленный. */
function discovery_pop_first(array &$list): array {
    $rank = fn($c) => (empty($c['deferred']) ? 10 : 0) + (int)($c['depth'] ?? 0);
    $best = 0;
    foreach ($list as $i => $c) if ($rank($c) < $rank($list[$best])) $best = $i;
    return array_splice($list, $best, 1)[0];
}

/** Запросы, начиная с позиции $offset по кругу: следующий прогон темы берёт другие запросы (YouTube и TikTok — первые по порядку). */
function discovery_rotate(array $items, int $offset): array {
    if (!$items) return [];
    $offset %= count($items);
    return [...array_slice($items, $offset), ...array_slice($items, 0, $offset)];
}

/** Строки сводки по площадкам: найдено, проверено, принято, отказ, не загрузилось. VK без подключения — отдельной строкой. */
function discovery_summary_lines(array $report, bool $vk_connected): array {
    $lines = [];
    foreach (DISCOVERY_GROUP_ORDER as $group) {
        $name = DISCOVERY_GROUP_NAMES[$group];
        if ($group === 'vk' && !$vk_connected) { $lines[] = "{$name}: не подключён"; continue; }
        $r = ($report[$group] ?? []) + ['found' => 0, 'checked' => 0, 'accepted' => 0, 'rejected' => 0, 'failed' => 0, 'known' => 0];   // нет счётчика — 0, без Warning
        $lines[] = "{$name}: найдено {$r['found']}, проверено {$r['checked']}, принято {$r['accepted']}, отказ {$r['rejected']}, не загрузилось {$r['failed']}, известных пропущено {$r['known']}";
    }
    return $lines;
}

/** Telegram: поиск каналов через аккаунт пользователя по запросам и «похожие» на лучшие каналы темы. */
function discovery_find_telegram(array $plan, array $seed_ids): array {
    $out = [];
    $searched = discovery_telegram_account_channels($plan['queries']);
    $groups = [];
    foreach ($searched as $ch) $groups[(string)$ch['origin']][] = $ch;
    cli_echo(app_user_bind() !== '' && telegram_user_connection() !== null ? '-> Поиск Telegram через аккаунт: найдено каналов: ' . count($searched) . '.'
        : '-> Аккаунт Telegram не подключён — поиск каналов внутри Telegram пропущен (подключается в «Личном кабинете»).');
    foreach (interleave(...array_values($groups)) as $ch) $out[] = ['platform' => PLATFORM_TELEGRAM, 'id' => $ch['username'], 'origin' => "поиск Telegram «{$ch['origin']}»", 'origin_kind' => 'tg_search', 'info' => null];
    $similar = array_slice($seed_ids, 0, 4);
    foreach ($similar ? discovery_telegram_account_channels([], $similar) : [] as $ch) $out[] = ['platform' => PLATFORM_TELEGRAM, 'id' => $ch['username'], 'origin' => "похож на @{$ch['origin']}", 'origin_kind' => 'similar', 'info' => null];
    return $out;
}

/** VK: поиск сообществ по запросам. Без подключения VK пропускается (это дополнение). */
function discovery_find_vk(array $queries): array {
    if (!vk_is_connected()) {
        cli_echo('-> VK не подключён — поиск сообществ VK пропущен (кнопка «Подключить VK» на вкладке «Источники»).');
        return [];
    }
    $out = [];
    foreach (array_slice($queries, 0, 8) as $q) {
        try { $found = vk_search_groups($q, 10); } catch (Throwable $e) { cli_echo("-> Поиск VK «{$q}»: " . $e->getMessage()); continue; }
        cli_echo("-> Поиск VK «{$q}»: найдено сообществ: " . count($found) . '.');
        foreach (array_slice($found, 0, 4) as $g) $out[] = ['platform' => PLATFORM_VK, 'id' => $g['id'], 'origin' => "поиск VK «{$q}»", 'info' => $g];
    }
    return $out;
}

/** YouTube: каналы по запросу (страница результатов «только каналы») и авторы шортсов; канал с шортсами — лента шортсов. */
function discovery_find_youtube(array $queries): array {
    $out = [];
    $seen = [];
    $until = time() + DISCOVERY_YOUTUBE_SECONDS;   // общее время поиска YouTube за прогон
    foreach (array_slice($queries, 0, 3) as $q) {
        if (time() > $until) return $out;
        $results = 'https://www.youtube.com/results?search_query=' . rawurlencode($q) . '&sp=EgIQAg%3D%3D';
        $rows = array_merge(discovery_youtube_rows(ytdlp_check_url($results)), discovery_youtube_rows('ytsearch10:' . $q . ' shorts'));
        cli_echo("-> Поиск YouTube «{$q}»: каналов в ответе: " . count($rows) . '.');
        foreach ($rows as [$cid, $name]) {
            if (time() > $until) { cli_echo('-> YouTube: время поиска (' . DISCOVERY_YOUTUBE_SECONDS . ' с) вышло.'); return $out; }
            if (isset($seen[$cid]) || count($out) >= DISCOVERY_YOUTUBE_MAX) continue;
            $seen[$cid] = true;
            // Проверку канала (шортсы, ролики) делает discovery_probe при очереди: поиск только собирает кандидатов
            $out[] = ['platform' => PLATFORM_RSS, 'id' => 'https://www.youtube.com/feeds/videos.xml?channel_id=' . $cid, 'origin' => "поиск YouTube «{$q}»", 'info' => ['title' => $name]];
        }
    }
    return $out;
}

/** Строки yt-dlp «channel_id|имя» по адресу выдачи или ytsearch-запросу. */
function discovery_youtube_rows(string $target): array {
    try {
        $result = ytdlp_run(['--flat-playlist', '--playlist-end', '10', '--print', '%(channel_id)s|%(channel)s', '--', $target], YTDLP_LIST_TIMEOUT);
    } catch (Throwable $e) { cli_echo('-> YouTube: ' . $e->getMessage()); return []; }
    if (!$result['ok'] && trim($result['stdout']) === '') { cli_echo('-> YouTube не отдал поиск: ' . ytdlp_error_text($result)); return []; }
    $rows = [];
    foreach (preg_split('/\R/', $result['stdout']) as $line) {
        [$cid, $name] = array_pad(explode('|', trim($line), 2), 2, '');
        if (preg_match('~^UC[A-Za-z0-9_-]{22}$~', $cid)) $rows[] = [$cid, mb_substr(trim($name), 0, 120)];
    }
    return $rows;
}


/** TikTok: аккаунты по поиску (не больше двух запросов за прогон). Ссылки tiktok.com/@ из найденных — отдельно. */
function discovery_find_tiktok(array $queries): array {
    $out = [];
    foreach (array_slice($queries, 0, 2) as $q) {   // не больше двух поисков за прогон
        try { $users = tiktok_search_videos($q); } catch (Throwable $e) { cli_echo('-> TikTok: ' . $e->getMessage()); continue; }
        cli_echo("-> Поиск TikTok по видео «{$q}»: авторов: " . count($users) . '.');
        foreach ($users as $u) $out[] = ['platform' => PLATFORM_TIKTOK, 'id' => $u, 'origin' => "авторы видео TikTok «{$q}»", 'info' => null];
    }
    return $out;
}

/** Запросы для TikTok — короткие и разговорные: слова темы из профиля (keywords), потом короткие запросы плана. Не «парадоксальные видео». */
function discovery_tiktok_queries(array $queries, $topic): array {
    $pool = [...array_map('strval', discovery_topic_keywords($topic)), ...$queries];
    $out = [];
    foreach ($pool as $q) {
        $q = mb_strtolower(trim($q));
        if ($q === '' || mb_strlen($q) > 24 || count(preg_split('/\s+/u', $q)) > 2 || str_contains($q, 'видео') || in_array($q, $out, true)) continue;
        $out[] = $q;
    }
    return array_slice($out, 0, 2);
}

/**
 * Одноклассники: группы-авторы из общих лент разделов, где запись про тему. Фильтр — по заголовку и тексту записи
 * (ключевых слов в профиле темы пока нет: берём название темы и запросы), модель по каждой группе не гоняем.
 */
function discovery_find_ok(array $queries, string $topic): array {
    // Основы слов (без окончания): «вязание» находит «вязания» в записях
    $words = array_values(array_unique(array_map(fn($w) => mb_substr($w, 0, max(4, mb_strlen($w) - 2)),
        array_filter(array_map('mb_strtolower', preg_split('/[\s,;:!?.()\/|«»"]+/u', implode(' ', [$topic, ...$queries])) ?: []), fn($w) => mb_strlen($w) >= 3))));
    $groups = [];
    foreach (OK_DISCOVERY_FEEDS as $name) {
        $url = "https://ok.ru/atom-feed/$name";
        try {
            $r = rss_get($url, 3 * 1024 * 1024, 15, BROWSER_UA);
            if (!$r['ok']) { cli_echo("-> ОК: лента «{$name}» не отвечает."); continue; }
            $posts = rss_parse($r['bytes'], 100, PLATFORM_OK, $url);
        } catch (Throwable $e) { cli_echo("-> ОК: лента «{$name}»: " . $e->getMessage()); continue; }
        foreach ($posts as $p) {
            if (!preg_match('~ok\.ru/group/(\d{10,20})~', (string)$p['url'], $m) || isset($groups[$m[1]])) continue;
            if (!$words || !array_filter($words, fn($w) => mb_stripos((string)$p['text'], $w) !== false)) continue;
            $groups[$m[1]] = "лента ОК «{$name}»";
        }
    }
    cli_echo('-> Одноклассники: групп по теме в лентах разделов: ' . count($groups) . '.');
    $out = [];
    foreach (array_slice($groups, 0, DISCOVERY_OK_MAX, true) as $gid => $origin) $out[] = ['platform' => PLATFORM_OK, 'id' => (string)$gid, 'origin' => $origin, 'info' => null];
    return $out;
}

/** Ссылка на ленту из кандидата: для ссылок на канал (YouTube, Pinterest, Tumblr) — адрес ленты; иначе как есть. */
function discovery_feed_url(string $url): string {
    try { return rss_known_feed($url) ?? $url; } catch (RuntimeException $e) { return $url; }
}

/** Сведения о кандидате для оценки: название, описание, посты с картинками, подписчики, даты, просмотры и ссылки. Ошибка — исключение. */
function discovery_probe(array $c): array {
    $info = $c['info'] ?? [];
    $base = ['id' => $c['id'], 'title' => '', 'description' => '', 'subscribers' => 0, 'posts' => [], 'post_times' => [], 'post_views' => [], 'linked' => []];
    switch ($c['platform']) {
        case PLATFORM_TELEGRAM:
            if (!($tg = telegram_channel_info($c['id'], 8))) {
                // Блок канала есть у любого существующего канала: страница без него — канала с таким именем нет
                $html = fetch_telegram_channel_page($c['id']);
                if ($html !== false && !str_contains($html, 'tgme_channel_info')) throw new DiscoveryMissing('канала нет: t.me отдал страницу без канала.');
                throw new RuntimeException('t.me не отдал страницу канала — проверю в следующий раз.');   // повтор — в очереди прогона
            }
            $pairs = array_map(fn($h) => ['platform' => PLATFORM_TELEGRAM, 'id' => $h], (array)($tg['linked'] ?? []));
            return discovery_finish_probe($tg + $base, $pairs);
        case PLATFORM_VK:
            $posts = build_vk_preview_posts(['id' => $c['id'], 'name' => source_display_name(PLATFORM_VK, $info['title'] ?? $c['id'])], 8);
            return discovery_finish_probe(['title' => $info['title'] ?? $c['id'], 'subscribers' => (int)($info['subscribers'] ?? 0), 'posts' => $posts,
                'post_times' => array_column($posts, 'posted_at')] + $base);
        case PLATFORM_TIKTOK:
            try { $posts = tiktok_fetch_posts($c['id'], 8); }
            catch (RuntimeException $e) {
                if (str_contains($e->getMessage(), 'does not have any videos')) throw new DiscoveryMissing('нет видео: аккаунт ничего не публиковал.');
                throw $e;
            }
            return discovery_finish_probe(['title' => '@' . $c['id'], 'posts' => $posts, 'post_times' => array_column($posts, 'posted_at')] + $base);
        case PLATFORM_OK:
            $html = ok_group_html($c['id']);
            $posts = ok_parse_feed($html, $c['id'], 8);
            return discovery_finish_probe(['title' => ok_title_from_html($html, $c['id']), 'posts' => $posts, 'post_times' => array_column($posts, 'posted_at')] + $base);
        default:
            if (($yt = youtube_feed_channel((string)$c['id'])) !== null) return discovery_youtube_probe($yt[0], $info);
            $feed = discovery_feed_url((string)$c['id']);
            try { $posts = rss_fetch_posts($feed, 15); } catch (RuntimeException $e) { sleep(2); $posts = rss_fetch_posts($feed, 15); }   // лента YouTube иногда отдаёт 404 на один запрос
            $title = $info['title'] ?? '';
            return discovery_finish_probe(['id' => $feed, 'title' => $title !== '' ? $title : rss_feed_title($feed), 'posts' => $posts,
                'post_times' => array_column($posts, 'posted_at')] + $base);
    }
}

/**
 * Проверка канала YouTube при очереди: свежие шортсы → лента шортсов (id), иначе ролики. Адрес ленты роликов — alt_id,
 * он помечается проверенным при любом исходе, чтобы повторный поиск не проверял канал снова.
 */
function discovery_youtube_probe(string $cid, array $info): array {
    $videos_id = 'https://www.youtube.com/feeds/videos.xml?channel_id=' . $cid;
    $base = ['title' => $info['title'] ?? '', 'description' => '', 'subscribers' => 0, 'post_views' => [], 'linked' => [], 'alt_id' => $videos_id];
    $shorts = [];
    try { $shorts = youtube_channel_posts($cid, 'shorts', YOUTUBE_LIST_COUNT); } catch (Throwable $e) { $shorts = []; }   // вкладки шортсов может не быть
    if (array_filter($shorts, fn($p) => !is_post_outdated($p['posted_at'] ?? 0))) {
        return discovery_finish_probe(['id' => rss_youtube_shorts_feed($cid), 'posts' => $shorts, 'post_times' => array_column($shorts, 'posted_at')] + $base);
    }
    try {
        $videos = youtube_channel_posts($cid, 'videos', YOUTUBE_LIST_COUNT);
    } catch (Throwable $e) {   // вкладки «Видео» нет (канал только с шортсами): берём шорты, даже старые — возраст решит проверка постов
        if (!$shorts) throw $e;
        return discovery_finish_probe(['id' => rss_youtube_shorts_feed($cid), 'posts' => $shorts, 'post_times' => array_column($shorts, 'posted_at')] + $base);
    }
    return discovery_finish_probe(['id' => $videos_id, 'posts' => $videos, 'post_times' => array_column($videos, 'posted_at')] + $base + ['alt_id' => '']);
}

/** Ссылки из описания и подписей постов — к ссылкам, которые уже известны по пути поиска; без дублей. */
function discovery_finish_probe(array $info, array $pairs = []): array {
    $texts = implode("\n", array_column($info['posts'] ?? [], 'text'));
    $all = [...$pairs, ...source_linked_sources(($info['description'] ?? '') . "\n" . $texts)];
    $unique = [];
    foreach ($all as $p) $unique[source_id_for($p['platform'], $p['id'])] = $p;
    $info['linked'] = array_values($unique);
    return $info;
}

/** Посты-источники для ссылок из найденных каналов темы: Telegram-страницы, по которым ищутся ссылки. */
function discovery_topic_seeds(array $db, string $topic): array {
    $seeds = array_values(array_filter($db['telegram_channels'], fn($e) => !empty($e['topics']) && catalog_entry_matches_topic($e, $topic, true)));
    usort($seeds, fn($a, $b) => (int)($b['score'] ?? 0) <=> (int)($a['score'] ?? 0));
    return array_map(fn($s) => ltrim((string)($s['id'] ?? ''), '@'), $seeds);
}
