<?php
// «Зеркало источника»: каждый новый пост личного источника без оценки моделью — в подборку или сразу в очередь канала.
// Посты берутся теми же сборщиками, что «Лучшие посты» (best_posts.php); в общий каталог зеркало не попадает.

const MIRROR_MAX_PER_USER = 10;
const MIRROR_MODES = ['pool', 'queue'];
const MIRROR_RECHECK_SECONDS = 900;      // зеркало проверяется не чаще раза в 15 минут
const MIRROR_BUDGET_SECONDS = 20;        // на все зеркала пользователя за один проход парсера
const MIRROR_FIRST_RUN = 3;              // при первом запуске — только последние посты
const MIRROR_KEEP_KEYS = 60;             // сколько ключей уже виденных постов помним
const MIRROR_WORDS_MAX = 50;
const MIRROR_REPLACE_MAX = 20;
const MIRROR_REGEX_MAX = 200;
const MIRROR_APPEND_MAX = 500;

/** Регулярное выражение без разделителей: проверка тем же разделителем, что и при применении. */
function mirror_regex_ok(string $pattern): bool {
    return @preg_match('~' . str_replace('~', '\~', $pattern) . '~u', '') !== false;
}

/** Правила из формы: проверенные и нормализованные; плохое регулярное выражение — ошибка сохранения. */
function mirror_rules_clean(array $rules): array {
    $words = fn($list) => array_values(array_slice(array_filter(array_map(fn($w) => mb_substr(trim((string)$w), 0, 100), (array)$list), 'strlen'), 0, MIRROR_WORDS_MAX));
    $replace = [];
    foreach (array_slice((array)($rules['replace'] ?? []), 0, MIRROR_REPLACE_MAX) as $pair) {
        $raw = trim((string)($pair['from'] ?? ''));
        if (!is_array($pair) || $raw === '') continue;
        $regex = !empty($pair['regex']);
        if ($regex && (mb_strlen($raw) > MIRROR_REGEX_MAX || !mirror_regex_ok($raw))) throw new RuntimeException('Регулярное выражение не работает или длиннее ' . MIRROR_REGEX_MAX . ' знаков: ' . mb_substr($raw, 0, 60));
        $replace[] = ['from' => mb_substr($raw, 0, 200), 'to' => mb_substr((string)($pair['to'] ?? ''), 0, 200), 'regex' => $regex];
    }
    return ['include' => $words($rules['include'] ?? []), 'exclude' => $words($rules['exclude'] ?? []), 'replace' => $replace,
        'strip_links' => !empty($rules['strip_links']), 'strip_mentions' => !empty($rules['strip_mentions']), 'strip_hashtags' => !empty($rules['strip_hashtags']),
        'append' => mb_substr(trim((string)($rules['append'] ?? '')), 0, MIRROR_APPEND_MAX)];
}

/** Замены по порядку. Регулярное выражение, которое упало (например, из-за лимита возвратов), пропускается. */
function mirror_replace(string $text, array $pairs): string {
    $old = ini_get('pcre.backtrack_limit');
    ini_set('pcre.backtrack_limit', '200000');
    foreach ($pairs as $pair) {
        if (!empty($pair['regex'])) {
            $done = @preg_replace('~' . str_replace('~', '\~', (string)$pair['from']) . '~u', (string)$pair['to'], $text);
            if (is_string($done)) $text = $done;
        } else {
            $text = str_replace((string)$pair['from'], (string)$pair['to'], $text);
        }
    }
    ini_set('pcre.backtrack_limit', (string)$old);
    return $text;
}

/** Текст поста по правилам зеркала. null — пост отсеян (не было нужных слов или есть запрещённое). */
function mirror_rules_apply(string $text, array $rules): ?string {
    $include = array_values(array_filter((array)($rules['include'] ?? []), 'strlen'));
    if ($include && !array_filter($include, fn($w) => mb_stripos($text, (string)$w) !== false)) return null;
    foreach ((array)($rules['exclude'] ?? []) as $w) if ((string)$w !== '' && mb_stripos($text, (string)$w) !== false) return null;
    $text = mirror_replace($text, (array)($rules['replace'] ?? []));
    if (!empty($rules['strip_links'])) $text = preg_replace('~https?://\S+~u', '', $text) ?? $text;
    if (!empty($rules['strip_mentions'])) $text = preg_replace('~(?<![\w@])@[A-Za-z0-9_]{3,}~u', '', $text) ?? $text;
    if (!empty($rules['strip_hashtags'])) $text = preg_replace('~(?<![\p{L}\p{N}_])#[\p{L}\p{N}_]+~u', '', $text) ?? $text;
    $text = trim(preg_replace('~[ \t]{2,}~u', ' ', $text) ?? $text);
    if (($append = trim((string)($rules['append'] ?? ''))) !== '') $text = trim($text . "\n" . $append);
    return $text;
}

