<?php
// «Лучшие посты источника»: ссылка на канал, паблик или группу → посты за период, отсортированные по выбранной метрике.
// Модель не вызывается: пост в подборку забирает сам пользователь (own_add_to_pool, как «Свой контент»).

const BEST_POSTS_DAYS = [1, 7, 30];
const BEST_POSTS_SORTS = ['date' => 'по дате', 'likes' => 'по лайкам', 'reposts' => 'по репостам', 'comments' => 'по комментариям', 'views' => 'по просмотрам', 'viral' => 'по виральности'];
const BEST_POSTS_TYPES = ['any', 'photo', 'album', 'video'];
const BEST_POSTS_RESULTS = 30;            // постов в выдаче
const BEST_POSTS_TAKE_MAX = 20;           // за один раз в подборку
const BEST_POSTS_TAKE_VIDEOS = 3;         // ролики качаются долго (до 30 МБ): за один раз не больше трёх
const BEST_POSTS_TG_PAGES = 5;            // страниц канала Telegram за период
const BEST_POSTS_VK_REQUESTS = 3;         // запросов wall.get по 100 записей
const BEST_POSTS_CACHE_SECONDS = 900;     // результат поиска хранится 15 минут
const BEST_POSTS_PER_DAY = 30;            // поисков (без попаданий в кэш) в сутки на пользователя
const BEST_POSTS_TEXT = 300;              // текста в выдаче

/** Сортировки, которые умеет площадка: счётчиков у RSS и YouTube нет. */
function best_posts_sorts_for($platform): array {
    return match ($platform) {
        PLATFORM_TELEGRAM => ['date', 'likes', 'views', 'viral'],
        PLATFORM_VK => ['date', 'likes', 'reposts', 'comments', 'views', 'viral'],
        PLATFORM_OK => ['date', 'likes', 'reposts', 'comments', 'viral'],
        default => ['date'],
    };
}

/** Медиана для делителя: ноль (или пусто) считается за 1, дробные доли (как доля реакций) не округляются. */
function best_posts_median_or_one(array $values): float {
    $m = (float)(median($values) ?? 0);
    return $m > 0 ? $m : 1.0;
}

/** Виральность: во сколько раз пост лучше обычного поста этого же источника (1 — как обычный). Медиана 0 считается за 1. */
function best_posts_virality(array $posts): array {
    if (!$posts) return $posts;
    if (($posts[0]['kind'] ?? '') === 'telegram') {
        // Telegram: просмотры и доля реакций от просмотров, поровну
        $views = array_map(fn($p) => (int)($p['views'] ?? 0), $posts);
        $rates = array_map(fn($p) => (int)($p['views'] ?? 0) > 0 ? (int)($p['likes'] ?? 0) / (int)$p['views'] : 0.0, $posts);
        [$med_views, $med_rate] = [best_posts_median_or_one($views), best_posts_median_or_one($rates)];
        foreach ($posts as $i => $p) $posts[$i]['viral'] = round(((int)($p['views'] ?? 0) / $med_views + $rates[$i] / $med_rate) / 2, 2);
        return $posts;
    }
    $score = fn($p) => (int)($p['likes'] ?? 0) + 2 * (int)($p['reposts'] ?? 0) + (int)($p['comments'] ?? 0);
    $median = best_posts_median_or_one(array_map($score, $posts));
    foreach ($posts as $i => $p) $posts[$i]['viral'] = round($score($p) / $median, 2);
    return $posts;
}

/** Посты Telegram-канала за период: страницы t.me/s/ назад, пока посты новее периода (не больше BEST_POSTS_TG_PAGES). */
function best_posts_from_telegram(string $channel, int $days): array {
    $cut = time() - $days * DAY_SECONDS;
    $posts = [];
    $before = null;
    for ($page = 0; $page < BEST_POSTS_TG_PAGES; $page++) {
        $html = fetch_telegram_channel_page($channel, $before);
        $blocks = $html ? telegram_page_blocks($html) : [];
        if (!$blocks) break;
        foreach (telegram_posts_from_html($html, ['id' => $channel, 'name' => source_display_name(PLATFORM_TELEGRAM, $channel)], 1000) as $p) {
            $engagement = $p['engagement'] ?? null;
            $posts[] = ['kind' => 'telegram', 'post_id' => $p['post_id'], 'image' => $p['image'], 'images' => $p['images'], 'video' => (string)($p['video'] ?? ''),
                'text' => (string)$p['text'], 'source' => $p['source'], 'url' => $p['url'], 'posted_at' => (int)$p['posted_at'],
                'views' => $engagement['views'] ?? null, 'likes' => $engagement ? array_sum($engagement['reactions']) : null, 'reposts' => null, 'comments' => null];
        }
        $ids = array_map(fn($b) => (int)$b[0], $blocks);
        $oldest = min(array_map(fn($b) => telegram_post_time($b[1]), $blocks));
        if ($oldest < $cut || min($ids) === $before) break;
        $before = min($ids);
    }
    return $posts;
}

