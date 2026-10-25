<?php
// Поиск постов встроенными поисками площадок (POST_SEARCH_PROMPT.md): посты по теме и уточнению — в подбор контента.
//
// Что проверено (2026-10-24):
// | Площадка | Работает | Как искать | Ограничения |
// |---|---|---|---|
// | YouTube | да | yt-dlp: results?search_query=… (без sp-фильтра: с sp=EgIIAw%3D%3D выдача пустая), hashtag/<тег>/shorts | даты в выдаче нет — отдельно, по одной ссылке |
// | Telegram | да (личный аккаунт, Telethon 1.45 в tools/tg-lib) | channels.SearchPostsRequest: хэштег бесплатно; по словам — если CheckSearchPostsFloodRequest разрешает | не больше TG_SEARCH_PER_HOUR поисков в час на аккаунт; FloodWait — стоп до конца часа |
// | VK | нет сейчас | newsfeed.search — только при подключённом VK (vk_is_connected) | VK не подключён — пропуск с записью в журнал |
// | TikTok | частично | авторы свежих роликов с поиска (tiktok_search_videos), их последние ролики — посты | сами ролики поиска без миниатюр не разбираем: поиск отдаёт только ссылки на авторов |
// | Двач, 4chan | поиска нет | — | фильтр каталога по словам не сделан |
// | Pinterest, Tumblr | не проверено | — | — |

const POST_SEARCH_SECONDS = 60;            // весь поиск постов за проход
const POST_SEARCH_QUERIES = 3;             // запросов на площадку за проход
const POST_SEARCH_LIMIT = 30;              // постов на запрос
const POST_SEARCH_DATE_LOOKUPS = 3;        // дат роликов на запрос: каждая — отдельный запуск yt-dlp, дороже самого поиска
const POST_SEARCH_CACHE_SECONDS = 3600;    // ответ площадки на (площадка, запрос) кэшируется на час
const TG_SEARCH_PER_HOUR = 5;              // поисков постов в час на личный аккаунт Telegram
const TG_SEARCH_CHANNEL_PAGES = 5;         // страниц каналов за проход: пост берём со страницы канала (картинка как у обычного поста)
const TIKTOK_SEARCH_AUTHORS = 2;           // авторов поиска TikTok за запрос: по их последним роликам берём посты

/** Ответ площадки из kv на CACHE_SECONDS. fetch возвращает null, если поиска не было (лимит, пауза) — такой пропуск не кэшируется. Сбой — пусто и строка в журнал. */
function post_search_cached(string $platform, string $query, callable $fetch): array {
    $key = 'post_search:' . substr(sha1("$platform|$query"), 0, 20);
    $cached = kv_get($key);
    if (is_array($cached) && time() - (int)($cached['at'] ?? 0) < POST_SEARCH_CACHE_SECONDS) return $cached['posts'];
    try {
        $posts = $fetch();
    } catch (Throwable $e) {
        cli_echo("Поиск постов {$platform} «{$query}»: " . $e->getMessage());
        return [];
    }
    if ($posts === null) return [];
    kv_update($key, fn() => ['at' => time(), 'posts' => $posts]);
    return $posts;
}

/** Все запросы темы для поиска постов, без ротации: уточнение, хэштеги слов и имён персонажей, запросы профиля. */
function post_search_candidates($topic): array {
    $queries = [];
    if (($query = current_query()) !== '') $queries[] = $query;
    $profile = topic_lookup($topic)['profile'] ?? [];
    if (($first = trim((string)($profile['keywords'][0] ?? ''))) !== '') $queries[] = '#' . preg_replace('/\s+/u', '', $first);   // хэштег — для Telegram и YouTube
    // Имена персонажей — хэштегами по одному слову: поиск по словам в Telegram платный, а #литвинков работает
    foreach (topic_entity_words(topic_lookup($topic)['entities'] ?? []) as $word) $queries[] = '#' . $word;
    foreach (array_merge($profile['keywords'] ?? [], $profile['queries'] ?? []) as $q) {
        $q = discovery_clean_query($q);
        if ($q !== '') $queries[] = $q;
    }
    return array_values(array_unique(array_filter(array_map('trim', $queries), 'strlen')));
}

/** Запросы для прохода (без вызова модели). Ротация — по номеру прохода: за проход берутся POST_SEARCH_QUERIES из списка. */
function post_search_queries($topic): array {
    $queries = post_search_candidates($topic);
    $pass = (int)kv_get('post_search:pass', 0);
    kv_update('post_search:pass', fn() => $pass + 1);
    return array_slice(discovery_rotate($queries, $pass), 0, POST_SEARCH_QUERIES);
}

