<?php
// Поиск постов встроенными поисками площадок (POST_SEARCH_PROMPT.md): посты по теме и уточнению — в подбор контента.
//
// Что проверено (2026-10-24):
// | Площадка | Работает | Как искать | Ограничения |
// |---|---|---|---|
// | YouTube | да | yt-dlp: results?search_query=… (без sp-фильтра: с sp=EgIIAw%3D%3D выдача пустая), hashtag/<тег>/shorts | даты в выдаче нет — отдельно, по одной ссылке |
// | Telegram | да (личный аккаунт, Telethon 1.45 в tools/tg-lib) | channels.SearchPostsRequest: хэштег бесплатно; по словам — если CheckSearchPostsFloodRequest разрешает | не больше TG_SEARCH_PER_HOUR поисков в час на аккаунт; FloodWait — стоп до конца часа |
// | VK | нет сейчас | newsfeed.search — только при подключённом VK (vk_is_connected) | VK не подключён — пропуск с записью в журнал |
// | TikTok | не сделано | tiktok_search_videos возвращает только авторов, а нужны видео — отдельный вариант | — |
// | Двач, 4chan | поиска нет | — | фильтр каталога по словам не сделан |
// | Pinterest, Tumblr | не проверено | — | — |

const POST_SEARCH_SECONDS = 60;            // весь поиск постов за проход
const POST_SEARCH_QUERIES = 3;             // запросов на площадку за проход
const POST_SEARCH_LIMIT = 30;              // постов на запрос
const POST_SEARCH_DATE_LOOKUPS = 3;        // дат роликов на запрос: каждая — отдельный запуск yt-dlp, дороже самого поиска
const POST_SEARCH_CACHE_SECONDS = 3600;    // ответ площадки на (площадка, запрос) кэшируется на час
const TG_SEARCH_PER_HOUR = 5;              // поисков постов в час на личный аккаунт Telegram
const TG_SEARCH_CHANNEL_PAGES = 5;         // страниц каналов за проход: пост берём со страницы канала (картинка как у обычного поста)

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

/** Запросы для прохода: уточнение пользователя первым, слова темы и запросы профиля (без вызова модели). Ротация — по номеру прохода. */
function post_search_queries($topic): array {
    $queries = [];
    if (($query = current_query()) !== '') $queries[] = $query;
    $profile = topic_lookup($topic)['profile'] ?? [];
    if (($first = trim((string)($profile['keywords'][0] ?? ''))) !== '') $queries[] = '#' . preg_replace('/\s+/u', '', $first);   // хэштег — для Telegram и YouTube
    foreach (array_merge($profile['keywords'] ?? [], $profile['queries'] ?? []) as $q) {
        $q = discovery_clean_query($q);
        if ($q !== '') $queries[] = $q;
    }
    $queries = array_values(array_unique(array_filter(array_map('trim', $queries), 'strlen')));
    $pass = (int)kv_get('post_search:pass', 0);
    kv_update('post_search:pass', fn() => $pass + 1);
    return array_slice(discovery_rotate($queries, $pass), 0, POST_SEARCH_QUERIES);
}

/** Локальный фильтр: в тексте поста есть основа слова темы или уточнения (слово без двух последних букв). */
function post_search_matches($topic, array $post): bool {
    $words = discovery_topic_keywords($topic);
    if (($query = current_query()) !== '') $words[] = $query;
    $text = mb_strtolower(preg_replace('/#\S+/u', ' ', (string)($post['text'] ?? '')) ?? '');   // #мем в спаме — не попадание по теме
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

/**
 * Все поиски постов за проход в пределах бюджета POST_SEARCH_SECONDS: YouTube и Telegram по каждому запросу.
 * Возвращает посты по площадкам (как в collect_posts_by_platform) и число запросов по каждой.
 */
function post_search_collect($topic, $uid): array {
    $deadline = time() + POST_SEARCH_SECONDS;
    $by = [PLATFORM_TELEGRAM => [], PLATFORM_RSS => []];
    $queries = post_search_queries($topic);
    $done = ['youtube' => 0, 'telegram' => 0];
    foreach ($queries as $query) {
        if (time() >= $deadline) break;
        $done['youtube']++;
        array_push($by[PLATFORM_RSS], ...post_search_youtube($query, POST_SEARCH_LIMIT));
        if (time() >= $deadline) break;
        $done['telegram']++;
        array_push($by[PLATFORM_TELEGRAM], ...post_search_telegram($query, POST_SEARCH_LIMIT, $uid));
    }
    $keep = fn($p) => !post_search_is_ad($p) && post_search_matches($topic, $p);
    $by[PLATFORM_RSS] = array_values(array_filter($by[PLATFORM_RSS], $keep));
    $by[PLATFORM_TELEGRAM] = array_values(array_filter($by[PLATFORM_TELEGRAM], $keep));
    return ['by' => $by, 'done' => $done, 'queries' => $queries];
}