/** Посты паблика VK за период: wall.get со смещением, не больше BEST_POSTS_VK_REQUESTS запросов. */
function best_posts_from_vk(string $domain, int $days): array {
    $cut = time() - $days * DAY_SECONDS;
    $posts = [];
    for ($request = 0; $request < BEST_POSTS_VK_REQUESTS; $request++) {
        $items = vk_wall_items($domain, 100, $request * 100);
        if (!$items) break;
        foreach ($items as $item) {
            if (!($p = vk_wall_post($item, source_display_name(PLATFORM_VK, $domain), $domain))) continue;
            $posts[] = ['kind' => 'social', 'post_id' => $p['post_id'], 'image' => $p['image'], 'images' => $p['images'], 'video' => '', 'text' => $p['text'],
                'source' => $p['source'], 'url' => $p['url'], 'posted_at' => $p['posted_at'], 'views' => $p['views'], 'likes' => $p['likes'], 'reposts' => $p['reposts'], 'comments' => $p['comments']];
        }
        if (min(array_map(fn($i) => (int)($i['date'] ?? 0), $items)) < $cut) break;
    }
    return $posts;
}

/** Посты группы Одноклассников: одна страница, сколько отдаёт. */
function best_posts_from_ok(string $group_id): array {
    return array_map(fn($p) => ['kind' => 'social', 'post_id' => $p['post_id'], 'image' => $p['image'], 'images' => $p['images'], 'video' => '', 'text' => $p['text'],
        'source' => $p['source'], 'url' => $p['url'], 'posted_at' => $p['posted_at'], 'views' => null, 'likes' => $p['likes'], 'reposts' => $p['reshares'], 'comments' => $p['comments']],
        ok_fetch_posts($group_id, OK_POSTS_PER_GROUP, source_display_name(PLATFORM_OK, $group_id)));
}

/** Лента RSS, YouTube, Pinterest, Tumblr: счётчиков нет, только даты. */
function best_posts_from_feed(string $feed_url): array {
    return array_map(fn($p) => ['kind' => 'feed', 'post_id' => $p['post_id'], 'image' => $p['image'], 'images' => $p['images'], 'video' => (string)($p['video'] ?? ''), 'text' => $p['text'],
        'source' => $p['source'], 'url' => $p['url'], 'posted_at' => $p['posted_at'], 'views' => null, 'likes' => null, 'reposts' => null, 'comments' => null],
        rss_fetch_posts($feed_url, 100));
}

/** Фильтр по типу и фразе: photo — одна картинка без видео, album — несколько картинок, video — с роликом. */
function best_posts_matching(array $posts, string $type, string $phrase): array {
    return array_values(array_filter($posts, function ($p) use ($type, $phrase) {
        $images = count($p['images']);
        if (($type === 'photo' && ($images > 1 || $p['video'] !== '')) || ($type === 'album' && $images < 2) || ($type === 'video' && $p['video'] === '')) return false;
        return $phrase === '' || mb_stripos($p['text'], $phrase) !== false;
    }));
}

/** Сортировка: по метрике, при равенстве — свежее первым. */
function best_posts_sort(array $posts, string $sort): array {
    $key = fn($p) => match ($sort) { 'likes', 'reposts', 'comments', 'views' => (int)($p[$sort] ?? 0), 'viral' => (float)($p['viral'] ?? 0), default => (int)$p['posted_at'] };
    usort($posts, fn($a, $b) => [$key($b), $b['posted_at']] <=> [$key($a), $a['posted_at']]);
    return $posts;
}