/** Локальный фильтр: в тексте поста есть основа слова темы или уточнения (слово без двух последних букв). */
function post_search_matches($topic, array $post): bool {
    $words = discovery_topic_keywords($topic);
    if (($query = current_query()) !== '') $words[] = $query;
    $raw = mb_strtolower((string)($post['text'] ?? ''));
    preg_match_all('/#(\p{L}[\p{L}\p{N}_]*)/u', $raw, $found);
    if (array_intersect($found[1], topic_entity_words(topic_lookup($topic)['entities'] ?? []))) return true;   // #литвинков — имя персонажа, это попадание
    $text = preg_replace('/#\S+/u', ' ', $raw) ?? '';   // #мем в спаме — не попадание по теме
    foreach ($words as $word) {
        $word = mb_strtolower(trim((string)$word));
        $stem = mb_substr($word, 0, max(3, mb_strlen($word) - 2));
        if ($stem !== '' && mb_strpos($text, $stem) !== false) return true;
    }
    return false;
}

/** Посты YouTube по запросу или #тегу (шортсы с хэштега). Даты — отдельным вызовом; без даты пост не берём. */
function post_search_youtube(string $query, int $limit): array {
    return post_search_cached('youtube', $query, function () use ($query, $limit) {
        $hashtag = str_starts_with($query, '#');
        $url = $hashtag
            ? 'https://www.youtube.com/hashtag/' . rawurlencode(ltrim($query, '#')) . '/shorts'
            : 'https://www.youtube.com/results?search_query=' . rawurlencode($query);
        $rows = youtube_list_rows(['--flat-playlist', '--playlist-end', (string)$limit, '--extractor-args', 'youtube:lang=ru',
            '--print', '%(id)s|%(channel_id)s|%(duration)s|%(view_count)s|%(thumbnails.-1.url)s|%(title)s', '--', ytdlp_check_url($url)], 6);
        $rows = array_values(array_filter($rows, fn($r) => preg_match('~^[A-Za-z0-9_-]{11}$~', $r[0])));
        $rows = array_slice($rows, 0, POST_SEARCH_DATE_LOOKUPS);   // даты — страницы роликов в одном запуске yt-dlp: дорого, берём только первые
        if (!$rows) return [];
        $short = fn($r) => $hashtag || (is_numeric($r[2]) && (int)$r[2] <= 60);
        $page = fn($r) => $short($r) ? "https://www.youtube.com/shorts/$r[0]" : "https://www.youtube.com/watch?v=$r[0]";
        $info = [];   // id => [время, канал] — канал в списке хэштега не приходит, берём со страницы
        // без JS-разгадывания: в 2–3 раза быстрее, метаданные те же; частичный вывод при ошибке одного ролика — тоже берём
        $dated = ytdlp_run(['--no-playlist', '--print', '%(id)s|%(timestamp)s|%(channel_id)s', '--', ...array_map(fn($r) => ytdlp_check_url($page($r)), $rows)], YTDLP_LIST_TIMEOUT, false);
        foreach (preg_split('/\R/', trim($dated['stdout'])) as $line) {
            [$id, $at, $cid] = array_pad(explode('|', trim($line)), 3, '');
            if (is_numeric($at) && preg_match('~^UC[A-Za-z0-9_-]{22}$~', $cid)) $info[$id] = [(int)$at, $cid];
        }
        $posts = [];
        foreach ($rows as $r) {
            [$id, $channel, , $views, $thumb, $title] = $r;
            $title = $title === 'NA' ? '' : $title;   // yt-dlp пишет NA вместо пустого поля
            if (!isset($info[$id])) continue;   // без даты и канала не знаем, свежий ли пост и чей он
            [$at, $channel] = $info[$id];
            $post = youtube_post($id, $title, $thumb, $at, (int)(is_numeric($views) ? $views : 0), $short($r));
            $posts[] = $post + [
                'source_key' => source_id_for(PLATFORM_RSS, "https://www.youtube.com/feeds/videos.xml?channel_id=$channel"),
                'source_url' => "https://www.youtube.com/channel/$channel",
                'found_by' => "поиск YouTube «{$query}»",
            ];
        }
        return $posts;
    });
}

/** Лимит поисков Telegram на час и пауза после FloodWait. true — можно искать. */
function tg_post_search_allowed(): bool {
    $now = time();
    if ((int)kv_get('tg_post_search:pause', 0) > $now) return false;
    $allowed = false;
    kv_update('tg_post_search:hour', function ($hour) use ($now, &$allowed) {   // проверка и приращение счётчика — в одной транзакции
        $hour = is_array($hour) ? $hour : ['at' => 0, 'n' => 0];
        if ($now - (int)$hour['at'] >= 3600) $hour = ['at' => $now, 'n' => 0];
        if ((int)$hour['n'] >= TG_SEARCH_PER_HOUR) return $hour;
        $allowed = true;
        return ['at' => $hour['at'], 'n' => (int)$hour['n'] + 1];
    });
    return $allowed;
}