function mirror_rows($uid): array {
    return db_all('SELECT * FROM mirrors WHERE user_id = ? ORDER BY created_at, id', [$uid]);
}

/** Зеркала пользователя для страницы: без служебных полей. */
function mirror_list($uid): array {
    return array_map(fn($r) => ['id' => $r['id'], 'name' => $r['name'], 'platform' => $r['platform'], 'mode' => $r['mode'], 'channel_id' => $r['channel_id'],
        'channel_title' => ($r['channel_id'] !== null ? (channel_get($r['channel_id'], $uid)['chat_title'] ?? '') : ''), 'rules' => db_unjson($r['rules'], []),
        'last_checked_at' => (int)$r['last_checked_at']], mirror_rows($uid));
}

/** Новое зеркало: ссылка → источник, режим и (для очереди) свой канал, правила проверяются при сохранении. */
function mirror_save($uid, array $in): array {
    if (count(mirror_rows($uid)) >= MIRROR_MAX_PER_USER) throw new RuntimeException('Зеркал не больше ' . MIRROR_MAX_PER_USER . ': удалите одно, чтобы добавить новое.');
    $src = source_parse_link((string)($in['link'] ?? ''));
    if ($src['platform'] === PLATFORM_VK && !vk_can_read()) throw new RuntimeException('Сначала подключите VK на вкладке «Источники».');
    $mode = (string)($in['mode'] ?? 'pool');
    if (!in_array($mode, MIRROR_MODES, true)) throw new RuntimeException('Неизвестный режим зеркала.');
    $channel_id = null;
    if ($mode === 'queue') {
        $channel = channel_get((string)($in['channel'] ?? ''), $uid) ?? throw new RuntimeException('Выберите свой канал для очереди.');
        if (!channel_schedule($channel)['slots']) throw new RuntimeException('У канала не настроено расписание: «Соцсети» → канал → «Расписание».');
        $channel_id = (string)$channel['id'];
    }
    $rules = mirror_rules_clean(is_array($in['rules'] ?? null) ? $in['rules'] : []);
    foreach (mirror_rows($uid) as $r) {
        if ($r['platform'] === $src['platform'] && $r['source_id'] === $src['id'] && $r['mode'] === $mode && $r['channel_id'] === $channel_id) throw new RuntimeException('Такое зеркало уже есть.');
    }
    $row = ['id' => bin2hex(random_bytes(6)), 'platform' => $src['platform'], 'source_id' => $src['id'], 'name' => mb_substr(source_display_name($src['platform'], $src['id']), 0, 160),
        'channel_id' => $channel_id, 'mode' => $mode, 'rules' => db_json($rules), 'last_post_key' => '[]', 'last_checked_at' => 0, 'created_at' => time()];
    db_exec('INSERT INTO mirrors (id, user_id, platform, source_id, name, channel_id, mode, rules, last_post_key, last_checked_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
        [$row['id'], $uid, $row['platform'], $row['source_id'], $row['name'], $row['channel_id'], $row['mode'], $row['rules'], $row['last_post_key'], 0, $row['created_at']]);
    return ['id' => $row['id'], 'name' => $row['name'], 'platform' => $row['platform'], 'mode' => $mode, 'channel_id' => $channel_id, 'rules' => $rules];
}

function mirror_delete($uid, $id): void {
    db_exec('DELETE FROM mirrors WHERE id = ? AND user_id = ?', [(string)$id, $uid]);
}

/** Новые посты источника зеркала (за два дня), от старых к новым. */
function mirror_fetch_posts(array $m): array {
    if (ua_source_blocked(source_id_for($m['platform'], (string)$m['source_id'])) !== null) return [];   // украинский источник не зеркалим (UA_FILTER_PROMPT.md, B5)
    $posts = match ($m['platform']) {
        PLATFORM_TELEGRAM => best_posts_from_telegram($m['source_id'], 2),
        PLATFORM_VK => best_posts_from_vk($m['source_id'], 2),
        PLATFORM_OK => best_posts_from_ok($m['source_id']),
        default => best_posts_from_feed($m['source_id']),
    };
    $posts = ua_filter_posts($posts);   // украинские посты не зеркалим (UA_FILTER_PROMPT.md, B5)
    usort($posts, fn($a, $b) => $a['posted_at'] <=> $b['posted_at']);
    return $posts;
}

