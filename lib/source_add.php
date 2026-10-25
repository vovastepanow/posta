<?php
// «Добавить источник» вручную: ссылка на Telegram-канал, паблик VK или сайт/RSS → проверка → каталог с темой пользователя.

const SOURCE_ADD_PER_DAY = 20;   // источники попадают в общий каталог и обходятся парсером всех: добавлений в сутки на пользователя ограничены
const SOURCE_ADD_MAX_LINK = 300;

/** Ссылка → ['platform', 'id']; RuntimeException с понятным текстом, если это не источник. */
function source_parse_link(string $input): array {
    $input = trim($input);
    if ($input === '' || strlen($input) > SOURCE_ADD_MAX_LINK || preg_match('/[\s<>"\']/', $input)) throw new RuntimeException('Вставьте одну ссылку: на Telegram-канал, паблик VK или сайт.');
    $reserved = array_diff(TELEGRAM_RESERVED_PATHS, ['durov']);   // служебные адреса t.me, не каналы
    if (preg_match('~^@([A-Za-z0-9_]+)$~', $input, $m) || preg_match('~^(?:https?://)?(?:www\.)?(?:t|telegram)\.me/(?:s/)?([^/?#]+)~i', $input, $m)) {
        $name = $m[1];
        if (str_starts_with($name, '+') || strtolower($name) === 'joinchat') throw new RuntimeException('Закрытые каналы не читаются: нужна публичная ссылка t.me/имя');
        if (!is_valid_telegram_channel($name) || in_array(strtolower($name), $reserved, true)) throw new RuntimeException('Не похоже на публичный Telegram-канал: нужна ссылка t.me/имя.');
        return ['platform' => PLATFORM_TELEGRAM, 'id' => $name];
    }
    if (preg_match('~^(club|public)(\d+)$~i', $input, $m)) return ['platform' => PLATFORM_VK, 'id' => strtolower($m[1]) . $m[2]];
    if (preg_match('~^(?:https?://)?(?:m\.)?vk\.(?:com|ru)/([^/?#]+)~i', $input, $m)) {
        if (!is_valid_vk_domain($m[1])) throw new RuntimeException('Не похоже на ссылку на паблик VK: нужна vk.com/имя.');
        return ['platform' => PLATFORM_VK, 'id' => $m[1]];
    }
    // Ссылка на группу ОК никогда не уходит в rss_discover: у ОК своя площадка
    if (preg_match('~^https?://(?:m\.)?ok\.ru(?:[/?#]|$)~i', $input)) return ['platform' => PLATFORM_OK, 'id' => ok_group_id($input)];
    // TikTok — только ссылкой: голое «@имя» уже разбирается как канал Telegram
    if (preg_match('~^https?://(?:(?:www|m|vm|vt)\.)?tiktok\.com(?:[/?#]|$)~i', $input)) return ['platform' => PLATFORM_TIKTOK, 'id' => tiktok_user_from_link($input)];
    if (preg_match('~^https?://~i', $input)) {
        $feed = rss_known_feed($input) ?? rss_discover($input);
        if (!is_valid_rss_url($feed)) throw new RuntimeException('Адрес ленты должен начинаться с https:// и быть не длиннее 500 символов.');
        return ['platform' => PLATFORM_RSS, 'id' => $feed];
    }
    throw new RuntimeException('Не понял ссылку. Telegram: t.me/канал · VK: vk.com/паблик · сайт или блог: адрес страницы.');
}

/** Название паблика VK по адресу; при сбое — сам адрес. */
function source_vk_title(string $domain): string {
    try {
        $r = vk_api_request('groups.getById', ['group_id' => $domain], true);
        $g = $r['groups'][0] ?? $r[0] ?? [];
        return mb_substr(clean_html_text($g['name'] ?? ''), 0, 120) ?: $domain;
    } catch (Throwable $e) { return $domain; }
}

/** Посты источника за последние CONTENT_MAX_AGE_DAYS и его название: [name, posts]. */
function source_probe(array $src): array {
    ['platform' => $platform, 'id' => $id] = $src;
    [$name, $posts] = match ($platform) {
        PLATFORM_TELEGRAM => (function () use ($id) {
            $info = telegram_channel_info($id, 10) ?? throw new RuntimeException('Канал не найден или закрыт: нужна публичная ссылка t.me/имя.');
            return [$info['title'], $info['posts']];
        })(),
        PLATFORM_VK => [source_vk_title($id), build_vk_preview_posts(['id' => $id], 10)],
        PLATFORM_RSS => [rss_feed_title($id), rss_fetch_posts($id, RSS_POSTS_PER_FEED)],
        PLATFORM_OK => (function () use ($id) {
            $html = ok_group_html($id);   // одна загрузка страницы: и название, и посты
            return [ok_title_from_html($html, $id), ok_parse_feed($html, $id, OK_POSTS_PER_GROUP)];
        })(),
        PLATFORM_TIKTOK => ['@' . $id, tiktok_fetch_posts($id, TIKTOK_POSTS_PER_ACCOUNT)],
    };
    return [$name, array_values(array_filter($posts, fn($p) => !empty($p['image']) && !is_post_outdated($p['posted_at'] ?? 0)))];
}

/** Не больше SOURCE_ADD_PER_DAY добавлений в сутки на пользователя. */
function source_add_quota_take($uid): void {
    $key = "source_add:$uid:" . date('Y-m-d');
    $used = kv_update($key, fn($n) => (int)$n + 1, 0);
    if ($used > SOURCE_ADD_PER_DAY) throw new RuntimeException('Сегодня добавлено слишком много источников: попробуйте завтра (до ' . SOURCE_ADD_PER_DAY . ' в сутки).');
}

/** Добавляет источник в каталог с темой пользователя. ['platform', 'id', 'name', 'added' => новый для темы?, 'posts' => n]. */
function source_add_manual(string $input, string $topic): array {
    $src = source_parse_link($input);
    if ($src['platform'] === PLATFORM_VK && !vk_can_read()) throw new RuntimeException('Сначала подключите VK на вкладке «Источники».');
    source_add_quota_take(app_user_id());
    [$name, $posts] = source_probe($src);
    if (!$posts) throw new RuntimeException('В источнике нет свежих постов с картинками.');
    ['platform' => $platform, 'id' => $id] = $src;
    $key = CATALOG_KEYS[$platform];
    $sid = source_id_for($platform, $id);
    $added = false;
    update_source_db(function ($db) use ($platform, $id, $name, $posts, $topic, $key, $sid, &$added) {
        $found = false;
        foreach ($db[$key] as $i => $e) {
            if (strcasecmp(ltrim((string)($e['id'] ?? ''), '@'), $id) !== 0) continue;
            $found = true;
            $topics = (array)($e['topics'] ?? []);
            if ($topics && !array_filter($topics, fn($t) => topic_same((string)$t, $topic))) { $db[$key][$i]['topics'][] = $topic; $added = true; }
        }
        if (!$found) { $db[$key][] = ['id' => $id, 'name' => $name, 'topics' => [$topic], 'manual' => true, 'preview' => source_preview_images($posts)]; $added = true; }
        // Раньше отклонённый источник по этой теме снова нужен
        $db['source_votes'] = array_values(array_filter($db['source_votes'], fn($v) => !(($v['source_id'] ?? '') === $sid && ($v['vote'] ?? '') === 'reject' && topic_same((string)($v['topic'] ?? ''), $topic))));
        return $db;
    });
    return ['platform' => $platform, 'id' => $id, 'name' => $name, 'added' => $added, 'posts' => count($posts)];
}