/** Реклама и боты в результатах поиска — локально, без модели: ботов по username, призывы и ссылки в тексте, много хэштегов. */
function post_search_is_ad(array $post): bool {
    $user = strtolower((string)preg_replace('~^https://t\.me/~', '', (string)($post['source_url'] ?? '')));
    if ($user !== '' && str_ends_with($user, 'bot')) return true;
    $text = (string)($post['text'] ?? '');
    if (preg_match('~@\w+bot\b|t\.me/\+|\?start=|bit\.ly|u\.to/~iu', $text)) return true;
    if (preg_match('~бесплатно|подписыва|переходи|скидк|акци|заработ|промокод|казино~iu', $text)) return true;
    return preg_match_all('/#\S+/u', $text) > 3;
}

/**
 * Посты Telegram: поиск постов через аккаунт владельца (хэштег или слова), затем пост берём со страницы канала — там картинка как у обычного поста.
 * Публичные каналы с username; больше TG_SEARCH_CHANNEL_PAGES страниц за проход не открываем.
 */
function post_search_telegram(string $query, int $limit, $uid): array {
    if ($uid === null || telegram_user_connection($uid) === null) return [];
    return post_search_cached('telegram', $query, function () use ($query, $limit, $uid) {
        if (!tg_post_search_allowed()) {
            cli_echo("Поиск постов Telegram «{$query}»: лимит поисков в час исчерпан — пропуск.");
            return null;
        }
        $hashtag = str_starts_with($query, '#');
        $request = ['action' => 'search_posts', 'hashtag' => $hashtag ? ltrim($query, '#') : '', 'query' => $hashtag ? '' : $query, 'limit' => $limit];
        $result = telegram_user_call($uid, $request, 90);
        if (!empty($result['flood_wait'])) {
            kv_update('tg_post_search:pause', fn() => time() + 3600);   // FloodWait — стоп до конца часа
            cli_echo('Поиск постов Telegram: FloodWait — прекращаю до конца часа.');
            return null;
        }
        if (!empty($result['paid'])) {
            cli_echo("Поиск постов Telegram «{$query}»: поиск по словам платный сейчас — пропуск (хэштеги работают).");
            return null;
        }
        $by_channel = [];
        foreach ((array)($result['posts'] ?? []) as $p) if ($p['has_media']) $by_channel[$p['username']][$p['id']] = true;   // страница канала отдаёт только посты с картинкой или видео
        $posts = [];
        foreach (array_slice($by_channel, 0, TG_SEARCH_CHANNEL_PAGES, true) as $username => $ids) {
            // страница канала — от самого нового найденного поста: старые посты не попадают на первую страницу
            if (!is_valid_telegram_channel((string)$username) || !($html = fetch_telegram_channel_page($username, max(array_keys($ids)) + 1))) continue;
            $source = ['id' => $username, 'name' => source_display_name(PLATFORM_TELEGRAM, $username)];
            foreach (telegram_posts_from_html($html, $source, TELEGRAM_POSTS_PER_CHANNEL) as $post) {
                $id = (int)substr(strrchr((string)$post['url'], '/'), 1);
                if (!isset($ids[$id])) continue;
                $posts[] = $post + ['source_key' => source_id_for(PLATFORM_TELEGRAM, $username), 'source_url' => "https://t.me/$username", 'found_by' => "поиск Telegram «{$query}»"];
            }
        }
        return $posts;
    });
}

/** Пост VK из поиска новостей: тот же разбор, что у стены паблика; адрес автора — club/id, как его читает wall.get. Без подключения VK — пусто. */
function post_search_vk(string $query, int $limit): array {
    if (!vk_is_connected()) {
        cli_echo("Поиск постов VK «{$query}»: VK не подключён (нужен вход в VK) — пропуск.");
        return [];
    }
    return post_search_cached('vk', $query, function () use ($query, $limit) {
        $posts = [];
        foreach (vk_api_request('newsfeed.search', ['q' => $query, 'count' => max(1, min(100, $limit)), 'extended' => 0])['items'] ?? [] as $item) {
            if (!((int)($item['owner_id'] ?? 0)) || !($post = vk_wall_post($item, '', '', false))) continue;
            $owner = (int)$item['owner_id'];
            $domain = $owner < 0 ? 'club' . abs($owner) : 'id' . $owner;
            $posts[] = $post + ['source_key' => source_id_for(PLATFORM_VK, $domain), 'source_url' => "https://vk.com/$domain", 'found_by' => "поиск VK «{$query}»"];
        }
        return $posts;
    });
}