/** Очередь канала зеркала: тот же путь, что ручная постановка в очередь (расписание, медиа, журнал очереди). */
function mirror_queue_post($uid, array $mirror, array $post): void {
    $channel = channel_get((string)$mirror['channel_id'], $uid) ?? throw new RuntimeException('канал зеркала не найден');
    if (!channel_schedule($channel)['slots']) throw new RuntimeException('у канала не настроено расписание');
    if (billing_post_denied(true, $uid)) throw new RuntimeException('лимит постов по тарифу исчерпан');
    $item = best_post_pool_item($post, true);
    $media = $item['video_local'] !== ''
        ? [['src' => $item['video_local'], 'kind' => 'video', 'name' => 'видео']]
        : array_map(fn($f) => ['src' => $f['image_local'] ?? $f['image'], 'kind' => 'photo', 'name' => 'фото'], $item['gallery'] ?: [['image' => $item['image'], 'image_local' => $item['image_local']]]);
    $item['text'] = text_with_hashtags((string)$item['text'], channel_hashtags($channel, post_hashtags($item)), $media ? TELEGRAM_CAPTION_LIMIT : TELEGRAM_TEXT_LIMIT);
    telegram_check_text($item['text'], count($media));
    $slot = channel_next_slot($channel, time(), outbox_load($uid)) ?? throw new RuntimeException('в расписании канала нет свободного слота');
    $work = telegram_work_dir();
    try {
        $prepared = telegram_prepare_media($media, [], $work, 'bot');
        outbox_queue_post($uid, $channel, $item['text'], $prepared, $slot['publish_at'], $slot['slot_at'] ?? null, $work);
        $work = null;   // файлы уже в папке поста очереди
    } finally {
        if ($work !== null) remove_dir($work);
    }
    billing_post_used($uid);
}

/** Один проход зеркала: новые посты → правила → подборка или очередь; ключи виденных постов сохраняются. */
function mirror_run_one($uid, array $m): array {
    $seen = array_values(array_map('strval', (array)db_unjson($m['last_post_key'], [])));
    $rules = db_unjson($m['rules'], []);
    $posts = mirror_fetch_posts($m);
    $fresh = $seen ? array_values(array_filter($posts, fn($p) => !in_array((string)$p['post_id'], $seen, true))) : array_slice($posts, -MIRROR_FIRST_RUN);
    $keep = array_slice(array_values(array_unique([...$seen, ...array_map(fn($p) => (string)$p['post_id'], $posts)])), -MIRROR_KEEP_KEYS);
    $counts = ['new' => count($fresh), 'pooled' => 0, 'queued' => 0, 'skipped' => 0];
    $items = [];
    $videos = 0;
    foreach ($fresh as $post) {
        $post['source_key'] = source_id_for($m['platform'], $m['source_id']);
        if (($text = mirror_rules_apply((string)$post['text'], $rules)) === null) { $counts['skipped']++; continue; }
        $post['text'] = $text;
        try {
            if ($m['mode'] === 'queue') {
                mirror_queue_post($uid, $m, $post);
                $counts['queued']++;
            } else {
                $with_video = $post['video'] !== '' && $videos < BEST_POSTS_TAKE_VIDEOS;
                $items[] = $item = best_post_pool_item($post, $with_video);
                if ($item['video_local'] !== '') $videos++;
            }
        } catch (RuntimeException $e) {
            $counts['skipped']++;
            cli_echo("-> Зеркало «{$m['name']}»: пост пропущен — " . $e->getMessage());
        }
    }
    if ($items) $counts['pooled'] = own_add_to_pool($items);
    db_exec('UPDATE mirrors SET last_post_key = ?, last_checked_at = ? WHERE id = ?', [db_json($keep), time(), $m['id']]);
    return $counts;
}

/** Все зеркала пользователя, проверенные давно: новые посты в подборку или очередь. Общий бюджет — MIRROR_BUDGET_SECONDS. */
function mirror_collect_user($uid): array {
    $started = time();
    $total = ['mirrors' => 0, 'new' => 0, 'pooled' => 0, 'queued' => 0, 'skipped' => 0];
    foreach (mirror_rows($uid) as $m) {
        if (time() - (int)$m['last_checked_at'] < MIRROR_RECHECK_SECONDS) continue;
        if (time() - $started > MIRROR_BUDGET_SECONDS) { cli_echo('Зеркала: время на проверку вышло, остальные — в следующий проход.'); break; }
        try {
            $r = mirror_run_one($uid, $m);
        } catch (RuntimeException $e) {
            cli_echo("Зеркало «{$m['name']}»: " . $e->getMessage());
            db_exec('UPDATE mirrors SET last_checked_at = ? WHERE id = ?', [time(), $m['id']]);
            continue;
        }
        cli_echo("Зеркало «{$m['name']}»: новых постов {$r['new']}, в подборку {$r['pooled']}, в очередь {$r['queued']}, пропущено {$r['skipped']}.");
        $total['mirrors']++;
        foreach (['new', 'pooled', 'queued', 'skipped'] as $k) $total[$k] += $r[$k];
    }
    return $total;
}