/** HMAC поста: подписан сервером, забрать можно только то, что сервер показал в выдаче. */
function best_post_token(array $p): string {
    return hash_hmac('sha256', ($p['source_key'] ?? '') . '|' . $p['post_id'] . '|' . $p['image'], secret_key());
}

/** Поиск «Лучших постов»: ['source', 'sort', 'sorts', 'posts' (до BEST_POSTS_RESULTS)]. Модель не вызывается. */
function best_posts(string $link, int $days, string $sort, string $type = 'any', string $phrase = ''): array {
    if (!in_array($days, BEST_POSTS_DAYS, true)) throw new RuntimeException('Период: день, неделя или месяц.');
    $src = source_parse_link($link);
    ['platform' => $platform, 'id' => $id] = $src;
    if ($platform === PLATFORM_VK && !vk_can_read()) throw new RuntimeException('Сначала подключите VK на вкладке «Источники».');
    $sorts = best_posts_sorts_for($platform);
    if (!in_array($sort, $sorts, true)) $sort = 'date';
    $collected = match ($platform) {
        PLATFORM_TELEGRAM => best_posts_from_telegram($id, $days),
        PLATFORM_VK => best_posts_from_vk($id, $days),
        PLATFORM_OK => best_posts_from_ok($id),
        default => best_posts_from_feed($id),
    };
    // Виральность — относительно всех собранных за период постов источника, а не отфильтрованных
    $cut = time() - $days * DAY_SECONDS;
    $period = best_posts_virality(array_values(array_filter($collected, fn($p) => $p['posted_at'] >= $cut)));
    $found = best_posts_sort(best_posts_matching($period, $type, $phrase), $sort);
    $source_key = source_id_for($platform, $id);
    $posts = array_map(fn($p) => $p + ['source_key' => $source_key, 'token' => best_post_token($p + ['source_key' => $source_key])], array_slice($found, 0, BEST_POSTS_RESULTS));
    return ['source' => ['name' => source_display_name($platform, $id), 'platform' => $platform], 'sort' => $sort, 'sorts' => $sorts, 'posts' => $posts];
}

function best_posts_quota_take($uid): void {
    $used = kv_update('best_posts:' . $uid . ':' . date('Y-m-d'), fn($n) => (int)$n + 1, 0);
    if ($used > BEST_POSTS_PER_DAY) throw new RuntimeException('Сегодня уже много поисков «Лучших постов»: попробуйте завтра (до ' . BEST_POSTS_PER_DAY . ' в сутки).');
}

/** Поиск с кэшем на 15 минут (ключ — пользователь и параметры). Возвращает результат с ключом кэша 'key'. */
function best_posts_search(string $link, int $days, string $sort, string $type = 'any', string $phrase = ''): array {
    $uid = app_user_id();
    $key = 'best:' . $uid . ':' . sha1(implode('|', [trim($link), $days, $sort, $type, $phrase]));
    $hit = kv_get($key);
    if (is_array($hit) && time() - (int)($hit['at'] ?? 0) < BEST_POSTS_CACHE_SECONDS) return ['key' => $key] + $hit['result'];
    best_posts_quota_take($uid);
    $result = best_posts(trim($link), $days, $sort, $type, $phrase);
    kv_update($key, fn() => ['at' => time(), 'result' => $result]);
    return ['key' => $key] + $result;
}

/** Выдача для страницы: без полного текста и адреса ролика (только признак видео). */
function best_posts_public(array $r): array {
    return ['key' => $r['key'], 'source' => $r['source'], 'sort' => $r['sort'], 'sorts' => $r['sorts'],
        'posts' => array_map(fn($p) => ['token' => $p['token'], 'image' => $p['image'], 'images' => $p['images'], 'video' => $p['video'] !== '',
            'text' => mb_substr((string)$p['text'], 0, BEST_POSTS_TEXT), 'source_url' => $p['url'], 'source_key' => $p['source_key'], 'posted_at' => $p['posted_at'],
            'views' => $p['views'], 'likes' => $p['likes'], 'reposts' => $p['reposts'], 'comments' => $p['comments'], 'viral' => $p['viral'] ?? 0], $r['posts'])];
}