/** Посты TikTok: авторы свежих роликов по запросу (tiktok_search_videos) и их последние ролики. Пауза поиска — не кэшируем, пропуск. */
function post_search_tiktok(string $query, int $limit): array {
    if (tiktok_search_paused()) return [];
    return post_search_cached('tiktok', $query, function () use ($query, $limit) {
        $posts = [];
        foreach (array_slice(tiktok_search_videos(ltrim($query, '#')), 0, TIKTOK_SEARCH_AUTHORS) as $user) {
            foreach (tiktok_fetch_posts($user, min($limit, 8)) as $post) {
                $posts[] = $post + ['source_key' => source_id_for(PLATFORM_TIKTOK, $user), 'source_url' => "https://www.tiktok.com/@$user", 'found_by' => "поиск TikTok «{$query}»"];
            }
        }
        return $posts;
    });
}

/** Автор поста по адресу источника: канал Telegram, канал YouTube (лента), аккаунт TikTok, сообщество или страница VK. null — автора не понять. */
function post_search_author(array $post): ?array {
    $url = (string)($post['source_url'] ?? '');
    if (preg_match('~^https://t\.me/([A-Za-z0-9_]+)$~', $url, $m)) return ['platform' => PLATFORM_TELEGRAM, 'id' => $m[1]];
    if (preg_match('~^https://www\.youtube\.com/channel/(UC[A-Za-z0-9_-]{22})$~', $url, $m)) return ['platform' => PLATFORM_RSS, 'id' => youtube_channel_feed_url($m[1])];
    if (preg_match('~^https://www\.tiktok\.com/@([A-Za-z0-9_.]{2,24})$~', $url, $m)) return ['platform' => PLATFORM_TIKTOK, 'id' => $m[1]];
    if (preg_match('~^https://vk\.com/((?:club|id)\d+)$~', $url, $m)) return ['platform' => PLATFORM_VK, 'id' => $m[1]];
    return null;
}

/**
 * Пост прошёл полный анализ и найден поиском: его автор, если он ещё не известен (каталог, решения, предложения, проверки), — в отложенные
 * подбора темы: следующий прогон проверит его полной проверкой и решит каталог или предложения (DISCOVERY_PLATFORMS_FIX_PROMPT.md, E1).
 */
function post_search_suggest(array $item, $topic): void {
    if (empty($item['found_by']) || !($author = post_search_author($item))) return;
    $sid = source_id_for($author['platform'], $author['id']);
    if (post_search_source_known(load_source_db(), $sid)) return;
    discovery_defer($topic, ['platform' => $author['platform'], 'id' => $author['id'], 'origin' => "пост прошёл отбор: " . ($item['url'] ?? $item['source_url'] ?? ''), 'depth' => 0],
        'пост прошёл отбор — проверю в следующем подборе');
}

/** Источник уже известен: в каталоге любой темы, в решениях, предложениях или проверках. */
function post_search_source_known(array $db, string $sid): bool {
    foreach (CATALOG_KEYS as $platform => $key) foreach ((array)$db[$key] as $e) if (source_id_for($platform, (string)($e['id'] ?? '')) === $sid) return true;
    foreach (array_merge((array)$db['source_votes'], (array)$db['source_suggestions']) as $e) if (($e['source_id'] ?? '') === $sid) return true;
    foreach (array_keys((array)($db['discovery']['checked_at'] ?? [])) as $c) if ($c === $sid || str_ends_with((string)$c, '|' . $sid)) return true;
    return false;
}

/**
 * Все поиски постов за проход в пределах бюджета POST_SEARCH_SECONDS: YouTube, Telegram и VK по каждому запросу.
 * TikTok сюда не входит: Chrome дорог, его запускает подбор источников (discovery_find_posts). Возвращает посты по площадкам и число запросов.
 */
function post_search_collect($topic, $uid): array {
    $deadline = time() + POST_SEARCH_SECONDS;
    $by = [PLATFORM_TELEGRAM => [], PLATFORM_RSS => [], PLATFORM_VK => []];
    $queries = post_search_queries($topic);
    $done = ['youtube' => 0, 'telegram' => 0, 'vk' => 0];
    foreach ($queries as $query) {
        if (time() >= $deadline) break;
        $done['youtube']++;
        array_push($by[PLATFORM_RSS], ...post_search_youtube($query, POST_SEARCH_LIMIT));
        if (time() >= $deadline) break;
        $done['telegram']++;
        array_push($by[PLATFORM_TELEGRAM], ...post_search_telegram($query, POST_SEARCH_LIMIT, $uid));
        if (time() >= $deadline) break;
        $done['vk']++;
        array_push($by[PLATFORM_VK], ...post_search_vk($query, POST_SEARCH_LIMIT));
    }
    $keep = fn($p) => !post_search_is_ad($p) && post_search_matches($topic, $p) && ua_item_blocked($p) === null;   // украинское — не берём (UA_FILTER_PROMPT.md, B4)
    foreach ($by as $platform => $posts) $by[$platform] = array_values(array_filter($posts, $keep));
    return ['by' => $by, 'done' => $done, 'queries' => $queries];
}