/** Поля ролика записи «Лучших постов»: готового файла нет, но есть страница поста, с которой его можно скачать — ролик ставится в очередь и подборка обновится сама. */
function best_post_video_fields(array $p, string $video_local): array {
    $page = (string)($p['url'] ?? '');
    if (($p['video'] ?? '') === '' || $video_local !== '' || ($via = video_page_via($page)) === null) return ['video' => '', 'video_local' => '', 'video_via' => '', 'video_page' => ''];
    return ['video' => '', 'video_local' => '', 'video_via' => $via, 'video_page' => $page];
}

/** Картинка и ролик поста в кэш медиа, запись пула как у парсера. Ролик — только первые BEST_POSTS_TAKE_VIDEOS, остальные — в очередь по странице поста. */
function best_post_pool_item(array $p, bool $with_video): array {
    $remote = array_values(array_unique(array_filter([$p['image'], ...$p['images']], fn($u) => str_starts_with((string)$u, 'https://'))));
    $cached = [];
    foreach (array_slice($remote, 0, 4) as $url) {
        try { $local = media_cache_store_image($url, fetch_public_image($url)); } catch (RuntimeException $e) { continue; }   // картинка не скачалась — пост без неё
        if ($local !== '') $cached[] = ['image' => $url, 'local' => $local];
    }
    if (!$cached) throw new RuntimeException('Картинка не скачалась.');
    $video_local = $with_video && $p['video'] !== '' ? media_cache_store_video($p['video']) : '';
    $text = (string)$p['text'] === EMPTY_POST_TEXT ? '' : (string)$p['text'];
    $pending = best_post_video_fields($p, $video_local);
    return ['image' => $cached[0]['image'], 'images' => array_column($cached, 'image'), 'image_local' => $cached[0]['local'], 'video' => $video_local !== '' ? $p['video'] : '', 'video_local' => $video_local,
        'video_via' => $pending['video_via'], 'video_page' => $pending['video_page'],
        'gallery' => count($cached) > 1 ? array_map(fn($c) => ['image' => $c['image'], 'image_local' => $c['local'], 'video' => ''], $cached) : [],
        'text' => $text, 'captions' => [$text], 'description' => $text, 'source' => $p['source'], 'source_key' => $p['source_key'], 'source_url' => $p['source_url'] ?? $p['url'],
        'posted_at' => (int)$p['posted_at'], 'pooled_at' => time(), 'topic' => current_topic(), 'relevance' => 100, 'rating' => 100, 'picked' => true];
}

/** Выбранные посты из кэшированной выдачи → в подборку пользователя. Возвращает сколько добавлено (повторы пропускаются). */
function best_posts_take(string $cache_key, array $tokens): int {
    $uid = app_user_id();
    if (!str_starts_with($cache_key, 'best:' . $uid . ':')) throw new RuntimeException('Результаты поиска не найдены: выполните поиск снова.');
    $hit = kv_get($cache_key);
    if (!is_array($hit) || time() - (int)($hit['at'] ?? 0) >= BEST_POSTS_CACHE_SECONDS) throw new RuntimeException('Результаты поиска устарели: выполните поиск снова.');
    $tokens = array_values(array_unique(array_filter(array_map('strval', $tokens), 'strlen')));
    if (!$tokens || count($tokens) > BEST_POSTS_TAKE_MAX) throw new RuntimeException('Выберите от 1 до ' . BEST_POSTS_TAKE_MAX . ' постов.');
    $by_token = [];
    foreach ($hit['result']['posts'] ?? [] as $p) $by_token[best_post_token($p)] = $p;
    $items = [];
    $videos = 0;
    foreach ($tokens as $token) {
        if (!isset($by_token[$token])) throw new RuntimeException('Пост не найден в результатах: обновите поиск.');
        $p = $by_token[$token];
        $with_video = $p['video'] !== '' && $videos < BEST_POSTS_TAKE_VIDEOS;
        try { $items[] = best_post_pool_item($p, $with_video); } catch (RuntimeException $e) { continue; }
        if ($with_video && $items[array_key_last($items)]['video_local'] !== '') $videos++;
    }
    if (!$items) throw new RuntimeException('Картинки выбранных постов не скачались: попробуйте другие посты.');
    return own_add_to_pool($items);
}
